package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/internal/exporter"
	otlpexp "github.com/gtataranni/trimon/internal/exporter/otlp"
	stdoutexp "github.com/gtataranni/trimon/internal/exporter/stdout"
	"github.com/gtataranni/trimon/internal/pipeline"
	"github.com/gtataranni/trimon/internal/probe"
	icmpprobe "github.com/gtataranni/trimon/internal/probe/icmp"
	"github.com/gtataranni/trimon/internal/scheduler"
	"github.com/gtataranni/trimon/internal/server"
	"github.com/gtataranni/trimon/pkg/types"
)

// Injected at build time: go build -ldflags "-X main.version=v1.0.0 -X main.commit=abc1234"
var (
	version = "dev"
	commit  = "none"
)

func main() {
	configPath := flag.String("config", "trimon.yaml", "path to config file")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	logFormat := flag.String("log-format", "json", "log format: json|text")
	flag.Parse()

	logger := buildLogger(*logLevel, *logFormat)

	// disable tracing
	otel.SetTracerProvider(noop.NewTracerProvider())

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	probeFactory := func(probeCfg types.ProbeConfig) (probe.Prober, error) {
		switch probeCfg.Type {
		case "icmp":
			return icmpprobe.New(probeCfg), nil
		default:
			return nil, fmt.Errorf("unknown probe type %q", probeCfg.Type)
		}
	}

	srv := server.New(cfg.Server.Listen, version, commit)

	exporters, closeExporters := buildExporters(context.Background(), cfg, srv, logger, version)
	pipe := pipeline.New(exporters, logger)

	sched := scheduler.New(probeFactory, pipe.Results(), logger)
	srv.SetScheduler(sched)
	srv.UpdateConfig(cfg)
	srv.SetReloadFunc(func() (*config.Config, error) {
		newCfg, loadErr := config.Load(*configPath)
		if loadErr != nil {
			return nil, loadErr
		}
		sched.Reload(newCfg.Probes)
		srv.UpdateConfig(newCfg)
		srv.Metrics().ConfigReloadTotal.Inc()
		logger.Info("config reloaded", "probes", len(newCfg.Probes))
		return newCfg, nil
	})

	if startErr := srv.Start(); startErr != nil {
		logger.Error("failed to start HTTP server", "error", startErr)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pipe.Run(ctx)

	sched.Start(cfg.Probes)
	logger.Info("trimon started", "version", version, "commit", commit, "probes", len(cfg.Probes))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigCh
	logger.Info("shutting down", "signal", sig)
	sched.Stop()
	cancel()
	pipe.Wait()
	closeExporters()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
}

func buildExporters(ctx context.Context, cfg *config.Config, srv *server.Server, logger *slog.Logger, version string) ([]exporter.Exporter, func()) {
	var list []exporter.Exporter
	var closers []func() error

	list = append(list, &metricsExporter{srv: srv})

	if cfg.Exporters.Stdout.Enabled {
		list = append(list, stdoutexp.New(cfg.Exporters.Stdout.Format))
	}

	if cfg.Exporters.OTLP.Enabled {
		oe, err := otlpexp.New(ctx, cfg.Exporters.OTLP, version, logger)
		if err != nil {
			logger.Error("failed to create OTLP exporter", "error", err)
		} else {
			list = append(list, oe)
			closers = append(closers, oe.Close)
		}
	}

	closeAll := func() {
		for _, fn := range closers {
			if err := fn(); err != nil {
				logger.Error("exporter close error", "error", err)
			}
		}
	}
	return list, closeAll
}

func buildLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}

// metricsExporter increments Prometheus counters for each result.
type metricsExporter struct {
	srv *server.Server
}

func (m *metricsExporter) Export(_ context.Context, r types.ProbeResult) error {
	m.srv.RecordResult(r)
	return nil
}

func (m *metricsExporter) Close() error { return nil }
