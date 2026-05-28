package probe

import (
	"context"

	"github.com/gtataranni/trimon/pkg/types"
)

// Prober executes a probe run and returns one result per probed IP.
// A single-IP probe returns a 1-element slice. A multi-target probe (or a
// hostname that resolves to N IPs) returns N elements.
// Errors are embedded in each ProbeResult (Status/ErrorMsg); Run never fails outright.
// The context must carry a deadline; the scheduler sets one via context.WithTimeout.
type Prober interface {
	Run(ctx context.Context) []types.ProbeResult
	Name() string
	Type() string
}
