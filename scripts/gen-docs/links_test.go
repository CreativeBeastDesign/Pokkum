package main

import "testing"

func TestEscapeCell(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "plain text", "plain text"},
		{"pipe is escaped", "a | b", "a \\| b"},
		{"multiple pipes are all escaped", "a | b | c", "a \\| b \\| c"},
		{"backtick is left alone", "contains `code`", "contains `code`"},
		{"backtick and pipe together", "contains `code` and | a pipe", "contains `code` and \\| a pipe"},
		{"newline collapses to a space", "line one\nline two", "line one line two"},
		{"CRLF collapses to a space", "line one\r\nline two", "line one line two"},
		{"leading/trailing whitespace trimmed", "  padded  ", "padded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := escapeCell(c.in); got != c.want {
				t.Errorf("escapeCell(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestItemLink_FromDocsDir(t *testing.T) {
	got := itemLink(docsFromDir, "kms-signing", "KMS-backed signing")
	want := "[KMS-backed signing](items/kms-signing.md)"
	if got != want {
		t.Errorf("itemLink() = %q, want %q", got, want)
	}
}

// TestItemLink_FromItemsDir pins the depth fix: an item page's own Related
// section links to a *sibling* file, so it must not carry the items/ prefix
// that would resolve to docs/items/items/<id>.md.
func TestItemLink_FromItemsDir(t *testing.T) {
	got := itemLink(itemsFromDir, "kms-signing", "KMS-backed signing")
	want := "[KMS-backed signing](kms-signing.md)"
	if got != want {
		t.Errorf("itemLink() = %q, want %q", got, want)
	}
}

func TestItemLink_EscapesPipeInTitle(t *testing.T) {
	// A title containing a literal pipe must not be allowed to break the
	// enclosing table row the link is rendered inside.
	got := itemLink(docsFromDir, "x", "A | B")
	want := "[A \\| B](items/x.md)"
	if got != want {
		t.Errorf("itemLink() = %q, want %q", got, want)
	}
}

// TestRelLink covers the two real link depths this generator ever produces:
// docs/*.md files sit one level below the repo root, docs/items/*.md files
// sit two levels below it. Both need to reach the same repo-relative
// source path with a different number of "../" segments.
func TestRelLink(t *testing.T) {
	cases := []struct {
		name          string
		fromDir       string
		targetRelPath string
		want          string
	}{
		{
			name:          "top-level doc (docs/*.md) reaches internal/",
			fromDir:       "docs",
			targetRelPath: "internal/adapters/cosign/signer.go",
			want:          "../internal/adapters/cosign/signer.go",
		},
		{
			name:          "item page (docs/items/*.md) reaches internal/",
			fromDir:       "docs/items",
			targetRelPath: "internal/adapters/cosign/signer.go",
			want:          "../../internal/adapters/cosign/signer.go",
		},
		{
			name:          "top-level doc reaches cmd/",
			fromDir:       "docs",
			targetRelPath: "cmd/pokkum/dev.go",
			want:          "../cmd/pokkum/dev.go",
		},
		{
			name:          "item page reaches cmd/",
			fromDir:       "docs/items",
			targetRelPath: "cmd/pokkum/dev.go",
			want:          "../../cmd/pokkum/dev.go",
		},
		{
			name:          "top-level doc reaches a root-level file",
			fromDir:       "docs",
			targetRelPath: "Makefile",
			want:          "../Makefile",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := relLink(c.fromDir, c.targetRelPath); got != c.want {
				t.Errorf("relLink(%q, %q) = %q, want %q", c.fromDir, c.targetRelPath, got, c.want)
			}
		})
	}
}

func TestFormatCommits(t *testing.T) {
	got := formatCommits([]string{"2f03609", "e918c52"})
	want := "`2f03609`, `e918c52`"
	if got != want {
		t.Errorf("formatCommits() = %q, want %q", got, want)
	}
}

func TestFormatCommits_Empty(t *testing.T) {
	if got := formatCommits(nil); got != "" {
		t.Errorf("formatCommits(nil) = %q, want empty string", got)
	}
}

func TestFormatFindings(t *testing.T) {
	got := formatFindings([]int{1, 14})
	want := "#1, #14"
	if got != want {
		t.Errorf("formatFindings() = %q, want %q", got, want)
	}
}

func TestFormatFlags(t *testing.T) {
	got := formatFlags([]string{"--sign", "POKKUM_SIGNING_KEY"})
	want := "`--sign`, `POKKUM_SIGNING_KEY`"
	if got != want {
		t.Errorf("formatFlags() = %q, want %q", got, want)
	}
}
