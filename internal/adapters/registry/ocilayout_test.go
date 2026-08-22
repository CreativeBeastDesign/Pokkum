package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// ---------------------------------------------------------------------------
// Up-front validation. Same shape and same sentinel as Write's, because a
// caller distinguishing "the local artefact could not be written" from "the
// registry rejected us" must get the same answer whichever local mode ran.

func TestOCILayoutWrite_EmptyPath(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Repo:    "pokkum.local/app",
		Payload: ports.Payload{Image: randomImage(t)},
	})
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Fatalf("err = %v, want core.ErrTarballFailed", err)
	}
}

func TestOCILayoutWrite_EmptyRepo(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path:    filepath.Join(t.TempDir(), "oci"),
		Payload: ports.Payload{Image: randomImage(t)},
	})
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Fatalf("err = %v, want core.ErrTarballFailed", err)
	}
}

func TestOCILayoutWrite_ZeroPayload(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Repo: "pokkum.local/app",
		Path: filepath.Join(t.TempDir(), "oci"),
	})
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Fatalf("err = %v, want core.ErrTarballFailed", err)
	}
}

// ---------------------------------------------------------------------------
// Structural conformance.

// TestOCILayoutWrite_SingleImage_StructurallyValid checks the three things
// the OCI image-layout spec actually requires on disk — the `oci-layout`
// marker file with the right version string, an `index.json`, and a
// content-addressed `blobs/<algo>/<hex>` tree — and then proves the result is
// readable back through go-containerregistry's own reader rather than only
// through assertions about file names. A layout that satisfies a structural
// checklist but that no tool can open is the failure mode this codebase has
// already been bitten by once (see Lessons.md's "packaged output was
// byte-correct and still could not run").
func TestOCILayoutWrite_SingleImage_StructurallyValid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci-out")
	img := imageWithPlatform(t, "linux", "arm64")
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path:    dir,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: img},
	})
	if err != nil {
		t.Fatalf("WriteOCILayout: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", res.Digest, wantDigest)
	}
	if res.Path != dir {
		t.Errorf("Path = %q, want %q", res.Path, dir)
	}
	if len(res.Tags) != 1 || res.Tags[0] != "v1" {
		t.Errorf("Tags = %v, want [v1]", res.Tags)
	}

	// 1. The oci-layout marker file, with the spec's imageLayoutVersion.
	markerBytes, err := os.ReadFile(filepath.Join(dir, "oci-layout"))
	if err != nil {
		t.Fatalf("read oci-layout marker: %v", err)
	}
	var marker struct {
		ImageLayoutVersion string `json:"imageLayoutVersion"`
	}
	if err := json.Unmarshal(markerBytes, &marker); err != nil {
		t.Fatalf("decode oci-layout marker %q: %v", markerBytes, err)
	}
	if marker.ImageLayoutVersion != "1.0.0" {
		t.Errorf("imageLayoutVersion = %q, want 1.0.0", marker.ImageLayoutVersion)
	}

	// 2. index.json exists and names this image under the requested tag.
	topIndex := readLayoutIndex(t, dir)
	if len(topIndex.Manifests) != 1 {
		t.Fatalf("index.json manifests = %d, want 1", len(topIndex.Manifests))
	}
	desc := topIndex.Manifests[0]
	if desc.Digest != wantDigest {
		t.Errorf("index.json descriptor digest = %s, want %s", desc.Digest, wantDigest)
	}
	if got := desc.Annotations[annotationRefName]; got != "v1" {
		t.Errorf("%s = %q, want %q", annotationRefName, got, "v1")
	}
	if got := desc.Annotations[annotationContainerdImageName]; got != "pokkum.local/app:v1" {
		t.Errorf("%s = %q, want %q", annotationContainerdImageName, got, "pokkum.local/app:v1")
	}
	// A single-image descriptor must carry its own platform, read off the
	// image's real config: there is no child index to hold it, and a consumer
	// picking an architecture has nothing else to go on. partial.Descriptor
	// (which layout.AppendImage uses) never populates Platform on its own, so
	// this asserts appendTagged's imagePlatform call, not library behaviour.
	if desc.Platform == nil {
		t.Fatal("index.json descriptor has no platform")
	}
	if desc.Platform.OS != "linux" || desc.Platform.Architecture != "arm64" {
		t.Errorf("index.json descriptor platform = %s/%s, want linux/arm64",
			desc.Platform.OS, desc.Platform.Architecture)
	}

	// 3. The blob tree is content-addressed, and every blob index.json
	// transitively names is actually present.
	blobs := layoutBlobs(t, dir)
	if len(blobs) == 0 {
		t.Fatal("blobs/sha256 is empty")
	}
	manifest, err := img.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	want := []string{wantDigest.Hex, manifest.Config.Digest.Hex}
	for _, l := range manifest.Layers {
		want = append(want, l.Digest.Hex)
	}
	for _, h := range want {
		if _, ok := blobs[h]; !ok {
			t.Errorf("blob %s missing from blobs/sha256 (present: %v)", h, sortedKeys(blobs))
		}
	}
}

// TestOCILayoutWrite_MultiPlatformIndexPreserved is the direct contrast with
// TestTarballWrite_Index_FlattensEveryPlatform: the docker-save format cannot
// hold a manifest list, so --tarball explodes an index into one
// platform-suffixed tag per child. A layout keeps the index itself, which is
// what makes the artefact a faithful copy of what a registry would have held.
func TestOCILayoutWrite_MultiPlatformIndexPreserved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci-out")
	idx := indexWithPlatforms(t, []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})
	wantDigest, err := idx.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path:    dir,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Index: idx},
	})
	if err != nil {
		t.Fatalf("WriteOCILayout: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want index digest %s", res.Digest, wantDigest)
	}
	// Exactly one tag, not one per platform — nothing was flattened.
	if len(res.Tags) != 1 || res.Tags[0] != "v1" {
		t.Errorf("Tags = %v, want exactly [v1] (a layout needs no per-platform tags)", res.Tags)
	}

	top := readLayoutIndex(t, dir)
	if len(top.Manifests) != 1 {
		t.Fatalf("index.json manifests = %d, want 1 (the index itself)", len(top.Manifests))
	}
	if mt := top.Manifests[0].MediaType; mt != types.OCIImageIndex && mt != types.DockerManifestList {
		t.Fatalf("index.json descriptor media type = %q, want an index media type (a flattened single image would not be)", mt)
	}
	if top.Manifests[0].Digest != wantDigest {
		t.Errorf("index.json descriptor digest = %s, want the payload index %s", top.Manifests[0].Digest, wantDigest)
	}

	// Descend into the real index blob and confirm both platforms survived
	// with their own manifests.
	child := readNestedIndex(t, dir, top.Manifests[0].Digest)
	var gotPlatforms []string
	for _, m := range child.Manifests {
		if m.Platform == nil {
			t.Errorf("child manifest %s has no platform", m.Digest)
			continue
		}
		gotPlatforms = append(gotPlatforms, m.Platform.OS+"/"+m.Platform.Architecture)
	}
	sort.Strings(gotPlatforms)
	if strings.Join(gotPlatforms, ",") != "linux/amd64,linux/arm64" {
		t.Errorf("platforms in layout = %v, want [linux/amd64 linux/arm64]", gotPlatforms)
	}
}

// TestOCILayoutWrite_MultipleTags_OneBlobManyDescriptors proves the
// content-addressed write is not duplicated per tag: two tags for the same
// payload produce two index.json descriptors pointing at one manifest blob,
// which is what a registry holding one digest under two tags looks like.
func TestOCILayoutWrite_MultipleTags_OneBlobManyDescriptors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci-out")
	img := randomImage(t)

	a := NewAdapter(nil)
	if _, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path:    dir,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1", "latest"},
		Payload: ports.Payload{Image: img},
	}); err != nil {
		t.Fatalf("WriteOCILayout: %v", err)
	}

	top := readLayoutIndex(t, dir)
	if len(top.Manifests) != 2 {
		t.Fatalf("index.json manifests = %d, want 2 (one per tag)", len(top.Manifests))
	}
	if top.Manifests[0].Digest != top.Manifests[1].Digest {
		t.Errorf("the two tag descriptors name different digests (%s, %s); the same payload must be stored once",
			top.Manifests[0].Digest, top.Manifests[1].Digest)
	}
	var refs []string
	for _, m := range top.Manifests {
		refs = append(refs, m.Annotations[annotationRefName])
	}
	sort.Strings(refs)
	if strings.Join(refs, ",") != "latest,v1" {
		t.Errorf("%s annotations = %v, want [latest v1]", annotationRefName, refs)
	}
}

// ---------------------------------------------------------------------------
// The headline behaviour: annotations survive.

// pokkumAnnotations is the annotation set both halves of the comparison
// below are built from: a representative pokkum.dev/* key carrying build
// semantics another pokkum command reads back, and two
// org.opencontainers.image.* keys from the descriptive set the packager
// stamps on every build.
func pokkumAnnotations() map[string]string {
	return map[string]string{
		ports.AnnotationPredecessor:         "sha256:" + strings.Repeat("a", 64),
		ports.AnnotationEnvBaked:            "PUBLIC_API_URL",
		ports.AnnotationVEXExemptions:       "CVE-2024-0001",
		ports.LabelSource:                   "https://github.com/example/app",
		"org.opencontainers.image.revision": "abc123",
	}
}

// TestOCILayout_AnnotationsSurvive_TarballLosesThem is the reason this output
// mode exists (docs/items/tarball-output-drops-annotations.md): --tarball
// writes the legacy docker-save format, which has no annotations field at
// all, so every OCI annotation Pokkum stamps is silently discarded. An OCI
// image layout has first-class annotation support.
//
// The two halves run the *same* annotated payload through both writers in one
// test rather than asserting the layout side alone, because "annotations are
// preserved" is only meaningful against the loss it is fixing: a test that
// only read the layout back would still pass if a future change made the
// tarball lossless too and this mode redundant, and — more importantly — it
// would not document that the two modes genuinely differ.
func TestOCILayout_AnnotationsSurvive_TarballLosesThem(t *testing.T) {
	anns := pokkumAnnotations()
	img := annotatedImage(t, randomImage(t), anns)

	dir := t.TempDir()
	layoutDir := filepath.Join(dir, "oci-out")
	tarPath := filepath.Join(dir, "out.tar")

	// --- the OCI layout half -------------------------------------------------
	layoutAdapter, layoutLogs := newLoggingAdapter()
	if _, err := layoutAdapter.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path:    layoutDir,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: img},
	}); err != nil {
		t.Fatalf("WriteOCILayout: %v", err)
	}

	fromLayout := readLayoutImageManifest(t, layoutDir, "v1")
	for k, want := range anns {
		got, ok := fromLayout.Annotations[k]
		if !ok {
			t.Errorf("oci layout lost annotation %s entirely (present: %v)", k, sortedKeys(fromLayout.Annotations))
			continue
		}
		if got != want {
			t.Errorf("oci layout annotation %s = %q, want %q", k, got, want)
		}
	}

	// The layout is lossless, so the dropped-annotations warning that
	// --tarball and --local fire must NOT fire here. A warning that says
	// metadata was lost when it was not is as wrong as silence when it was.
	if warns := warnLogMessages(t, layoutLogs); len(warns) != 0 {
		t.Errorf("oci layout write logged warnings = %v, want none (nothing is dropped)", warns)
	}

	// --- the --tarball half, same payload ------------------------------------
	tarAdapter, tarLogs := newLoggingAdapter()
	if _, err := tarAdapter.Write(context.Background(), ports.TarballRequest{
		Path:    tarPath,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: img},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	tag, err := name.NewTag("pokkum.local/app:v1")
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	fromTar, err := tarball.ImageFromPath(tarPath, &tag)
	if err != nil {
		t.Fatalf("tarball.ImageFromPath: %v", err)
	}
	tarManifest, err := fromTar.Manifest()
	if err != nil {
		t.Fatalf("tarball manifest: %v", err)
	}
	if len(tarManifest.Annotations) != 0 {
		t.Fatalf("the docker-save tarball unexpectedly carries annotations %v — this test's premise "+
			"(and the fix it guards) assumes the format has no annotations field at all",
			tarManifest.Annotations)
	}
	// ...and the tarball path warns about exactly that, so the contrast is
	// visible to an operator and not only to this test.
	if warns := warnLogMessages(t, tarLogs); len(warns) != 1 {
		t.Errorf("tarball write warn lines = %d, want exactly 1 naming the dropped keys: %v", len(warns), warns)
	}
}

// TestOCILayout_IndexAnnotationsSurvive covers the annotation set that lives
// on the *index* rather than on a per-platform manifest — packager's
// indexAnnotations, which is where org.opencontainers.image.created and the
// caller's own index-level annotations land. --tarball cannot carry these at
// all, since it has no index to hang them on: flattening throws the index
// away before its annotations are ever considered.
func TestOCILayout_IndexAnnotationsSurvive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci-out")

	perImage := map[string]string{ports.AnnotationPredecessor: "sha256:" + strings.Repeat("b", 64)}
	indexLevel := map[string]string{
		ports.LabelCreated:            "1970-01-01T00:00:00Z",
		ports.AnnotationVEXExemptions: "CVE-2024-0002",
	}

	amd64 := annotatedImage(t, imageWithPlatform(t, "linux", "amd64"), perImage)
	arm64 := annotatedImage(t, imageWithPlatform(t, "linux", "arm64"), perImage)
	base := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: amd64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: arm64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)
	idx, ok := mutate.Annotations(base, indexLevel).(v1.ImageIndex)
	if !ok {
		t.Fatal("mutate.Annotations: result is not a v1.ImageIndex")
	}

	a := NewAdapter(nil)
	if _, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path:    dir,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Index: idx},
	}); err != nil {
		t.Fatalf("WriteOCILayout: %v", err)
	}

	top := readLayoutIndex(t, dir)
	if len(top.Manifests) != 1 {
		t.Fatalf("index.json manifests = %d, want 1", len(top.Manifests))
	}
	child := readNestedIndex(t, dir, top.Manifests[0].Digest)

	for k, want := range indexLevel {
		if got := child.Annotations[k]; got != want {
			t.Errorf("index annotation %s = %q, want %q (present: %v)", k, got, want, sortedKeys(child.Annotations))
		}
	}

	// And every per-platform manifest kept its own annotations too.
	for _, m := range child.Manifests {
		mf := readManifestBlob(t, dir, m.Digest)
		for k, want := range perImage {
			if got := mf.Annotations[k]; got != want {
				t.Errorf("child %s annotation %s = %q, want %q", m.Digest, k, got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Replacement / crash safety.

// TestOCILayoutWrite_ReplacesExistingLayoutWholesale proves the staging-and-
// swap strategy: writing a second, different image into the same directory
// leaves exactly that image's blobs behind, not those plus the first run's.
// An additive write would grow the directory without bound across a dev
// loop's repeated builds and would leave index.json referencing content the
// user did not just build.
func TestOCILayoutWrite_ReplacesExistingLayoutWholesale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci-out")
	a := NewAdapter(nil)

	first := randomImage(t)
	if _, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path: dir, Repo: "pokkum.local/app", Tags: []string{"v1"},
		Payload: ports.Payload{Image: first},
	}); err != nil {
		t.Fatalf("WriteOCILayout (first): %v", err)
	}
	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	second := randomImage(t)
	if _, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path: dir, Repo: "pokkum.local/app", Tags: []string{"v1"},
		Payload: ports.Payload{Image: second},
	}); err != nil {
		t.Fatalf("WriteOCILayout (second): %v", err)
	}
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	top := readLayoutIndex(t, dir)
	if len(top.Manifests) != 1 || top.Manifests[0].Digest != secondDigest {
		t.Fatalf("index.json = %+v, want exactly the second image %s", top.Manifests, secondDigest)
	}
	blobs := layoutBlobs(t, dir)
	if _, stale := blobs[firstDigest.Hex]; stale {
		t.Errorf("first run's manifest blob %s survived the second write; the layout must be replaced, not merged into", firstDigest.Hex)
	}

	// No staging or backup directories left lying around next to the layout.
	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(dir) {
			t.Errorf("leftover entry %q beside the layout directory", e.Name())
		}
	}
}

// TestOCILayoutWrite_FailureLeavesPreviousLayoutIntact is the layout analogue
// of TestTarballWrite_FailureLeavesNoTruncatedFile: a write that fails partway
// through must leave the destination exactly as it was, never a half-written
// layout whose index.json points at blobs that were never written — which a
// cluster import would open successfully and then fail on.
func TestOCILayoutWrite_FailureLeavesPreviousLayoutIntact(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci-out")
	a := NewAdapter(nil)

	good := randomImage(t)
	if _, err := a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path: dir, Repo: "pokkum.local/app", Tags: []string{"v1"},
		Payload: ports.Payload{Image: good},
	}); err != nil {
		t.Fatalf("WriteOCILayout (good): %v", err)
	}
	goodDigest, err := good.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	_, err = a.WriteOCILayout(context.Background(), ports.OCILayoutRequest{
		Path: dir, Repo: "pokkum.local/app", Tags: []string{"v1"},
		Payload: ports.Payload{Image: brokenImage{randomImage(t)}},
	})
	if err == nil {
		t.Fatal("WriteOCILayout: want error from a broken image, got nil")
	}
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Errorf("err = %v, want core.ErrTarballFailed", err)
	}

	top := readLayoutIndex(t, dir)
	if len(top.Manifests) != 1 || top.Manifests[0].Digest != goodDigest {
		t.Errorf("index.json = %+v after a failed write, want the previous layout's %s untouched", top.Manifests, goodDigest)
	}

	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(dir) {
			t.Errorf("staging directory %q not cleaned up after a failed write", e.Name())
		}
	}
}

// TestOCILayoutWrite_CancelledContext checks the ctx guard fires before any
// directory is created, so a cancelled build leaves no artefact behind.
func TestOCILayoutWrite_CancelledContext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oci-out")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := NewAdapter(nil)
	_, err := a.WriteOCILayout(ctx, ports.OCILayoutRequest{
		Path: dir, Repo: "pokkum.local/app",
		Payload: ports.Payload{Image: randomImage(t)},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("layout directory exists after a cancelled write: %v", statErr)
	}
}

// ---------------------------------------------------------------------------
// Helpers. These read the layout the way an external consumer would — off
// disk, through go-containerregistry's own layout reader and raw blob files —
// rather than through anything ocilayout.go itself provides, so a bug in the
// writer cannot be masked by a matching bug in a shared helper.

// readLayoutIndex loads the layout's top-level index.json via
// layout.ImageIndexFromPath, which is the entry point every external consumer
// of an OCI layout uses.
func readLayoutIndex(t *testing.T, dir string) *v1.IndexManifest {
	t.Helper()
	ii, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		t.Fatalf("layout.ImageIndexFromPath(%s): %v", dir, err)
	}
	im, err := ii.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	return im
}

// readNestedIndex descends from index.json into a child image index (the real
// multi-platform index, for an index payload).
func readNestedIndex(t *testing.T, dir string, h v1.Hash) *v1.IndexManifest {
	t.Helper()
	ii, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		t.Fatalf("layout.ImageIndexFromPath(%s): %v", dir, err)
	}
	child, err := ii.ImageIndex(h)
	if err != nil {
		t.Fatalf("ImageIndex(%s): %v", h, err)
	}
	im, err := child.IndexManifest()
	if err != nil {
		t.Fatalf("child IndexManifest: %v", err)
	}
	return im
}

// readLayoutImageManifest resolves the descriptor tagged tag in index.json and
// returns that image's own manifest, read back through the layout reader.
func readLayoutImageManifest(t *testing.T, dir, tag string) *v1.Manifest {
	t.Helper()
	ii, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		t.Fatalf("layout.ImageIndexFromPath(%s): %v", dir, err)
	}
	im, err := ii.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	for _, d := range im.Manifests {
		if d.Annotations[annotationRefName] != tag {
			continue
		}
		img, err := ii.Image(d.Digest)
		if err != nil {
			t.Fatalf("Image(%s): %v", d.Digest, err)
		}
		mf, err := img.Manifest()
		if err != nil {
			t.Fatalf("Manifest: %v", err)
		}
		return mf
	}
	t.Fatalf("no index.json descriptor tagged %q", tag)
	return nil
}

// readManifestBlob decodes blobs/<algo>/<hex> as a manifest directly off
// disk, bypassing the layout reader entirely — the strongest available check
// that the bytes on disk really are what they claim to be.
func readManifestBlob(t *testing.T, dir string, h v1.Hash) *v1.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "blobs", h.Algorithm, h.Hex))
	if err != nil {
		t.Fatalf("read blob %s: %v", h, err)
	}
	var mf v1.Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		t.Fatalf("decode manifest blob %s: %v", h, err)
	}
	return &mf
}

// layoutBlobs returns the set of blob file names (hex digests) under
// blobs/sha256.
func layoutBlobs(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "blobs", "sha256"))
	if err != nil {
		t.Fatalf("read blobs/sha256: %v", err)
	}
	out := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		out[e.Name()] = struct{}{}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
