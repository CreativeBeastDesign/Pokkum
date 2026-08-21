package integration

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Why the runtime smoke tests inject a production dependency
//
// The startup-attestation bug fixed in b439e6b bricked every layered image
// Pokkum produced — build time hashed 11762 files under the roots it packaged,
// pokkum-init's mirrored walk set omitted /app/node_modules and could only find
// 509, so every container exited 125 with "startup attestation mismatch". The
// runtime smoke tests boot a real produced image and were still green
// throughout, and re-running them against a locally reverted fix reproduces
// that: PASS, with the container logging `startup attestation verified
// ... files=79`.
//
// The reason is the fixture. testdata/fixtures/sveltekit-adapter-node declares
// every package it needs under devDependencies and has NO production
// dependencies at all, so bunexec's stageProductionDependencies runs `bun
// install --production` and legitimately finds nothing to stage,
// prep.NodeModulesDir comes back empty, and the packager's `if
// req.AppNodeModulesDir != ""` branch never runs. No node_modules layer is
// built, no node_modules records enter the attestation manifest, and the two
// sides agree on a reduced root set — the bug is real, shipped, and invisible,
// because the only test that boots an image builds an image that does not
// contain the thing the bug is about. mem:self_review_checklist row 12: a
// fixture that structurally cannot exercise the feature cannot catch its bugs,
// however carefully the assertions are written.
//
// So the smoke tests give the scratch copy of the fixture a real production
// dependency. It is delivered as a local npm tarball (`file:/abs/path.tgz`)
// rather than a registry package for two reasons: it needs no network (the
// smoke tests already gate on one network path, to the base-image registry —
// adding npm as a second failure surface would buy nothing), and bun extracts a
// tarball dependency into node_modules as REAL regular files. A `file:` pointing
// at a directory is installed as a tree of symlinks instead, and the packager
// deliberately excludes non-regular entries from both the layer and the
// attestation records (see layer.go's fi.Mode().IsRegular() check), so a
// directory-shaped dependency would have reproduced exactly the zero-coverage
// hole this fixes.
const (
	smokeDepName    = "pokkum-smoke-dep"
	smokeDepVersion = "1.0.0"

	// smokeDepNestedMember is the in-image path of the dependency's nested
	// file. Nested on purpose: a flattened single-level fixture cannot expose a
	// prefix/nesting bug in the packaged tree (row 20), and real packages ship
	// nested directories.
	smokeDepNestedMember = "app/node_modules/" + smokeDepName + "/dist/util.js"

	// smokeDepFileCount is how many regular files the dependency ships. It is
	// the floor asserted against the produced image: if the injection silently
	// stops working (a bun behaviour change, a lockfile that reappears and
	// makes --frozen-lockfile reject the manifest), the smoke test must fail
	// loudly rather than quietly return to testing an image with no
	// node_modules in it — "did nothing" must not look like "found nothing"
	// (row 47).
	smokeDepFileCount = 3
)

// injectProductionDependency rewrites the copied fixture project so that a
// real, non-empty node_modules tree is staged, packaged at
// ports.AppNodeModulesDirPrefix, folded into the startup-attestation manifest,
// and re-derived by pokkum-init from the live tree at boot.
//
// It must only ever be handed the scratch copy from copyFixtureProject, never
// testdata/fixtures/... itself: it edits package.json and deletes the lockfile.
func injectProductionDependency(t *testing.T, projectDir string) {
	t.Helper()

	tgz := writeSmokeDepTarball(t)

	manifestPath := filepath.Join(projectDir, "package.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("injectProductionDependency: read %s: %v", manifestPath, err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("injectProductionDependency: parse %s: %v", manifestPath, err)
	}
	deps := map[string]any{}
	if existing, ok := manifest["dependencies"].(map[string]any); ok {
		// Merge rather than replace: if the fixture ever gains real production
		// dependencies of its own, silently dropping them here would change
		// what the smoke test builds without anyone noticing.
		for k, v := range existing {
			deps[k] = v
		}
	}
	deps[smokeDepName] = "file:" + tgz
	manifest["dependencies"] = deps

	out, err := json.MarshalIndent(manifest, "", "\t")
	if err != nil {
		t.Fatalf("injectProductionDependency: marshal manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("injectProductionDependency: write %s: %v", manifestPath, err)
	}

	// bunexec's stageProductionDependencies passes --frozen-lockfile whenever a
	// lockfile is present, and the manifest just changed, so the install would
	// fail. The lockfile pins devDependencies for `bun run build`, which reads
	// the node_modules symlinked in by copyFixtureProject and never installs, so
	// removing it from the scratch copy costs this test nothing. (It does mean
	// the vendored dependency is resolved unpinned — for a locally-provided
	// tarball with no transitive dependencies that resolution is the tarball
	// itself, so nothing about the build stops being deterministic.)
	for _, lock := range []string{"bun.lock", "bun.lockb", "package-lock.json", "yarn.lock", "pnpm-lock.yaml"} {
		if err := os.Remove(filepath.Join(projectDir, lock)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("injectProductionDependency: remove %s: %v", lock, err)
		}
	}
}

// writeSmokeDepTarball writes a minimal but real npm package tarball (the
// `package/`-rooted layout npm and bun both expect) and returns its absolute
// path.
func writeSmokeDepTarball(t *testing.T) string {
	t.Helper()

	files := map[string]string{
		"package/package.json": `{"name":"` + smokeDepName + `","version":"` + smokeDepVersion + `","main":"index.js"}` + "\n",
		"package/index.js":     "module.exports = require('./dist/util.js');\n",
		"package/dist/util.js": "module.exports = { pokkumSmokeDep: true };\n",
	}
	if len(files) != smokeDepFileCount {
		t.Fatalf("writeSmokeDepTarball: smokeDepFileCount = %d but the package ships %d files; keep them in sync",
			smokeDepFileCount, len(files))
	}

	tgzPath := filepath.Join(t.TempDir(), smokeDepName+"-"+smokeDepVersion+".tgz")
	f, err := os.Create(tgzPath)
	if err != nil {
		t.Fatalf("writeSmokeDepTarball: create %s: %v", tgzPath, err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	// Sorted for determinism, matching this codebase's ordering invariant even
	// in test scaffolding.
	for _, name := range []string{"package/package.json", "package/index.js", "package/dist/util.js"} {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
		}); err != nil {
			t.Fatalf("writeSmokeDepTarball: header %s: %v", name, err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatalf("writeSmokeDepTarball: write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("writeSmokeDepTarball: close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("writeSmokeDepTarball: close gzip: %v", err)
	}
	return tgzPath
}

// attestationRootMembers groups the regular tar members the produced image
// carries by which of ports.AttestationRoots they live under, reading the real
// image on disk rather than any build-time bookkeeping (row 49 / the b439e6b
// post-mortem: an oracle built from the producer's own table can only confirm
// the producer agrees with itself).
func attestationRootMembers(t *testing.T, tarballPath string) map[string][]string {
	t.Helper()

	img, err := tarball.ImageFromPath(tarballPath, nil)
	if err != nil {
		t.Fatalf("tarball.ImageFromPath: %v", err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers: %v", err)
	}

	members := map[string][]string{}
	for _, layer := range layers {
		for _, name := range tarMemberNames(t, layer) {
			for _, root := range ports.AttestationRoots {
				prefix := strings.TrimPrefix(root, "/") + "/"
				if strings.HasPrefix(name, prefix) {
					members[root] = append(members[root], name)
					break
				}
			}
		}
	}
	return members
}

// tarMemberNames returns the names of a layer's regular-file members without
// reading their content — the runtime smoke image carries a ~90MB Bun binary,
// and tarEntries (which buffers every member's bytes) would pull all of it into
// memory for a check that only needs names.
func tarMemberNames(t *testing.T, layer v1.Layer) []string {
	t.Helper()
	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatalf("layer.Uncompressed: %v", err)
	}
	defer rc.Close()

	var names []string
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		names = append(names, path.Clean(hdr.Name))
	}
	return names
}

var attestedFileCountRe = regexp.MustCompile(`startup attestation verified.*?files=(\d+)`)

// assertNodeModulesAttestedAtRuntime is the assertion the b439e6b bug would
// have failed, and the one that keeps the smoke test honest about what it
// covered.
//
// Booting the image is necessary but not sufficient: a container that starts is
// only evidence that build-time and runtime agreed on *something*, and they
// agree trivially when neither side sees any node_modules at all. So this
// asserts three things together:
//
//  1. the produced image really ships the injected dependency under
//     ports.AppNodeModulesDirPrefix (a floor of smokeDepFileCount members, plus
//     the exact nested path — row 20's "assert the exact reconstructed path,
//     not just 'some file with the right bytes somewhere'");
//  2. pokkum-init really ran the verification at boot (the log line exists at
//     all — its absence means POKKUM_ATTESTATION_DIGEST was never stamped and
//     the whole control was inert, which no amount of successful serving would
//     reveal);
//  3. the number of files it verified equals the number of regular members the
//     image actually carries under every attestation root. This is the
//     load-bearing one: it is derived from the shipped artifact, so a root the
//     packager hashes but pokkum-init never walks (or vice versa) makes the two
//     numbers disagree even in the cases where the digests would still match.
func assertNodeModulesAttestedAtRuntime(t *testing.T, tarballPath, containerLogs string) {
	t.Helper()

	members := attestationRootMembers(t, tarballPath)
	nm := len(members[ports.AppNodeModulesDirPrefix])
	if nm < smokeDepFileCount {
		t.Fatalf("image carries %d regular members under %s, want at least %d: the injected production dependency "+
			"never reached the image, so this run proved nothing about whether node_modules is attested — "+
			"see injectProductionDependency",
			nm, ports.AppNodeModulesDirPrefix, smokeDepFileCount)
	}
	if !slices.Contains(members[ports.AppNodeModulesDirPrefix], smokeDepNestedMember) {
		t.Errorf("image does not carry %s; the dependency's nested file must survive staging, packaging and prefixing intact",
			smokeDepNestedMember)
	}

	m := attestedFileCountRe.FindStringSubmatch(containerLogs)
	if m == nil {
		t.Fatalf("container logs contain no \"startup attestation verified ... files=N\" line, so the startup "+
			"attestation never ran — the image served, but the control this test exists to cover was inert. Logs:\n%s",
			containerLogs)
	}
	verified, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse attested file count from %q: %v", m[0], err)
	}

	total := 0
	counts := map[string]int{}
	for _, root := range ports.AttestationRoots {
		counts[root] = len(members[root])
		total += counts[root]
	}
	if verified != total {
		t.Errorf("pokkum-init verified %d files at boot, but the image carries %d regular members under "+
			"ports.AttestationRoots (%v; per-root: %v). A difference means the two sides do not cover the same "+
			"set of roots — exactly the drift that made every layered image exit 125 (see Lessons.md 2026-08-21).",
			verified, total, ports.AttestationRoots, counts)
	}
	t.Logf("startup attestation covered %d files, %d of them under %s", verified, nm, ports.AppNodeModulesDirPrefix)
}
