//go:build integration

package main_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/gtataranni/trimon/internal/config"
	"github.com/gtataranni/trimon/internal/exporter"
	otlpexp "github.com/gtataranni/trimon/internal/exporter/otlp"
	"github.com/gtataranni/trimon/internal/pipeline"
	"github.com/gtataranni/trimon/internal/probe"
	icmpprobe "github.com/gtataranni/trimon/internal/probe/icmp"
	"github.com/gtataranni/trimon/internal/scheduler"
	"github.com/gtataranni/trimon/internal/server"
	"github.com/gtataranni/trimon/pkg/types"
)

// requireSocket skips if raw ICMP sockets are unavailable (needs CAP_NET_RAW / sudo).
func requireSocket(t *testing.T) {
	t.Helper()
	p := icmpprobe.New(types.ProbeConfig{
		Name:           "socket-check",
		Type:           "icmp",
		Target:         "127.0.0.1",
		Count:          1,
		PacketInterval: 100 * time.Millisecond,
		Timeout:        500 * time.Millisecond,
		Interval:       5 * time.Second,
	})
	r, _ := p.Run(context.Background())
	if r.Status == types.StatusError &&
		(strings.Contains(r.ErrorMsg, "operation not permitted") ||
			strings.Contains(r.ErrorMsg, "permission denied")) {
		t.Skip("raw socket unavailable — re-run with sudo or CAP_NET_RAW")
	}
}

// freePort finds an available TCP port on loopback, then releases it.
// There is a brief TOCTOU window before the caller binds; acceptable in tests.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// captureExporter forwards every ProbeResult to a channel.
type captureExporter struct {
	ch chan types.ProbeResult
}

func (e *captureExporter) Name() string { return "capture" }
func (e *captureExporter) Close() error { return nil }
func (e *captureExporter) Export(_ context.Context, r types.ProbeResult) error {
	select {
	case e.ch <- r:
	default:
	}
	return nil
}

// TestSmokeWiring starts the full daemon stack (config → scheduler → pipeline →
// exporter + HTTP server) with a loopback ICMP probe, verifies that at least one
// ProbeResult flows through the pipeline, and verifies that /healthz returns 200.
func TestSmokeWiring(t *testing.T) {
	requireSocket(t)

	port := freePort(t)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)

	// Minimal config: one fast loopback probe, no source_ip (OS picks).
	// Timings: packet_interval(100ms) * count(1) = 100ms < timeout(500ms) < probe_every(1s).
	cfgYAML := fmt.Sprintf(`
global:
  probe_every: 1s
  packet_interval: 100ms
  timeout: 500ms
  count: 1

server:
  listen: "%s"

probes:
  - name: ping-loopback
    type: icmp
    target: "127.0.0.1"
`, listenAddr)

	f, err := os.CreateTemp("", "trimon-smoke-*.yaml")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := io.WriteString(f, cfgYAML); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	f.Close()

	cfg, err := config.Load(f.Name())
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	otel.SetTracerProvider(noop.NewTracerProvider())

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	exp, err := otlpexp.New(context.Background(), cfg.Exporters.OTLP, "test", "none", logger)
	if err != nil {
		t.Fatalf("otlpexp.New: %v", err)
	}

	captured := make(chan types.ProbeResult, 10)
	exporters := []exporter.Exporter{exp, &captureExporter{ch: captured}}
	pipe := pipeline.New(exporters, logger, cfg.Pipeline.BufferSize)
	pipe.SetExportErrorRecorder(exp.RecordExporterError)

	probeFactory := func(probeCfg types.ProbeConfig) (probe.Prober, error) {
		switch probeCfg.Type {
		case "icmp":
			return icmpprobe.New(probeCfg), nil
		default:
			return nil, fmt.Errorf("unknown probe type %q", probeCfg.Type)
		}
	}
	sched := scheduler.New(probeFactory, pipe.Results(), logger)
	exp.SetGoroutinesGetter(sched.WorkerCount)

	srv := server.New(listenAddr)
	srv.SetLogger(logger)
	srv.SetMetricsHandler(exp.PrometheusHandler())
	srv.UpdateConfig(cfg)
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	go pipe.Run(ctx)
	sched.Start(cfg.Probes)

	t.Cleanup(func() {
		sched.Stop()
		cancel()
		pipe.Wait()
		if err := exp.Close(); err != nil {
			t.Logf("exp.Close: %v", err)
		}
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
	})

	// Wait for at least one probe result to flow through the pipeline.
	select {
	case result := <-captured:
		t.Logf("probe result: name=%s status=%s packets_sent=%d packets_received=%d",
			result.ProbeName, result.Status, result.PacketsSent, result.PacketsReceived)
		if result.ProbeName != "ping-loopback" {
			t.Errorf("ProbeName = %q, want %q", result.ProbeName, "ping-loopback")
		}
		if result.Status != types.StatusSuccess {
			t.Errorf("Status = %s, want StatusSuccess (error: %s)", result.Status, result.ErrorMsg)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for probe result")
	}

	// Verify the HTTP server is up and healthy.
	resp, err := http.Get(fmt.Sprintf("http://%s/healthz", listenAddr))
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
