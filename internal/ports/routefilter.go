package ports

import "context"

// RouteFilterRequest asks for a set of prerendered routes to be dropped from a
// built output tree before it is packaged.
type RouteFilterRequest struct {
	// PrerenderedDir is the prerendered output tree to filter.
	PrerenderedDir string
	// Patterns are absolute route paths. A bare path covers the route and its
	// subtree; "*" matches within a segment and "**" across segments.
	Patterns []string
}

// RouteFilterDeadLink is a link in a surviving page pointing at a route that
// was excluded, and which will therefore 404 in the shipped image.
type RouteFilterDeadLink struct {
	FromPage string `json:"from_page"`
	Href     string `json:"href"`
	Route    string `json:"route"`
}

// RouteFilterResult reports what was removed, in deterministic order.
type RouteFilterResult struct {
	ExcludedRoutes []string `json:"excluded_routes,omitempty"`
	RemovedFiles   []string `json:"removed_files,omitempty"`
	// UnmatchedPatterns are patterns that matched no prerendered route —
	// a typo, or a server-rendered route that is compiled into the server
	// bundle and cannot be removed by deleting a file. Either way the route
	// the operator asked to exclude is still reachable, so this is reported
	// rather than dropped.
	UnmatchedPatterns []string `json:"unmatched_patterns,omitempty"`
	// SkippedSymlinks matched an exclusion but were left in place because
	// following them could delete outside the output tree.
	SkippedSymlinks []string              `json:"skipped_symlinks,omitempty"`
	DeadLinks       []RouteFilterDeadLink `json:"dead_links,omitempty"`
}

// RouteFilter defines the boundary port for removing prerendered routes from a
// build's output before packaging.
type RouteFilter interface {
	FilterRoutes(ctx context.Context, req RouteFilterRequest) (RouteFilterResult, error)
}
