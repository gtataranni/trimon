package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gtataranni/trimon/internal/probe"
	"github.com/gtataranni/trimon/pkg/types"
)

// ProberFactory constructs a Prober from a ProbeConfig.
type ProberFactory func(cfg types.ProbeConfig) (probe.Prober, error)

// Scheduler manages one goroutine per probe and fans results into Results.
type Scheduler struct {
	factory ProberFactory
	results chan<- types.ProbeResult
	logger  *slog.Logger

	mu      sync.Mutex
	workers map[string]*worker
}

type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
	cfg    types.ProbeConfig
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

// Reload diffs new against running probes: stops removed, starts new, restarts changed.
func (s *Scheduler) Reload(cfgs []types.ProbeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	incoming := make(map[string]types.ProbeConfig, len(cfgs))
	for _, cfg := range cfgs {
		incoming[cfg.Name] = cfg
	}

	// Stop probes that are gone or changed.
	for name, w := range s.workers {
		newCfg, exists := incoming[name]
		if !exists || configChanged(w.cfg, newCfg) {
			s.stopLocked(name)
		}
	}

	// Start probes that are new or were just stopped due to change.
	for _, cfg := range cfgs {
		if _, running := s.workers[cfg.Name]; !running {
			s.startLocked(cfg)
		}
	}
}

// Stop shuts down all probe goroutines and waits for them to exit.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	names := make([]string, 0, len(s.workers))
	for name := range s.workers {
		names = append(names, name)
	}
	s.mu.Unlock()

	for _, name := range names {
		s.mu.Lock()
		s.stopLocked(name)
		s.mu.Unlock()
	}
}

// WorkerCount returns the number of currently running probe goroutines.
func (s *Scheduler) WorkerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.workers)
}

// startLocked must be called with s.mu held.
func (s *Scheduler) startLocked(cfg types.ProbeConfig) {
	p, err := s.factory(cfg)
	if err != nil {
		s.logger.Error("failed to create probe", "name", cfg.Name, "error", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{cancel: cancel, done: make(chan struct{}), cfg: cfg}
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
				}
			}
		}
	}()

	s.logger.Info("probe started", "name", cfg.Name, "interval", cfg.Interval)
}

// stopLocked cancels the worker and waits for exit. Must be called with s.mu held.
func (s *Scheduler) stopLocked(name string) {
	w, ok := s.workers[name]
	if !ok {
		return
	}
	w.cancel()
	delete(s.workers, name)
	// Wait outside the lock to avoid deadlock if the goroutine tries to acquire it.
	s.mu.Unlock()
	<-w.done
	s.mu.Lock()
	s.logger.Info("probe stopped", "name", name)
}

func configChanged(old, new types.ProbeConfig) bool {
	return old.Target != new.Target ||
		old.SourceIP != new.SourceIP ||
		old.Interval != new.Interval ||
		old.Timeout != new.Timeout ||
		old.Count != new.Count
}
