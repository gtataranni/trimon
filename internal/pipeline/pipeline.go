package pipeline

import (
	"context"
	"log/slog"

	"github.com/gtataranni/trimon/internal/exporter"
	"github.com/gtataranni/trimon/pkg/types"
)

// Pipeline fans results from a shared channel out to all registered exporters.
type Pipeline struct {
	results       chan types.ProbeResult
	exporters     []exporter.Exporter
	logger        *slog.Logger
	done          chan struct{}
	onExportError func(ctx context.Context, exporterName string)
}

// New creates a Pipeline with a buffered results channel of the given size.
func New(exporters []exporter.Exporter, logger *slog.Logger, bufferSize int) *Pipeline {
	return &Pipeline{
		results:   make(chan types.ProbeResult, bufferSize),
		exporters: exporters,
		logger:    logger,
		done:      make(chan struct{}),
	}
}

// BufferUsage returns the fraction of the results channel currently occupied (0.0–1.0).
func (p *Pipeline) BufferUsage() float64 {
	return float64(len(p.results)) / float64(cap(p.results))
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
					// Use Background so exporters can flush results even though the parent context is cancelled.
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

// SetExportErrorRecorder registers a callback invoked whenever an exporter
// returns an error. exporterName is the value returned by the failing exporter's
// Name() method. Intended for recording trimon.exporter.errors via the OTLP exporter.
func (p *Pipeline) SetExportErrorRecorder(fn func(ctx context.Context, exporterName string)) {
	p.onExportError = fn
}

func (p *Pipeline) dispatch(ctx context.Context, result types.ProbeResult) {
	for _, exp := range p.exporters {
		if err := exp.Export(ctx, result); err != nil {
			p.logger.Error("exporter error", "exporter", exp.Name(), "error", err)
			if p.onExportError != nil {
				p.onExportError(ctx, exp.Name())
			}
		}
	}
}
