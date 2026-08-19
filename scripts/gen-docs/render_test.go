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
		"[Alpha](items/a.md)", "first summary", "feature", "open",
		"[Beta](items/b.md)", "second summary", "fix", "shipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected table output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestWriteItemTable_EscapesPipeInSummary(t *testing.T) {
	// Title deliberately has no "|" so the row's column count can be
	// checked unambiguously.
	items := []Item{{ID: "a", Title: "Alpha", Summary: "before | after", Kind: "feature", Status: "open"}}
	var b strings.Builder
	writeItemTable(&b, items, false)
	out := b.String()
	if !strings.Contains(out, "before \\| after") {
		t.Errorf("expected escaped pipe in table cell, got:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dataLine := lines[len(lines)-1]
	// Strip the escaped form first, so the remaining "|" count reflects
	// only real, un-escaped pipes. Expect exactly 5 column delimiters for
	// 4 columns: a markdown link "[Alpha](items/a.md)" contributes no pipe
	// of its own, so any 6th pipe means the summary's pipe leaked through
	// escapeCell and split the row into a phantom extra column.
	withoutEscaped := strings.ReplaceAll(dataLine, "\\|", "")
	if got := strings.Count(withoutEscaped, "|"); got != 5 {
		t.Errorf("expected 5 column delimiters in %q, got %d", dataLine, got)
	}
}

// TestRenderRoadmap_ExcludesShippedButListsNonGoals pins the board's membership
// contract. Shipped items leave the board entirely (they belong to Shipped.md
// and, if they are capabilities, Features.md). wont-do items are deliberately
// the exception: they are excluded from the stage sections but listed under
// their own Non-goals heading, because a non-goal reachable only from its own
// item page is invisible to anyone asking "why isn't Pokkum doing X" — which
// defeats the reason for writing it down.
func TestRenderRoadmap_ExcludesShippedButListsNonGoals(t *testing.T) {
	areas := []Area{{
		Name: "Area",
		Items: []Item{
			{ID: "open-item", Title: "Open", Summary: "s", Status: "open", Stage: "v1.1", AreaName: "Area"},
			{ID: "shipped-item", Title: "Shipped", Summary: "s", Status: "shipped", AreaName: "Area"},
			{ID: "wontdo-item", Title: "WontDo", Summary: "s", Status: "wont-do", AreaName: "Area"},
		},
	}}
	out := RenderRoadmap(areas)

	if !strings.Contains(out, "open-item") {
		t.Errorf("expected active item in docs/Roadmap.md output, got:\n%s", out)
	}
	if strings.Contains(out, "shipped-item") {
		t.Errorf("shipped item must not appear in docs/Roadmap.md, got:\n%s", out)
	}

	nonGoalsIdx := strings.Index(out, "## Non-goals")
	if nonGoalsIdx < 0 {
		t.Fatalf("expected a Non-goals section when a wont-do item exists, got:\n%s", out)
	}
	wontDoIdx := strings.Index(out, "wontdo-item")
	if wontDoIdx < 0 {
		t.Fatalf("wont-do item must be listed under Non-goals, got:\n%s", out)
	}
	if wontDoIdx < nonGoalsIdx {
		t.Errorf("wont-do item appeared before the Non-goals heading, i.e. inside a stage section:\n%s", out)
	}
}

// TestRenderRoadmap_NoNonGoalsSectionWhenNone keeps the heading from appearing
// empty, which would read as "there are no non-goals" rather than "none are
// recorded in this source".
func TestRenderRoadmap_NoNonGoalsSectionWhenNone(t *testing.T) {
	areas := []Area{{
		Name:  "Area",
		Items: []Item{{ID: "open-item", Title: "Open", Summary: "s", Status: "open", Stage: "v1.1", AreaName: "Area"}},
	}}
	if out := RenderRoadmap(areas); strings.Contains(out, "## Non-goals") {
		t.Errorf("Non-goals heading must be omitted when no wont-do items exist, got:\n%s", out)
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
	if !strings.Contains(out, "[Target Title](target-id.md)") {
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
