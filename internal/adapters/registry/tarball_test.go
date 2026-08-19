package registry

import (
	"bytes"
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
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestTarballWrite_EmptyPath(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.Write(context.Background(), ports.TarballRequest{
		Repo:    "pokkum.local/app",
		Payload: ports.Payload{Image: randomImage(t)},
	})
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Fatalf("err = %v, want core.ErrTarballFailed", err)
	}
}

func TestTarballWrite_ZeroPayload(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.Write(context.Background(), ports.TarballRequest{
		Repo: "pokkum.local/app",
		Path: filepath.Join(t.TempDir(), "out.tar"),
	})
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Fatalf("err = %v, want core.ErrTarballFailed", err)
	}
}

func TestTarballWrite_SingleImage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")
	img := randomImage(t)
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: img},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", res.Digest, wantDigest)
	}
	if res.Path != path {
		t.Errorf("Path = %q, want %q", res.Path, path)
	}
	if len(res.Tags) != 1 || res.Tags[0] != "v1" {
		t.Errorf("Tags = %v, want [v1]", res.Tags)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("archive not present at %s: %v", path, err)
	}

	tag, err := name.NewTag("pokkum.local/app:v1")
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	pulled, err := tarball.ImageFromPath(path, &tag)
	if err != nil {
		t.Fatalf("tarball.ImageFromPath: %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatalf("pulled.Digest: %v", err)
	}
	if gotDigest != wantDigest {
		t.Errorf("archive digest = %s, want %s", gotDigest, wantDigest)
	}
}

func TestTarballWrite_Index_FlattensEveryPlatform(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")
	idx := indexWithPlatforms(t, []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})
	wantDigest, err := idx.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Index: idx},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want index digest %s", res.Digest, wantDigest)
	}

	wantTags := []string{"v1-linux-amd64", "v1-linux-arm64"}
	gotTags := append([]string(nil), res.Tags...)
	sort.Strings(gotTags)
	sort.Strings(wantTags)
	if strings.Join(gotTags, ",") != strings.Join(wantTags, ",") {
		t.Fatalf("Tags = %v, want %v", res.Tags, wantTags)
	}

	// Every platform must be independently readable back out of the one
	// archive — nothing silently dropped.
	for _, want := range []struct {
		tag      string
		os, arch string
	}{
		{"v1-linux-amd64", "linux", "amd64"},
		{"v1-linux-arm64", "linux", "arm64"},
	} {
		tag, err := name.NewTag("pokkum.local/app:" + want.tag)
		if err != nil {
			t.Fatalf("NewTag(%q): %v", want.tag, err)
		}
		img, err := tarball.ImageFromPath(path, &tag)
		if err != nil {
			t.Fatalf("tarball.ImageFromPath(%q): %v", want.tag, err)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatalf("ConfigFile(%q): %v", want.tag, err)
		}
		if cf.OS != want.os || cf.Architecture != want.arch {
			t.Errorf("tag %q image is %s/%s, want %s/%s", want.tag, cf.OS, cf.Architecture, want.os, want.arch)
		}
	}
}

// TestTarballWrite_FailureLeavesNoTruncatedFile proves the temp-file-and-
// rename strategy: when the underlying write fails partway through, the
// destination path is left exactly as it was before the call (here,
// nonexistent), never a partially-written file that a later `docker load`
// would accept and then fail on.
func TestTarballWrite_FailureLeavesNoTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")

	a := NewAdapter(nil)
	_, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: brokenImage{randomImage(t)}},
	})
	if err == nil {
		t.Fatal("Write: want error from a broken image, got nil")
	}
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Errorf("err = %v, want core.ErrTarballFailed", err)
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("destination path exists after a failed write: %v", statErr)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("temp directory not cleaned up, found: %v", names)
	}
}

// brokenImage wraps a real image but fails Layers(), so tarball.
// MultiWriteToFile errors out partway through — deep enough that the failure
// exercises the real write path, not just Write's own up-front validation.
type brokenImage struct{ v1.Image }

func (brokenImage) Layers() ([]v1.Layer, error) {
	return nil, errors.New("brokenImage: injected failure")
}

// --- dropped-annotations warning ---------------------------------------------
//
// The legacy docker-save tarball format Write emits has no annotations field
// at all (go-containerregistry v0.21.9's writer struct carries only Config,
// RepoTags, Layers and LayerSources), so every annotation Pokkum stamps —
// pokkum.dev/predecessor, pokkum.dev/asset-overlay-sources,
// org.opencontainers.image.*, etc. — is silently discarded for
// --output=tarball. See docs/items/tarball-output-drops-annotations.md.
// These tests prove the fix: a clear, deterministic, Warn-level line that
// names the exact keys, fired only when the image actually carries any.

// annotatedImage returns img with anns set as manifest-level OCI annotations,
// the same mechanism internal/adapters/packager uses to stamp
// pokkum.dev/predecessor and friends onto a real build (mutate.Annotations).
func annotatedImage(t *testing.T, img v1.Image, anns map[string]string) v1.Image {
	t.Helper()
	out, ok := mutate.Annotations(img, anns).(v1.Image)
	if !ok {
		t.Fatalf("mutate.Annotations: result is not a v1.Image")
	}
	return out
}

// warnLogMessages decodes every JSON log line in buf and returns the "msg"
// field of every record logged at WARN level, in the order they were
// written.
// captureTarballWarn writes a tarball for an image carrying the given
// annotations and returns the single Warn-level message produced.
func captureTarballWarn(t *testing.T, annotations map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.tar")
	img := annotatedImage(t, randomImage(t), annotations)
	a, buf := newLoggingAdapter()
	if _, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Payload: ports.Payload{Image: img},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	warnings := warnLogMessages(t, buf)
	if len(warnings) != 1 {
		t.Fatalf("warn-level log lines = %d, want exactly 1: %v", len(warnings), warnings)
	}
	return warnings[0]
}

func warnLogMessages(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	var msgs []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if rec["level"] == "WARN" {
			if msg, ok := rec["msg"].(string); ok {
				msgs = append(msgs, msg)
			}
		}
	}
	return msgs
}

// TestTarballWrite_WarnsOnDroppedAnnotations_NamesTheKeys is the core
// regression test: an image carrying annotations must produce a Warn-level
// line that names each dropped key by name, not a generic "annotations may
// be lost" message. The three keys are chosen so that alphabetical order
// differs from map/insertion order — org.opencontainers.image.revision <
// pokkum.dev/env-baked < pokkum.dev/predecessor — so a test that only checked
// "message contains each key" could pass even if the implementation forgot
// to sort. Asserting the exact string catches that.
func TestTarballWrite_WarnsOnDroppedAnnotations_NamesTheKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")
	img := annotatedImage(t, randomImage(t), map[string]string{
		ports.AnnotationPredecessor:         "sha256:" + strings.Repeat("a", 64),
		ports.AnnotationEnvBaked:            "PUBLIC_API_URL",
		"org.opencontainers.image.revision": "abc123",
	})

	a, buf := newLoggingAdapter()
	if _, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Payload: ports.Payload{Image: img},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	warnings := warnLogMessages(t, buf)
	if len(warnings) != 1 {
		t.Fatalf("warn-level log lines = %d, want exactly 1: %v", len(warnings), warnings)
	}
	want := "tarball output drops OCI annotations: org.opencontainers.image.revision, pokkum.dev/env-baked, pokkum.dev/predecessor" +
		" (annotations survive a registry push — use --output=push to keep them)" +
		" — pokkum.dev/env-baked, pokkum.dev/predecessor carry build metadata other pokkum commands read back," +
		" so commands depending on them cannot work against this output"
	if warnings[0] != want {
		t.Errorf("warning = %q, want %q", warnings[0], want)
	}
}

// TestTarballWrite_NoAnnotations_NoWarning proves the flip side: an ordinary
// image with no annotations at all must not trigger any Warn-level line. A
// warning that fires on every routine build is noise that trains operators
// to ignore it — the defect this fixes is silence on the annotated case, not
// an excuse to add noise to the unannotated one.
func TestTarballWrite_NoAnnotations_NoWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")
	img := randomImage(t)

	a, buf := newLoggingAdapter()
	if _, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Payload: ports.Payload{Image: img},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if warnings := warnLogMessages(t, buf); len(warnings) != 0 {
		t.Errorf("warn-level log lines = %v, want none for an unannotated image", warnings)
	}
}

// TestTarballWrite_DroppedAnnotationsWarning_Deterministic runs the same
// annotated write several times over and asserts byte-identical warning
// text every time. Go's map iteration order is randomized per-process, so
// this specifically guards against a regression that lists annotation keys
// in map order instead of sorting them first.
func TestTarballWrite_DroppedAnnotationsWarning_Deterministic(t *testing.T) {
	anns := map[string]string{
		ports.AnnotationVEXExemptions:       "CVE-2024-0001",
		ports.AnnotationAssetOverlaySources: "sha256:" + strings.Repeat("b", 64),
		"org.opencontainers.image.source":   "https://example.com/repo",
	}

	var messages []string
	for i := 0; i < 5; i++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "out.tar")
		img := annotatedImage(t, randomImage(t), anns)

		a, buf := newLoggingAdapter()
		if _, err := a.Write(context.Background(), ports.TarballRequest{
			Path:    path,
			Repo:    "pokkum.local/app",
			Payload: ports.Payload{Image: img},
		}); err != nil {
			t.Fatalf("Write (run %d): %v", i, err)
		}
		warnings := warnLogMessages(t, buf)
		if len(warnings) != 1 {
			t.Fatalf("run %d: warn-level log lines = %d, want exactly 1: %v", i, len(warnings), warnings)
		}
		messages = append(messages, warnings[0])
	}

	for i := 1; i < len(messages); i++ {
		if messages[i] != messages[0] {
			t.Errorf("run %d warning = %q, want identical to run 0's %q", i, messages[i], messages[0])
		}
	}
	wantSorted := "org.opencontainers.image.source, pokkum.dev/asset-overlay-sources, pokkum.dev/vex-exemptions"
	if !strings.Contains(messages[0], wantSorted) {
		t.Errorf("warning = %q, want it to contain sorted key list %q", messages[0], wantSorted)
	}
}

// TestTarballWrite_WarnsOnDroppedAnnotations_IndexFlattening proves the
// index/multi-platform path: tagToImage flattens each platform to its own
// tar entry (see flattenIndexTags), and each child's own manifest
// annotations are what is actually discarded — so the warning must name
// every key present on any child, not just the outer index's own
// (nonexistent, in this codepath) annotations.
func TestTarballWrite_WarnsOnDroppedAnnotations_IndexFlattening(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")

	amd64 := annotatedImage(t, imageWithPlatform(t, "linux", "amd64"), map[string]string{
		ports.AnnotationPredecessor: "sha256:" + strings.Repeat("c", 64),
	})
	arm64 := annotatedImage(t, imageWithPlatform(t, "linux", "arm64"), map[string]string{
		ports.AnnotationEnvBaked: "PUBLIC_API_URL",
	})
	idx := mutate.AppendManifests(
		empty.Index,
		mutate.IndexAddendum{Add: amd64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: arm64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)

	a, buf := newLoggingAdapter()
	if _, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Payload: ports.Payload{Index: idx},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	warnings := warnLogMessages(t, buf)
	if len(warnings) != 1 {
		t.Fatalf("warn-level log lines = %d, want exactly 1: %v", len(warnings), warnings)
	}
	want := "tarball output drops OCI annotations: pokkum.dev/env-baked, pokkum.dev/predecessor" +
		" (annotations survive a registry push — use --output=push to keep them)" +
		" — pokkum.dev/env-baked, pokkum.dev/predecessor carry build metadata other pokkum commands read back," +
		" so commands depending on them cannot work against this output"
	if warnings[0] != want {
		t.Errorf("warning = %q, want %q", warnings[0], want)
	}
}

// TestTarballWrite_DroppedAnnotationsWarning_CallsOutPokkumSemantics pins the
// distinction that keeps this warning worth reading. Pokkum sets
// org.opencontainers.image.base.name whenever a base ref exists, so a warning
// listing only descriptive keys fires on close to every tarball build. Losing a
// pokkum.dev/* key is a different kind of loss: those are read back by other
// pokkum commands (verify reconstructs the asset-overlay layer from
// pokkum.dev/asset-overlay-sources), so that case must be called out
// specifically rather than blended into the same sentence.
func TestTarballWrite_DroppedAnnotationsWarning_CallsOutPokkumSemantics(t *testing.T) {
	t.Run("descriptive only: no semantics clause", func(t *testing.T) {
		msg := captureTarballWarn(t, map[string]string{
			"org.opencontainers.image.base.name": "gcr.io/distroless/cc-debian12:nonroot",
		})
		if strings.Contains(msg, "other pokkum commands read back") {
			t.Errorf("descriptive-only annotations must not trigger the build-metadata clause, got:\n%s", msg)
		}
		if !strings.Contains(msg, "org.opencontainers.image.base.name") {
			t.Errorf("expected the dropped key to still be named, got:\n%s", msg)
		}
	})

	t.Run("pokkum.dev key: names it in the semantics clause", func(t *testing.T) {
		msg := captureTarballWarn(t, map[string]string{
			"org.opencontainers.image.base.name": "gcr.io/distroless/cc-debian12:nonroot",
			"pokkum.dev/asset-overlay-sources":   "sha256:aaaa",
		})
		if !strings.Contains(msg, "other pokkum commands read back") {
			t.Errorf("a dropped pokkum.dev/* annotation must trigger the build-metadata clause, got:\n%s", msg)
		}
		// The clause must name the semantic key specifically, not just repeat
		// the full list — otherwise it adds no information over the first half.
		idx := strings.Index(msg, "other pokkum commands read back")
		clause := msg[:idx]
		lastSep := strings.LastIndex(clause, " — ")
		if lastSep < 0 || !strings.Contains(clause[lastSep:], "pokkum.dev/asset-overlay-sources") {
			t.Errorf("expected the semantics clause to name pokkum.dev/asset-overlay-sources, got:\n%s", msg)
		}
	})
}
