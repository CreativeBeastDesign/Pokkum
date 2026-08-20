package main

import (
	"fmt"
	"sort"
	"strings"
)

const (
	roadmapSourceGlob = "docs/roadmap/*.yaml"
	docsFromDir       = "docs"       // repo-relative dir docs/Roadmap.md etc. live in
	itemsFromDir      = "docs/items" // repo-relative dir docs/items/*.md live in
	// findingsFromPath is the retired overnight bug log. It moved under
	// docs/archive/ when the hand-maintained status docs were retired; the
	// generator still reads it to validate evidence.findings numbers.
	findingsFromPath = "docs/archive/overnight-findings.md"
)

// --- filtering / grouping helpers -------------------------------------

func filterActive(items []Item) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if it.Status != "shipped" && it.Status != "wont-do" {
			out = append(out, it)
		}
	}
	return out
}

func filterStatus(items []Item, status string) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if it.Status == status {
			out = append(out, it)
		}
	}
	return out
}

// filterShippedFeatures selects what a reader would call a capability:
// shipped items of kind "feature" or "hardening". Hardening is included
// deliberately — a shipped security capability (trust-root refresh, a
// fail-closed verification path) is something a user chooses Pokkum for, and
// filtering it out left real shipped work visible only in Shipped.md, which
// two independent readers flagged as surprising. Kinds "fix" and "infra" stay
// out: a bug fix or a CI guard is history, not a capability, and belongs in
// Shipped.md alone.
func filterShippedFeatures(items []Item) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if it.Status != "shipped" {
			continue
		}
		if it.Kind == "feature" || it.Kind == "hardening" {
			out = append(out, it)
		}
	}
	return out
}

func sortByID(items []Item) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
}

func groupByStage(items []Item) map[string][]Item {
	m := map[string][]Item{}
	for _, it := range items {
		m[it.Stage] = append(m[it.Stage], it)
	}
	return m
}

func groupByArea(items []Item) map[string][]Item {
	m := map[string][]Item{}
	for _, it := range items {
		m[it.AreaName] = append(m[it.AreaName], it)
	}
	return m
}

func sortedAreaNames(m map[string][]Item) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// orderedStages returns the stage keys actually present in byStage, walked
// in stageOrder (or its reverse), with the "no stage set" bucket ("")
// always placed last regardless of direction — it isn't part of the
// ordinal stage sequence, so there's no well-defined "newest"/"nearest"
// position for it in either direction.
func orderedStages(byStage map[string][]Item, reverse bool) []string {
	order := append([]string{}, stageOrder...)
	if reverse {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	var out []string
	for _, s := range order {
		if len(byStage[s]) > 0 {
			out = append(out, s)
		}
	}
	if len(byStage[""]) > 0 {
		out = append(out, "")
	}
	return out
}

func stageHeading(stage string) string {
	if stage == "" {
		return "Unscheduled"
	}
	return stage
}

// --- table rendering ----------------------------------------------------

// writeItemTable renders the standard Title/Summary/Kind/Status table
// (optionally with a trailing Commits column) for docs/Roadmap.md and
// docs/Shipped.md.
func writeItemTable(b *strings.Builder, items []Item, withCommits bool) {
	if withCommits {
		b.WriteString("| Title | Summary | Kind | Status | Commits |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
	} else {
		b.WriteString("| Title | Summary | Kind | Status |\n")
		b.WriteString("| --- | --- | --- | --- |\n")
	}
	for _, it := range items {
		cols := []string{
			itemLink(docsFromDir, it.ID, it.Title),
			escapeCell(it.Summary),
			escapeCell(it.Kind),
			escapeCell(it.Status),
		}
		if withCommits {
			cols = append(cols, escapeCell(formatCommits(it.Evidence.Commits)))
		}
		b.WriteString("| " + strings.Join(cols, " | ") + " |\n")
	}
	b.WriteString("\n")
}

// --- docs/Roadmap.md -----------------------------------------------------

// RenderRoadmap renders every active item (status not shipped/wont-do): a
// "Needs decision" spotlight table for status: awaiting-decision items
// first, then the complete active set grouped by stage and, within each
// stage, by area.
func RenderRoadmap(areas []Area) string {
	// docs/*.md sit one level below the repo root; resolve item: refs to that depth.
	areas = resolveRefsInAreas(docsFromDir, areas)
	items := filterActive(allItems(areas))

	var b strings.Builder
	b.WriteString(generatedHeader(roadmapSourceGlob))
	b.WriteString("# Roadmap\n\n")

	needsDecision := filterStatus(items, "awaiting-decision")
	sortByID(needsDecision)
	b.WriteString("## Needs decision\n\n")
	if len(needsDecision) == 0 {
		b.WriteString("_None._\n\n")
	} else {
		writeItemTable(&b, needsDecision, false)
	}

	byStage := groupByStage(items)
	for _, stage := range orderedStages(byStage, false) {
		fmt.Fprintf(&b, "## %s\n\n", stageHeading(stage))
		byArea := groupByArea(byStage[stage])
		for _, areaName := range sortedAreaNames(byArea) {
			fmt.Fprintf(&b, "### %s\n\n", areaName)
			areaItems := byArea[areaName]
			sortByID(areaItems)
			writeItemTable(&b, areaItems, false)
		}
	}

	// Non-goals are listed on the board deliberately. filterActive excludes
	// wont-do, which left them reachable only via their own item page — and
	// nobody lands there without already knowing the id. The whole point of
	// writing a non-goal down is that it reads as a decision rather than an
	// omission, which requires it being visible where someone looks for
	// "why isn't Pokkum doing X".
	nonGoals := filterStatus(allItems(areas), "wont-do")
	if len(nonGoals) > 0 {
		sortByID(nonGoals)
		b.WriteString("## Non-goals\n\n")
		b.WriteString("Deliberate decisions, not gaps. Each item page states the reasoning.\n\n")
		writeItemTable(&b, nonGoals, false)
	}

	return b.String()
}

// --- docs/Shipped.md -----------------------------------------------------

// RenderShipped renders every status: shipped item, newest stage first
// (stageOrder walked in reverse), grouped by area within each stage, with
// an added Commits column.
func RenderShipped(areas []Area) string {
	// docs/*.md sit one level below the repo root; resolve item: refs to that depth.
	areas = resolveRefsInAreas(docsFromDir, areas)
	items := filterStatus(allItems(areas), "shipped")

	var b strings.Builder
	b.WriteString(generatedHeader(roadmapSourceGlob))
	b.WriteString("# Shipped\n\n")

	byStage := groupByStage(items)
	for _, stage := range orderedStages(byStage, true) {
		fmt.Fprintf(&b, "## %s\n\n", stageHeading(stage))
		byArea := groupByArea(byStage[stage])
		for _, areaName := range sortedAreaNames(byArea) {
			fmt.Fprintf(&b, "### %s\n\n", areaName)
			areaItems := byArea[areaName]
			sortByID(areaItems)
			writeItemTable(&b, areaItems, true)
		}
	}

	if len(items) == 0 {
		b.WriteString("_Nothing shipped yet._\n\n")
	}

	return b.String()
}

// --- docs/Features.md ----------------------------------------------------

// RenderFeatures renders every status: shipped, kind: feature item grouped
// by area, followed by an aggregated Known Limitations section built from
// every item's limitations (regardless of status/kind — a limitation can
// be worth surfacing even before or after a feature's "shipped feature"
// window), each attributed to its item.
func RenderFeatures(areas []Area) string {
	// docs/*.md sit one level below the repo root; resolve item: refs to that depth.
	areas = resolveRefsInAreas(docsFromDir, areas)
	items := filterShippedFeatures(allItems(areas))

	var b strings.Builder
	b.WriteString(generatedHeader(roadmapSourceGlob))
	b.WriteString("# Features\n\n")

	byArea := groupByArea(items)
	if len(byArea) == 0 {
		b.WriteString("_No shipped features yet._\n\n")
	}
	for _, areaName := range sortedAreaNames(byArea) {
		fmt.Fprintf(&b, "## %s\n\n", areaName)
		areaItems := byArea[areaName]
		sortByID(areaItems)
		for _, it := range areaItems {
			fmt.Fprintf(&b, "### %s\n\n", itemLink(docsFromDir, it.ID, it.Title))
			b.WriteString(it.Summary + "\n\n")
			if len(it.Flags) > 0 {
				fmt.Fprintf(&b, "- Flags: %s\n", formatFlags(it.Flags))
			}
			if len(it.Impl) > 0 {
				b.WriteString("- Implementation:\n")
				for _, path := range it.Impl {
					fmt.Fprintf(&b, "  - [%s](%s)\n", path, relLink(docsFromDir, path))
				}
			}
			b.WriteString("\n")
		}
	}

	// Grouped by area rather than one flat list: a single global list was
	// readable with three items and becomes unusable as areas land content,
	// which is the state this document is heading into.
	b.WriteString("## Known Limitations\n\n")
	limitations := collectLimitations(areas)
	if len(limitations) == 0 {
		b.WriteString("_None recorded._\n\n")
	} else {
		byLimArea := make(map[string][]limitationEntry)
		for _, l := range limitations {
			byLimArea[l.AreaName] = append(byLimArea[l.AreaName], l)
		}
		names := make([]string, 0, len(byLimArea))
		for name := range byLimArea {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&b, "### %s\n\n", name)
			for _, l := range byLimArea[name] {
				fmt.Fprintf(&b, "- %s (%s)\n", l.Text, itemLink(docsFromDir, l.ItemID, l.Title))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

type limitationEntry struct {
	ItemID   string
	Title    string
	Text     string
	AreaName string
}

// collectLimitations aggregates every item's limitations across every
// area, sorted by item id then by limitation text — this is a collection
// built by iterating every item, so (per this repo's determinism
// convention) it must be sorted explicitly rather than left in whatever
// order allItems/area-file-glob order happens to produce.
func collectLimitations(areas []Area) []limitationEntry {
	var out []limitationEntry
	for _, it := range allItems(areas) {
		for _, lim := range it.Limitations {
			out = append(out, limitationEntry{ItemID: it.ID, Title: it.Title, Text: lim, AreaName: it.AreaName})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ItemID != out[j].ItemID {
			return out[i].ItemID < out[j].ItemID
		}
		return out[i].Text < out[j].Text
	})
	return out
}

// --- docs/items/<id>.md ---------------------------------------------------

// RenderItemPage renders the full detail page for a single item. allByID
// is the full id->item lookup table (already validated to have no dangling
// related references) so Related links can show the target item's real
// title instead of just repeating its id.
func RenderItemPage(it Item, allByID map[string]Item) string {
	// An item page sits in docs/items/, two levels below the repo root.
	it = resolveRefsInItem(itemsFromDir, it)
	var b strings.Builder
	b.WriteString(generatedHeader(fmt.Sprintf("%s (item id: %s)", roadmapSourceGlob, it.ID)))
	fmt.Fprintf(&b, "# %s\n\n", it.Title)

	b.WriteString("| Field | Value |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| Status | %s |\n", escapeCell(it.Status))
	if it.Stage != "" {
		fmt.Fprintf(&b, "| Stage | %s |\n", escapeCell(it.Stage))
	}
	if it.Kind != "" {
		fmt.Fprintf(&b, "| Kind | %s |\n", escapeCell(it.Kind))
	}
	if it.Tier != "" {
		fmt.Fprintf(&b, "| Tier | %s |\n", escapeCell(it.Tier))
	}
	if it.AreaName != "" {
		fmt.Fprintf(&b, "| Area | %s |\n", escapeCell(it.AreaName))
	}
	b.WriteString("\n")

	b.WriteString("## Summary\n\n")
	b.WriteString(strings.TrimSpace(it.Summary) + "\n\n")

	if strings.TrimSpace(it.Problem) != "" {
		b.WriteString("## Problem\n\n")
		b.WriteString(strings.TrimSpace(it.Problem) + "\n\n")
	}

	if len(it.Options) > 0 {
		b.WriteString("## Options\n\n")
		b.WriteString("| Option | Description | Tradeoffs |\n| --- | --- | --- |\n")
		for _, opt := range it.Options {
			fmt.Fprintf(&b, "| %s | %s | %s |\n",
				escapeCell(opt.Title), escapeCell(opt.Description), escapeCell(opt.Tradeoffs))
		}
		b.WriteString("\n")
	}

	if strings.TrimSpace(it.Recommendation) != "" {
		b.WriteString("## Recommendation\n\n")
		b.WriteString(strings.TrimSpace(it.Recommendation) + "\n\n")
	}

	if strings.TrimSpace(it.Decision) != "" {
		b.WriteString("## Decision\n\n")
		b.WriteString(strings.TrimSpace(it.Decision) + "\n\n")
	}

	if len(it.Flags) > 0 {
		b.WriteString("## Flags\n\n")
		for _, f := range it.Flags {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}

	if len(it.Impl) > 0 {
		b.WriteString("## Implementation\n\n")
		b.WriteString(implLinks(itemsFromDir, it.Impl))
		b.WriteString("\n")
	}

	if len(it.Evidence.Commits) > 0 || len(it.Evidence.Findings) > 0 {
		b.WriteString("## Evidence\n\n")
		if len(it.Evidence.Commits) > 0 {
			fmt.Fprintf(&b, "- Commits: %s\n", formatCommits(it.Evidence.Commits))
		}
		if len(it.Evidence.Findings) > 0 {
			fmt.Fprintf(&b, "- Findings: %s (see [overnight-findings.md](%s))\n", formatFindings(it.Evidence.Findings), relLink(itemsFromDir, findingsFromPath))
		}
		b.WriteString("\n")
	}

	if len(it.Limitations) > 0 {
		b.WriteString("## Known Limitations\n\n")
		for _, l := range it.Limitations {
			fmt.Fprintf(&b, "- %s\n", l)
		}
		b.WriteString("\n")
	}

	if len(it.Related) > 0 {
		b.WriteString("## Related\n\n")
		for _, rel := range it.Related {
			title := rel
			if target, ok := allByID[rel]; ok {
				title = target.Title
			}
			fmt.Fprintf(&b, "- %s\n", itemLink(itemsFromDir, rel, title))
		}
		b.WriteString("\n")
	}

	return b.String()
}
