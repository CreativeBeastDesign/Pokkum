package pruneutils_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
)

func TestIsJunk_Defaults(t *testing.T) {
	opts := pruneutils.PruneOptions{}

	junkCases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"index.d.ts", false, true},
		{"index.d.ts.map", false, true},
		{"index.d.mts", false, true},
		{"tsconfig.json", false, true},
		{"tsconfig.build.json", false, true},
		{"bundle.js.map", false, true},
		{"README.md", false, true},
		{"readme.markdown", false, true},
		{"CHANGELOG.md", false, true},
		{"LICENSE", false, true},
		{"LICENSE.txt", false, true},
		{".npmignore", false, true},
		{".eslintrc.json", false, true},
		{".prettierrc", false, true},
		{"__tests__/util.test.js", false, true},
		{"__tests__", true, true},
		{"test/suite.js", false, true},
		{"tests/spec.ts", false, true},
		{"component.test.js", false, true},
		{"component.spec.ts", false, true},
		{".github/workflows/ci.yml", false, true},
		{".github", true, true},
		{"Makefile", false, true},
		{"Dockerfile", false, true},

		// Valid runtime production files (must NOT be marked junk)
		{"index.js", false, false},
		{"package.json", false, false},
		{"server.mjs", false, false},
		{"runtime.cjs", false, false},
		{"styles.css", false, false},
		{"image.png", false, false},
		{"fonts/inter.woff2", false, false},
		{"lib/helper.js", false, false},
		{"bin/cli.js", false, false},
	}

	for _, tc := range junkCases {
		got := pruneutils.IsJunk(tc.path, tc.isDir, opts)
		if got != tc.want {
			t.Errorf("IsJunk(%q, isDir=%v) = %v; want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

func TestIsJunk_KeepSourcemap(t *testing.T) {
	opts := pruneutils.PruneOptions{KeepSourcemap: true}

	if pruneutils.IsJunk("bundle.js.map", false, opts) {
		t.Errorf("expected bundle.js.map to be kept when KeepSourcemap is true")
	}
	// TypeScript source map should still be pruned
	if !pruneutils.IsJunk("index.d.ts.map", false, opts) {
		t.Errorf("expected index.d.ts.map to be pruned even when KeepSourcemap is true")
	}
}

func TestIsJunk_KeepPatterns(t *testing.T) {
	opts := pruneutils.PruneOptions{
		KeepPatterns: []string{"*.md", "docs/**"},
	}

	if pruneutils.IsJunk("README.md", false, opts) {
		t.Errorf("expected README.md to be kept when matched by KeepPatterns")
	}
	if pruneutils.IsJunk("docs/guide.html", false, opts) {
		t.Errorf("expected docs/guide.html to be kept when matched by KeepPatterns")
	}
	if !pruneutils.IsJunk("index.d.ts", false, opts) {
		t.Errorf("expected index.d.ts to still be pruned")
	}
}

func TestIsJunk_NoPrune(t *testing.T) {
	opts := pruneutils.PruneOptions{NoPrune: true}

	if pruneutils.IsJunk("index.d.ts", false, opts) {
		t.Errorf("expected index.d.ts to NOT be junk when NoPrune is true")
	}
	if pruneutils.IsJunk("bundle.js.map", false, opts) {
		t.Errorf("expected bundle.js.map to NOT be junk when NoPrune is true")
	}
}

func TestPruneDirectory_RealFilesystem(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"index.js":                       "console.log('hello');",
		"index.d.ts":                     "export declare function hello(): void;",
		"index.js.map":                   "{\"version\":3}",
		"README.md":                      "# Package README",
		"LICENSE":                        "MIT License",
		"__tests__/helper.test.js":       "test('ok', () => {});",
		"sub/nested.js":                  "module.exports = {};",
		"sub/nested.d.ts":                "export declare const nested: any;",
		"empty_after_prune/spec.test.js": "test()",
	}

	for rel, content := range files {
		fullPath := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(fullPath), 0o755)
		_ = os.WriteFile(fullPath, []byte(content), 0o644)
	}

	res, err := pruneutils.PruneDirectory(dir, pruneutils.PruneOptions{})
	if err != nil {
		t.Fatalf("PruneDirectory failed: %v", err)
	}

	if res.FilesPruned < 6 {
		t.Errorf("expected at least 6 files pruned, got %d", res.FilesPruned)
	}
	if res.BytesSaved <= 0 {
		t.Errorf("expected BytesSaved > 0, got %d", res.BytesSaved)
	}

	// Ensure runtime files exist
	if _, err := os.Stat(filepath.Join(dir, "index.js")); err != nil {
		t.Errorf("index.js was deleted unexpectedly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "nested.js")); err != nil {
		t.Errorf("sub/nested.js was deleted unexpectedly: %v", err)
	}

	// Ensure junk files were removed
	if _, err := os.Stat(filepath.Join(dir, "index.d.ts")); !os.IsNotExist(err) {
		t.Errorf("index.d.ts was not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.js.map")); !os.IsNotExist(err) {
		t.Errorf("index.js.map was not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !os.IsNotExist(err) {
		t.Errorf("README.md was not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "__tests__")); !os.IsNotExist(err) {
		t.Errorf("__tests__ directory was not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "empty_after_prune")); !os.IsNotExist(err) {
		t.Errorf("empty_after_prune folder was not cleaned up")
	}
}

func TestPruneDirectory_KeepSourcemap(t *testing.T) {
	dir := t.TempDir()

	_ = os.WriteFile(filepath.Join(dir, "app.js"), []byte("runtime"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "app.js.map"), []byte("{\"version\":3}"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "app.d.ts"), []byte("declare const a: number;"), 0o644)

	res, err := pruneutils.PruneDirectory(dir, pruneutils.PruneOptions{KeepSourcemap: true})
	if err != nil {
		t.Fatalf("PruneDirectory failed: %v", err)
	}

	if res.FilesPruned != 1 {
		t.Errorf("expected 1 file pruned (app.d.ts), got %d", res.FilesPruned)
	}

	if _, err := os.Stat(filepath.Join(dir, "app.js.map")); err != nil {
		t.Errorf("app.js.map was deleted despite KeepSourcemap: %v", err)
	}
}
