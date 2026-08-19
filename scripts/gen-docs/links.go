package main

import (
	"fmt"
	"path/filepath"
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

// wikiLink renders a doc-to-doc link in the IDE-resolved wiki-link
// convention: [[items/<id>|<title>]]. The title is escaped the same as any
// other table cell content, since wiki links appear inside tables here.
func wikiLink(id, title string) string {
	return fmt.Sprintf("[[items/%s|%s]]", id, escapeCell(title))
}

// relLink computes the relative markdown link from a generated doc file to
// a repo-relative source path, e.g. relLink("docs", "internal/x/y.go") ->
// "../internal/x/y.go", and relLink("docs/items", "internal/x/y.go") ->
// "../../internal/x/y.go". fromDir is the repo-relative directory the
// *linking* document lives in; targetRepoRelPath is the repo-relative path
// being linked to. Wiki links do not resolve to source files (only to other
// docs), so every doc->code link in this generator must go through this
// function instead.
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
		b.WriteString(fmt.Sprintf("- [%s](%s)\n", path, relLink(fromDir, path)))
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

// formatFindings renders a list of overnight-findings.md entry numbers,
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
