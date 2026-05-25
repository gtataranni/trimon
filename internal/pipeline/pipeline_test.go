package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"
	"os"

	"github.com/gtataranni/trimon/internal/exporter"
	"github.com/gtataranni/trimon/pkg/types"
)

// countExporter counts Export calls and records the last result seen.
type countExporter struct {
	calls  atomic.Int64
	lastID atomic.Value // stores string (ProbeName)
}

func (e *countExporter) Name() string { return "count" }
func (e *countExporter) Export(_ context.Context, r types.ProbeResult) error {
	e.calls.Add(1)
	e.lastID.Store(r.ProbeName)
	return nil
}
func (e *countExporter) Close() error { return nil }

var _ exporter.Exporter = (*countExporter)(nil)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPipelineDispatchesToExporter(t *testing.T) {
	exp := &countExporter{}
	pipe := New([]exporter.Exporter{exp}, testLogger(), 16)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pipe.Run(ctx)

	pipe.Results() <- types.ProbeResult{ProbeName: "probe-a", Status: types.StatusSuccess}

	// Give the goroutine time to dispatch.
	time.Sleep(50 * time.Millisecond)
	cancel()
	pipe.Wait()

	if exp.calls.Load() == 0 {
		t.Error("expected exporter to be called at least once")
	}
	if got := exp.lastID.Load(); got != "probe-a" {
		t.Errorf("lastID: want probe-a, got %v", got)
	}
}

func TestPipelineDrainsOnCancel(t *testing.T) {
	exp := &countExporter{}
	pipe := New([]exporter.Exporter{exp}, testLogger(), 16)

	ctx, cancel := context.WithCancel(context.Background())

	go pipe.Run(ctx)

	const n = 10
	for i := 0; i < n; i++ {
		pipe.Results() <- types.ProbeResult{ProbeName: "p", Status: types.StatusSuccess}
	}

	// Cancel before the goroutine finishes; it must drain the buffered channel.
	cancel()
	pipe.Wait()

	if got := exp.calls.Load(); got != n {
		t.Errorf("expected %d exports after drain, got %d", n, got)
	}
}

func TestPipelineMultipleExporters(t *testing.T) {
	e1, e2 := &countExporter{}, &countExporter{}
	pipe := New([]exporter.Exporter{e1, e2}, testLogger(), 16)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pipe.Run(ctx)

	pipe.Results() <- types.ProbeResult{ProbeName: "x", Status: types.StatusSuccess}

	time.Sleep(50 * time.Millisecond)
	cancel()
	pipe.Wait()

	if e1.calls.Load() == 0 || e2.calls.Load() == 0 {
		t.Error("both exporters should have been called")
	}
}

func TestBufferUsage(t *testing.T) {
	pipe := New([]exporter.Exporter{}, testLogger(), 10)

	if got := pipe.BufferUsage(); got != 0.0 {
		t.Errorf("empty buffer: want 0.0, got %v", got)
	}

	// Fill half the buffer without running the pipeline (no consumer).
	for i := 0; i < 5; i++ {
		pipe.results <- types.ProbeResult{ProbeName: "p"}
	}
	if got := pipe.BufferUsage(); got != 0.5 {
		t.Errorf("half-full buffer: want 0.5, got %v", got)
	}
}

func TestPipelineWaitReturnsAfterRun(t *testing.T) {
	pipe := New([]exporter.Exporter{}, testLogger(), 16)
	ctx, cancel := context.WithCancel(context.Background())

	go pipe.Run(ctx)
	cancel()

	done := make(chan struct{})
	go func() {
		pipe.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return within 1s after cancel")
	}
}
