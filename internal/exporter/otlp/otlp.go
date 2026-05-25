package otlp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc/credentials"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/pkg/types"
)

const instrScope = "github.com/gtataranni/trimon"

// Exporter is the unified telemetry exporter for trimon.
// It ships probe results and self-observability metrics to both:
//   - a Prometheus /metrics endpoint via the OTel Prometheus bridge (always active)
//   - an OTLP collector via gRPC or HTTP (when cfg.Enabled is true)
type Exporter struct {
	provider    *sdkmetric.MeterProvider
	promHandler http.Handler
	logger      *slog.Logger

	// probe result instruments — recorded on every Export call
	rttMin, rttMean, rttMax, rttStddev metric.Float64Gauge
	packetLoss                         metric.Float64Gauge
	pktSent                            metric.Int64Counter
	pktReceived                        metric.Int64Counter
	success                            metric.Int64Gauge
	probeUp                            metric.Int64Gauge

	// self-observability instruments
	probeRuns      metric.Int64Counter
	probeErrors    metric.Int64Counter
	exporterErrors metric.Int64Counter
	configReloads  metric.Int64Counter

	// set once before first scrape via SetGoroutinesGetter
	getGoroutines func() int
}

// New creates the unified telemetry Exporter.
// The Prometheus bridge reader is always registered; the OTLP reader is added
// only when cfg.Enabled is true.
func New(ctx context.Context, cfg config.OTLPExporterConfig, version, commit string, logger *slog.Logger) (*Exporter, error) {
	e := &Exporter{logger: logger}

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

	// Prometheus bridge — always active; serves /metrics
	reg := prometheus.NewRegistry()
	promExp, err := promexporter.New(
		promexporter.WithRegisterer(reg),
		promexporter.WithoutTargetInfo(),
		promexporter.WithoutScopeInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp: build prometheus bridge: %w", err)
	}
	e.promHandler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	opts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(promExp),
	}

	// OTLP reader — only when configured
	if cfg.Enabled {
		otlpExp, err := buildExporter(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("otlp: build OTLP exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(otlpExp,
			sdkmetric.WithInterval(cfg.Batch.ExportInterval),
			sdkmetric.WithTimeout(cfg.Batch.ExportTimeout),
		)))
	}

	provider := sdkmetric.NewMeterProvider(opts...)
	e.provider = provider

	meter := provider.Meter(instrScope)
	if err := e.registerInstruments(meter, version, commit); err != nil {
		_ = provider.Shutdown(ctx)
		return nil, fmt.Errorf("otlp: register instruments: %w", err)
	}

	return e, nil
}

// Name returns the exporter identifier used in self-observability metrics.
func (e *Exporter) Name() string { return "otlp" }

// PrometheusHandler returns the http.Handler that serves /metrics.
func (e *Exporter) PrometheusHandler() http.Handler { return e.promHandler }

// SetGoroutinesGetter registers the function called on each /metrics scrape to
// report the live scheduler goroutine count. Must be called before the HTTP
// server starts.
func (e *Exporter) SetGoroutinesGetter(f func() int) { e.getGoroutines = f }

// RecordConfigReload increments the config-reload counter.
func (e *Exporter) RecordConfigReload(ctx context.Context) {
	e.configReloads.Add(ctx, 1)
}

// RecordExporterError increments the exporter-error counter for the named exporter.
// Intended to be passed as a callback to pipeline.SetExportErrorRecorder.
func (e *Exporter) RecordExporterError(ctx context.Context, exporterName string) {
	e.exporterErrors.Add(ctx, 1, metric.WithAttributes(attribute.String("exporter.name", exporterName)))
}

func (e *Exporter) registerInstruments(meter metric.Meter, version, commit string) error {
	var err error

	// probe result gauges
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
	e.success, err = meter.Int64Gauge("trimon.probe.success")
	if err != nil {
		return err
	}
	e.probeUp, err = meter.Int64Gauge("trimon.probe.up")
	if err != nil {
		return err
	}

	// probe result counters (cumulative, monotonically increasing)
	e.pktSent, err = meter.Int64Counter("trimon.probe.packets_sent", metric.WithUnit("{packets}"))
	if err != nil {
		return err
	}
	e.pktReceived, err = meter.Int64Counter("trimon.probe.packets_received", metric.WithUnit("{packets}"))
	if err != nil {
		return err
	}

	// self-observability counters
	e.probeRuns, err = meter.Int64Counter("trimon.probe.runs", metric.WithUnit("{runs}"))
	if err != nil {
		return err
	}
	e.probeErrors, err = meter.Int64Counter("trimon.probe.errors", metric.WithUnit("{errors}"))
	if err != nil {
		return err
	}
	e.exporterErrors, err = meter.Int64Counter("trimon.exporter.errors", metric.WithUnit("{errors}"))
	if err != nil {
		return err
	}
	e.configReloads, err = meter.Int64Counter("trimon.config.reloads", metric.WithUnit("{reloads}"))
	if err != nil {
		return err
	}

	// build info — static observable gauge, reported on every scrape
	buildInfoGauge, err := meter.Int64ObservableGauge("trimon.build.info")
	if err != nil {
		return err
	}
	if _, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(buildInfoGauge, 1, metric.WithAttributes(
			attribute.String("version", version),
			attribute.String("commit", commit),
			attribute.String("goversion", runtime.Version()),
		))
		return nil
	}, buildInfoGauge); err != nil {
		return err
	}

	// scheduler goroutines — live observable gauge
	goroutinesGauge, err := meter.Int64ObservableGauge("trimon.scheduler.goroutines",
		metric.WithUnit("{goroutines}"))
	if err != nil {
		return err
	}
	if _, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		var count int64
		if e.getGoroutines != nil {
			count = int64(e.getGoroutines())
		}
		o.ObserveInt64(goroutinesGauge, count)
		return nil
	}, goroutinesGauge); err != nil {
		return err
	}

	return nil
}

// Export records one ProbeResult as metric observations.
// The OTel SDK delivers these to both the Prometheus bridge and the OTLP
// collector (when configured).
func (e *Exporter) Export(ctx context.Context, r types.ProbeResult) error {
	probeAttrs := buildAttrs(r)
	nameAttr := metric.WithAttributes(attribute.String("probe.name", r.ProbeName))

	e.probeRuns.Add(ctx, 1, nameAttr)

	var (
		rttMin, rttMean, rttMax, rttStddev float64
		packetLoss                         float64
		successVal, upVal                  int64
	)

	if r.Status != types.StatusError {
		e.pktSent.Add(ctx, int64(r.PacketsSent), probeAttrs)
		e.pktReceived.Add(ctx, int64(r.PacketsReceived), probeAttrs)
	}

	switch r.Status {
	case types.StatusSuccess, types.StatusPartial:
		rttMin = r.RTTMinMS
		rttMean = r.RTTMeanMS
		rttMax = r.RTTMaxMS
		rttStddev = r.RTTStddevMS
		packetLoss = r.PacketLossRatio
		upVal = 1
		if r.Status == types.StatusSuccess {
			successVal = 1
		}
	case types.StatusFailure:
		packetLoss = 1.0
	case types.StatusError:
		packetLoss = math.NaN()
		errType := r.ErrorType
		if errType == "" {
			errType = "unknown"
		}
		e.probeErrors.Add(ctx, 1, metric.WithAttributes(
			attribute.String("probe.name", r.ProbeName),
			attribute.String("error.type", errType),
		))
	}

	e.rttMin.Record(ctx, rttMin, probeAttrs)
	e.rttMean.Record(ctx, rttMean, probeAttrs)
	e.rttMax.Record(ctx, rttMax, probeAttrs)
	e.rttStddev.Record(ctx, rttStddev, probeAttrs)
	e.packetLoss.Record(ctx, packetLoss, probeAttrs)
	e.success.Record(ctx, successVal, probeAttrs)
	e.probeUp.Record(ctx, upVal, probeAttrs)

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
	kv := make([]attribute.KeyValue, 0, 4+len(r.Labels))
	kv = append(kv,
		attribute.String("probe.name", r.ProbeName),
		attribute.String("probe.type", r.ProbeType),
		attribute.String("probe.target", r.Target),
		attribute.String("probe.source_ip", r.SourceIP),
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
		otlpmetricgrpc.WithRetry(otlpmetricgrpc.RetryConfig{
			Enabled:        cfg.Retry.Enabled,
			MaxElapsedTime: cfg.Retry.MaxElapsedTime,
		}),
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
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{
			Enabled:        cfg.Retry.Enabled,
			MaxElapsedTime: cfg.Retry.MaxElapsedTime,
		}),
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
