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
//     **Check your Go toolchain before concluding either.** The claim above —
//     that goldenConfigDigest cannot move for toolchain reasons — is not quite
//     true, and believing it costs an afternoon. On 2026-09-01 this test failed
//     locally with goldenConfigDigest AND goldenManifestDigest moved while both
//     asserted layer diffIDs held, which reads exactly like a deliberate
//     config-assembly change nobody re-recorded. It was not: the machine had Go
//     1.27.0 installed while go.mod pins 1.26.6, and CI resolves its toolchain
//     from go.mod (`go-version-file`). `GOTOOLCHAIN=go1.26.6 go test ./...`
//     passed on the same tree, unchanged. Run that FIRST — before bisecting,
//     and certainly before re-recording, since re-recording under a newer
//     toolchain than the pinned one hard-breaks CI and quietly makes a digest
//     no released build produces the canonical one.
//
//   - only goldenManifestDigest or goldenIndexDigest changed. These additionally
//     depend on the *compressed* layer digests. Note that Pokkum's OWN layers are
//     NOT a source of drift here: layer.go pins github.com/klauspost/compress/gzip
//     (not stdlib compress/gzip) precisely so its compressed bytes are stable
//     across Go releases, and that pin holds — see the 2026-09-05 note below,
//     where a full Go minor bump moved neither layer digest by a single bit.
//     What can still move them is a compressed digest produced by something
//     other than this package, which in this fixture means the synthetic base
//     image (below).
//
// # 2026-09-05 update: Go 1.26.6 -> 1.27.1, and what actually drifts
//
// goldenConfigDigest, goldenManifestDigest and goldenIndexDigest moved on this
// toolchain bump. All four layer constants — supervisor AND app, diffID AND
// compressed digest — held exactly. That split localises the cause precisely,
// and it is worth writing down because the bullet above used to name the wrong
// mechanism (it blamed stdlib compress/flate at gzip.BestSpeed, which this
// package has not used since the klauspost pin landed 2026-08-17).
//
// The measured cause: this fixture's *synthetic base image* is built in-process
// by go-containerregistry, which compresses with stdlib compress/gzip. Go 1.27
// changed that output, so the synthetic base's layer digest changed, so the base
// image's own digest changed, so the value of the
// org.opencontainers.image.base.digest label changed
// (71e40aed... -> 5ef56487...), so the config JSON bytes changed, and the
// manifest and index digests followed. Diffing the config JSON under both
// toolchains showed that label as the ONLY differing byte range; everything
// else, including all three rootfs diff_ids, was identical.
//
// Consequence worth being clear about: a real build is not affected the way this
// fixture is. A real base image's digest is whatever the registry serves, not
// something recomputed locally, so a toolchain bump does not move it. Pokkum's
// own layer bytes are toolchain-stable by construction (the klauspost pin), which
// is exactly what these four unchanged layer constants demonstrate.
//
// # 2026-08-18 update: immutable-binary layer timestamp decoupling
//
// goldenSupervisorLayerDiffID/Digest, goldenConfigDigest, goldenManifestDigest
// and goldenIndexDigest moved in this update; goldenAppLayerDiffID/Digest did
// NOT. That split is expected, not a red flag: the supervisor layer is now
// pinned to pinnedImmutableBinaryEpoch (the Unix epoch) instead of this
// fixture's buildEpoch (see layer.go's pinnedImmutableBinaryEpoch doc comment
// and docs/archive/Roadmap.md item 3f), so its tar bytes — and everything whose digest
// transitively includes them (the config's rootfs diff_ids, the manifest's
// layer digests, the index) — changed, while the app layer (this build's own
// content, still keyed on buildEpoch) is untouched. Confirmed by diffing the
// regenerated config/manifest JSON against the pre-change values: the only
// substantive difference is the supervisor layer's ModTime (2023-11-14 ->
// 1970-01-01) and the digests that derive from it; every label, annotation,
// env var and history CreatedBy string is unchanged.
const (
	goldenSupervisorLayerDiffID = "sha256:63041eb6fe4836c3a90ce0d40dcf3ccd7db094a0b3ed104c4e6c911f48778f74"
	goldenSupervisorLayerDigest = "sha256:8ead9ea773a6f603898992e1dbc6974ab1dfb85fcdc9ccbd95f15ae021db343d"
	goldenAppLayerDiffID        = "sha256:444f537f1513ae1971fb23beaec92dd1fb046f8c533d411518d421ad94707602"
	goldenAppLayerDigest        = "sha256:f145163ebb449b41bb6c46cf894839aabb2c80e937a8185c124ce234610fe62a"
	goldenConfigDigest          = "sha256:8f8ec8b8c941d1b6189abb865aa4bfa3afa721baaef89ad34e0425b793d67037"
	goldenManifestDigest        = "sha256:e988fbeda7c518717e15cef3433b6cd4f8360ecd60a00bb7df9454501dd45ebb"
	goldenIndexDigest           = "sha256:c1d32be58c537e4a717156f8c673adce5fafaa8611c5400ee5933a4c73b5811f"
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
