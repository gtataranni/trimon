package pipeline

import (
	"context"
	"log/slog"

	"github.com/gtataranni/trimon/internal/exporter"
	"github.com/gtataranni/trimon/pkg/types"
)

const bufferSize = 1000

// Pipeline fans results from a shared channel out to all registered exporters.
type Pipeline struct {
	results   chan types.ProbeResult
	exporters []exporter.Exporter
	logger    *slog.Logger
	done      chan struct{}
}

// New creates a Pipeline with a buffered results channel of size 1000.
func New(exporters []exporter.Exporter, logger *slog.Logger) *Pipeline {
	return &Pipeline{
		results:   make(chan types.ProbeResult, bufferSize),
		exporters: exporters,
		logger:    logger,
		done:      make(chan struct{}),
	}
}

// Results returns the write end of the results channel for probe goroutines.
func (p *Pipeline) Results() chan<- types.ProbeResult {
	return p.results
}

// Run reads from the results channel and dispatches to all exporters.
// It blocks until ctx is cancelled, then drains remaining results.
func (p *Pipeline) Run(ctx context.Context) {
	defer close(p.done)
	for {
		select {
		case result, ok := <-p.results:
			if !ok {
				return
			}
			p.dispatch(ctx, result)
		case <-ctx.Done():
			// Drain buffered results before exiting.
			for {
				select {
				case result := <-p.results:
					p.dispatch(context.Background(), result)
				default:
					return
				}
			}
		}
	}
}

// Wait blocks until Run has returned.
func (p *Pipeline) Wait() {
	<-p.done
}

func (p *Pipeline) dispatch(ctx context.Context, result types.ProbeResult) {
	for _, exp := range p.exporters {
		if err := exp.Export(ctx, result); err != nil {
			p.logger.Error("exporter error", "error", err)
		}
	}
}
