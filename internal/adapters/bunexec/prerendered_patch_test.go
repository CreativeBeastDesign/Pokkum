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
// single-quote "dir" pattern, matching every adapter-node handler.js fixture
// below (all use `path.join(dir, 'prerendered')`).
const wantedWrap = `(process.env.POKKUM_PRERENDERED_DIR || path.join(dir, 'prerendered'))`

// literalDirPattern is the unwrapped literal this repo's real fixtures
// contain. Counting it in the pre-patch fixture is the regression signal: if
// a future re-sourced fixture ever contains it more than once, the current
// strings.ReplaceAll call in patchPrerenderedEnv would silently patch every
// occurrence with no error and no warning.
const literalDirPattern = `path.join(dir, 'prerendered')`

// repoRootTestdataDir locates the repo-root testdata/ directory from this
// package's directory (internal/adapters/bunexec), since `go test` runs with
// the package directory as its working directory. The adapter-node fixtures
// live at the repo-root testdata/adapter-node/ (see its README.md), not
// under a package-local testdata/.
const repoRootTestdataDir = "../../../testdata"

// testPatchPrerenderedEnvRealFixture is shared by the v3/v5 tests below: copy
// the checked-in adapter-node handler.js fixture into a temp dir (so the
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
// against @sveltejs/adapter-node@3.0.3's handler.js.
//
// Scope, stated plainly: this fixture is adapter-node's **pre-bundling source
// template** (`npm pack` → `package/files/handler.js`), NOT the bundled
// `build/handler.js` a real `bun run build` emits — see
// testdata/adapter-node/README.md for provenance and Lessons.md's 2026-08-16
// entry "patchPrerenderedHandler's 'real fixture' regression tests exercised
// the wrong artifact". It proves the matcher handles the upstream template
// shape (which some versions/configs still inline into build output), and
// nothing about real build output; that coverage is
// TestPatchPrerenderedEnv_RealBundledReExport below.
//
// It also asserts the false-positive regression signal documented on
// literalDirPattern.
func TestPatchPrerenderedEnv_RealAdapterNodeV3(t *testing.T) {
	testPatchPrerenderedEnvRealFixture(t, filepath.Join(repoRootTestdataDir, "adapter-node", "v3", "handler.js"))
}

// TestPatchPrerenderedEnv_RealAdapterNodeV5 is the v5.5.7 counterpart of
// TestPatchPrerenderedEnv_RealAdapterNodeV3 — same pre-bundling-template
// scope and caveats.
func TestPatchPrerenderedEnv_RealAdapterNodeV5(t *testing.T) {
	testPatchPrerenderedEnvRealFixture(t, filepath.Join(repoRootTestdataDir, "adapter-node", "v5", "handler.js"))
}

// bundledReExportChunkRel is the chunk path the checked-in bundled fixture's
// handler.js re-exports from, relative to that handler.js.
var bundledReExportChunkRel = filepath.Join("server", "chunks", "handler-Cl6LqmpI.js")

// copyTree copies the fixture directory rooted at src into dst, so a test can
// patch a real two-file build-output shape without mutating the checked-in
// fixture.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatalf("copy fixture tree %s: %v", src, err)
	}
}

// TestPatchPrerenderedEnv_RealBundledReExport is the coverage the v3/v5 tests
// above do NOT provide: the real, post-Vite/Rollup `build/` shape, where
// handler.js is only a re-export barrel
// (`export { h as handler } from './server/chunks/handler-<hash>.js';`) and
// the prerendered-path join lives in the hashed chunk it points at. See
// testdata/adapter-node/README.md's "bundled-real" section for provenance.
//
// It asserts the matcher follows the re-export, patches the chunk, and leaves
// handler.js byte-identical — handler.js has nothing to patch, so patching it
// would mean the matcher had matched something it should not have.
func TestPatchPrerenderedEnv_RealBundledReExport(t *testing.T) {
	fixtureDir := filepath.Join(repoRootTestdataDir, "adapter-node", "bundled-real")

	originalHandler, err := os.ReadFile(filepath.Join(fixtureDir, "handler.js"))
	if err != nil {
		t.Fatalf("read fixture barrel: %v", err)
	}
	originalChunk, err := os.ReadFile(filepath.Join(fixtureDir, bundledReExportChunkRel))
	if err != nil {
		t.Fatalf("read fixture chunk: %v", err)
	}

	// Preconditions that make this fixture meaningful: the barrel must carry
	// nothing to patch, and the chunk must carry the literal exactly once.
	if strings.Contains(string(originalHandler), "prerendered") {
		t.Fatalf("fixture barrel unexpectedly mentions prerendered; it is supposed to be a pure re-export shell:\n%s", originalHandler)
	}
	if n := strings.Count(string(originalChunk), literalDirPattern); n != 1 {
		t.Fatalf("expected literal pattern %q exactly once in the bundled chunk fixture, found %d", literalDirPattern, n)
	}

	work := t.TempDir()
	buildDir := filepath.Join(work, "build")
	copyTree(t, fixtureDir, buildDir)

	handlerPath := filepath.Join(buildDir, "handler.js")
	chunkPath := filepath.Join(buildDir, bundledReExportChunkRel)
	pokkumDir := filepath.Join(work, ".pokkum")

	if err := patchPrerenderedEnv(handlerPath, pokkumDir, discLogger()); err != nil {
		t.Fatalf("patchPrerenderedEnv on bundled re-export barrel: %v", err)
	}

	// The chunk — not handler.js — is what must have been rewritten.
	gotChunk, err := os.ReadFile(chunkPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(gotChunk), wantedWrap); n != 1 {
		t.Fatalf("expected exactly one %q in the patched chunk, found %d", wantedWrap, n)
	}

	gotHandler, err := os.ReadFile(handlerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotHandler) != string(originalHandler) {
		t.Fatalf("expected the re-export barrel to be left untouched, got:\n%s", gotHandler)
	}

	// The staged sandbox copy must record the file that actually changed.
	staged, err := os.ReadFile(filepath.Join(pokkumDir, filepath.Base(chunkPath)))
	if err != nil {
		t.Fatalf("expected staged copy of the patched chunk in .pokkum sandbox: %v", err)
	}
	if !strings.Contains(string(staged), wantedWrap) {
		t.Fatalf("expected staged chunk copy to carry the patch, got:\n%s", staged)
	}

	// The checked-in fixtures themselves must be untouched.
	if cur, err := os.ReadFile(filepath.Join(fixtureDir, bundledReExportChunkRel)); err != nil || string(cur) != string(originalChunk) {
		t.Fatalf("checked-in bundled chunk fixture was mutated by the test (err=%v)", err)
	}
}

// TestPatchPrerenderedHandler_FollowsBundledReExport drives the same real
// bundled shape through the locator, i.e. the way the compiler calls it.
func TestPatchPrerenderedHandler_FollowsBundledReExport(t *testing.T) {
	work := t.TempDir()
	buildDir := filepath.Join(work, "build")
	copyTree(t, filepath.Join(repoRootTestdataDir, "adapter-node", "bundled-real"), buildDir)

	c := &Compiler{logger: discLogger()}
	if err := c.patchPrerenderedHandler(buildDir, work); err != nil {
		t.Fatalf("patchPrerenderedHandler: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(buildDir, bundledReExportChunkRel))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), wantedWrap) {
		t.Fatalf("expected the re-exported chunk to be patched, got:\n%s", got)
	}
}

// TestPatchPrerenderedEnv_ReExportIdentifierAndHashAreNotHardcoded guards the
// two per-build-unstable parts of the real shape: Rollup assigns both the
// local export identifier and the chunk's content hash, so neither may be
// baked into the matcher. Shape below is the real one from
// testdata/adapter-node/bundled-real/handler.js with only those two varied.
func TestPatchPrerenderedEnv_ReExportIdentifierAndHashAreNotHardcoded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		export string
		chunk  string
	}{
		{"different identifier and hash", `export { q as handler } from './server/chunks/handler-ZZ9aaaaa.js';`, filepath.Join("server", "chunks", "handler-ZZ9aaaaa.js")},
		{"no alias", `export { handler } from './server/chunks/handler-ZZ9aaaaa.js';`, filepath.Join("server", "chunks", "handler-ZZ9aaaaa.js")},
		{"flat chunk layout", `export { h as handler } from './handler-ZZ9aaaaa.js';`, "handler-ZZ9aaaaa.js"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			chunkPath := filepath.Join(dir, tc.chunk)
			if err := os.MkdirAll(filepath.Dir(chunkPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(chunkPath, []byte("const handler = serve(path.join(dir, 'prerendered'));\nexport { handler as h };\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "handler.js")
			if err := os.WriteFile(p, []byte("import './shims.js';\n"+tc.export+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := patchPrerenderedEnv(p, filepath.Join(dir, ".pokkum"), discLogger()); err != nil {
				t.Fatalf("patchPrerenderedEnv: %v", err)
			}
			got, err := os.ReadFile(chunkPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), wantedWrap) {
				t.Fatalf("expected chunk to be patched, got:\n%s", got)
			}
		})
	}
}

// TestPatchPrerenderedEnv_ReExportWithoutPatternStillHardFails is the
// constraint the fallback must never erode: following a re-export that leads
// nowhere useful still fails the build, rather than warning and continuing
// with prerendered pages silently resolving to the adapter default.
func TestPatchPrerenderedEnv_ReExportWithoutPatternStillHardFails(t *testing.T) {
	dir := t.TempDir()
	chunkDir := filepath.Join(dir, "server", "chunks")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chunkPath := filepath.Join(chunkDir, "handler-ZZ9aaaaa.js")
	const chunk = "const handler = serve(somewhereElse());\nexport { handler as h };\n"
	if err := os.WriteFile(chunkPath, []byte(chunk), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "handler.js")
	const barrel = "export { h as handler } from './server/chunks/handler-ZZ9aaaaa.js';\n"
	if err := os.WriteFile(p, []byte(barrel), 0o600); err != nil {
		t.Fatal(err)
	}

	err := patchPrerenderedEnv(p, filepath.Join(dir, ".pokkum"), discLogger())
	if err == nil {
		t.Fatal("expected a hard failure when neither handler.js nor its re-export target has a recognizable pattern")
	}
	if !strings.Contains(err.Error(), "no recognizable prerendered or client path pattern") {
		t.Fatalf("expected the unchanged no-match error, got: %v", err)
	}

	// Nothing may have been written to either file.
	if got, _ := os.ReadFile(p); string(got) != barrel {
		t.Fatalf("expected handler.js unchanged, got:\n%s", got)
	}
	if got, _ := os.ReadFile(chunkPath); string(got) != chunk {
		t.Fatalf("expected chunk unchanged, got:\n%s", got)
	}
}

// TestPatchPrerenderedEnv_BarePackageReExportIsNotFollowed keeps the fallback
// scoped to files the build itself emitted: a non-relative specifier is a
// node_modules package, not a sibling build artifact, so it must not be
// resolved or written to.
func TestPatchPrerenderedEnv_BarePackageReExportIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "handler.js")
	if err := os.WriteFile(p, []byte(`export { h as handler } from '@sveltejs/some-pkg';`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := patchPrerenderedEnv(p, filepath.Join(dir, ".pokkum"), discLogger()); err == nil {
		t.Fatal("expected a hard failure for a bare-package re-export")
	}
}

// TestHandlerReExportTargets_RolldownSplitForm covers the barrel shape
// SvelteKit 3 / adapter-node 6 emit.
//
// Vite 8 bundles the SSR output with Rolldown rather than Rollup, and Rolldown
// splits `export { h as handler } from './x.js'` into a separate import and
// export. handlerReExportPattern requires the `from` clause on the export, so
// it matched nothing and EVERY SvelteKit 3 build failed at the handler patch
// with "no recognizable prerendered or client path pattern" — the guard firing
// correctly on a shape it could not read.
func TestHandlerReExportTargets_RolldownSplitForm(t *testing.T) {
	// Real shape, taken from an actual SvelteKit 3 RC + adapter-node 6 build.
	const src = `import { n as handler } from "./server/chunks/handler-CKPSwPdR.js";
import "./server/chunks/index.js-Bq1P.js";
export { handler };
`
	got := handlerReExportTargets("/app/build/handler.js", src)
	want := "/app/build/server/chunks/handler-CKPSwPdR.js"
	if len(got) != 1 || got[0] != want {
		t.Errorf("handlerReExportTargets = %v, want exactly [%s]", got, want)
	}
}

// TestHandlerReExportTargets_RollupCombinedFormStillWorks pins that adding the
// split form did not regress the SvelteKit 2 / adapter-node 5 shape.
func TestHandlerReExportTargets_RollupCombinedFormStillWorks(t *testing.T) {
	const src = `export { h as handler } from './server/chunks/handler-Cl6LqmpI.js';`
	got := handlerReExportTargets("/app/build/handler.js", src)
	want := "/app/build/server/chunks/handler-Cl6LqmpI.js"
	if len(got) != 1 || got[0] != want {
		t.Errorf("handlerReExportTargets = %v, want exactly [%s]", got, want)
	}
}

// TestHandlerReExportTargets_ImportWithoutReExportIsNotABarrel guards the
// false positive the split form could introduce: a file that imports a handler
// to USE it is not re-exporting it, and patching whatever it imported would be
// wrong. The local-export requirement is what excludes it.
func TestHandlerReExportTargets_ImportWithoutReExportIsNotABarrel(t *testing.T) {
	const src = `import { handler } from './server/chunks/handler-XYZ.js';
const server = createServer(handler);
export { server };
`
	if got := handlerReExportTargets("/app/build/handler.js", src); len(got) != 0 {
		t.Errorf("handlerReExportTargets = %v, want none: the file imports handler but never re-exports it", got)
	}
}
