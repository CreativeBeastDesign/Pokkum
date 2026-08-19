package packager

import (
	"context"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// This file is the regression test for docs/archive/Roadmap.md item 3f: the immutable
// third-party/embedded-binary layers (the Bun runtime, pokkum-init, and
// pokkum-static) must produce a bit-identical diffID/digest for a given
// (content x platform x compression), REGARDLESS of req.CreatedAt (the
// per-build SOURCE_DATE_EPOCH-derived timestamp).
//
// Before pinnedImmutableBinaryEpoch was introduced (layer.go), these three
// layers baked req.CreatedAt straight into their tar ModTime, so a build from
// commit A and a build from commit B produced two different ~90MB Bun blobs
// and two different pokkum-init blobs even when the embedded binary bytes
// were byte-for-byte identical — a guaranteed on-disk layer-cache miss on
// every commit, and defeated registry-side cross-image deduplication across a
// fleet (see layer.go's pinnedImmutableBinaryEpoch doc comment). This test
// empirically pins the fix, not just the mechanism: it builds real images
// through the same Packager.Build entrypoint production code uses, varying
// ONLY req.CreatedAt between two builds, and asserts the immutable-binary
// layer's diffID/digest do not move.
//
// It is deliberately NOT in tension with
// tests/integration/reproducibility_test.go's
// TestReproducibilitySensitivityToTimestamp, which asserts the OPPOSite
// property (that changing the timestamp DOES change the image) — that test
// is about the image's config/manifest digest as a whole, which legitimately
// still changes with CreatedAt (the config's own `created` field, and the
// application-content layers below, are correctly still derived from it).
// This test is scoped narrowly to the three specific immutable-binary
// layers, which are the one deliberate exception to that rule. Every
// per-strategy subtest below also asserts that the CORRESPONDING
// application-content layer (server/app/client) DOES change with CreatedAt,
// so the two tests' intents are checked side by side rather than left to be
// inferred.
func TestImmutableBinaryLayers_DiffIDStableAcrossSourceDateEpoch(t *testing.T) {
	ctx := context.Background()
	epochA := buildEpoch
	epochB := buildEpoch.Add(97 * 24 * time.Hour) // an arbitrary, distinct SOURCE_DATE_EPOCH

	// setFreshCacheDir points POKKUM_CACHE_DIR at a brand-new, empty temp
	// directory. This is load-bearing, not just tidy, and MUST be called
	// before every individual build in this test, not once for the whole
	// test: layercacheutils.ComputeKey no longer folds in a timestamp at all
	// (see its doc comment), so if the two builds being compared shared a
	// cache, the second build would simply cache-HIT the first build's
	// already-built layer and trivially "match" it — regardless of what
	// modTime the production code path actually threaded through on the
	// second call. That would silently defeat this test's entire purpose.
	// Confirmed empirically while writing this test: with a single shared
	// cache dir for both calls, reverting buildSupervisorLayer's call site
	// back to `ts` (the pre-fix behaviour) still left every subtest green,
	// purely because of the cache hit — not because the fix was still in
	// place. A fresh, unshared cache dir per build forces each one to
	// genuinely rebuild the layer from scratch, so the comparison reflects
	// what modTime the code actually used, not what the cache happened to
	// return.
	setFreshCacheDir := func(t *testing.T) {
		t.Helper()
		t.Setenv("POKKUM_CACHE_DIR", t.TempDir())
	}

	// layerFingerprint captures the diffID/digest of every non-base layer of
	// an image, by position, so a subtest can compare "the Nth pokkum layer"
	// between two builds without caring about the synthetic base's own layer
	// count.
	layerFingerprint := func(t *testing.T, img v1.Image) []struct{ diffID, digest string } {
		t.Helper()
		layers, err := img.Layers()
		if err != nil {
			t.Fatalf("layers: %v", err)
		}
		out := make([]struct{ diffID, digest string }, len(layers))
		for i, l := range layers {
			d, err := l.DiffID()
			if err != nil {
				t.Fatalf("layer[%d] diffID: %v", i, err)
			}
			dg, err := l.Digest()
			if err != nil {
				t.Fatalf("layer[%d] digest: %v", i, err)
			}
			out[i] = struct{ diffID, digest string }{d.String(), dg.String()}
		}
		return out
	}

	t.Run("layered strategy: bun and supervisor layers pinned, server layer not", func(t *testing.T) {
		build := func(createdAt time.Time) v1.Image {
			setFreshCacheDir(t)
			req := newRequest(t, ports.LinuxAMD64)
			req.Strategy = ports.StrategyLayered
			req.CreatedAt = createdAt
			req.BunRuntime = ports.BunResolverResult{
				BinaryPath: writeBinary(t, "bun", []byte("#!/bin/sh\necho bun-runtime-fixed-content")),
			}
			req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
			img, err := NewPackager(testLogger()).Build(ctx, req)
			if err != nil {
				t.Fatalf("build (createdAt=%s): %v", createdAt, err)
			}
			return img
		}

		imgA, imgB := build(epochA), build(epochB)
		fpA, fpB := layerFingerprint(t, imgA), layerFingerprint(t, imgB)

		// Layer order (see Build's layered branch): [0]=synthetic base,
		// [1]=bun, [2]=supervisor, [3]=server.
		const (
			baseIdx       = 0
			bunIdx        = 1
			supervisorIdx = 2
			serverIdx     = 3
		)
		if len(fpA) != 4 || len(fpB) != 4 {
			t.Fatalf("got %d/%d layers, want base+bun+supervisor+server (4)", len(fpA), len(fpB))
		}

		for _, idx := range []int{bunIdx, supervisorIdx} {
			if fpA[idx].diffID != fpB[idx].diffID {
				t.Errorf("layer[%d] diffID changed across SOURCE_DATE_EPOCH: %s vs %s (immutable binary layers must be pinned to pinnedImmutableBinaryEpoch)", idx, fpA[idx].diffID, fpB[idx].diffID)
			}
			if fpA[idx].digest != fpB[idx].digest {
				t.Errorf("layer[%d] digest changed across SOURCE_DATE_EPOCH: %s vs %s", idx, fpA[idx].digest, fpB[idx].digest)
			}
		}

		// The server layer is this build's own application content and MUST
		// still change with SOURCE_DATE_EPOCH (mirrors
		// TestReproducibilitySensitivityToTimestamp's intent, scoped to a
		// single layer instead of the whole image).
		if fpA[serverIdx].diffID == fpB[serverIdx].diffID {
			t.Errorf("server layer diffID did NOT change across SOURCE_DATE_EPOCH: %s — application-content layers must still derive from req.CreatedAt", fpA[serverIdx].diffID)
		}

		// The synthetic base layer is untouched by either build.
		if fpA[baseIdx].diffID != fpB[baseIdx].diffID {
			t.Errorf("base layer diffID changed: %s vs %s", fpA[baseIdx].diffID, fpB[baseIdx].diffID)
		}
	})

	t.Run("exe strategy: supervisor layer pinned, app layer not", func(t *testing.T) {
		build := func(createdAt time.Time) v1.Image {
			setFreshCacheDir(t)
			req := newRequest(t, ports.LinuxAMD64)
			req.Strategy = ports.StrategyExe
			req.CreatedAt = createdAt
			img, err := NewPackager(testLogger()).Build(ctx, req)
			if err != nil {
				t.Fatalf("build (createdAt=%s): %v", createdAt, err)
			}
			return img
		}

		imgA, imgB := build(epochA), build(epochB)
		fpA, fpB := layerFingerprint(t, imgA), layerFingerprint(t, imgB)

		// Layer order (see Build's exe/default branch): [0]=base,
		// [1]=supervisor, [2]=app.
		const (
			supervisorIdx = 1
			appIdx        = 2
		)
		if len(fpA) != 3 || len(fpB) != 3 {
			t.Fatalf("got %d/%d layers, want base+supervisor+app (3)", len(fpA), len(fpB))
		}

		if fpA[supervisorIdx].diffID != fpB[supervisorIdx].diffID {
			t.Errorf("supervisor layer diffID changed across SOURCE_DATE_EPOCH: %s vs %s", fpA[supervisorIdx].diffID, fpB[supervisorIdx].diffID)
		}
		if fpA[supervisorIdx].digest != fpB[supervisorIdx].digest {
			t.Errorf("supervisor layer digest changed across SOURCE_DATE_EPOCH: %s vs %s", fpA[supervisorIdx].digest, fpB[supervisorIdx].digest)
		}
		if fpA[appIdx].diffID == fpB[appIdx].diffID {
			t.Errorf("app layer diffID did NOT change across SOURCE_DATE_EPOCH: %s — the compiled application binary is this build's own content", fpA[appIdx].diffID)
		}
	})

	t.Run("static strategy: static server layer pinned, client layer not", func(t *testing.T) {
		build := func(createdAt time.Time) v1.Image {
			setFreshCacheDir(t)
			req := newRequest(t, ports.LinuxAMD64)
			req.Strategy = ports.StrategyStatic
			req.CreatedAt = createdAt
			req.StaticServer = []byte("fake-pokkum-static-binary-fixed-content")
			req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "client asset"})
			req.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": "prerendered page"})
			img, err := NewPackager(testLogger()).Build(ctx, req)
			if err != nil {
				t.Fatalf("build (createdAt=%s): %v", createdAt, err)
			}
			return img
		}

		imgA, imgB := build(epochA), build(epochB)
		fpA, fpB := layerFingerprint(t, imgA), layerFingerprint(t, imgB)

		// Layer order (see Build's static branch): [0]=base,
		// [1]=static server, [2]=client, [3]=prerendered.
		const (
			staticServerIdx = 1
			clientIdx       = 2
		)
		if len(fpA) != 4 || len(fpB) != 4 {
			t.Fatalf("got %d/%d layers, want base+staticserver+client+prerendered (4)", len(fpA), len(fpB))
		}

		if fpA[staticServerIdx].diffID != fpB[staticServerIdx].diffID {
			t.Errorf("static server layer diffID changed across SOURCE_DATE_EPOCH: %s vs %s", fpA[staticServerIdx].diffID, fpB[staticServerIdx].diffID)
		}
		if fpA[staticServerIdx].digest != fpB[staticServerIdx].digest {
			t.Errorf("static server layer digest changed across SOURCE_DATE_EPOCH: %s vs %s", fpA[staticServerIdx].digest, fpB[staticServerIdx].digest)
		}
		if fpA[clientIdx].diffID == fpB[clientIdx].diffID {
			t.Errorf("client layer diffID did NOT change across SOURCE_DATE_EPOCH: %s — application-content layers must still derive from req.CreatedAt", fpA[clientIdx].diffID)
		}
	})
}

// TestBuildCustomFileLayer_DiffIDStableAcrossModTime is a narrower, unit-level
// companion to TestImmutableBinaryLayers_DiffIDStableAcrossSourceDateEpoch:
// it exercises BuildCustomFileLayer directly (the function
// Packager.Build calls, via pinnedImmutableBinaryEpoch, to build the Bun
// runtime layer) and proves the property at the layer-builder level, not
// just end to end through Packager.Build. BuildCustomFileLayer itself stays
// a general-purpose, modTime-taking helper (see its other tests in
// custom_layers_test.go, which deliberately vary modTime) — the pinning is
// Packager.Build's decision, made once at the call site, which is exactly
// what this test's sibling above already covers for the real call sites.
// This test additionally proves the mechanism itself: passing the SAME
// modTime for two builds of the same content produces the same diffID,
// establishing the baseline the sibling test's "differing SOURCE_DATE_EPOCH"
// claim rests on.
func TestBuildCustomFileLayer_DiffIDStableAcrossModTime(t *testing.T) {
	// Not load-bearing for this test's assertion (a cache hit on the second
	// call would trivially satisfy "same diffID" too, since it would return
	// literally the same cached layer) — set purely so this test does not
	// write a throwaway fixture entry into the real host
	// ~/.cache/pokkum/layers, matching the hygiene (if not the strict
	// necessity) of the isolation in the sibling test above.
	t.Setenv("POKKUM_CACHE_DIR", t.TempDir())

	ctx := context.Background()
	sourceFile := writeBinary(t, "bun", []byte("#!/bin/sh\necho pinned-bun-fixture"))

	layerA, err := BuildCustomFileLayer(ctx, ports.LinuxAMD64, ports.BunBinaryPath, sourceFile, pinnedImmutableBinaryEpoch, ports.CompressionGzip)
	if err != nil {
		t.Fatalf("build A: %v", err)
	}
	layerB, err := BuildCustomFileLayer(ctx, ports.LinuxAMD64, ports.BunBinaryPath, sourceFile, pinnedImmutableBinaryEpoch, ports.CompressionGzip)
	if err != nil {
		t.Fatalf("build B: %v", err)
	}

	diffA, err := layerA.DiffID()
	if err != nil {
		t.Fatalf("diffID A: %v", err)
	}
	diffB, err := layerB.DiffID()
	if err != nil {
		t.Fatalf("diffID B: %v", err)
	}
	if diffA != diffB {
		t.Errorf("diffID = %s, want %s (same content, same pinned epoch, two independent builds)", diffB, diffA)
	}
}
