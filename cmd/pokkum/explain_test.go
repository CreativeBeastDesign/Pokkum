package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	pokkumregistry "github.com/CreativeBeastDesign/pokkum/pkg/registry"
)

// --- fixture construction --------------------------------------------------

// layerSpec describes one layer of a hand-built test image: its file
// contents and the v1.History.CreatedBy string it carries, matching the
// real "pokkum: add <path>" format internal/adapters/packager/packager.go
// stamps on every layer it appends.
type layerSpec struct {
	files     map[string]string
	createdBy string
}

func buildLayerFromFiles(t *testing.T, files map[string]string) v1.Layer {
	t.Helper()

	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, name := range names {
			body := files[name]
			hdr := &tar.Header{
				Typeflag: tar.TypeReg,
				Name:     name,
				Mode:     0o644,
				Size:     int64(len(body)),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, err
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				return nil, err
			}
		}
		if err := tw.Close(); err != nil {
			return nil, err
		}
		return io.NopCloser(&buf), nil
	})
	if err != nil {
		t.Fatalf("tarball.LayerFromOpener: %v", err)
	}
	return layer
}

// buildImageFromLayers assembles a real v1.Image the same way packager.go
// does: mutate.Append over a base, one Addendum per layer, each carrying a
// real v1.History entry. specs[0] plays the role of "the base image's own
// layer" (often with no useful CreatedBy, the way a real Chainguard/
// distroless layer looks from the outside); the rest play Pokkum's own
// appended layers.
func buildImageFromLayers(t *testing.T, osName, arch string, specs []layerSpec) v1.Image {
	t.Helper()

	cf, err := empty.Image.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	cf = cf.DeepCopy()
	cf.OS = osName
	cf.Architecture = arch
	cf.Variant = ""
	base, err := mutate.ConfigFile(empty.Image, cf)
	if err != nil {
		t.Fatalf("mutate.ConfigFile: %v", err)
	}

	addenda := make([]mutate.Addendum, 0, len(specs))
	for _, spec := range specs {
		addenda = append(addenda, mutate.Addendum{
			Layer:   buildLayerFromFiles(t, spec.files),
			History: v1.History{CreatedBy: spec.createdBy},
		})
	}

	img, err := mutate.Append(base, addenda...)
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}
	return img
}

// standardTestLayers is the layer set every non-index-selection test builds
// against: one base-image-style layer with no Pokkum history, then three
// real Pokkum-style layers, matching packager.go's actual CreatedBy strings.
func standardTestLayers() []layerSpec {
	return []layerSpec{
		{files: map[string]string{"etc/os-release": "ID=synthetic\n"}, createdBy: ""},
		{files: map[string]string{"usr/local/bin/bun": "bun-binary-content"}, createdBy: "pokkum: add /usr/local/bin/bun"},
		{files: map[string]string{"pokkum/init": "init-binary"}, createdBy: "pokkum: add /pokkum/init"},
		{files: map[string]string{
			"app/server/index.js":    "server code v1",
			"app/server/chunks/a.js": "chunk",
		}, createdBy: "pokkum: add /app/server"},
	}
}

func pushImage(t *testing.T, srv *pokkumregistry.Server, repo string, img v1.Image) string {
	t.Helper()
	ref := srv.Repo(repo) + ":v1"
	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", ref, err)
	}
	if err := remote.Write(tag, img); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return ref
}

func pushIndex(t *testing.T, srv *pokkumregistry.Server, repo string, children map[ports.Platform]v1.Image) string {
	t.Helper()
	ref := srv.Repo(repo) + ":v1"

	var addenda []mutate.IndexAddendum
	for p, img := range children {
		addenda = append(addenda, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				Platform: &v1.Platform{OS: p.OS, Architecture: p.Arch},
			},
		})
	}
	idx := mutate.AppendManifests(empty.Index, addenda...)

	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", ref, err)
	}
	if err := remote.WriteIndex(tag, idx); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	return ref
}

func newTestServer(t *testing.T) *pokkumregistry.Server {
	t.Helper()
	srv, err := pokkumregistry.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// captureStdout runs fn with os.Stdout redirected, and returns everything
// fn printed alongside whatever error it returned.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	fnErr := fn()

	_ = w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), fnErr
}

func decodeEnvelopeData(t *testing.T, raw string, command string, out interface{}) {
	t.Helper()
	var env ports.JSONEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("failed to parse JSON envelope: %v, raw: %s", err, raw)
	}
	if env.Command != command || env.Status != "success" {
		t.Fatalf("unexpected envelope: %+v, raw: %s", env, raw)
	}
	dataBytes, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("re-marshal envelope data: %v", err)
	}
	if err := json.Unmarshal(dataBytes, out); err != nil {
		t.Fatalf("decode envelope data into %T: %v, raw: %s", out, err, raw)
	}
}

// --- explain -----------------------------------------------------------

func TestExplainCommand_RealLayerBreakdown(t *testing.T) {
	srv := newTestServer(t)
	img := buildImageFromLayers(t, "linux", "amd64", standardTestLayers())
	ref := pushImage(t, srv, "explain-real", img)

	opts := &explainOptions{output: "json", platform: "linux/amd64"}
	raw, err := captureStdout(t, func() error {
		return runExplain(context.Background(), opts, ref)
	})
	if err != nil {
		t.Fatalf("runExplain: %v, output: %s", err, raw)
	}

	var out ports.ExplainOutput
	decodeEnvelopeData(t, raw, "explain", &out)

	// The real fix: this is 4, not a hardcoded 5 or 8 — whatever the actual
	// image has.
	if len(out.Layers) != 4 {
		t.Fatalf("expected 4 real layers, got %d: %+v", len(out.Layers), out.Layers)
	}

	wantPurposes := []string{
		"(no history metadata)",
		"Adds /usr/local/bin/bun",
		"Adds /pokkum/init",
		"Adds /app/server",
	}
	wantFileCounts := []int{1, 1, 1, 2}
	for i, l := range out.Layers {
		if l.LayerIndex != i {
			t.Errorf("layer %d: LayerIndex = %d", i, l.LayerIndex)
		}
		if l.Purpose != wantPurposes[i] {
			t.Errorf("layer %d: Purpose = %q, want %q", i, l.Purpose, wantPurposes[i])
		}
		if l.FileCount != wantFileCounts[i] {
			t.Errorf("layer %d: FileCount = %d, want %d", i, l.FileCount, wantFileCounts[i])
		}
		if l.Digest == "" || l.Digest == "(digest unknown)" {
			t.Errorf("layer %d: expected a real digest, got %q", i, l.Digest)
		}
		if l.SizeBytes <= 0 {
			t.Errorf("layer %d: expected a real positive size, got %d", i, l.SizeBytes)
		}
	}
	if out.TotalSize <= 0 {
		t.Errorf("expected a real positive TotalSize, got %d", out.TotalSize)
	}
}

func TestExplainCommand_HistoryDiffIDsMismatchFallback(t *testing.T) {
	srv := newTestServer(t)
	img := buildImageFromLayers(t, "linux", "amd64", standardTestLayers())

	// Corrupt the config's History (drop one entry) without touching the
	// real layers/DiffIDs, simulating a base image whose History array is
	// malformed relative to its own layers. layerPurposes must recognize
	// the count disagreement and fall back to "unknown" for every index
	// rather than silently misattributing purposes.
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	cf = cf.DeepCopy()
	if len(cf.History) < 2 {
		t.Fatalf("test setup: expected at least 2 history entries, got %d", len(cf.History))
	}
	cf.History = cf.History[1:]
	corrupted, err := mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatalf("mutate.ConfigFile: %v", err)
	}

	ref := pushImage(t, srv, "explain-mismatch", corrupted)

	opts := &explainOptions{output: "json", platform: "linux/amd64"}
	raw, err := captureStdout(t, func() error {
		return runExplain(context.Background(), opts, ref)
	})
	if err != nil {
		t.Fatalf("runExplain: %v, output: %s", err, raw)
	}

	var out ports.ExplainOutput
	decodeEnvelopeData(t, raw, "explain", &out)

	for i, l := range out.Layers {
		if l.Purpose != "(no history metadata)" {
			t.Errorf("layer %d: expected the mismatch fallback, got Purpose = %q", i, l.Purpose)
		}
	}
}

func TestExplainCommand_PlatformSelection(t *testing.T) {
	srv := newTestServer(t)

	amd64Layers := standardTestLayers()
	// A 5th, amd64-only layer, so the two children have a distinguishably
	// different real layer count — the only way to prove the right child
	// was actually fetched, not just that fetching succeeded.
	amd64Layers = append(amd64Layers, layerSpec{
		files:     map[string]string{"app/native/addon.node": "native binary"},
		createdBy: "pokkum: add /app/native",
	})

	children := map[ports.Platform]v1.Image{
		ports.LinuxAMD64: buildImageFromLayers(t, "linux", "amd64", amd64Layers),
		ports.LinuxARM64: buildImageFromLayers(t, "linux", "arm64", standardTestLayers()),
	}
	ref := pushIndex(t, srv, "explain-platform", children)

	for _, tc := range []struct {
		platform   string
		wantLayers int
	}{
		{"linux/amd64", 5},
		{"linux/arm64", 4},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			opts := &explainOptions{output: "json", platform: tc.platform}
			raw, err := captureStdout(t, func() error {
				return runExplain(context.Background(), opts, ref)
			})
			if err != nil {
				t.Fatalf("runExplain: %v, output: %s", err, raw)
			}

			var out ports.ExplainOutput
			decodeEnvelopeData(t, raw, "explain", &out)
			if len(out.Layers) != tc.wantLayers {
				t.Errorf("platform %s: got %d layers, want %d", tc.platform, len(out.Layers), tc.wantLayers)
			}
			if out.Platform != tc.platform {
				t.Errorf("Platform = %q, want %q", out.Platform, tc.platform)
			}
		})
	}
}

// --- why -----------------------------------------------------------------

func TestWhyCommand_FoundNotFoundDeletedAmbiguous(t *testing.T) {
	srv := newTestServer(t)

	found := "app/server/index.js"
	deletedByFile := "app/server/chunks/a.js"
	deletedTargetLayers := append(standardTestLayers(), layerSpec{
		files:     map[string]string{"app/server/chunks/.wh.a.js": ""},
		createdBy: "pokkum: prune stale chunk",
	})
	deletedImg := buildImageFromLayers(t, "linux", "amd64", deletedTargetLayers)
	deletedRef := pushImage(t, srv, "why-deleted", deletedImg)

	ambiguousLayers := append(standardTestLayers(), layerSpec{
		files:     map[string]string{"app/server/.wh..wh..opq": ""},
		createdBy: "pokkum: reset app/server",
	})
	ambiguousImg := buildImageFromLayers(t, "linux", "amd64", ambiguousLayers)
	ambiguousRef := pushImage(t, srv, "why-ambiguous", ambiguousImg)

	plainImg := buildImageFromLayers(t, "linux", "amd64", standardTestLayers())
	plainRef := pushImage(t, srv, "why-plain", plainImg)

	t.Run("found", func(t *testing.T) {
		opts := &explainOptions{output: "json", platform: "linux/amd64"}
		raw, err := captureStdout(t, func() error {
			return runWhy(context.Background(), opts, plainRef, found)
		})
		if err != nil {
			t.Fatalf("runWhy: %v, output: %s", err, raw)
		}
		var out map[string]interface{}
		decodeEnvelopeData(t, raw, "explain why", &out)
		if found, _ := out["found"].(bool); !found {
			t.Fatalf("expected found=true, got %+v", out)
		}
		if idx, _ := out["layer_index"].(float64); int(idx) != 3 {
			t.Errorf("expected layer_index=3, got %+v", out["layer_index"])
		}
	})

	t.Run("found in an earlier, non-last layer", func(t *testing.T) {
		// The walk starts at the LAST layer (index 3) and must correctly
		// continue backward through layers 2 and 1, neither of which
		// contain this path, before finding it at layer 1 — proving the
		// loop doesn't stop or misindex after the first miss.
		opts := &explainOptions{output: "json", platform: "linux/amd64"}
		raw, err := captureStdout(t, func() error {
			return runWhy(context.Background(), opts, plainRef, "usr/local/bin/bun")
		})
		if err != nil {
			t.Fatalf("runWhy: %v, output: %s", err, raw)
		}
		var out map[string]interface{}
		decodeEnvelopeData(t, raw, "explain why", &out)
		if found, _ := out["found"].(bool); !found {
			t.Fatalf("expected found=true, got %+v", out)
		}
		if idx, _ := out["layer_index"].(float64); int(idx) != 1 {
			t.Errorf("expected layer_index=1, got %+v", out["layer_index"])
		}
	})

	t.Run("not found", func(t *testing.T) {
		opts := &explainOptions{output: "json", platform: "linux/amd64"}
		raw, err := captureStdout(t, func() error {
			return runWhy(context.Background(), opts, plainRef, "app/does/not/exist.txt")
		})
		if err != nil {
			t.Fatalf("runWhy: %v, output: %s", err, raw)
		}
		var out map[string]interface{}
		decodeEnvelopeData(t, raw, "explain why", &out)
		if found, _ := out["found"].(bool); found {
			t.Fatalf("expected found=false, got %+v", out)
		}
		if _, deleted := out["deleted_at_layer"]; deleted {
			t.Errorf("did not expect deleted_at_layer for a path never present, got %+v", out)
		}
	})

	t.Run("deleted", func(t *testing.T) {
		opts := &explainOptions{output: "json", platform: "linux/amd64"}
		raw, err := captureStdout(t, func() error {
			return runWhy(context.Background(), opts, deletedRef, deletedByFile)
		})
		if err != nil {
			t.Fatalf("runWhy: %v, output: %s", err, raw)
		}
		var out map[string]interface{}
		decodeEnvelopeData(t, raw, "explain why", &out)
		if found, _ := out["found"].(bool); found {
			t.Fatalf("expected found=false for a deleted file, got %+v", out)
		}
		if idx, ok := out["deleted_at_layer"].(float64); !ok || int(idx) != 4 {
			t.Errorf("expected deleted_at_layer=4, got %+v", out)
		}
	})

	t.Run("ambiguous opaque whiteout", func(t *testing.T) {
		opts := &explainOptions{output: "json", platform: "linux/amd64"}
		raw, err := captureStdout(t, func() error {
			return runWhy(context.Background(), opts, ambiguousRef, found)
		})
		if err != nil {
			t.Fatalf("runWhy: %v, output: %s", err, raw)
		}
		var out map[string]interface{}
		decodeEnvelopeData(t, raw, "explain why", &out)
		if found, _ := out["found"].(bool); found {
			t.Fatalf("expected found=false under an opaque whiteout, got %+v", out)
		}
		if idx, ok := out["ambiguous_at_layer"].(float64); !ok || int(idx) != 4 {
			t.Errorf("expected ambiguous_at_layer=4, got %+v", out)
		}
	})
}

// --- diff ------------------------------------------------------------------

func TestDiffCommand_Identical(t *testing.T) {
	srv := newTestServer(t)
	img := buildImageFromLayers(t, "linux", "amd64", standardTestLayers())
	ref1 := pushImage(t, srv, "diff-a", img)
	ref2 := pushImage(t, srv, "diff-b", img)

	opts := &explainOptions{output: "json", platform: "linux/amd64"}
	raw, err := captureStdout(t, func() error {
		return runDiff(context.Background(), opts, ref1, ref2)
	})
	if err != nil {
		t.Fatalf("runDiff: %v, output: %s", err, raw)
	}

	var out map[string]interface{}
	decodeEnvelopeData(t, raw, "explain diff", &out)
	if identical, _ := out["identical"].(bool); !identical {
		t.Fatalf("expected identical=true for two pushes of the same image, got %+v", out)
	}
}

func TestDiffCommand_LayerCountMismatch(t *testing.T) {
	srv := newTestServer(t)
	img1 := buildImageFromLayers(t, "linux", "amd64", standardTestLayers())
	extraLayers := append(standardTestLayers(), layerSpec{
		files:     map[string]string{"app/native/addon.node": "native binary"},
		createdBy: "pokkum: add /app/native",
	})
	img2 := buildImageFromLayers(t, "linux", "amd64", extraLayers)

	ref1 := pushImage(t, srv, "diff-count-a", img1)
	ref2 := pushImage(t, srv, "diff-count-b", img2)

	opts := &explainOptions{output: "json", platform: "linux/amd64"}
	raw, err := captureStdout(t, func() error {
		return runDiff(context.Background(), opts, ref1, ref2)
	})
	if err != nil {
		t.Fatalf("runDiff: %v, output: %s", err, raw)
	}

	var out map[string]interface{}
	decodeEnvelopeData(t, raw, "explain diff", &out)
	if identical, _ := out["identical"].(bool); identical {
		t.Fatalf("expected identical=false for images with a different layer count, got %+v", out)
	}

	modified, _ := out["modified"].([]interface{})
	found := false
	for _, m := range modified {
		s, _ := m.(string)
		if bytes.Contains([]byte(s), []byte("only in the second image")) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an entry reporting layer 4 as present only in the second image, got: %+v", modified)
	}
}

func TestDiffCommand_Modified(t *testing.T) {
	srv := newTestServer(t)
	img1 := buildImageFromLayers(t, "linux", "amd64", standardTestLayers())

	modifiedLayers := standardTestLayers()
	modifiedLayers[3] = layerSpec{
		files: map[string]string{
			"app/server/index.js":    "server code v2 — different content",
			"app/server/chunks/b.js": "a brand new chunk",
		},
		createdBy: "pokkum: add /app/server",
	}
	img2 := buildImageFromLayers(t, "linux", "amd64", modifiedLayers)

	ref1 := pushImage(t, srv, "diff-mod-a", img1)
	ref2 := pushImage(t, srv, "diff-mod-b", img2)

	opts := &explainOptions{output: "json", platform: "linux/amd64"}
	raw, err := captureStdout(t, func() error {
		return runDiff(context.Background(), opts, ref1, ref2)
	})
	if err != nil {
		t.Fatalf("runDiff: %v, output: %s", err, raw)
	}

	var out map[string]interface{}
	decodeEnvelopeData(t, raw, "explain diff", &out)
	if identical, _ := out["identical"].(bool); identical {
		t.Fatalf("expected identical=false for two genuinely different images, got %+v", out)
	}

	modified, _ := out["modified"].([]interface{})
	if len(modified) == 0 {
		t.Fatalf("expected real per-file change entries, got none: %+v", out)
	}

	joined := ""
	for _, m := range modified {
		s, _ := m.(string)
		joined += s + "\n"
	}
	for _, want := range []string{"index.js", "chunks/a.js", "chunks/b.js"} {
		if !bytes.Contains([]byte(joined), []byte(want)) {
			t.Errorf("expected a change entry mentioning %q, got:\n%s", want, joined)
		}
	}
}
