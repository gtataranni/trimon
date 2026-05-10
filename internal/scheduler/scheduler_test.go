package scheduler

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtataranni/trimon/internal/probe"
	"github.com/gtataranni/trimon/pkg/types"
)

// countProbe counts how many times Run is called.
type countProbe struct {
	name  string
	runs  atomic.Int64
	delay time.Duration
}

func (p *countProbe) Name() string { return p.name }
func (p *countProbe) Type() string { return "test" }
func (p *countProbe) Run(_ context.Context) (types.ProbeResult, error) {
	p.runs.Add(1)
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	return types.ProbeResult{ProbeName: p.name, Status: types.StatusSuccess}, nil
}

func testFactory(probes map[string]*countProbe) ProberFactory {
	return func(cfg types.ProbeConfig) (probe.Prober, error) {
		p := &countProbe{name: cfg.Name}
		probes[cfg.Name] = p
		return p, nil
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSchedulerStartStop(t *testing.T) {
	results := make(chan types.ProbeResult, 100)
	probeMap := make(map[string]*countProbe)
	sched := New(testFactory(probeMap), results, testLogger())

	cfg := types.ProbeConfig{
		Name:     "p1",
		Type:     "test",
		Target:   "127.0.0.1",
		SourceIP: "127.0.0.1",
		Interval: 50 * time.Millisecond,
		Timeout:  time.Second,
		Count:    1,
	}

	sched.Start([]types.ProbeConfig{cfg})

	time.Sleep(200 * time.Millisecond)

	sched.Stop()

	if sched.WorkerCount() != 0 {
		t.Errorf("expected 0 workers after Stop, got %d", sched.WorkerCount())
	}

	runs := probeMap["p1"].runs.Load()
	if runs == 0 {
		t.Error("expected probe to have run at least once")
	}
}

func TestSchedulerWorkerCount(t *testing.T) {
	results := make(chan types.ProbeResult, 100)
	probeMap := make(map[string]*countProbe)
	sched := New(testFactory(probeMap), results, testLogger())

	cfgs := []types.ProbeConfig{
		{Name: "a", Type: "test", SourceIP: "127.0.0.1", Interval: time.Minute, Timeout: time.Second, Count: 1},
		{Name: "b", Type: "test", SourceIP: "127.0.0.1", Interval: time.Minute, Timeout: time.Second, Count: 1},
		{Name: "c", Type: "test", SourceIP: "127.0.0.1", Interval: time.Minute, Timeout: time.Second, Count: 1},
	}
	sched.Start(cfgs)

	if got := sched.WorkerCount(); got != 3 {
		t.Errorf("expected 3 workers, got %d", got)
	}

	sched.Stop()

	if got := sched.WorkerCount(); got != 0 {
		t.Errorf("expected 0 workers after stop, got %d", got)
	}
}

func TestSchedulerReload(t *testing.T) {
	results := make(chan types.ProbeResult, 100)
	probeMap := make(map[string]*countProbe)
	sched := New(testFactory(probeMap), results, testLogger())

	initial := []types.ProbeConfig{
		{Name: "keep", Type: "test", SourceIP: "127.0.0.1", Interval: time.Minute, Timeout: time.Second, Count: 1},
		{Name: "remove", Type: "test", SourceIP: "127.0.0.1", Interval: time.Minute, Timeout: time.Second, Count: 1},
	}
	sched.Start(initial)

	if got := sched.WorkerCount(); got != 2 {
		t.Fatalf("expected 2 workers, got %d", got)
	}

	reloaded := []types.ProbeConfig{
		{Name: "keep", Type: "test", SourceIP: "127.0.0.1", Interval: time.Minute, Timeout: time.Second, Count: 1},
		{Name: "new", Type: "test", SourceIP: "127.0.0.1", Interval: time.Minute, Timeout: time.Second, Count: 1},
	}
	sched.Reload(reloaded)

	if got := sched.WorkerCount(); got != 2 {
		t.Errorf("expected 2 workers after reload, got %d", got)
	}

	sched.Stop()
}
