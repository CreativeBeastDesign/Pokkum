// Command gen-docs renders docs/Roadmap.md, docs/Shipped.md, docs/Features.md,
// and docs/items/<id>.md from the machine-readable source of truth in
// docs/roadmap/*.yaml.
//
// Why this exists: before this tool, "where does the project stand" meant
// reading and reconciling up to eight hand-maintained markdown files
// (Roadmap.md, overnight-findings.md, CodeReview.md, AdditionalFeatures.md,
// Feature-list.md, Roadmap-v1-Archive.md, fixes-to-v1.md, Meantime.md — all now
// retired under docs/archive/), and they drifted from each other and from the code repeatedly — this project's
// single most-repeated failure mode (see Lessons.md). docs/roadmap/*.yaml is
// now the one source; everything else in docs/ is generated and must never
// be hand-edited.
//
// Usage:
//
//	go run ./scripts/gen-docs      # regenerate docs/Roadmap.md, Shipped.md,
//	                                # Features.md, and docs/items/*.md
//	make docs                      # same, via the Makefile target
package main

// Area is one docs/roadmap/<area>.yaml file: a display-name grouping plus
// its items. One file per area lets multiple people/agents edit different
// areas without merge conflicts.
type Area struct {
	Name  string `yaml:"area"`
	Items []Item `yaml:"items"`
}

// Item is a single roadmap/shipped/decision entry. Every field is optional
// except ID, Title, Status, and Summary — see validateItem in validate.go
// for the enforcement of that contract.
type Item struct {
	ID             string   `yaml:"id"`
	Title          string   `yaml:"title"`
	Stage          string   `yaml:"stage,omitempty"`
	Status         string   `yaml:"status"`
	Kind           string   `yaml:"kind,omitempty"`
	Tier           string   `yaml:"tier,omitempty"`
	Summary        string   `yaml:"summary"`
	Problem        string   `yaml:"problem,omitempty"`
	Options        []Option `yaml:"options,omitempty"`
	Recommendation string   `yaml:"recommendation,omitempty"`
	Decision       string   `yaml:"decision,omitempty"`
	Flags          []string `yaml:"flags,omitempty"`
	Impl           []string `yaml:"impl,omitempty"`
	Evidence       Evidence `yaml:"evidence,omitempty"`
	Limitations    []string `yaml:"limitations,omitempty"`
	Related        []string `yaml:"related,omitempty"`

	// AreaName and SourceFile are populated by LoadAreas from the
	// enclosing Area, not decoded from the item's own YAML — hence
	// yaml:"-", which also keeps them invisible to KnownFields(true)
	// strict decoding.
	AreaName   string `yaml:"-"`
	SourceFile string `yaml:"-"`
}

// Option is one row of an item's decision table.
type Option struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description,omitempty"`
	Tradeoffs   string `yaml:"tradeoffs,omitempty"`
}

// Evidence links an item back to the commits that implemented it and the
// numbered findings (docs/archive/overnight-findings.md's "### N." entries) that
// motivated it.
type Evidence struct {
	Commits  []string `yaml:"commits,omitempty"`
	Findings []int    `yaml:"findings,omitempty"`
}

// Enumerated field values. Keep in sync with the schema documentation in
// the task brief and with Vocabulary.md once real content is migrated.
var (
	validStatuses = map[string]bool{
		"open":              true,
		"in-progress":       true,
		"awaiting-decision": true,
		"shipped":           true,
		"wont-do":           true,
	}
	validKinds = map[string]bool{
		"feature":   true,
		"hardening": true,
		"fix":       true,
		"dx":        true,
		"infra":     true,
	}
	validTiers = map[string]bool{
		"moat":       true,
		"foundation": true,
		"polish":     true,
		"non-goal":   true,
	}
	// stageOrder is the canonical, ascending "nearest-term first" order
	// used to group docs/Roadmap.md. docs/Shipped.md walks it in reverse
	// ("newest stage first"). Items with no stage set are always placed
	// in a final, separate bucket regardless of direction — see
	// orderedStages in render.go.
	stageOrder = []string{"v1.1", "v1.2", "v2.0", "backlog"}
)

func isValidStage(s string) bool {
	for _, v := range stageOrder {
		if v == s {
			return true
		}
	}
	return false
}
