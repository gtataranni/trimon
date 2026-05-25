package exporter

import (
	"context"

	"github.com/gtataranni/trimon/pkg/types"
)

// Exporter receives ProbeResults and forwards them to a sink.
// Implementations must be safe for concurrent use.
type Exporter interface {
	Name() string
	Export(ctx context.Context, result types.ProbeResult) error
	Close() error
}
