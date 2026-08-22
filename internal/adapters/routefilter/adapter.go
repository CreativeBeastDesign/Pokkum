// Package routefilter implements ports.RouteFilter over routefilterutils,
// keeping the filesystem work behind the hexagonal boundary so internal/core
// depends on the port rather than on the helper.
package routefilter

import (
	"context"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/routefilterutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Adapter is the concrete ports.RouteFilter implementation.
type Adapter struct{}

// NewAdapter returns a RouteFilter backed by routefilterutils.
func NewAdapter() *Adapter { return &Adapter{} }

// FilterRoutes removes matching prerendered routes and reports what that left
// dangling. The dead-link scan runs after removal, so it reflects the tree as
// it will actually be packaged.
func (a *Adapter) FilterRoutes(ctx context.Context, req ports.RouteFilterRequest) (ports.RouteFilterResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.RouteFilterResult{}, err
	}
	res, err := routefilterutils.ApplyExclusions(req.PrerenderedDir, req.Patterns)
	if err != nil {
		return ports.RouteFilterResult{}, err
	}
	out := ports.RouteFilterResult{
		ExcludedRoutes:    res.ExcludedRoutes,
		RemovedFiles:      res.RemovedFiles,
		UnmatchedPatterns: filterHandledPatterns(res.UnmatchedPatterns, req.AlreadyExcluded),
		SkippedSymlinks:   res.SkippedSymlinks,
	}
	dead, err := routefilterutils.FindDeadLinks(req.PrerenderedDir, req.Patterns)
	if err != nil {
		return ports.RouteFilterResult{}, err
	}
	for _, l := range dead {
		out.DeadLinks = append(out.DeadLinks, ports.RouteFilterDeadLink{
			FromPage: l.FromPage, Href: l.Href, Route: l.Route,
		})
	}
	return out, nil
}

var _ ports.RouteFilter = (*Adapter)(nil)

// filterHandledPatterns drops patterns that matched no prerendered file only
// because the build already excluded the route. Without this, the feature
// working perfectly produces a warning saying it did nothing.
func filterHandledPatterns(unmatched, alreadyExcluded []string) []string {
	if len(unmatched) == 0 || len(alreadyExcluded) == 0 {
		return unmatched
	}
	kept := make([]string, 0, len(unmatched))
	for _, p := range unmatched {
		handled := false
		for _, route := range alreadyExcluded {
			if routefilterutils.MatchesAny(route, []string{p}) {
				handled = true
				break
			}
		}
		if !handled {
			kept = append(kept, p)
		}
	}
	return kept
}
