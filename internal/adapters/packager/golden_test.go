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
//   - a *DiffID or goldenConfigDigest changed. These depend only on the
//     uncompressed tar bytes and the config JSON, both of which are fully
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
	goldenSupervisorLayerDiffID = "sha256:dfbcaa2cb264f3acee10b7b6aeced293c3283460e07c0f63bca2d05177a60d4e"
	goldenSupervisorLayerDigest = "sha256:39064dcb430d05629cfc39ac324657459f412d0424f9d0ad9d5a8da5d712cfba"
	goldenAppLayerDiffID        = "sha256:444f537f1513ae1971fb23beaec92dd1fb046f8c533d411518d421ad94707602"
	goldenAppLayerDigest        = "sha256:363520a76df60ab6a9ce193a54076c20d19a5c18d96678d84c1bc4d433b19701"
	goldenConfigDigest          = "sha256:50c44d23216338fcb78bf3b0f111f9c351d726642ee17e75b7c42d4ee27e549b"
	goldenManifestDigest        = "sha256:3ea634df883efc60ea2cae0c70acf3b96ee17341c1028850332283264cc7d29a"
	goldenIndexDigest           = "sha256:a1afd0511491d24a163abdbc1ebf43247a9a7e120708922d1cfab44eb6ea8266"
)

func TestGoldenImageDigests(t *testing.T) {
	img, err := NewPackager(testLogger()).Build(context.Background(), newRequest(t, ports.LinuxAMD64))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	fp := fingerprint(t, img)

	// fp.LayerDiffIDs/LayerDigests are ordered [base, supervisor, app]: the base
	// layer comes from the synthetic fixture and is not pinned here, the two
	// pokkum-added layers are the last two, in the order they are appended.
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"supervisor layer diffID", fp.LayerDiffIDs[len(fp.LayerDiffIDs)-2], goldenSupervisorLayerDiffID},
		{"supervisor layer digest", fp.LayerDigests[len(fp.LayerDigests)-2], goldenSupervisorLayerDigest},
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
