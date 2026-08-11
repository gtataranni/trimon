//go:build linux

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
	dnsprobe "github.com/gtataranni/trimon/internal/probe/dns"
	httpprobe "github.com/gtataranni/trimon/internal/probe/http"
	icmpprobe "github.com/gtataranni/trimon/internal/probe/icmp"
	tcpprobe "github.com/gtataranni/trimon/internal/probe/tcp"
	udpprobe "github.com/gtataranni/trimon/internal/probe/udp"
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
	configPath := flag.String("config", "", "path to ops config file (exporters, server, pipeline)")
	probesPath := flag.String("probes", "", "path to probe config directory of *.yaml probe files")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	logFormat := flag.String("log-format", "json", "log format: json|text")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "flag --config is required")
		os.Exit(1)
	}
	if *probesPath == "" {
		fmt.Fprintln(os.Stderr, "flag --probes is required")
		os.Exit(1)
	}

	logger := buildLogger(*logLevel, *logFormat)

	// disable tracing
	otel.SetTracerProvider(noop.NewTracerProvider())

	cfg, err := config.Load(*configPath, *probesPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	logger.Info("config loaded", "config", *configPath, "probes", *probesPath, "probe_files", len(cfg.ProbeFiles), "sha256", cfg.SHA256)
	if len(cfg.SkippedFiles) > 0 {
		logger.Warn("probe config entries ignored", "probes", *probesPath, "skipped", cfg.SkippedFiles)
	}

	probeFactory := func(probeCfg types.ProbeConfig) (probe.Prober, error) {
		switch probeCfg.Type {
		case types.ProbeTypeICMP:
			return icmpprobe.New(probeCfg), nil
		case types.ProbeTypeTCP:
			return tcpprobe.New(probeCfg), nil
		case types.ProbeTypeUDP:
			return udpprobe.New(probeCfg), nil
		case types.ProbeTypeDNS:
			return dnsprobe.New(probeCfg), nil
		case types.ProbeTypeHTTP:
			return httpprobe.New(probeCfg), nil
		default:
			return nil, fmt.Errorf("unknown probe type %q", probeCfg.Type)
		}
	}

	// Telemetry exporter — always created; Prometheus bridge is always active,
	// OTLP transport is added when cfg.Exporters.OTLP.Enabled is true.
	exp, err := otlpexp.New(context.Background(), cfg.Exporters.OTLP, version, commit, logger)
	if err != nil {
		logger.Error("failed to create telemetry exporter", "error", err)
		os.Exit(1)
	}

	srv := server.New(cfg.Server.Listen)
	srv.SetLogger(logger)
	srv.SetMetricsHandler(exp.PrometheusHandler())
	srv.UpdateConfig(cfg)

	exporters := []exporter.Exporter{exp}
	if cfg.Exporters.Stdout.Enabled {
		exporters = append(exporters, stdoutexp.New(cfg.Exporters.Stdout.Format))
	}
	pipe := pipeline.New(exporters, logger, cfg.Pipeline.BufferSize)
	pipe.SetExportErrorRecorder(exp.RecordExporterError)
	srv.SetHealthChecker(pipe.BufferUsage)

	sched := scheduler.New(probeFactory, pipe.Results(), logger)
	exp.SetGoroutinesGetter(sched.WorkerCount)
	sched.SetDroppedResultRecorder(exp.RecordDroppedResult)

	srv.SetReloadFunc(func() (*config.Config, error) {
		newCfg, loadErr := config.Load(*configPath, *probesPath)
		if loadErr != nil {
			return nil, loadErr
		}
		sched.Reload(newCfg.Probes)
		srv.UpdateConfig(newCfg)
		exp.RecordConfigReload(context.Background())
		logger.Info("config reloaded", "probes", len(newCfg.Probes), "probe_files", len(newCfg.ProbeFiles), "sha256", newCfg.SHA256)
		if len(newCfg.SkippedFiles) > 0 {
			logger.Warn("probe config entries ignored", "probes", *probesPath, "skipped", newCfg.SkippedFiles)
		}
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
	// Shutdown order: stop senders → cancel pipeline context → wait for drain → close exporters.
	// sched.Stop() blocks until every probe goroutine has exited, guaranteeing no further
	// sends on the results channel before cancel() triggers pipeline drain.
	sched.Stop()
	cancel()
	pipe.Wait()
	if err := exp.Close(); err != nil {
		logger.Error("exporter close error", "error", err)
	}
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Warn("http server shutdown error", "error", err)
	}
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
