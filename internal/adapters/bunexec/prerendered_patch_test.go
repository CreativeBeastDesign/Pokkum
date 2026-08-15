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

	if err := patchPrerenderedEnv(p, discLogger()); err != nil {
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

	if err := patchPrerenderedEnv(p, discLogger()); err != nil {
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

func TestPatchPrerenderedEnv_UnknownPatternLeavesFileUntouched(t *testing.T) {
	const handler = `somethingElse()`
	dir := t.TempDir()
	p := filepath.Join(dir, "handler.js")
	if err := os.WriteFile(p, []byte(handler), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := patchPrerenderedEnv(p, discLogger()); err != nil {
		t.Fatalf("patchPrerenderedEnv should not error on unknown pattern, got: %v", err)
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
	if err := patchPrerenderedEnv(filepath.Join(t.TempDir(), "nope.js"), discLogger()); err == nil {
		t.Fatal("expected error for missing handler file")
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
	c.patchPrerenderedHandler(dir) // dir is outputDir; handler is nested under server/

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "POKKUM_PRERENDERED_DIR") {
		t.Fatalf("expected nested handler to be patched, got:\n%s", got)
	}
}
