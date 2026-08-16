package bunexec

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// discLogger returns a discard handler so patch warnings don't spam test output.
func discLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPatchPrerenderedEnv_ReplacesDoubleQuotePattern(t *testing.T) {
	const handler = `import { dir } from "node:path";
export function handle() {
	return path.join(dir, "prerendered");
}
`
	dir := t.TempDir()
	p := filepath.Join(dir, "handler.js")
	if err := os.WriteFile(p, []byte(handler), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := patchPrerenderedEnv(p, filepath.Join(dir, ".pokkum"), discLogger()); err != nil {
		t.Fatalf("patchPrerenderedEnv: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, `(process.env.POKKUM_PRERENDERED_DIR || path.join(dir, "prerendered"))`) {
		t.Fatalf("expected process.env fallback wrapper, got:\n%s", s)
	}
	if strings.Contains(s, `path.join(dir, "prerendered");`) {
		t.Fatalf("expected unreplaced pattern not to remain:\n%s", s)
	}
}

func TestPatchPrerenderedEnv_ReplacesSingleQuotePattern(t *testing.T) {
	const handler = `page(path.join(server_dir, 'prerendered'))`
	dir := t.TempDir()
	p := filepath.Join(dir, "handler.js")
	if err := os.WriteFile(p, []byte(handler), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := patchPrerenderedEnv(p, filepath.Join(dir, ".pokkum"), discLogger()); err != nil {
		t.Fatalf("patchPrerenderedEnv: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `(process.env.POKKUM_PRERENDERED_DIR || path.join(server_dir, 'prerendered'))`) {
		t.Fatalf("expected single-quote wrapper, got:\n%s", got)
	}
}

func TestPatchPrerenderedEnv_UnknownPatternErrorsAndLeavesFileUntouched(t *testing.T) {
	const handler = `somethingElse()`
	dir := t.TempDir()
	p := filepath.Join(dir, "handler.js")
	if err := os.WriteFile(p, []byte(handler), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := patchPrerenderedEnv(p, filepath.Join(dir, ".pokkum"), discLogger()); err == nil {
		t.Fatal("expected error for a handler with no recognizable prerendered path pattern")
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != handler {
		t.Fatalf("expected file unchanged, got:\n%s", got)
	}
}

func TestPatchPrerenderedEnv_MissingFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := patchPrerenderedEnv(filepath.Join(dir, "nope.js"), filepath.Join(dir, ".pokkum"), discLogger()); err == nil {
		t.Fatal("expected error for missing handler file")
	}
}

func TestPatchPrerenderedEnv_StagesTransformInPokkumSandbox(t *testing.T) {
	const handler = `x = path.join(dir, "prerendered")`
	dir := t.TempDir()
	p := filepath.Join(dir, "handler.js")
	if err := os.WriteFile(p, []byte(handler), 0o600); err != nil {
		t.Fatal(err)
	}
	pokkumDir := filepath.Join(dir, ".pokkum")

	if err := patchPrerenderedEnv(p, pokkumDir, discLogger()); err != nil {
		t.Fatalf("patchPrerenderedEnv: %v", err)
	}

	staged, err := os.ReadFile(filepath.Join(pokkumDir, "handler.js"))
	if err != nil {
		t.Fatalf("expected staged copy in .pokkum sandbox: %v", err)
	}
	if !strings.Contains(string(staged), "POKKUM_PRERENDERED_DIR") {
		t.Fatalf("expected staged copy to carry the patch, got:\n%s", staged)
	}
}

func TestPatchPrerenderedHandler_LocatesNestedHandler(t *testing.T) {
	dir := t.TempDir()
	serverDir := filepath.Join(dir, "server")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(serverDir, "handler.js")
	if err := os.WriteFile(p, []byte(`x = path.join(dir, "prerendered")`), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Compiler{logger: discLogger()}
	if err := c.patchPrerenderedHandler(dir, dir); err != nil { // dir is both outputDir and projectDir; handler is nested under server/
		t.Fatalf("patchPrerenderedHandler: %v", err)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "POKKUM_PRERENDERED_DIR") {
		t.Fatalf("expected nested handler to be patched, got:\n%s", got)
	}
}

func TestPatchPrerenderedHandler_NoHandlerFoundReturnsError(t *testing.T) {
	dir := t.TempDir()
	c := &Compiler{logger: discLogger()}
	if err := c.patchPrerenderedHandler(dir, dir); err == nil {
		t.Fatal("expected error when no handler.js exists under outputDir")
	}
}

// wantedWrap is the exact wrapping patchPrerenderedEnv produces around the
// single-quote "dir" pattern, matching the real adapter-node handler.js
// fixtures below (both use `path.join(dir, 'prerendered')`).
const wantedWrap = `(process.env.POKKUM_PRERENDERED_DIR || path.join(dir, 'prerendered'))`

// literalDirPattern is the unwrapped literal this repo's real fixtures
// contain. Counting it in the pre-patch fixture is the regression signal: if
// a future re-sourced fixture ever contains it more than once, the current
// strings.ReplaceAll call in patchPrerenderedEnv would silently patch every
// occurrence with no error and no warning.
const literalDirPattern = `path.join(dir, 'prerendered')`

// repoRootTestdataDir locates the repo-root testdata/ directory from this
// package's directory (internal/adapters/bunexec), since `go test` runs with
// the package directory as its working directory. The real adapter-node
// fixtures live at the repo-root testdata/adapter-node/{v3,v5}/handler.js
// (see testdata/adapter-node/README.md), not under a package-local testdata/.
const repoRootTestdataDir = "../../../testdata"

// testPatchPrerenderedEnvRealFixture is shared by the v3/v5 tests below: copy
// the checked-in real adapter-node handler.js fixture into a temp dir (so the
// checked-in copy under testdata/ is never mutated), patch the copy, and
// assert the result.
func testPatchPrerenderedEnvRealFixture(t *testing.T, fixturePath string) {
	t.Helper()

	original, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixturePath, err)
	}

	// Regression signal: the literal unwrapped pattern must appear exactly
	// once in the untouched fixture. See literalDirPattern doc comment.
	if n := strings.Count(string(original), literalDirPattern); n != 1 {
		t.Fatalf("expected literal pattern %q to appear exactly once in %s, found %d", literalDirPattern, fixturePath, n)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "handler.js")
	if err := os.WriteFile(p, original, 0o600); err != nil {
		t.Fatal(err)
	}
	pokkumDir := filepath.Join(dir, ".pokkum")

	if err := patchPrerenderedEnv(p, pokkumDir, discLogger()); err != nil {
		t.Fatalf("patchPrerenderedEnv: %v", err)
	}

	// The checked-in fixture itself must remain untouched.
	unchanged, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("re-read fixture %s: %v", fixturePath, err)
	}
	if string(unchanged) != string(original) {
		t.Fatalf("checked-in fixture %s was mutated by the test", fixturePath)
	}

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, wantedWrap) {
		t.Fatalf("expected patched handler to contain %q, but it did not (patched copy: %s)", wantedWrap, p)
	}
	if n := strings.Count(s, wantedWrap); n != 1 {
		t.Fatalf("expected exactly one occurrence of %q in patched handler, found %d", wantedWrap, n)
	}

	staged, err := os.ReadFile(filepath.Join(pokkumDir, "handler.js"))
	if err != nil {
		t.Fatalf("expected staged copy in .pokkum sandbox: %v", err)
	}
	if !strings.Contains(string(staged), wantedWrap) {
		t.Fatalf("expected staged copy under .pokkum to carry the patch, got:\n%s", staged)
	}
}

// TestPatchPrerenderedEnv_RealAdapterNodeV3 exercises patchPrerenderedEnv
// against a real @sveltejs/adapter-node@3.0.3 handler.js (see
// testdata/adapter-node/README.md for sourcing provenance). It also asserts
// the false-positive regression signal documented on literalDirPattern.
func TestPatchPrerenderedEnv_RealAdapterNodeV3(t *testing.T) {
	testPatchPrerenderedEnvRealFixture(t, filepath.Join(repoRootTestdataDir, "adapter-node", "v3", "handler.js"))
}

// TestPatchPrerenderedEnv_RealAdapterNodeV5 is the v5.5.7 counterpart of
// TestPatchPrerenderedEnv_RealAdapterNodeV3.
func TestPatchPrerenderedEnv_RealAdapterNodeV5(t *testing.T) {
	testPatchPrerenderedEnvRealFixture(t, filepath.Join(repoRootTestdataDir, "adapter-node", "v5", "handler.js"))
}
