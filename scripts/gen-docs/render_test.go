package main

import (
	"strings"
	"testing"
)

func TestOrderedStages_AscendingWithUnspecifiedLast(t *testing.T) {
	byStage := map[string][]Item{
		"v2.0":    {{ID: "a"}},
		"v1.1":    {{ID: "b"}},
		"":        {{ID: "c"}},
		"backlog": {{ID: "d"}},
	}
	got := orderedStages(byStage, false)
	want := []string{"v1.1", "v2.0", "backlog", ""}
	if !equalStrings(got, want) {
		t.Errorf("orderedStages(ascending) = %v, want %v", got, want)
	}
}

func TestOrderedStages_ReversedWithUnspecifiedStillLast(t *testing.T) {
	byStage := map[string][]Item{
		"v2.0":    {{ID: "a"}},
		"v1.1":    {{ID: "b"}},
		"":        {{ID: "c"}},
		"backlog": {{ID: "d"}},
	}
	got := orderedStages(byStage, true)
	want := []string{"backlog", "v2.0", "v1.1", ""}
	if !equalStrings(got, want) {
		t.Errorf("orderedStages(reversed) = %v, want %v", got, want)
	}
}

func TestOrderedStages_EmptyBucketsOmitted(t *testing.T) {
	byStage := map[string][]Item{
		"v1.1": {{ID: "a"}},
		"v1.2": {}, // present as a key but no items — must not appear
	}
	got := orderedStages(byStage, false)
	want := []string{"v1.1"}
	if !equalStrings(got, want) {
		t.Errorf("orderedStages() = %v, want %v", got, want)
	}
}

// TestWriteItemTable_MultiItemDifferingContent is the row-3 multi-item
// check: two items with genuinely different field values, so a bug that
// e.g. always renders the first item's kind/status for every row would be
// caught instead of hidden by identical fixtures.
func TestWriteItemTable_MultiItemDifferingContent(t *testing.T) {
	items := []Item{
		{ID: "a", Title: "Alpha", Summary: "first summary", Kind: "feature", Status: "open"},
		{ID: "b", Title: "Beta", Summary: "second summary", Kind: "fix", Status: "shipped"},
	}
	var b strings.Builder
	writeItemTable(&b, items, false)
	out := b.String()

	for _, want := range []string{
		"[[items/a|Alpha]]", "first summary", "feature", "open",
		"[[items/b|Beta]]", "second summary", "fix", "shipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected table output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestWriteItemTable_EscapesPipeInSummary(t *testing.T) {
	// Title deliberately has no "|" so the row's column count can be
	// checked unambiguously (a wiki link like [[items/a|A]] legitimately
	// contains an un-escaped "|" of its own, which is not a column
	// delimiter and would otherwise confuse a naive pipe count).
	items := []Item{{ID: "a", Title: "Alpha", Summary: "before | after", Kind: "feature", Status: "open"}}
	var b strings.Builder
	writeItemTable(&b, items, false)
	out := b.String()
	if !strings.Contains(out, "before \\| after") {
		t.Errorf("expected escaped pipe in table cell, got:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dataLine := lines[len(lines)-1]
	// Splitting on an escaped-pipe-aware basis: replace the escaped form
	// with a placeholder first, so the remaining "|" count reflects only
	// real, un-escaped pipes. Expect 6: 5 column delimiters (4 columns)
	// plus the wiki link's own "[[items/a|Alpha]]" separator pipe, which
	// is real markdown syntax, not a table delimiter, and must NOT have
	// been escaped by escapeCell (only the summary's pipe should be).
	withoutEscaped := strings.ReplaceAll(dataLine, "\\|", "")
	if got := strings.Count(withoutEscaped, "|"); got != 6 {
		t.Errorf("expected 6 real pipes (5 column delimiters + 1 wiki-link separator) in %q, got %d", dataLine, got)
	}
}

func TestRenderRoadmap_ExcludesShippedAndWontDo(t *testing.T) {
	areas := []Area{{
		Name: "Area",
		Items: []Item{
			{ID: "open-item", Title: "Open", Summary: "s", Status: "open", AreaName: "Area"},
			{ID: "shipped-item", Title: "Shipped", Summary: "s", Status: "shipped", AreaName: "Area"},
			{ID: "wontdo-item", Title: "WontDo", Summary: "s", Status: "wont-do", AreaName: "Area"},
		},
	}}
	out := RenderRoadmap(areas)
	if !strings.Contains(out, "open-item") {
		t.Errorf("expected active item in Roadmap.md output, got:\n%s", out)
	}
	if strings.Contains(out, "shipped-item") {
		t.Errorf("shipped item must not appear in Roadmap.md, got:\n%s", out)
	}
	if strings.Contains(out, "wontdo-item") {
		t.Errorf("wont-do item must not appear in Roadmap.md, got:\n%s", out)
	}
}

func TestRenderRoadmap_NeedsDecisionSpotlightsAwaitingDecision(t *testing.T) {
	areas := []Area{{
		Name: "Area",
		Items: []Item{
			{ID: "decide-me", Title: "Decide", Summary: "s", Status: "awaiting-decision", Stage: "v1.1", AreaName: "Area"},
			{ID: "just-open", Title: "Open", Summary: "s", Status: "open", Stage: "v1.1", AreaName: "Area"},
		},
	}}
	out := RenderRoadmap(areas)
	decisionSection := out[strings.Index(out, "## Needs decision"):strings.Index(out, "## v1.1")]
	if !strings.Contains(decisionSection, "decide-me") {
		t.Errorf("expected awaiting-decision item in Needs Decision table, got:\n%s", decisionSection)
	}
	if strings.Contains(decisionSection, "just-open") {
		t.Errorf("open (non-awaiting-decision) item must not appear in Needs Decision table, got:\n%s", decisionSection)
	}
	// It should still also appear in its normal stage/area section below.
	if !strings.Contains(out, "decide-me") || strings.Count(out, "decide-me") < 2 {
		t.Errorf("expected awaiting-decision item to also appear in its stage section, got:\n%s", out)
	}
}

func TestRenderShipped_OnlyShippedNewestStageFirst(t *testing.T) {
	areas := []Area{{
		Name: "Area",
		Items: []Item{
			{ID: "old-ship", Title: "Old", Summary: "s", Status: "shipped", Stage: "v1.1", AreaName: "Area"},
			{ID: "new-ship", Title: "New", Summary: "s", Status: "shipped", Stage: "v2.0", AreaName: "Area"},
			{ID: "not-shipped", Title: "Nope", Summary: "s", Status: "open", Stage: "v1.1", AreaName: "Area"},
		},
	}}
	out := RenderShipped(areas)
	if strings.Contains(out, "not-shipped") {
		t.Errorf("non-shipped item must not appear in Shipped.md, got:\n%s", out)
	}
	if idx1, idx2 := strings.Index(out, "new-ship"), strings.Index(out, "old-ship"); idx1 == -1 || idx2 == -1 || idx1 > idx2 {
		t.Errorf("expected newest stage (v2.0) before older stage (v1.1), got:\n%s", out)
	}
}

func TestRenderFeatures_OnlyShippedFeatureKind(t *testing.T) {
	areas := []Area{{
		Name: "Area",
		Items: []Item{
			{ID: "ship-feature", Title: "Feature", Summary: "s", Status: "shipped", Kind: "feature", AreaName: "Area"},
			{ID: "ship-fix", Title: "Fix", Summary: "s", Status: "shipped", Kind: "fix", AreaName: "Area"},
			{ID: "open-feature", Title: "OpenFeat", Summary: "s", Status: "open", Kind: "feature", AreaName: "Area"},
		},
	}}
	out := RenderFeatures(areas)
	if !strings.Contains(out, "ship-feature") {
		t.Errorf("expected shipped feature in Features.md, got:\n%s", out)
	}
	if strings.Contains(out, "ship-fix") {
		t.Errorf("shipped non-feature must not appear in Features.md, got:\n%s", out)
	}
	if strings.Contains(out, "open-feature") {
		t.Errorf("non-shipped feature must not appear in Features.md, got:\n%s", out)
	}
}

func TestCollectLimitations_SortedDeterministically(t *testing.T) {
	areas := []Area{
		{Name: "Area B", Items: []Item{{ID: "zzz", Title: "Z", Limitations: []string{"z-limit"}}}},
		{Name: "Area A", Items: []Item{{ID: "aaa", Title: "A", Limitations: []string{"a-limit-2", "a-limit-1"}}}},
	}
	got := collectLimitations(areas)
	if len(got) != 3 {
		t.Fatalf("expected 3 limitation entries, got %d: %v", len(got), got)
	}
	// Sorted by item id first: "aaa" entries before "zzz" entries.
	if got[0].ItemID != "aaa" || got[1].ItemID != "aaa" || got[2].ItemID != "zzz" {
		t.Errorf("expected limitations grouped/sorted by item id, got %+v", got)
	}
	// Within the same item id, sorted by text.
	if got[0].Text != "a-limit-1" || got[1].Text != "a-limit-2" {
		t.Errorf("expected limitations within an item sorted by text, got %+v", got)
	}
}

func TestRenderItemPage_RelatedShowsTargetTitle(t *testing.T) {
	target := Item{ID: "target-id", Title: "Target Title"}
	src := Item{ID: "src", Title: "Src", Summary: "s", Status: "open", Related: []string{"target-id"}}
	allByID := map[string]Item{"target-id": target}
	out := RenderItemPage(src, allByID)
	if !strings.Contains(out, "[[items/target-id|Target Title]]") {
		t.Errorf("expected related section to link to target's real title, got:\n%s", out)
	}
}

func TestRenderItemPage_LinkDepthIsTwoLevelsUp(t *testing.T) {
	it := Item{ID: "x", Title: "X", Summary: "s", Status: "open", Impl: []string{"internal/adapters/cosign/signer.go"}}
	out := RenderItemPage(it, map[string]Item{})
	if !strings.Contains(out, "(../../internal/adapters/cosign/signer.go)") {
		t.Errorf("expected item page impl link to use ../../ depth, got:\n%s", out)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
