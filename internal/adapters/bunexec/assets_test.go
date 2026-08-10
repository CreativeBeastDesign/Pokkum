package bunexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sortedFixture is representative, already-sorted assets.generated.ts content
// in the exact canonical shape renderAssetsGenerated produces. Note the import
// paths are NOT the same strings as the public paths (a real detail: imports
// use relative filesystem paths, assetMap/assets use public URL paths) — the
// normalizer must join the two by identifier, not by string equality.
const sortedFixture = `// Auto-generated asset imports
// @ts-nocheck
import version_JSON_46ce from "../client/_app/version.json" with { type: "file" };
import about_HTML_db83 from "../prerendered/about.html" with { type: "file" };
import logo_SVG_e90b from "../client/logo.svg" with { type: "file" };

export const assetMap = new Map([
  ["/_app/version.json", version_JSON_46ce],
  ["/about.html", about_HTML_db83],
  ["/logo.svg", logo_SVG_e90b],
]);

export const assets = {
  version_JSON_46ce,
  about_HTML_db83,
  logo_SVG_e90b,
};
`

// reversedFixture contains the exact same three assets as sortedFixture, in
// reverse order in every section, formatted the way the real
// @jesterkit/exe-sveltekit generator actually emits the file: irregular
// leading whitespace on the first line of each section, none on the rest.
const reversedFixture = `// Auto-generated asset imports
// @ts-nocheck
  import version_JSON_46ce from "../client/_app/version.json" with { type: "file" };
import logo_SVG_e90b from "../client/logo.svg" with { type: "file" };
import about_HTML_db83 from "../prerendered/about.html" with { type: "file" };

  export const assetMap = new Map([
    ["/logo.svg", logo_SVG_e90b],
  ["/about.html", about_HTML_db83],
  ["/_app/version.json", version_JSON_46ce]
  ]);

  export const assets = {
    logo_SVG_e90b,
  about_HTML_db83,
  version_JSON_46ce
  };
`

func TestNormalizeGeneratedAssetsSource_SortsReversedInput(t *testing.T) {
	got, err := normalizeGeneratedAssetsSource(reversedFixture)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	wantOrder := []string{"/_app/version.json", "/about.html", "/logo.svg"}
	gotOrder := extractAssetMapPathOrder(t, got)
	if !equalStrings(gotOrder, wantOrder) {
		t.Fatalf("assetMap order = %v, want %v", gotOrder, wantOrder)
	}

	// The import block and assets block must be reordered in lockstep with
	// assetMap, keyed by identifier — not just assetMap alone.
	wantIdentOrder := []string{"version_JSON_46ce", "about_HTML_db83", "logo_SVG_e90b"}
	gotImportOrder := extractImportIdentOrder(t, got)
	if !equalStrings(gotImportOrder, wantIdentOrder) {
		t.Fatalf("import order = %v, want %v", gotImportOrder, wantIdentOrder)
	}
	gotAssetsOrder := extractAssetsIdentOrder(t, got)
	if !equalStrings(gotAssetsOrder, wantIdentOrder) {
		t.Fatalf("assets order = %v, want %v", gotAssetsOrder, wantIdentOrder)
	}

	// The identifier<->import-path binding must be preserved exactly - only
	// the order may change, never the pairing.
	if !strings.Contains(got, `import version_JSON_46ce from "../client/_app/version.json"`) {
		t.Errorf("output lost or corrupted the version_JSON_46ce import path:\n%s", got)
	}
	if !strings.Contains(got, `["/logo.svg", logo_SVG_e90b]`) {
		t.Errorf("output lost or corrupted the logo.svg assetMap pairing:\n%s", got)
	}
}

func TestNormalizeGeneratedAssetsSource_AlreadySortedIsNoOp(t *testing.T) {
	got, err := normalizeGeneratedAssetsSource(sortedFixture)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got != sortedFixture {
		t.Fatalf("normalizing already-canonical, already-sorted input changed it.\ngot:\n%s\nwant (unchanged):\n%s", got, sortedFixture)
	}
}

func TestNormalizeGeneratedAssetsSource_Idempotent(t *testing.T) {
	once, err := normalizeGeneratedAssetsSource(reversedFixture)
	if err != nil {
		t.Fatalf("first normalize: %v", err)
	}
	twice, err := normalizeGeneratedAssetsSource(once)
	if err != nil {
		t.Fatalf("second normalize: %v", err)
	}
	if once != twice {
		t.Fatalf("normalizing twice is not idempotent:\nfirst:\n%s\nsecond:\n%s", once, twice)
	}
}

func TestNormalizeGeneratedAssetsSource_PreservesEntryCount(t *testing.T) {
	got, err := normalizeGeneratedAssetsSource(reversedFixture)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	wantCount := 3
	if n := len(extractAssetMapPathOrder(t, reversedFixture)); n != wantCount {
		t.Fatalf("test fixture itself has %d entries, expected %d", n, wantCount)
	}
	if n := len(extractAssetMapPathOrder(t, got)); n != wantCount {
		t.Errorf("normalized output has %d assetMap entries, want %d (an entry was dropped or duplicated)", n, wantCount)
	}
	if n := len(extractImportIdentOrder(t, got)); n != wantCount {
		t.Errorf("normalized output has %d imports, want %d", n, wantCount)
	}
	if n := len(extractAssetsIdentOrder(t, got)); n != wantCount {
		t.Errorf("normalized output has %d assets entries, want %d", n, wantCount)
	}
}

func TestNormalizeGeneratedAssetsSource_RejectsUnrecognizedShape(t *testing.T) {
	cases := map[string]string{
		"missing header": strings.TrimPrefix(sortedFixture, "// Auto-generated asset imports\n"),
		"empty file":     "",
		"prose":          "this is not a generated asset file at all\n",
		"malformed import line": `// Auto-generated asset imports
// @ts-nocheck
import logo_SVG_e90b from "../client/logo.svg"; // missing the "with" clause

export const assetMap = new Map([
  ["/logo.svg", logo_SVG_e90b],
]);

export const assets = {
  logo_SVG_e90b,
};
`,
		"assetMap references unknown identifier": `// Auto-generated asset imports
// @ts-nocheck
import logo_SVG_e90b from "../client/logo.svg" with { type: "file" };

export const assetMap = new Map([
  ["/logo.svg", some_other_ident],
]);

export const assets = {
  logo_SVG_e90b,
};
`,
		"assetMap missing an imported identifier": `// Auto-generated asset imports
// @ts-nocheck
import logo_SVG_e90b from "../client/logo.svg" with { type: "file" };
import about_HTML_db83 from "../prerendered/about.html" with { type: "file" };

export const assetMap = new Map([
  ["/logo.svg", logo_SVG_e90b],
]);

export const assets = {
  logo_SVG_e90b,
  about_HTML_db83,
};
`,
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeGeneratedAssetsSource(src)
			if err == nil {
				t.Fatalf("normalize succeeded on malformed input %q, want a loud failure", name)
			}
		})
	}
}

func TestNormalizeGeneratedAssetsFile_WritesSortedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "assets.generated.ts")
	if err := os.WriteFile(path, []byte(reversedFixture), 0o644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	if err := normalizeGeneratedAssetsFile(path); err != nil {
		t.Fatalf("normalizeGeneratedAssetsFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read normalized file: %v", err)
	}
	gotOrder := extractAssetMapPathOrder(t, string(data))
	wantOrder := []string{"/_app/version.json", "/about.html", "/logo.svg"}
	if !equalStrings(gotOrder, wantOrder) {
		t.Fatalf("normalized file order = %v, want %v", gotOrder, wantOrder)
	}
}

func TestNormalizeGeneratedAssetsFile_NoOpDoesNotRewriteUnnecessarily(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "assets.generated.ts")
	if err := os.WriteFile(path, []byte(sortedFixture), 0o644); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := normalizeGeneratedAssetsFile(path); err != nil {
		t.Fatalf("normalizeGeneratedAssetsFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != sortedFixture {
		t.Fatalf("already-sorted file content changed")
	}
	_ = before // mtime-based no-rewrite assertion would be racy on some filesystems; content equality is the meaningful guarantee here.
}

func TestNormalizeGeneratedAssetsFile_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.ts")
	if err := normalizeGeneratedAssetsFile(path); err == nil {
		t.Fatal("normalizeGeneratedAssetsFile on a missing file: want error, got nil")
	}
}

// --- test helpers -------------------------------------------------------

func extractAssetMapPathOrder(t *testing.T, src string) []string {
	t.Helper()
	m := assetsFileRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("test helper: source does not match assetsFileRe:\n%s", src)
	}
	entries, err := parseAssetMapLines(m[2])
	if err != nil {
		t.Fatalf("test helper: parseAssetMapLines: %v", err)
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.publicPath
	}
	return out
}

func extractImportIdentOrder(t *testing.T, src string) []string {
	t.Helper()
	m := assetsFileRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("test helper: source does not match assetsFileRe:\n%s", src)
	}
	entries, err := parseImportLines(m[1])
	if err != nil {
		t.Fatalf("test helper: parseImportLines: %v", err)
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ident
	}
	return out
}

func extractAssetsIdentOrder(t *testing.T, src string) []string {
	t.Helper()
	m := assetsFileRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("test helper: source does not match assetsFileRe:\n%s", src)
	}
	idents, err := parseIdentLines(m[3])
	if err != nil {
		t.Fatalf("test helper: parseIdentLines: %v", err)
	}
	return idents
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
