package packager

import (
	"context"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Golden digests for the fixed synthetic input defined in helper_test.go.
//
// TestBuildIsReproducible proves that two runs in one process agree.
// These constants extend that to across processes, across machines and across
// time, which is the property Pokkum actually promises: they were recorded once
// and nothing but a deliberate change should move them.
//
// # What a failure means
//
// Two different causes, distinguishable by which constants moved:
//
//   - goldenAppLayerDiffID or goldenConfigDigest changed. These depend only on
//     the uncompressed tar bytes and the config JSON, both of which are fully
//     determined by this package's own code. Something in the layer layout, the
//     header pinning, the runtime config or the label/annotation assembly
//     changed. If that was intentional, re-record; if not, it is a bug.
//
//   - only goldenManifestDigest or goldenIndexDigest changed. These additionally
//     depend on the *compressed* layer digests, and therefore on the output of
//     compress/flate at gzip.BestSpeed. Go does not promise that output is
//     stable across releases. A Go toolchain upgrade can legitimately move these
//     two while the diffID and config digest hold — which is worth knowing,
//     because it means images rebuilt after the upgrade get new digests even
//     though their contents are identical.
const (
	goldenAppLayerDiffID = "sha256:bada7ac9442add50ffa24d014918bfcfa17f2534178dc57c20c97e84f550293f"
	goldenAppLayerDigest = "sha256:4fc0309a591641b850d8f24e71be2c757e92ff634d8b09b0c3b1f8459de45eec"
	goldenConfigDigest   = "sha256:0a84fb5784dc266549456d255bf33a1b3c024694e2c8f3fda85bf0a7e21332bc"
	goldenManifestDigest = "sha256:12552cc8e3b6cdc4df80cf83033c29f266f2cc1d3bfb78acb71fec3f6aeeb41b"
	goldenIndexDigest    = "sha256:f6e63da88a8ec636e29a64d96a3e84043da668bf06a839765c3ff4b75d749902"
)

func TestGoldenImageDigests(t *testing.T) {
	img, err := NewPackager(testLogger()).Build(context.Background(), newRequest(t, ports.LinuxAMD64))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	fp := fingerprint(t, img)

	checks := []struct {
		name string
		got  string
		want string
	}{
		{"application layer diffID", fp.LayerDiffIDs[len(fp.LayerDiffIDs)-1], goldenAppLayerDiffID},
		{"application layer digest", fp.LayerDigests[len(fp.LayerDigests)-1], goldenAppLayerDigest},
		{"config digest", fp.Config, goldenConfigDigest},
		{"manifest digest", fp.Manifest, goldenManifestDigest},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
	if t.Failed() {
		t.Logf("config JSON: %s", fp.RawConfig)
		t.Logf("manifest JSON: %s", fp.RawManifest)
	}
}

func TestGoldenIndexDigest(t *testing.T) {
	p := NewPackager(testLogger())
	images := map[ports.Platform]v1.Image{}
	for _, plat := range []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64} {
		img, err := p.Build(context.Background(), newRequest(t, plat))
		if err != nil {
			t.Fatalf("build %s: %v", plat, err)
		}
		images[plat] = img
	}

	idx, err := p.Index(context.Background(), ports.IndexRequest{
		Images:    images,
		CreatedAt: buildEpoch,
	})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	d, err := idx.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if d.String() != goldenIndexDigest {
		raw, _ := idx.RawManifest()
		t.Errorf("index digest = %s, want %s\nmanifest: %s", d, goldenIndexDigest, raw)
	}
}
