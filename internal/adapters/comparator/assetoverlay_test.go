package comparator_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/assetoverlay"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/comparator"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// --- shared fixture helpers -------------------------------------------------

// assetOverlayBaseImage returns a minimal base image with a populated
// config, standing in for a real distroless base — the packager only reads
// Architecture/OS/Config off it, never its actual filesystem content, so an
// empty.Image with a config is sufficient for exercising real layer/config
// construction end to end.
func assetOverlayBaseImage(t *testing.T) v1.Image {
	t.Helper()
	cfg := &v1.ConfigFile{Architecture: "amd64", OS: "linux"}
	img, err := mutate.ConfigFile(empty.Image, cfg)
	if err != nil {
		t.Fatalf("build base image: %v", err)
	}
	return img
}

// writeFile creates dir/rel with content, creating parent directories as
// needed, and returns dir.
func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return dir
}

// buildLayeredImage packages a real StrategyLayered image via the real
// packager, the same code path core.Build uses — RuntimeNode is used
// throughout so no real Bun binary is needed (the packager's validation only
// requires BunRuntime.BinaryPath for RuntimeBun).
func buildLayeredImage(t *testing.T, base v1.Image, appServerDir, appClientDir, assetOverlayDir string, sourceDigests []string, ts time.Time) v1.Image {
	t.Helper()
	req := ports.PackageRequest{
		Platform:                  ports.LinuxAMD64,
		Base:                      base,
		Strategy:                  ports.StrategyLayered,
		AppRuntime:                ports.RuntimeNode,
		AppServerDir:              appServerDir,
		AppClientDir:              appClientDir,
		AssetOverlayDir:           assetOverlayDir,
		AssetOverlaySourceDigests: sourceDigests,
		Supervisor:                []byte("fake-pokkum-init-binary"),
		CreatedAt:                 ts,
	}
	img, err := packager.NewPackager(nil).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("package image: %v", err)
	}
	return img
}

// pushImage pushes img to host/repo:tag on the given test registry server
// and returns its bare "sha256:..." digest string.
func pushImage(t *testing.T, serverURL, repo, tag string, img v1.Image) string {
	t.Helper()
	host := strings.TrimPrefix(serverURL, "http://")
	ref, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", host, repo, tag), name.WeakValidation)
	if err != nil {
		t.Fatalf("parse push ref: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("push image: %v", err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatalf("digest image: %v", err)
	}
	return d.String()
}

func writeLocalTarball(t *testing.T, img v1.Image, refStr string) string {
	t.Helper()
	f, err := os.CreateTemp("", "pokkum-comparator-local-*.tar")
	if err != nil {
		t.Fatalf("create temp tar: %v", err)
	}
	defer f.Close()
	ref, err := name.ParseReference(refStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("parse local ref: %v", err)
	}
	if err := tarball.Write(ref, img, f); err != nil {
		t.Fatalf("write local tarball: %v", err)
	}
	return f.Name()
}

// --- tests -------------------------------------------------------------

// TestComparator_AssetOverlay_LegitimateOverlayVerifies is the regression
// test for the false positive described in
// docs/items/asset-overlay-verify-gap.md: an image legitimately built with
// --asset-overlay must verify successfully even though the local rebuild
// handed to `pokkum verify --against` has no idea --asset-overlay was ever
// used (verify carries no --asset-overlay flags of its own, by design).
//
// This test is written so that reverting the reconciliation call in
// comparator.go's CompareImages makes it fail with an L3 (or worse) result —
// see the fail-first check performed manually before this change was
// committed (recorded in the task's own verification, not re-run here).
func TestComparator_AssetOverlay_LegitimateOverlayVerifies(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	const repo = "acme/asset-overlay-app"

	ts := time.Unix(1_700_000_000, 0).UTC()
	base := assetOverlayBaseImage(t)

	// Generation 0 (the predecessor): a real, previously-pushed image whose
	// own /app/client layer carries one content-hashed immutable asset —
	// exactly what a real prior SvelteKit build's output looks like.
	gen0ClientDir := t.TempDir()
	writeFile(t, gen0ClientDir, "_app/immutable/chunks/abc123.js", "console.log('gen0 cached chunk');")
	gen0ServerDir := t.TempDir()
	writeFile(t, gen0ServerDir, "index.js", "console.log('gen0 server');")
	gen0Img := buildLayeredImage(t, base, gen0ServerDir, gen0ClientDir, "", nil, ts)
	gen0Digest := pushImage(t, server.URL, repo, "gen0", gen0Img)

	// Reconstruct the merged overlay directory exactly the way core.Build's
	// Stage 4.4 -- Stage 6 pipeline does: resolve the predecessor's
	// immutable assets via the real assetoverlay adapter against the real
	// registry, then package that directory as its own layer.
	resolver := assetoverlay.NewResolver()
	overlayDir, err := resolver.BuildOverlayDir(context.Background(), fmt.Sprintf("%s/%s", strings.TrimPrefix(server.URL, "http://"), repo), []string{gen0Digest}, "", false)
	if err != nil {
		t.Fatalf("build overlay dir: %v", err)
	}
	defer os.RemoveAll(overlayDir)

	// The current generation: same server content and same CURRENT client
	// content in both the "real pushed image" and the "operator's plain
	// local rebuild" below -- the only difference between them is the
	// pushed image legitimately carries the --asset-overlay layer (built
	// from gen0's content) and its sources annotation; the local rebuild
	// has neither, simulating an operator's `pokkum build --tarball`
	// rebuild that has no idea --asset-overlay was ever used.
	serverDir := t.TempDir()
	writeFile(t, serverDir, "index.js", "console.log('current server');")
	clientDir := t.TempDir()
	writeFile(t, clientDir, "_app/immutable/chunks/def456.js", "console.log('current build own chunk');")

	pushedImg := buildLayeredImage(t, base, serverDir, clientDir, overlayDir, []string{gen0Digest}, ts)
	pushedDigest := pushImage(t, server.URL, repo, "current", pushedImg)

	localImg := buildLayeredImage(t, base, serverDir, clientDir, "", nil, ts)
	localTarPath := writeLocalTarball(t, localImg, fmt.Sprintf("%s/%s:current", strings.TrimPrefix(server.URL, "http://"), repo))
	defer os.Remove(localTarPath)

	c := comparator.NewComparatorWithAssetOverlay(slog.Default(), assetoverlay.NewResolver(), packager.NewLayerBuilder())
	res, err := c.CompareImages(context.Background(), ports.ImageComparatorRequest{
		RemoteImageRef: fmt.Sprintf("%s/%s@%s", strings.TrimPrefix(server.URL, "http://"), repo, pushedDigest),
		LocalTarball:   localTarPath,
	})
	if err != nil {
		t.Fatalf("compare images: %v", err)
	}
	if res.Level == "L3" {
		t.Fatalf("expected a non-mismatch verdict for a legitimate --asset-overlay image, got L3: %s\ndiffs: %v", res.Summary, res.L3FileDiffs)
	}
	if !res.L2SemanticMatch {
		t.Errorf("expected at least L2 semantic match once the asset-overlay layer is reconstructed, got level=%s summary=%s", res.Level, res.Summary)
	}
}

// TestComparator_AssetOverlay_TamperedAnnotationFailsClosed proves the
// fail-closed half of the fix: an image whose pokkum.dev/asset-overlay-sources
// annotation does not actually describe its own asset-overlay layer content
// (simulating a tampered annotation, or a tampered layer -- the two are
// indistinguishable from the outside, which is exactly why any mismatch
// here must be treated as tampering rather than silently ignored) must
// still fail comparison. A fix that made verification pass unconditionally
// would satisfy the companion "legitimate overlay verifies" test alone
// while destroying the feature -- this test is what actually guards against
// that.
func TestComparator_AssetOverlay_TamperedAnnotationFailsClosed(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	const repo = "acme/asset-overlay-tampered"
	registryHost := strings.TrimPrefix(server.URL, "http://")

	ts := time.Unix(1_700_000_000, 0).UTC()
	base := assetOverlayBaseImage(t)

	// Two distinct, real predecessors with different immutable content.
	gen0ClientDir := t.TempDir()
	writeFile(t, gen0ClientDir, "_app/immutable/chunks/real.js", "console.log('the real gen0 content');")
	gen0ServerDir := t.TempDir()
	writeFile(t, gen0ServerDir, "index.js", "console.log('gen0 server');")
	gen0Img := buildLayeredImage(t, base, gen0ServerDir, gen0ClientDir, "", nil, ts)
	gen0Digest := pushImage(t, server.URL, repo, "gen0", gen0Img)

	gen1ClientDir := t.TempDir()
	writeFile(t, gen1ClientDir, "_app/immutable/chunks/decoy.js", "console.log('a different generation entirely');")
	gen1ServerDir := t.TempDir()
	writeFile(t, gen1ServerDir, "index.js", "console.log('gen1 server');")
	gen1Img := buildLayeredImage(t, base, gen1ServerDir, gen1ClientDir, "", nil, ts)
	gen1Digest := pushImage(t, server.URL, repo, "gen1", gen1Img)

	// Build the ACTUAL overlay layer content from gen0 (the real
	// predecessor whose content is really baked into the pushed image's
	// overlay layer)...
	resolver := assetoverlay.NewResolver()
	overlayDir, err := resolver.BuildOverlayDir(context.Background(), fmt.Sprintf("%s/%s", registryHost, repo), []string{gen0Digest}, "", false)
	if err != nil {
		t.Fatalf("build overlay dir: %v", err)
	}
	defer os.RemoveAll(overlayDir)

	serverDir := t.TempDir()
	writeFile(t, serverDir, "index.js", "console.log('current server');")
	clientDir := t.TempDir()
	writeFile(t, clientDir, "_app/immutable/chunks/def456.js", "console.log('current build own chunk');")

	// ...but stamp the annotation as if it came from gen1 -- a tampered
	// (or simply wrong) annotation that does not describe the real layer
	// content. The packager stamps whatever AssetOverlaySourceDigests it is
	// given verbatim; this is exactly the shape a corrupted or
	// maliciously-edited annotation would take from the outside.
	pushedImg := buildLayeredImage(t, base, serverDir, clientDir, overlayDir, []string{gen1Digest}, ts)
	pushedDigest := pushImage(t, server.URL, repo, "current", pushedImg)

	localImg := buildLayeredImage(t, base, serverDir, clientDir, "", nil, ts)
	localTarPath := writeLocalTarball(t, localImg, fmt.Sprintf("%s/%s:current", registryHost, repo))
	defer os.Remove(localTarPath)

	c := comparator.NewComparatorWithAssetOverlay(slog.Default(), assetoverlay.NewResolver(), packager.NewLayerBuilder())
	res, err := c.CompareImages(context.Background(), ports.ImageComparatorRequest{
		RemoteImageRef: fmt.Sprintf("%s/%s@%s", registryHost, repo, pushedDigest),
		LocalTarball:   localTarPath,
	})
	if err == nil {
		t.Fatalf("expected comparison to fail closed for a tampered asset-overlay annotation, got success: level=%s summary=%s", res.Level, res.Summary)
	}
	if !strings.Contains(err.Error(), "does not describe the actual layer content") && !strings.Contains(err.Error(), "asset-overlay") {
		t.Errorf("expected an asset-overlay-specific mismatch error, got: %v", err)
	}
}

// TestComparator_AssetOverlay_MalformedAnnotationFailsClosed proves that an
// unparseable pokkum.dev/asset-overlay-sources annotation is a hard error,
// never a silent skip of the overlay reconciliation.
//
// The annotated image is pushed through a real registry (not a local
// tarball): go-containerregistry's tarball package implements the legacy
// `docker save` tar format, which has no manifest-annotations field at all
// — round-tripping an annotated image through tarball.Write/ImageFromPath
// silently drops the annotation, which would make this test pass for the
// wrong reason (annotation invisible, not annotation rejected).
func TestComparator_AssetOverlay_MalformedAnnotationFailsClosed(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	const repo = "acme/asset-overlay-malformed"
	registryHost := strings.TrimPrefix(server.URL, "http://")

	ts := time.Unix(1_700_000_000, 0).UTC()
	base := assetOverlayBaseImage(t)
	serverDir := t.TempDir()
	writeFile(t, serverDir, "index.js", "console.log('server');")
	clientDir := t.TempDir()
	writeFile(t, clientDir, "_app/immutable/chunks/x.js", "console.log('x');")

	img := buildLayeredImage(t, base, serverDir, clientDir, "", nil, ts)
	annotated, ok := mutate.Annotations(img, map[string]string{
		ports.AnnotationAssetOverlaySources: "not-a-valid-digest",
	}).(v1.Image)
	if !ok {
		t.Fatal("annotate: result is not an image")
	}
	annotatedDigest := pushImage(t, server.URL, repo, "current", annotated)

	localTarPath := writeLocalTarball(t, img, fmt.Sprintf("%s/%s:current", registryHost, repo))
	defer os.Remove(localTarPath)

	c := comparator.NewComparatorWithAssetOverlay(slog.Default(), assetoverlay.NewResolver(), packager.NewLayerBuilder())
	_, err := c.CompareImages(context.Background(), ports.ImageComparatorRequest{
		RemoteImageRef: fmt.Sprintf("%s/%s@%s", registryHost, repo, annotatedDigest),
		LocalTarball:   localTarPath,
	})
	if err == nil {
		t.Fatal("expected an error for a malformed asset-overlay annotation, got success")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("expected a malformed-annotation error, got: %v", err)
	}
}

// TestComparator_AssetOverlay_NoReconstructionSupportFailsClosed proves that
// a Comparator built without asset-overlay support (the bare NewComparator,
// as every caller before this feature used) refuses to silently skip the
// overlay comparison for an image that carries the annotation -- it must
// error, not quietly fall back to a comparison that omits the overlay
// layer entirely. Pushed through a real registry for the same reason as
// TestComparator_AssetOverlay_MalformedAnnotationFailsClosed above.
func TestComparator_AssetOverlay_NoReconstructionSupportFailsClosed(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	const repo = "acme/asset-overlay-nosupport"
	registryHost := strings.TrimPrefix(server.URL, "http://")

	ts := time.Unix(1_700_000_000, 0).UTC()
	base := assetOverlayBaseImage(t)
	serverDir := t.TempDir()
	writeFile(t, serverDir, "index.js", "console.log('server');")
	clientDir := t.TempDir()
	writeFile(t, clientDir, "_app/immutable/chunks/x.js", "console.log('x');")

	img := buildLayeredImage(t, base, serverDir, clientDir, "", nil, ts)
	annotated, ok := mutate.Annotations(img, map[string]string{
		ports.AnnotationAssetOverlaySources: "sha256:" + strings.Repeat("a", 64),
	}).(v1.Image)
	if !ok {
		t.Fatal("annotate: result is not an image")
	}
	annotatedDigest := pushImage(t, server.URL, repo, "current", annotated)

	localTarPath := writeLocalTarball(t, img, fmt.Sprintf("%s/%s:current", registryHost, repo))
	defer os.Remove(localTarPath)

	c := comparator.NewComparator(slog.Default())
	_, err := c.CompareImages(context.Background(), ports.ImageComparatorRequest{
		RemoteImageRef: fmt.Sprintf("%s/%s@%s", registryHost, repo, annotatedDigest),
		LocalTarball:   localTarPath,
	})
	if err == nil {
		t.Fatal("expected an error when no asset-overlay reconstruction support is configured, got success")
	}
	if !strings.Contains(err.Error(), "asset-overlay reconstruction support configured") {
		t.Errorf("expected the no-reconstruction-support error, got: %v", err)
	}
}

// TestComparator_AssetOverlay_StrippedAnnotationStillFails closes the one gap
// the other four tests leave open, and the one a reader of
// reconcileAssetOverlay will reasonably worry about: the reconciliation is
// gated on the annotation being *present*, and an absent annotation means
// "skip reconstruction". Read alone, that looks like a fail-open — strip the
// annotation and the overlay comparison appears to vanish.
//
// It does not, and this test is the proof. Stripping the annotation does not
// remove the overlay *layer*, so the remote still carries a layer the plain
// local rebuild does not, and the ordinary layer-by-layer comparison reports
// the mismatch. An attacker therefore gains nothing by removing the
// annotation: the honest path (annotation present, content reconstructed and
// checked) passes, and every dishonest path fails, including this one.
//
// This case previously existed only as throwaway evidence from a fail-first
// check, which is not a regression guard — the behaviour it depends on is a
// property of the surrounding comparison, so a future refactor of that
// comparison could silently turn the skip into a real fail-open.
func TestComparator_AssetOverlay_StrippedAnnotationStillFails(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	const repo = "acme/asset-overlay-stripped"
	registryHost := strings.TrimPrefix(server.URL, "http://")

	ts := time.Unix(1_700_000_000, 0).UTC()
	base := assetOverlayBaseImage(t)

	gen0ClientDir := t.TempDir()
	writeFile(t, gen0ClientDir, "_app/immutable/chunks/abc123.js", "console.log('gen0 cached chunk');")
	gen0ServerDir := t.TempDir()
	writeFile(t, gen0ServerDir, "index.js", "console.log('gen0 server');")
	gen0Img := buildLayeredImage(t, base, gen0ServerDir, gen0ClientDir, "", nil, ts)
	gen0Digest := pushImage(t, server.URL, repo, "gen0", gen0Img)

	resolver := assetoverlay.NewResolver()
	overlayDir, err := resolver.BuildOverlayDir(context.Background(), fmt.Sprintf("%s/%s", registryHost, repo), []string{gen0Digest}, "", false)
	if err != nil {
		t.Fatalf("build overlay dir: %v", err)
	}
	defer os.RemoveAll(overlayDir)

	serverDir := t.TempDir()
	writeFile(t, serverDir, "index.js", "console.log('current server');")
	clientDir := t.TempDir()
	writeFile(t, clientDir, "_app/immutable/chunks/def456.js", "console.log('current build own chunk');")

	// The overlay layer is really present, but no source digests are passed,
	// so no pokkum.dev/asset-overlay-sources annotation is written — which is
	// exactly the shape an attacker stripping the annotation would leave
	// behind.
	strippedImg := buildLayeredImage(t, base, serverDir, clientDir, overlayDir, nil, ts)
	strippedDigest := pushImage(t, server.URL, repo, "stripped", strippedImg)

	// Confirm the premise rather than assuming it: the pushed image really
	// must carry no annotation, or this test proves nothing.
	strippedRef, err := name.ParseReference(fmt.Sprintf("%s/%s@%s", registryHost, repo, strippedDigest), name.WeakValidation)
	if err != nil {
		t.Fatalf("parse stripped ref: %v", err)
	}
	fetched, err := remote.Image(strippedRef)
	if err != nil {
		t.Fatalf("fetch stripped image: %v", err)
	}
	mf, err := fetched.Manifest()
	if err != nil {
		t.Fatalf("read stripped manifest: %v", err)
	}
	if _, present := mf.Annotations[ports.AnnotationAssetOverlaySources]; present {
		t.Fatalf("test premise broken: expected no %s annotation on the stripped image", ports.AnnotationAssetOverlaySources)
	}

	localImg := buildLayeredImage(t, base, serverDir, clientDir, "", nil, ts)
	localTarPath := writeLocalTarball(t, localImg, fmt.Sprintf("%s/%s:stripped", registryHost, repo))
	defer os.Remove(localTarPath)

	c := comparator.NewComparatorWithAssetOverlay(slog.Default(), assetoverlay.NewResolver(), packager.NewLayerBuilder())
	res, err := c.CompareImages(context.Background(), ports.ImageComparatorRequest{
		RemoteImageRef: fmt.Sprintf("%s/%s@%s", registryHost, repo, strippedDigest),
		LocalTarball:   localTarPath,
	})
	// Either an outright error or an L3 mismatch is an acceptable failure
	// here; what must never happen is a clean pass, which would mean removing
	// one annotation bought an attacker a successful verification of an image
	// carrying a layer the source does not produce.
	if err != nil {
		return
	}
	if res.Level != "L3" && res.L2SemanticMatch {
		t.Fatalf("stripping the asset-overlay annotation must not yield a passing verification: got level=%s l2=%v summary=%s",
			res.Level, res.L2SemanticMatch, res.Summary)
	}
}
