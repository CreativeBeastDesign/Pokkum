package integration

import (
	"archive/tar"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Why the runtime smoke tests assert on node_modules
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
// The reason was the fixture. testdata/fixtures/sveltekit-adapter-node used to
// declare every package it needs under devDependencies with NO production
// dependencies at all, so bunexec's stageProductionDependencies ran `bun
// install --production`, legitimately found nothing to stage, and
// prep.NodeModulesDir came back empty. The packager's `if
// req.AppNodeModulesDir != ""` branch never ran, no node_modules layer was
// built, no node_modules records entered the attestation manifest, and the two
// sides agreed on a reduced root set — the bug was real, shipped, and
// invisible, because the only test that boots an image built an image that did
// not contain the thing the bug was about. mem:self_review_checklist row 12: a
// fixture that structurally cannot exercise the feature cannot catch its bugs,
// however carefully the assertions are written.
//
// The fixture now declares a real production dependency, so every test that
// builds it — not only these smoke tests — exercises the node_modules path.
// clsx is deliberate: it was already present in the lockfile as a transitive
// dependency, so declaring it direct adds no new package to the tree, it ships
// nested files (a flattened package cannot expose a prefix/nesting bug in the
// packaged tree, row 20), and it installs as REAL regular files. That last
// point is load-bearing: the packager excludes non-regular entries from both
// the layer and the attestation records (layer.go's fi.Mode().IsRegular()
// check), so a symlinked dependency — which is what a `file:` path pointing at
// a directory produces — would have reproduced exactly this zero-coverage hole.
//
// Keeping it in the checked-in fixture rather than injecting it per-test also
// means the vendor install runs with --frozen-lockfile, the path real users
// hit; an injected manifest had to delete the lockfile to avoid being rejected
// by it, and so exercised the unpinned path instead.
const (
	smokeDepName = "clsx"

	// smokeDepNestedMember is the in-image path of a nested file the
	// dependency ships. Nested on purpose (row 20).
	smokeDepNestedMember = "app/node_modules/" + smokeDepName + "/dist/clsx.mjs"

	// smokeDepFileCount is the floor asserted against the produced image. The
	// dependency ships ten regular files and pruning strips the type
	// declarations and docs, so this is deliberately conservative: it exists to
	// fail loudly if the node_modules layer silently returns to being empty,
	// not to pin an exact post-pruning count that legitimate pruning changes
	// would churn. "Did nothing" must not look like "found nothing" (row 47).
	smokeDepFileCount = 3
)

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
		t.Fatalf("image carries %d regular members under %s, want at least %d: the fixture's production "+
			"dependency never reached the image, so this run proved nothing about whether node_modules is "+
			"attested. Check that testdata/fixtures/sveltekit-adapter-node still declares a \"dependencies\" "+
			"entry and that bun.lock matches it — with none, stageProductionDependencies correctly stages "+
			"nothing and this test silently returns to booting an image the bug cannot live in",
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

// staticContentFloor is the minimum number of regular files the produced
// static image must carry under its content roots.
//
// It is deliberately a floor and not an exact count: the fixture's page set
// and Vite's chunking both change legitimately. Its job is to fail when the
// image stops carrying a site at all — a packaging regression that ships two
// files still serves /robots.txt and /healthz perfectly well, so every
// path-specific assertion in the static smoke test would keep passing while
// the artifact became empty.
const staticContentFloor = 8

// imageMemberBytes returns the bytes of one regular member of the produced
// image, reading the real OCI archive rather than the build's staging
// directories. Used to compare what the container SERVES against what the
// image actually SHIPS.
func imageMemberBytes(t *testing.T, tarballPath, member string) []byte {
	t.Helper()

	img, err := tarball.ImageFromPath(tarballPath, nil)
	if err != nil {
		t.Fatalf("imageMemberBytes: open %s: %v", tarballPath, err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("imageMemberBytes: layers: %v", err)
	}

	var found []byte
	// Later layers win, matching layer-squash semantics.
	for _, layer := range layers {
		rc, err := layer.Uncompressed()
		if err != nil {
			t.Fatalf("imageMemberBytes: uncompressed: %v", err)
		}
		tr := tar.NewReader(rc)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				rc.Close() //nolint:errcheck // test cleanup
				t.Fatalf("imageMemberBytes: read tar: %v", err)
			}
			if hdr.Typeflag != tar.TypeReg || path.Clean(strings.TrimPrefix(hdr.Name, "./")) != member {
				continue
			}
			body, err := io.ReadAll(tr)
			if err != nil {
				rc.Close() //nolint:errcheck // test cleanup
				t.Fatalf("imageMemberBytes: read %s: %v", member, err)
			}
			found = body
		}
		rc.Close() //nolint:errcheck // test cleanup
	}
	if found == nil {
		t.Fatalf("imageMemberBytes: %q is not a regular member of the produced image", member)
	}
	return found
}

// assertStaticImageCarriesSite gives the static smoke test the coverage floor
// the layered one gets from the attestation's file count.
//
// The static strategy does not attest (no supervisor digest to compare
// against), so "the container served the three URLs we asked for" was its
// entire claim. That is the same shape as the hole that let the layered
// attestation bug ship: the test asserted an outcome without ever asking how
// much of the artifact the run actually touched, so an image that had lost
// almost all of its content would still have passed. This reads the produced
// image and asserts (a) it carries a site's worth of files under the roots
// pokkum-static is configured to serve, and (b) a real content-hashed asset
// the image ships is served byte-for-byte — not merely 200.
func assertStaticImageCarriesSite(t *testing.T, tarballPath string, appPort int) {
	t.Helper()

	members := attestationRootMembers(t, tarballPath)
	client := members[ports.AppClientDirPrefix]
	prerendered := members[ports.AppPrerenderedDirPrefix]
	total := len(client) + len(prerendered)

	if total < staticContentFloor {
		t.Errorf("produced static image carries only %d regular files under %s + %s (want >= %d). "+
			"pokkum-static serves exactly these roots (%s), so an image this empty is not a site — "+
			"and every path-specific assertion in this test can still pass against it.",
			total, ports.AppClientDirPrefix, ports.AppPrerenderedDirPrefix, staticContentFloor,
			ports.EnvStaticRoots)
	}
	t.Logf("static image carries %d files under %s and %d under %s",
		len(client), ports.AppClientDirPrefix, len(prerendered), ports.AppPrerenderedDirPrefix)

	// Pick a content-hashed immutable asset out of the IMAGE (not out of the
	// build directory): the question is whether what shipped is what is
	// served, so both sides of the comparison must come from the artifact.
	immutable := make([]string, 0, len(client))
	for _, m := range client {
		if strings.Contains(m, "/_app/immutable/") && strings.HasSuffix(m, ".js") &&
			!strings.HasSuffix(m, ".br") && !strings.HasSuffix(m, ".gz") {
			immutable = append(immutable, m)
		}
	}
	if len(immutable) == 0 {
		t.Fatalf("produced static image carries no immutable client JS asset; client members: %v", client)
	}
	slices.Sort(immutable) // deterministic pick
	member := immutable[0]

	url := "http://127.0.0.1:" + strconv.Itoa(appPort) + "/" + strings.TrimPrefix(member, "app/client/")
	ok, body := pollHTTP200Body(url, 30*time.Second)
	if !ok {
		t.Fatalf("static server never served %s (an asset the image demonstrably contains at %s)", url, member)
	}
	if want := imageMemberBytes(t, tarballPath, member); string(want) != body {
		t.Errorf("served bytes for %s differ from the bytes the image ships at %s: served %d bytes, image has %d. "+
			"A 200 alone would not have caught this.", url, member, len(body), len(want))
	}
}
