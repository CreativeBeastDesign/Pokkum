package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestResolveItemRefs_DepthAwarePerOutputDir pins the reason the item: scheme
// exists: one authored string is rendered into two directories at different
// depths, so the resolved path must differ between them.
func TestResolveItemRefs_DepthAwarePerOutputDir(t *testing.T) {
	in := "see [the signing item](item:image-signing) for details"
	if got, want := resolveItemRefs(docsFromDir, in), "see [the signing item](items/image-signing.md) for details"; got != want {
		t.Errorf("from docs/: got %q, want %q", got, want)
	}
	if got, want := resolveItemRefs(itemsFromDir, in), "see [the signing item](image-signing.md) for details"; got != want {
		t.Errorf("from docs/items/: got %q, want %q", got, want)
	}
}

func TestResolveItemRefs_LeavesOtherLinksAlone(t *testing.T) {
	in := "a [normal](../internal/x.go) link and an [external](https://example.com/item:x) one"
	if got := resolveItemRefs(docsFromDir, in); got != in {
		t.Errorf("non-item links must be untouched, got %q", got)
	}
}

func TestResolveItemRefs_MultipleRefsInOneString(t *testing.T) {
	in := "[a](item:alpha) then [b](item:beta)"
	want := "[a](items/alpha.md) then [b](items/beta.md)"
	if got := resolveItemRefs(docsFromDir, in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveRefsInItem_CoversEveryFreeTextField guards against a new prose
// field being added to the schema and silently not getting its refs resolved,
// which would ship the raw item: scheme into a published doc.
func TestResolveRefsInItem_CoversEveryFreeTextField(t *testing.T) {
	ref := "[t](item:x)"
	it := Item{
		Summary:        ref,
		Problem:        ref,
		Recommendation: ref,
		Decision:       ref,
		Limitations:    []string{ref, ref},
		Options:        []Option{{Description: ref, Tradeoffs: ref}},
	}
	got := resolveRefsInItem(docsFromDir, it)
	for i, f := range freeTextFields(got) {
		if strings.Contains(f, "item:") {
			t.Errorf("free-text field %d still carries an unresolved item: ref: %q", i, f)
		}
	}
}

// TestResolveRefsInAreas_DoesNotMutateInput matters because the same parsed
// areas are rendered at two different depths; mutating in place would make the
// second render resolve already-resolved text.
func TestResolveRefsInAreas_DoesNotMutateInput(t *testing.T) {
	areas := []Area{{Name: "a", Items: []Item{{ID: "i", Summary: "[t](item:x)"}}}}
	_ = resolveRefsInAreas(docsFromDir, areas)
	if got := areas[0].Items[0].Summary; got != "[t](item:x)" {
		t.Errorf("input was mutated: summary is now %q", got)
	}
}

func TestItemRefIDs(t *testing.T) {
	got := itemRefIDs("[a](item:alpha) [b](item:beta) [c](../x.go)")
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("itemRefIDs() = %v, want [alpha beta]", got)
	}
}

// TestValidate_RejectsUnknownItemRef ensures a typo'd id fails the build
// rather than shipping as a dead link.
func TestValidate_RejectsUnknownItemRef(t *testing.T) {
	areas := []Area{{Name: "area", Items: []Item{
		{ID: "real", Title: "Real", Status: "open", Summary: "ok"},
		{ID: "src", Title: "Src", Status: "open", Summary: "see [x](item:typoed-id)"},
	}}}
	for i := range areas[0].Items {
		areas[0].Items[i].SourceFile = "test.yaml"
	}
	errs := Validate(areas, ".", map[int]bool{})
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Msg, "unknown id") && strings.Contains(e.Msg, "typoed-id") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an unknown-id validation error, got %v", errs)
	}
}

// TestValidate_AcceptsKnownItemRef is the negative control for the test above:
// without it, a Validate that rejected *every* ref would still pass.
func TestValidate_AcceptsKnownItemRef(t *testing.T) {
	areas := []Area{{Name: "area", Items: []Item{
		{ID: "real", Title: "Real", Status: "open", Summary: "ok", SourceFile: "test.yaml"},
		{ID: "src", Title: "Src", Status: "open", Summary: "see [x](item:real)", SourceFile: "test.yaml"},
	}}}
	for _, e := range Validate(areas, ".", map[int]bool{}) {
		if strings.Contains(e.Msg, "item: reference") {
			t.Errorf("a valid ref was rejected: %v", e)
		}
	}
}

var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// TestGeneratedDocsHaveNoDeadRelativeLinks walks the committed generated docs
// and resolves every relative link against the linking file's own directory.
// This is the check that was missing when a hardcoded items/ prefix produced
// docs/items/items/<id>.md from inside an item page: the unit tests above
// cover the helper, but only walking the real output catches a *call site*
// that passes the wrong fromDir.
func TestGeneratedDocsHaveNoDeadRelativeLinks(t *testing.T) {
	repoRoot := "../.."
	docsDir := filepath.Join(repoRoot, "docs")
	if _, err := os.Stat(docsDir); err != nil {
		t.Skipf("docs/ not present: %v", err)
	}

	// docs/archive/ is included deliberately: retiring the hand-maintained
	// status docs into it moved 33 links that had been correct relative to the
	// repository root and silently became dead one directory deeper. Nothing
	// regenerates those files, so a walker is the only thing that can catch it.
	var files []string
	for _, pat := range []string{"*.md", "items/*.md", "archive/*.md"} {
		matches, err := filepath.Glob(filepath.Join(docsDir, pat))
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Skip("no generated docs on disk")
	}

	var checked int
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range mdLinkRe.FindAllStringSubmatch(stripCodeBlocks(string(body)), -1) {
			target := strings.TrimSpace(m[1])
			// Skip absolute URLs and any unresolved scheme — a leftover
			// item: ref is caught by TestGeneratedDocsHaveNoRawItemRefs.
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "item:") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			if target == "" {
				continue
			}
			// A link to a filename containing spaces is legitimately written
			// percent-encoded ("Supply%20Chain%20Hardening%20v1.md") and
			// resolves that way in a browser and an IDE, so decode before
			// touching the filesystem — otherwise the walker rejects a link
			// that is in fact correct.
			if decoded, err := url.PathUnescape(target); err == nil {
				target = decoded
			}
			checked++
			if _, err := os.Stat(filepath.Join(filepath.Dir(f), target)); err != nil {
				t.Errorf("dead link in %s: %q does not resolve", f, target)
			}
		}
	}
	if checked == 0 {
		t.Error("no relative links were checked — the walker is not exercising anything")
	}
}

// TestGeneratedDocsHaveNoRawItemRefs catches the opposite failure: a prose
// field whose refs were never resolved, leaking the internal item: scheme
// into a human-facing page.
func TestGeneratedDocsHaveNoRawItemRefs(t *testing.T) {
	docsDir := filepath.Join("../..", "docs")
	for _, pat := range []string{"*.md", "items/*.md"} {
		matches, _ := filepath.Glob(filepath.Join(docsDir, pat))
		for _, f := range matches {
			body, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			if strings.Contains(string(body), "](item:") {
				t.Errorf("%s contains an unresolved item: reference", f)
			}
		}
	}
}

// stripCodeBlocks blanks out fenced and indented code blocks so the link
// walkers below read prose only.
//
// Markdown link syntax and regular-expression syntax overlap: a character
// class like [^"'\s(){}] is not a link, but it matches the same
// `[...](...)` shape. An item documenting a regex in a code block therefore
// failed the dead-link check for a "link" that was never a link — the doc was
// correct and the checker was wrong. Blanking rather than deleting keeps byte
// offsets and line numbers intact for any error message built from them.
func stripCodeBlocks(md string) string {
	lines := strings.Split(md, "\n")
	inFence := false
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		// An indented code block: four spaces or a tab, the CommonMark rule.
		if inFence || strings.HasPrefix(ln, "    ") || strings.HasPrefix(ln, "\t") {
			lines[i] = ""
			continue
		}
		// Inline code spans on a prose line, for the same reason.
		lines[i] = inlineCodeRe.ReplaceAllString(ln, "")
	}
	return strings.Join(lines, "\n")
}

// inlineCodeRe matches a backtick-delimited inline code span.
var inlineCodeRe = regexp.MustCompile("`[^`]*`")
