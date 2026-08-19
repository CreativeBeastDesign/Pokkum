package staticserver

// blob_freshness_test.go closes a gap left open by the 2026-08-19 CI/release
// fix (see Lessons.md's "The embedded PID-1 binaries were gitignored local
// artifacts that no CI job or release pipeline built" entry): CI now
// rebuilds internal/adapters/staticserver/bin and internal/adapters/supervisor/
// bin's go:embedded pokkum-static/pokkum-init blobs (`make supervisor
// static-server`), but nothing yet checked that a *locally* built blob still
// on disk actually corresponds to the source that produced it. A developer
// who edits supervisor/cmd/pokkum-static and forgets to rerun `make
// static-server` gets zero compiler or test signal: go:embed happily embeds
// the stale .zst, and the bug only ever shows up at runtime inside a produced
// image. That exact scenario cost real debugging time twice this session and
// was only caught because a container log printed a line the fixed code
// could not produce.
//
// Mechanism: for every platform with an embedded blob, rebuild the PID-1
// binary fresh from source (go build -trimpath -buildvcs=false -ldflags "-s -w", identical
// to the Makefile's supervisor/static-server targets) and compare it against
// what is actually embedded, byte-for-byte.
//
// Decompressed bytes are compared, not the raw .zst bytes. Empirically
// verified before choosing (see Lessons.md): two consecutive `go build
// -trimpath -buildvcs=false -ldflags "-s -w"` runs of the same source produced identical
// SHA256 raw ELF output, and scripts/compress-zstd.go's single-shot
// zstd.EncodeAll(SpeedBestCompression) is likewise bit-for-bit reproducible
// across repeated runs on this toolchain — so a raw compressed-bytes
// comparison would also work today. It was rejected anyway: the compressed
// bytes additionally depend on github.com/klauspost/compress/zstd's exact
// encoder internals, which is free to change its output for unchanged input
// across a future dependency bump (only the zstd *decode* format carries a
// stability guarantee — its encoder does not). Comparing decompressed
// content anchors the guard to what actually matters, the PID-1 bytes that
// end up running as PID 1 inside a produced image, decoded with the exact
// same embeddedbinaryutils path both adapters use at runtime — so a future
// zstd encoder change cannot produce a false failure unrelated to staleness.
//
// This runs unconditionally: no -short gate, no network, and no tool other
// than the Go toolchain that is already required to run `go test` at all.
// Each rebuild is a hermetic `CGO_ENABLED=0 go build` for a package already
// part of this module; measured at roughly 2s per platform with a warm
// build cache (four platform/binary combinations run in parallel below).
// Because it is unconditional, `go test ./internal/adapters/...` — step 3 of
// the agent verification suite (Makefile's `verify` target, mem:task_completion)
// — is exactly where this is meant to catch a stale blob locally, not only in
// CI. It is deliberately *not* part of the fast/-short-gated main CI job
// (ci.yml's "Run Full Test Suite" step): that job passes -short, which this
// test ignores by design, so it always runs wherever the adapter package
// tests run without -short.
//
// A fresh checkout (only .gitkeep, no .zst yet) is not a failure: it skips,
// mirroring provider_test.go's own "not embedded yet" skip convention,
// because `make supervisor static-server` genuinely has not run yet and
// there is nothing to compare against.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/embeddedbinaryutils"
)

// errPID1BlobDecodedEmpty is the sentinel passed to embeddedbinaryutils.Decode
// for this test's own decode calls. It is distinct from either adapter's own
// errStaticServerCorrupt/errSupervisorCorrupt sentinels (the latter is
// unexported in a different package and not reachable from here) — this test
// only needs *an* error to detect "decoded to nothing", not a specific one,
// since a decode failure here always becomes a t.Fatalf, never a returned error.
var errPID1BlobDecodedEmpty = errors.New("embedded PID-1 blob decoded to empty content")

// pid1BlobTarget describes one embedded PID-1 binary: its source package, the
// directory holding its compressed platform blobs, and the filename prefix
// used for each platform. Mirrors the Makefile's SUPERVISOR_BIN/STATIC_BIN
// variables and `supervisor`/`static-server` targets, and each adapter's own
// `//go:embed all:bin` exactly.
type pid1BlobTarget struct {
	name       string // human-readable, used in subtest/failure names
	sourcePkg  string // import path passed to `go build`
	binDir     string // directory (relative to module root) holding the .zst blobs
	blobPrefix string // e.g. "pokkum-static-linux-" — arch + ".zst" is appended
}

// pid1Targets covers both PID-1 binaries from this one test, rather than one
// guard per adapter package: the risk (stale blob, no build signal), the
// mechanism (rebuild + decompressed-byte compare), and the fix command are
// identical for both, and internal/adapters/supervisor's own provider.go
// already documents itself as "mirroring the static server build exactly".
var pid1Targets = []pid1BlobTarget{
	{
		name:       "pokkum-static",
		sourcePkg:  "./supervisor/cmd/pokkum-static",
		binDir:     "internal/adapters/staticserver/bin",
		blobPrefix: "pokkum-static-linux-",
	},
	{
		name:       "pokkum-init",
		sourcePkg:  "./supervisor/cmd/pokkum-init",
		binDir:     "internal/adapters/supervisor/bin",
		blobPrefix: "pokkum-init-linux-",
	},
}

// pid1Arches mirrors the Makefile's `for arch in amd64 arm64` loop.
var pid1Arches = []string{"amd64", "arm64"}

// TestEmbeddedPID1Binaries_MatchSource is the blob-freshness guard described
// in the file-level comment above: for every embedded platform of both PID-1
// binaries, it rebuilds fresh from source and diffs the result against what
// go:embed will actually pack into the pokkum CLI, byte-for-byte
// (decompressed).
func TestEmbeddedPID1Binaries_MatchSource(t *testing.T) {
	root := pid1ModuleRoot(t)

	for _, target := range pid1Targets {
		for _, arch := range pid1Arches {
			target, arch := target, arch
			t.Run(target.name+"/"+arch, func(t *testing.T) {
				t.Parallel()

				blobPath := filepath.Join(root, target.binDir, target.blobPrefix+arch+".zst")
				compressed, err := os.ReadFile(blobPath)
				if err != nil {
					if os.IsNotExist(err) {
						t.Skipf("%s not embedded yet; run `make supervisor static-server` first", blobPath)
					}
					t.Fatalf("reading embedded blob %s: %v", blobPath, err)
				}

				decoder := embeddedbinaryutils.NewDecoder()
				embedded, err := embeddedbinaryutils.Decode(decoder, compressed, errPID1BlobDecodedEmpty)
				if err != nil {
					t.Fatalf("decompressing embedded blob %s: %v", blobPath, err)
				}
				if len(embedded) == 0 {
					t.Fatalf("embedded blob %s decoded to zero bytes", blobPath)
				}

				fresh := pid1BuildFresh(t, root, target.sourcePkg, arch)

				if !bytes.Equal(embedded, fresh) {
					t.Errorf(
						"embedded %s binary for linux/%s (%s, %d bytes decompressed) does "+
							"not match a fresh build of %s (%d bytes) — the embedded blob "+
							"was built from stale source and never rebuilt.\n"+
							"Fix: run `make supervisor static-server` to regenerate both "+
							"embedded PID-1 binaries from current source, then rerun this test.",
						target.name, arch, blobPath, len(embedded), target.sourcePkg, len(fresh),
					)
				}
			})
		}
	}
}

// pid1ModuleRoot resolves the repository root from this test file's own
// location (internal/adapters/staticserver/blob_freshness_test.go, three
// directories below the module root) rather than assuming a working
// directory, since `go test` may be invoked from any directory.
func pid1ModuleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to resolve this test file's own path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolved module root %q from %q does not contain go.mod: %v", root, thisFile, err)
	}
	return root
}

// pid1BuildFresh cross-compiles sourcePkg for linux/arch with the exact flags
// the Makefile's supervisor/static-server targets use (-trimpath -buildvcs=false -ldflags
// "-s -w", CGO_ENABLED=0) and returns the resulting binary's bytes. A build
// failure is a genuine finding (the source does not even compile for this
// platform) and fails the test rather than skipping — unlike a missing
// embedded blob, there is nothing conditional about the Go toolchain being
// able to build a package that already ships in this module.
func pid1BuildFresh(t *testing.T, root, sourcePkg, arch string) []byte {
	t.Helper()

	outPath := filepath.Join(t.TempDir(), "pid1-fresh-build")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w", "-o", outPath, sourcePkg)
	cmd.Dir = root
	cmd.Env = pid1BuildEnv("linux", arch)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s (CGO_ENABLED=0 GOOS=linux GOARCH=%s) failed: %v\n%s", sourcePkg, arch, err, out)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading freshly built %s: %v", sourcePkg, err)
	}
	return data
}

// pid1BuildEnv returns os.Environ() with CGO_ENABLED/GOOS/GOARCH overridden.
// Existing entries for those keys are filtered out first rather than merely
// appending overrides after them, since duplicate environment entries are not
// guaranteed to resolve to the last occurrence across every platform.
func pid1BuildEnv(goos, goarch string) []string {
	overrides := map[string]string{
		"CGO_ENABLED": "0",
		"GOOS":        goos,
		"GOARCH":      goarch,
	}
	base := os.Environ()
	env := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		key, _, found := strings.Cut(kv, "=")
		if found {
			if _, overridden := overrides[key]; overridden {
				continue
			}
		}
		env = append(env, kv)
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}
