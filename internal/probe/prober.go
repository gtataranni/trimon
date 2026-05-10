package probe

import (
	"context"

	"github.com/gtataranni/trimon/pkg/types"
)

// Prober executes a single probe run and returns a ProbeResult.
type Prober interface {
	Run(ctx context.Context) (types.ProbeResult, error)
	Name() string
	Type() string
}
