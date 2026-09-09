// Package staticviability implements ports.StaticViabilityAnalyzer over
// sveltekitutils, keeping the filesystem work behind the hexagonal boundary so
// internal/core depends on the port rather than on the helper.
package staticviability

import (
	"context"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Adapter is the concrete ports.StaticViabilityAnalyzer implementation.
type Adapter struct{}

var _ ports.StaticViabilityAnalyzer = (*Adapter)(nil)

// NewAdapter returns a StaticViabilityAnalyzer backed by sveltekitutils.
func NewAdapter() *Adapter { return &Adapter{} }

// AnalyzeStaticViability reports what, if anything, rules a static build out.
//
// Cancellation is honoured before the walk starts rather than threaded through
// it: the scan reads a bounded set of small route sources and completes in
// milliseconds, so a mid-walk cancellation check would add a parameter to every
// classifier for no observable benefit. A cancelled context yields
// StaticUnknown — the verdict that means "no evidence", which callers are
// required not to act on.
func (a *Adapter) AnalyzeStaticViability(ctx context.Context, req ports.StaticViabilityRequest) ports.StaticReport {
	if err := ctx.Err(); err != nil {
		return ports.StaticReport{
			Verdict:      ports.StaticUnknown,
			UndecidedWhy: "the build was cancelled before the project could be scanned",
		}
	}
	return sveltekitutils.AnalyzeStaticViability(req.ProjectDir)
}
