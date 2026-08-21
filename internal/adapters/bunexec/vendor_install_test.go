package bunexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
