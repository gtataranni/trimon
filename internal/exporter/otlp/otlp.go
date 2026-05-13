package otlp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"math"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc/credentials"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/pkg/types"
)

const instrScope = "github.com/gtataranni/trimon"

// Exporter ships ProbeResults as OTel metrics via OTLP gRPC or HTTP.
type Exporter struct {
	provider *sdkmetric.MeterProvider
	logger   *slog.Logger

	rttMin      metric.Float64Gauge
	rttMean     metric.Float64Gauge
	rttMax      metric.Float64Gauge
	rttStddev   metric.Float64Gauge
	packetLoss  metric.Float64Gauge
	pktSent     metric.Int64Gauge
	pktReceived metric.Int64Gauge
	success     metric.Int64Gauge
}

// New creates and starts an OTLP Exporter using cfg.
// version is the build-time version string embedded in the resource.
func New(ctx context.Context, cfg config.OTLPExporterConfig, version string, logger *slog.Logger) (*Exporter, error) {
	exp, err := buildExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("otlp: build exporter: %w", err)
	}

	hostname, _ := os.Hostname()
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("trimon"),
			semconv.ServiceVersion(version),
			semconv.HostName(hostname),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp: build resource: %w", err)
	}

	reader := sdkmetric.NewPeriodicReader(exp,
		sdkmetric.WithInterval(cfg.Batch.ExportInterval),
		sdkmetric.WithTimeout(cfg.Batch.ExportTimeout),
	)

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)

	meter := provider.Meter(instrScope)

	e := &Exporter{provider: provider, logger: logger}
	if err := e.registerInstruments(meter); err != nil {
		_ = provider.Shutdown(ctx)
		return nil, fmt.Errorf("otlp: register instruments: %w", err)
	}
	return e, nil
}

func (e *Exporter) registerInstruments(meter metric.Meter) error {
	var err error

	e.rttMin, err = meter.Float64Gauge("trimon.probe.rtt.min", metric.WithUnit("ms"))
	if err != nil {
		return err
	}
	e.rttMean, err = meter.Float64Gauge("trimon.probe.rtt.mean", metric.WithUnit("ms"))
	if err != nil {
		return err
	}
	e.rttMax, err = meter.Float64Gauge("trimon.probe.rtt.max", metric.WithUnit("ms"))
	if err != nil {
		return err
	}
	e.rttStddev, err = meter.Float64Gauge("trimon.probe.rtt.stddev", metric.WithUnit("ms"))
	if err != nil {
		return err
	}
	e.packetLoss, err = meter.Float64Gauge("trimon.probe.packet_loss", metric.WithUnit("ratio"))
	if err != nil {
		return err
	}
	e.pktSent, err = meter.Int64Gauge("trimon.probe.packets_sent", metric.WithUnit("{packets}"))
	if err != nil {
		return err
	}
	e.pktReceived, err = meter.Int64Gauge("trimon.probe.packets_received", metric.WithUnit("{packets}"))
	if err != nil {
		return err
	}
	e.success, err = meter.Int64Gauge("trimon.probe.success")
	if err != nil {
		return err
	}
	return nil
}

// Export records one ProbeResult as gauge observations.
// The OTel SDK periodic reader will flush these to the collector on its own schedule.
func (e *Exporter) Export(ctx context.Context, r types.ProbeResult) error {
	attrs := buildAttrs(r)

	var (
		rttMin, rttMean, rttMax, rttStddev float64
		pktSent, pktRecv                   int64
		packetLoss                         float64
		successVal                         int64
	)

	switch r.Status {
	case types.StatusSuccess, types.StatusPartial:
		rttMin = r.RTTMinMS
		rttMean = r.RTTMeanMS
		rttMax = r.RTTMaxMS
		rttStddev = r.RTTStddevMS
		pktSent = int64(r.PacketsSent)
		pktRecv = int64(r.PacketsReceived)
		packetLoss = r.PacketLossRatio
		if r.Status == types.StatusSuccess {
			successVal = 1
		}
	case types.StatusFailure:
		packetLoss = 1.0
	case types.StatusError:
		packetLoss = math.NaN()
	}

	e.rttMin.Record(ctx, rttMin, attrs)
	e.rttMean.Record(ctx, rttMean, attrs)
	e.rttMax.Record(ctx, rttMax, attrs)
	e.rttStddev.Record(ctx, rttStddev, attrs)
	e.packetLoss.Record(ctx, packetLoss, attrs)
	e.pktSent.Record(ctx, pktSent, attrs)
	e.pktReceived.Record(ctx, pktRecv, attrs)
	e.success.Record(ctx, successVal, attrs)

	return nil
}

// Close flushes in-flight data and shuts down the MeterProvider.
func (e *Exporter) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.provider.ForceFlush(ctx); err != nil {
		e.logger.Warn("otlp: force flush error", "err", err)
	}
	return e.provider.Shutdown(ctx)
}

func buildAttrs(r types.ProbeResult) metric.MeasurementOption {
	kv := make([]attribute.KeyValue, 0, 5+len(r.Labels))
	kv = append(kv,
		attribute.String("probe.name", r.ProbeName),
		attribute.String("probe.type", r.ProbeType),
		attribute.String("probe.target", r.Target),
		attribute.String("probe.source_ip", r.SourceIP),
		attribute.String("probe.status", string(r.Status)),
	)
	for k, v := range r.Labels {
		kv = append(kv, attribute.String(k, v))
	}
	return metric.WithAttributes(kv...)
}

func buildExporter(ctx context.Context, cfg config.OTLPExporterConfig) (sdkmetric.Exporter, error) {
	switch cfg.Protocol {
	case "grpc":
		return buildGRPC(ctx, cfg)
	case "http":
		return buildHTTP(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported protocol %q", cfg.Protocol)
	}
}

func buildGRPC(ctx context.Context, cfg config.OTLPExporterConfig) (sdkmetric.Exporter, error) {
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	} else if cfg.TLS.CertFile != "" {
		creds, err := loadTLSCredentials(cfg.TLS)
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlpmetricgrpc.WithTLSCredentials(creds))
	}
	return otlpmetricgrpc.New(ctx, opts...)
}

func buildHTTP(ctx context.Context, cfg config.OTLPExporterConfig) (sdkmetric.Exporter, error) {
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	} else if cfg.TLS.CertFile != "" {
		tlsCfg, err := buildTLSConfig(cfg.TLS)
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlpmetrichttp.WithTLSClientConfig(tlsCfg))
	}
	return otlpmetrichttp.New(ctx, opts...)
}

func loadTLSCredentials(tlsCfg config.OTLPTLSConfig) (credentials.TransportCredentials, error) {
	cfg, err := buildTLSConfig(tlsCfg)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

func buildTLSConfig(tlsCfg config.OTLPTLSConfig) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if tlsCfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("otlp: load cert/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	if tlsCfg.CAFile != "" {
		ca, err := os.ReadFile(tlsCfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("otlp: read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("otlp: no valid CA certs in %q", tlsCfg.CAFile)
		}
		cfg.RootCAs = pool
	}

	return cfg, nil
}

