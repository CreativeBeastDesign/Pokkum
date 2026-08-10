// The tests in this file drive internal/adapters/packager.Packager directly
// against a fixed synthetic app binary (fakeAppContent, defined in
// harness_test.go) and a synthetic base image. They are genuinely useful —
// packager-level determinism is a real precondition for reproducible
// images — but none of them invoke bun, so none of them can see
// nondeterminism introduced upstream in the SvelteKit build or the Bun
// compile step. In particular they would not have caught the
// assets.generated.ts ordering bug that made real builds non-reproducible
// (see internal/adapters/bunexec/assets.go and
// TestRealBuildIsReproducibleAcrossRuns in reproducibility_e2e_test.go,
// which does invoke the real toolchain). Read a passing test in this file as
// "the packager is deterministic given deterministic inputs," not as
// "a real pokkum build is reproducible."
package integration

import (
	"context"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

type imageSnapshot struct {
	ManifestDigest string
	ConfigDigest   string
	LayerDigests   []string
	LayerDiffIDs   []string
	RawManifest    string
	RawConfig      string
}

func snapshotImage(t *testing.T, img v1.Image) imageSnapshot {
	t.Helper()

	d, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	rawM, err := img.RawManifest()
	if err != nil {
		t.Fatalf("RawManifest: %v", err)
	}
	rawC, err := img.RawConfigFile()
	if err != nil {
		t.Fatalf("RawConfigFile: %v", err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}

	snap := imageSnapshot{
		ManifestDigest: d.String(),
		ConfigDigest:   m.Config.Digest.String(),
		RawManifest:    string(rawM),
		RawConfig:      string(rawC),
	}

	for _, l := range layers {
		ld, err := l.Digest()
		if err != nil {
			t.Fatalf("Layer.Digest: %v", err)
		}
		diffID, err := l.DiffID()
		if err != nil {
			t.Fatalf("Layer.DiffID: %v", err)
		}
		snap.LayerDigests = append(snap.LayerDigests, ld.String())
		snap.LayerDiffIDs = append(snap.LayerDiffIDs, diffID.String())
	}
	return snap
}

// TestPackagingIsDeterministicAcrossRuns checks that Packager.Build produces a
// byte-identical image across two runs given identical synthetic inputs. It
// does not exercise bun or the SvelteKit build — see the package-level
// comment above.
func TestPackagingIsDeterministicAcrossRuns(t *testing.T) {
	pkg := packager.NewPackager(testLogger())
	plat := ports.LinuxAMD64

	// Run 1
	req1 := NewTestPackageRequest(t, plat)
	img1, err := pkg.Build(context.Background(), req1)
	if err != nil {
		t.Fatalf("Build run 1: %v", err)
	}
	snap1 := snapshotImage(t, img1)

	// Run 2: independent temp files on host system, identical input contents & timestamp
	req2 := NewTestPackageRequest(t, plat)
	img2, err := pkg.Build(context.Background(), req2)
	if err != nil {
		t.Fatalf("Build run 2: %v", err)
	}
	snap2 := snapshotImage(t, img2)

	// Assert complete equality between Run 1 and Run 2
	if snap1.ManifestDigest != snap2.ManifestDigest {
		t.Errorf("ManifestDigest mismatch:\nRun 1: %s\nRun 2: %s", snap1.ManifestDigest, snap2.ManifestDigest)
	}
	if snap1.ConfigDigest != snap2.ConfigDigest {
		t.Errorf("ConfigDigest mismatch:\nRun 1: %s\nRun 2: %s", snap1.ConfigDigest, snap2.ConfigDigest)
	}
	if len(snap1.LayerDigests) != len(snap2.LayerDigests) {
		t.Fatalf("Layer count mismatch: %d vs %d", len(snap1.LayerDigests), len(snap2.LayerDigests))
	}

	for i := range snap1.LayerDigests {
		if snap1.LayerDigests[i] != snap2.LayerDigests[i] {
			t.Errorf("Layer [%d] digest mismatch:\nRun 1: %s\nRun 2: %s", i, snap1.LayerDigests[i], snap2.LayerDigests[i])
		}
		if snap1.LayerDiffIDs[i] != snap2.LayerDiffIDs[i] {
			t.Errorf("Layer [%d] diffID mismatch:\nRun 1: %s\nRun 2: %s", i, snap1.LayerDiffIDs[i], snap2.LayerDiffIDs[i])
		}
	}

	if snap1.RawManifest != snap2.RawManifest {
		t.Errorf("RawManifest JSON mismatch across runs")
	}
	if snap1.RawConfig != snap2.RawConfig {
		t.Errorf("RawConfig JSON mismatch across runs")
	}
}

// TestMultiArchIndexPackagingIsDeterministic checks that Packager.Index
// produces a byte-identical multi-platform index across two runs given
// identical synthetic per-platform images. It does not exercise bun or the
// SvelteKit build — see the package-level comment above.
func TestMultiArchIndexPackagingIsDeterministic(t *testing.T) {
	pkg := packager.NewPackager(testLogger())
	platforms := []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64}

	buildIndex := func() string {
		images := map[ports.Platform]v1.Image{}
		for _, plat := range platforms {
			img, err := pkg.Build(context.Background(), NewTestPackageRequest(t, plat))
			if err != nil {
				t.Fatalf("Build(%s): %v", plat, err)
			}
			images[plat] = img
		}
		idx, err := pkg.Index(context.Background(), ports.IndexRequest{
			Images:    images,
			CreatedAt: testEpoch,
		})
		if err != nil {
			t.Fatalf("Index: %v", err)
		}
		d, err := idx.Digest()
		if err != nil {
			t.Fatalf("Index digest: %v", err)
		}
		return d.String()
	}

	d1 := buildIndex()
	d2 := buildIndex()

	if d1 != d2 {
		t.Errorf("Index digest mismatch across runs:\nRun 1: %s\nRun 2: %s", d1, d2)
	}
}

func TestReproducibilitySensitivityToTimestamp(t *testing.T) {
	pkg := packager.NewPackager(testLogger())
	plat := ports.LinuxAMD64

	req1 := NewTestPackageRequest(t, plat)
	img1, err := pkg.Build(context.Background(), req1)
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	snap1 := snapshotImage(t, img1)

	// Modify SOURCE_DATE_EPOCH
	req2 := NewTestPackageRequest(t, plat)
	req2.CreatedAt = time.Unix(1700000099, 0).UTC()
	img2, err := pkg.Build(context.Background(), req2)
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	snap2 := snapshotImage(t, img2)

	// Config digest and manifest digest must change when timestamp changes
	if snap1.ConfigDigest == snap2.ConfigDigest {
		t.Errorf("ConfigDigest did not change when timestamp changed: %s", snap1.ConfigDigest)
	}
	if snap1.ManifestDigest == snap2.ManifestDigest {
		t.Errorf("ManifestDigest did not change when timestamp changed: %s", snap1.ManifestDigest)
	}
}
