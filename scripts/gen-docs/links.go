package main

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// escapeCell makes arbitrary item text (summaries, kinds, statuses) safe to
// embed as a single markdown table cell: newlines are collapsed to a single
// space (a real newline would terminate the table row) and literal "|"
// characters are escaped so they don't get parsed as a new column
// boundary. Backticks are left untouched — an inline code span in a table
// cell renders fine as long as its own content has no unescaped "|", which
// is why pipe-escaping runs unconditionally rather than skipping text
// inside backticks.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}

// itemLink renders a doc-to-doc link to an item page as a standard markdown
// link, which resolves in both an IDE and on GitHub (a [[wiki]] link resolves
// only in the former).
//
// fromDir is the repo-relative directory the *linking* document lives in, so
// the path is correct from both places item links appear: "docs" (Roadmap,
// Shipped, Features) yields items/<id>.md, while "docs/items" (an item page's
// own Related section) yields the sibling <id>.md. Hardcoding the items/
// prefix produced docs/items/items/<id>.md from item pages — a dead link.
//
// The title is escaped like any other table cell, since these links appear
// inside tables.
func itemLink(fromDir, id, title string) string {
	target := path.Join(itemsFromDir, id+".md")
	return fmt.Sprintf("[%s](%s)", escapeCell(title), relLink(fromDir, target))
}

// relLink computes the relative markdown link from a generated doc file to
// a repo-relative source path, e.g. relLink("docs", "internal/x/y.go") ->
// "../internal/x/y.go", and relLink("docs/items", "internal/x/y.go") ->
// "../../internal/x/y.go". fromDir is the repo-relative directory the
// *linking* document lives in; targetRepoRelPath is the repo-relative path
// being linked to. Every doc->code link in this generator goes through this
// function, since the depth differs between docs/*.md and docs/items/*.md.
func relLink(fromDir, targetRepoRelPath string) string {
	rel, err := filepath.Rel(fromDir, targetRepoRelPath)
	if err != nil {
		// Should be unreachable for the fixed fromDir values this tool
		// uses ("docs", "docs/items") against any repo-relative target;
		// fall back to the raw target rather than panicking.
		return targetRepoRelPath
	}
	return filepath.ToSlash(rel)
}

// implLinks renders a bullet list of markdown links from fromDir to every
// path in impl, in source order (already deterministic — it's a YAML
// sequence, not a map).
func implLinks(fromDir string, impl []string) string {
	var b strings.Builder
	for _, path := range impl {
		fmt.Fprintf(&b, "- [%s](%s)\n", path, relLink(fromDir, path))
	}
	return b.String()
}

// formatCommits renders a list of short SHAs as code-formatted, comma
// separated text, e.g. "`2f03609`, `e918c52`". No repository URL is
// invented — plain code-formatted SHAs only, per the schema's link
// conventions.
func formatCommits(commits []string) string {
	parts := make([]string, 0, len(commits))
	for _, c := range commits {
		parts = append(parts, "`"+c+"`")
	}
	return strings.Join(parts, ", ")
}

// formatFindings renders a list of docs/archive/overnight-findings.md entry numbers,
// e.g. "#1, #14".
func formatFindings(findings []int) string {
	parts := make([]string, 0, len(findings))
	for _, n := range findings {
		parts = append(parts, fmt.Sprintf("#%d", n))
	}
	return strings.Join(parts, ", ")
}

// formatFlags renders a list of CLI flags/env vars as inline code, e.g.
// "`--sign`, `POKKUM_SIGNING_KEY`".
func formatFlags(flags []string) string {
	parts := make([]string, 0, len(flags))
	for _, f := range flags {
		parts = append(parts, "`"+f+"`")
	}
	return strings.Join(parts, ", ")
}

// itemRefRe matches a depth-agnostic item reference in authored YAML prose:
// [some title](item:<id>). The item: scheme exists because the same authored
// string (a summary, a limitation, an option's trade-offs) is rendered into
// two directories at different depths — docs/*.md and docs/items/*.md — so no
// literal relative path in the source can be correct in both. The renderer
// resolves the scheme per output location instead.
var itemRefRe = regexp.MustCompile(`\[([^\]]*)\]\(item:([A-Za-z0-9._-]+)\)`)

// resolveItemRefs rewrites every [title](item:<id>) reference in text into a
// real relative markdown link correct for a document living in fromDir.
// Unmatched text is returned untouched. The title is not escaped here: this
// runs on prose, and table cells are escaped separately by escapeCell, which
// leaves the resolved link alone since it contains no "|".
func resolveItemRefs(fromDir, text string) string {
	return itemRefRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := itemRefRe.FindStringSubmatch(m)
		title, id := sub[1], sub[2]
		return fmt.Sprintf("[%s](%s)", title, relLink(fromDir, path.Join(itemsFromDir, id+".md")))
	})
}

// itemRefIDs returns every item id referenced via the item: scheme in text,
// so validation can reject a reference to an id that does not exist (a typo
// in a hand-authored ref would otherwise ship as a dead link).
func itemRefIDs(text string) []string {
	matches := itemRefRe.FindAllStringSubmatch(text, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m[2])
	}
	return ids
}

// freeTextFields returns every authored prose field of an item that may carry
// item: references, so resolution and validation both cover the same set and
// cannot drift apart as the schema grows.
func freeTextFields(it Item) []string {
	fields := []string{it.Summary, it.Problem, it.Recommendation, it.Decision}
	fields = append(fields, it.Limitations...)
	for _, o := range it.Options {
		fields = append(fields, o.Description, o.Tradeoffs)
	}
	return fields
}

// resolveRefsInItem returns a copy of it with every free-text field's item:
// references resolved for a document living in fromDir.
func resolveRefsInItem(fromDir string, it Item) Item {
	it.Summary = resolveItemRefs(fromDir, it.Summary)
	it.Problem = resolveItemRefs(fromDir, it.Problem)
	it.Recommendation = resolveItemRefs(fromDir, it.Recommendation)
	it.Decision = resolveItemRefs(fromDir, it.Decision)
	lims := make([]string, len(it.Limitations))
	for i, l := range it.Limitations {
		lims[i] = resolveItemRefs(fromDir, l)
	}
	it.Limitations = lims
	opts := make([]Option, len(it.Options))
	for i, o := range it.Options {
		o.Description = resolveItemRefs(fromDir, o.Description)
		o.Tradeoffs = resolveItemRefs(fromDir, o.Tradeoffs)
		opts[i] = o
	}
	it.Options = opts
	return it
}

// resolveRefsInAreas returns a deep-enough copy of areas with every item's
// item: references resolved for fromDir. The originals are left untouched so
// the same parsed areas can be rendered at more than one depth.
func resolveRefsInAreas(fromDir string, areas []Area) []Area {
	out := make([]Area, len(areas))
	for i, a := range areas {
		items := make([]Item, len(a.Items))
		for j, it := range a.Items {
			items[j] = resolveRefsInItem(fromDir, it)
		}
		a.Items = items
		out[i] = a
	}
	return out
}
