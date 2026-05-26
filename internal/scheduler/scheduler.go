package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gtataranni/trimon/internal/probe"
	"github.com/gtataranni/trimon/pkg/types"
)

// DroppedResultRecorder is called when a probe result is dropped because the
// results channel is full. probeName identifies the probe that produced the
// dropped result.
type DroppedResultRecorder func(ctx context.Context, probeName string)

// ProberFactory constructs a Prober from a ProbeConfig.
type ProberFactory func(cfg types.ProbeConfig) (probe.Prober, error)

// Scheduler manages one goroutine per probe and fans results into Results.
type Scheduler struct {
	factory          ProberFactory
	results          chan<- types.ProbeResult
	logger           *slog.Logger
	onDroppedResult  DroppedResultRecorder

	mu      sync.Mutex
	workers map[string]*worker
}

type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a Scheduler. results must be a buffered channel owned by the caller.
func New(factory ProberFactory, results chan<- types.ProbeResult, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		factory: factory,
		results: results,
		logger:  logger,
		workers: make(map[string]*worker),
	}
}

// Start launches goroutines for all probes in cfgs.
func (s *Scheduler) Start(cfgs []types.ProbeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cfg := range cfgs {
		s.startLocked(cfg)
	}
}

// Reload stops all running probes and starts the new set from cfgs.
func (s *Scheduler) Reload(cfgs []types.ProbeConfig) {
	s.Stop()
	s.Start(cfgs)
}

// Stop shuts down all probe goroutines and waits for them to exit.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.workers))
	dones := make([]chan struct{}, 0, len(s.workers))
	for name, w := range s.workers {
		cancels = append(cancels, w.cancel)
		dones = append(dones, w.done)
		delete(s.workers, name)
	}
	s.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, done := range dones {
		<-done
	}
}

// WorkerCount returns the number of currently running probe goroutines.
func (s *Scheduler) WorkerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.workers)
}

// SetDroppedResultRecorder registers a callback invoked whenever a probe result
// is dropped because the results channel is full. Must be called before Start.
func (s *Scheduler) SetDroppedResultRecorder(fn DroppedResultRecorder) {
	s.onDroppedResult = fn
}

// startLocked must be called with s.mu held.
func (s *Scheduler) startLocked(cfg types.ProbeConfig) {
	p, err := s.factory(cfg)
	if err != nil {
		s.logger.Error("failed to create probe", "name", cfg.Name, "error", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{cancel: cancel, done: make(chan struct{})}
	s.workers[cfg.Name] = w

	go func() {
		defer close(w.done)
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCtx, runCancel := context.WithTimeout(ctx, cfg.Timeout)
				result, runErr := p.Run(runCtx)
				runCancel()
				if runErr != nil {
					s.logger.Error("probe run error", "name", cfg.Name, "error", runErr)
				}
				select {
				case s.results <- result:
				default:
					s.logger.Warn("results channel full, dropping result", "probe", cfg.Name)
					if s.onDroppedResult != nil {
						s.onDroppedResult(ctx, cfg.Name)
					}
				}
			}
		}
	}()

	s.logger.Info("probe started", "name", cfg.Name, "interval", cfg.Interval)
}

