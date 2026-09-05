package bunexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// Production dependency vendoring — the fix for images that were not
// self-contained.
//
// Before this, a layered image shipped the server bundle with bare imports and
// no dependency tree. Bun's runtime auto-install masked it by fetching the
// missing packages from npm inside the running container: code that is not in
// the image, not in the SBOM, not covered by the signature and not named in the
// provenance, executing in production. Under readOnlyRootFilesystem the write
// failed and the pod crash-looped after a successful-looking rollout.

// A missing lockfile must not stop the build.
//
// The first cut of this made it a hard error, on the reasoning that an unpinned
// dependency tree cannot be reproduced. That was over-reach: it turned a
// reproducibility preference into a gate on building at all, and would have
// refused every project without a lockfile — including three of this package's
// own fixtures, which is how it was caught. The honest behaviour is to install
// anyway and say plainly what it costs.
func TestStageProductionDependencies_MissingLockfileDoesNotRefuseTheBuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := stageProductionDependencies(context.Background(), dir, false, discLogger())
	if errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("a missing lockfile was treated as an invalid request; it should warn and proceed: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "no lockfile") {
		t.Errorf("the build was refused for want of a lockfile: %v", err)
	}
}

func TestStageProductionDependencies_RequiresAManifest(t *testing.T) {
	if _, err := stageProductionDependencies(context.Background(), t.TempDir(), false, discLogger()); err == nil {
		t.Fatal("a directory with no package.json was accepted")
	}
}

// TestStageProductionDependencies_StagesUnderPokkumDir pins the zero-mutation
// invariant: the install must never write into the user's own tree. It runs
// against a project whose lockfile is deliberately unusable, so the staging
// happens and the install then fails — which is enough to observe where the
// files were put.
func TestStageProductionDependencies_StagesUnderPokkumDir(t *testing.T) {
	dir := t.TempDir()
	writeVendorFixture(t, dir)

	before := snapshotTree(t, dir)

	_, _ = stageProductionDependencies(context.Background(), dir, false, discLogger())

	stage := filepath.Join(dir, ".pokkum", "vendor")
	if _, err := os.Stat(filepath.Join(stage, "package.json")); err != nil {
		t.Errorf("package.json was not staged under .pokkum/vendor: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "bun.lock")); err != nil {
		t.Errorf("lockfile was not staged under .pokkum/vendor: %v", err)
	}

	// Everything the user wrote must be byte-identical, and no new file may
	// appear outside .pokkum/.
	for path, sum := range before {
		now, err := os.ReadFile(path) //nolint:gosec // paths come from our own snapshot of a temp dir
		if err != nil {
			t.Errorf("user file disappeared: %s", path)
			continue
		}
		if string(now) != sum {
			t.Errorf("user file was modified: %s", path)
		}
	}
	for path := range snapshotTree(t, dir) {
		if _, existed := before[path]; !existed && !strings.Contains(path, string(os.PathSeparator)+".pokkum"+string(os.PathSeparator)) {
			t.Errorf("wrote outside the .pokkum sandbox: %s", path)
		}
	}
}

func TestFindLockfile_PrefersBunLockAndReportsAbsence(t *testing.T) {
	dir := t.TempDir()
	if name, _ := findLockfile(dir); name != "" {
		t.Errorf("empty dir reported lockfile %q", name)
	}
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if name, _ := findLockfile(dir); name != "package-lock.json" {
		t.Errorf("npm lockfile not found, got %q", name)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if name, _ := findLockfile(dir); name != "bun.lock" {
		t.Errorf("bun.lock should win when both are present, got %q", name)
	}
}

func writeVendorFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","dependencies":{"nope-not-real":"1.0.0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte(`{"lockfileVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src.js"), []byte("// user source"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil //nolint:nilerr // a partially-readable tree is still a usable snapshot
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // temp dir under the test's control
		if rerr == nil {
			out[path] = string(data)
		}
		return nil
	})
	return out
}

// --- staleness: the staged tree is keyed on content, never on mtime ---------
//
// The install used to wipe and fully re-materialise .pokkum/vendor on every
// build, including the overwhelmingly common one where nothing about the
// dependency set had changed. These pin both halves of the fix: that an
// unchanged (package.json, lockfile) pair is reused, and — the half an
// mtime-based key would get wrong — that a *reverted* lockfile carrying an
// older timestamp than the staged tree still invalidates it.

// writeVendorProject writes a project whose package.json and lockfile have the
// given contents.
func writeVendorProject(t *testing.T, dir, manifest, lock string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
}

const vendorManifestA = `{"name":"x","dependencies":{"left-pad":"1.0.0"}}`
const vendorLockA = `{"lockfileVersion":1,"left-pad":"1.0.0"}`
const vendorLockB = `{"lockfileVersion":1,"left-pad":"1.0.1"}`

// installCounterBun puts a fake `bun` on PATH that records every `bun install`
// invocation and materialises the node_modules tree a real one would. Returns
// the path of the log file.
func installCounterBun(t *testing.T) string {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "installs.log")
	putFakeBunOnPath(t, `
case "$1" in
  install)
    echo install >> `+counter+`
    mkdir -p node_modules/left-pad
    printf '{"name":"left-pad","version":"1.0.0","main":"index.js"}\n' > node_modules/left-pad/package.json
    printf 'module.exports=1;\n' > node_modules/left-pad/index.js
    ;;
esac
exit 0`)
	return counter
}

func installCount(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter) //nolint:gosec // path is this test's own temp dir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		t.Fatal(err)
	}
	return len(strings.Fields(string(data)))
}

func TestStageProductionDependencies_ReusesStagedTreeWhenInputsUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeVendorProject(t, dir, vendorManifestA, vendorLockA)
	counter := installCounterBun(t)

	first, err := stageProductionDependencies(context.Background(), dir, false, discLogger())
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if first == "" {
		t.Fatal("first install staged no node_modules; the fake bun should have created one")
	}
	if got := installCount(t, counter); got != 1 {
		t.Fatalf("installs after first call = %d, want 1", got)
	}

	second, err := stageProductionDependencies(context.Background(), dir, false, discLogger())
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if second != first {
		t.Errorf("reused tree = %q, want the first call's %q", second, first)
	}
	if got := installCount(t, counter); got != 1 {
		t.Errorf("installs after second call with identical inputs = %d, want 1 (the staged tree must be reused)", got)
	}
	// The reuse must be a genuine skip, not a re-install that happened to
	// produce the same path: the tree the first install left behind is still
	// there untouched.
	if _, err := os.Stat(filepath.Join(second, "left-pad", "package.json")); err != nil {
		t.Errorf("reused tree is not intact: %v", err)
	}
}

func TestStageProductionDependencies_ChangedLockfileInvalidatesTheStagedTree(t *testing.T) {
	dir := t.TempDir()
	writeVendorProject(t, dir, vendorManifestA, vendorLockA)
	counter := installCounterBun(t)

	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err != nil {
		t.Fatalf("first install: %v", err)
	}
	writeVendorProject(t, dir, vendorManifestA, vendorLockB)
	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err != nil {
		t.Fatalf("install after lockfile change: %v", err)
	}
	if got := installCount(t, counter); got != 2 {
		t.Errorf("installs = %d, want 2: a changed lockfile must invalidate the staged tree", got)
	}
}

func TestStageProductionDependencies_ChangedManifestInvalidatesTheStagedTree(t *testing.T) {
	dir := t.TempDir()
	writeVendorProject(t, dir, vendorManifestA, vendorLockA)
	counter := installCounterBun(t)

	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err != nil {
		t.Fatalf("first install: %v", err)
	}
	writeVendorProject(t, dir, `{"name":"x","dependencies":{"left-pad":"1.0.0","right-pad":"2.0.0"}}`, vendorLockA)
	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err != nil {
		t.Fatalf("install after manifest change: %v", err)
	}
	if got := installCount(t, counter); got != 2 {
		t.Errorf("installs = %d, want 2: a changed package.json must invalidate the staged tree", got)
	}
}

// TestStageProductionDependencies_RevertedLockfileWithOlderMtimeInvalidates is
// the case an mtime-keyed cache gets silently wrong, and the reason the key is
// a content digest.
//
// Sequence: install from lockfile A, then from lockfile B, then check A back
// out — a `git checkout`, a revert, a restored backup. The restored file's
// content differs from what the staged tree was installed from, so it MUST
// reinstall; but its modification time is *older* than the staged tree, so
// "is the lockfile newer than the stage?" answers "no, still fresh" and serves
// B's dependency versions into an image built from A's lockfile, with no error
// anywhere. The test asserts the misleading mtime relationship explicitly, so
// it is testing the hazard rather than assuming it.
func TestStageProductionDependencies_RevertedLockfileWithOlderMtimeInvalidates(t *testing.T) {
	dir := t.TempDir()
	writeVendorProject(t, dir, vendorManifestA, vendorLockA)
	counter := installCounterBun(t)

	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err != nil {
		t.Fatalf("install from lockfile A: %v", err)
	}
	writeVendorProject(t, dir, vendorManifestA, vendorLockB)
	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err != nil {
		t.Fatalf("install from lockfile B: %v", err)
	}
	if got := installCount(t, counter); got != 2 {
		t.Fatalf("installs before the revert = %d, want 2", got)
	}

	// Revert to A's bytes, backdated well before the staged tree was written.
	lockPath := filepath.Join(dir, "bun.lock")
	if err := os.WriteFile(lockPath, []byte(vendorLockA), 0o600); err != nil {
		t.Fatal(err)
	}
	backdated := time.Unix(1_000_000, 0)
	if err := os.Chtimes(lockPath, backdated, backdated); err != nil {
		t.Fatal(err)
	}

	// Prove the mtime relationship that would fool a timestamp-keyed cache.
	lockInfo, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Stat(filepath.Join(dir, ".pokkum", "vendor", vendorStampFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !lockInfo.ModTime().Before(stageInfo.ModTime()) {
		t.Fatalf("test setup is not exercising the hazard: lockfile mtime %v is not older than the staged tree's %v",
			lockInfo.ModTime(), stageInfo.ModTime())
	}

	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err != nil {
		t.Fatalf("install after reverting the lockfile: %v", err)
	}
	if got := installCount(t, counter); got != 3 {
		t.Errorf("installs after reverting the lockfile = %d, want 3: a reverted lockfile whose mtime is older than "+
			"the staged tree must still invalidate it (content key, not mtime)", got)
	}
}

// A failed install must not leave a stamp behind, or the next build reuses a
// tree that was never completed.
func TestStageProductionDependencies_FailedInstallLeavesNoReusableStamp(t *testing.T) {
	dir := t.TempDir()
	writeVendorProject(t, dir, vendorManifestA, vendorLockA)
	counter := filepath.Join(t.TempDir(), "installs.log")
	putFakeBunOnPath(t, `echo install >> `+counter+`; echo "lockfile had changes" >&2; exit 1`)

	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err == nil {
		t.Fatal("a failing bun install was reported as success")
	}
	if _, err := os.Stat(filepath.Join(dir, ".pokkum", "vendor", vendorStampFilename)); err == nil {
		t.Error("a staleness stamp was written for an install that failed")
	}
	if _, err := stageProductionDependencies(context.Background(), dir, false, discLogger()); err == nil {
		t.Fatal("the second call succeeded; a failed install must not become a cache hit")
	}
	if got := installCount(t, counter); got != 2 {
		t.Errorf("installs = %d, want 2: a failed install must be retried, not served from cache", got)
	}
}
