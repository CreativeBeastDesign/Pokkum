package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	pokkumregistry "github.com/CreativeBeastDesign/pokkum/pkg/registry"
)

// F7: `pokkum history "$(pokkum build .)"` drops git provenance because
// `pokkum build`'s printed ref is the multi-platform INDEX digest, and the
// full org.opencontainers.image.* annotation set (revision, source,
// base.name, ...) lives on each per-platform child manifest, never on the
// index's own manifest -- packager.Index (internal/adapters/packager) writes
// only "created" plus caller-supplied annotations onto the index itself, the
// same shape reproduced here.
//
// pushMultiPlatformIndex builds a real 2-platform index -- deliberately with
// linux/amd64 as the SECOND child, not the first, so a selection bug that
// just grabs im.Manifests[0] cannot pass this test by accident (self-review
// checklist row 4: non-first-item failure injection) -- and gives each
// platform a distinct revision so a test that doesn't check the actual value
// (row 3: multi-item, differing content) can't pass vacuously either.
func pushMultiPlatformIndex(t *testing.T, indexAnnotations map[string]string, perPlatform map[string]map[string]string) string {
	t.Helper()

	srv, err := pokkumregistry.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(srv.Close)

	// Deliberate order: arm64 first, amd64 second.
	platforms := []v1.Platform{
		{OS: "linux", Architecture: "arm64"},
		{OS: "linux", Architecture: "amd64"},
	}

	adds := make([]mutate.IndexAddendum, 0, len(platforms))
	for _, p := range platforms {
		img, ierr := random.Image(128, 1)
		if ierr != nil {
			t.Fatalf("random.Image: %v", ierr)
		}
		anns := perPlatform[p.String()]
		if len(anns) > 0 {
			img = mutate.Annotations(img, anns).(v1.Image)
		}
		adds = append(adds, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				Platform: &v1.Platform{OS: p.OS, Architecture: p.Architecture},
			},
		})
	}

	idx := mutate.AppendManifests(empty.Index, adds...)
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
	if len(indexAnnotations) > 0 {
		idx = mutate.Annotations(idx, indexAnnotations).(v1.ImageIndex)
	}

	repo := srv.Repo("history-index-test")
	tag, err := name.NewTag(repo+":v1", name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	if err := remote.WriteIndex(tag, idx); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}

	digest, err := idx.Digest()
	if err != nil {
		t.Fatalf("idx.Digest: %v", err)
	}
	// Mirrors `pokkum build`'s printed output: a repo@digest reference to the
	// INDEX, not a tag and not a per-platform digest.
	return repo + "@" + digest.String()
}

func runHistoryJSON(t *testing.T, ref string) ports.JSONEnvelope {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	flags := &historyFlags{output: "json"}
	err := runHistory(context.Background(), logger, flags, ref)
	_ = w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runHistory failed: %v", err)
	}

	var output bytes.Buffer
	_, _ = io.Copy(&output, r)

	var env ports.JSONEnvelope
	if jerr := json.Unmarshal(output.Bytes(), &env); jerr != nil {
		t.Fatalf("failed to parse JSON envelope: %v, raw: %s", jerr, output.String())
	}
	return env
}

// TestHistoryCommand_IndexRefDescendsToChildManifest is the fail-first proof
// for F7: given the exact ref shape `pokkum build` prints (repo@index-digest),
// `pokkum history` must report the richer per-platform annotation set, not
// just the index's own impoverished one -- and it must say so, rather than
// silently presenting a child manifest's annotations as the index's own.
func TestHistoryCommand_IndexRefDescendsToChildManifest(t *testing.T) {
	ref := pushMultiPlatformIndex(t,
		map[string]string{
			"org.opencontainers.image.created": "2026-01-02T03:04:05Z",
			"pokkum.dev/build-input-hash":      "deadbeef",
		},
		map[string]map[string]string{
			"linux/arm64": {
				"org.opencontainers.image.revision": "arm64-revision-should-not-win",
				"org.opencontainers.image.source":   "https://github.com/acme/app",
			},
			"linux/amd64": {
				"org.opencontainers.image.revision":  "bc5c6ab1234567890",
				"org.opencontainers.image.source":    "https://github.com/acme/app",
				"org.opencontainers.image.base.name": "docker.io/library/node:22-slim",
			},
		},
	)

	env := runHistoryJSON(t, ref)
	if env.Status != "success" {
		t.Fatalf("expected success, got: %+v", env)
	}

	dataBytes, _ := json.Marshal(env.Data)

	var res ports.HistoryOutput
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		t.Fatalf("failed to parse HistoryOutput: %v", err)
	}

	// The bug: history reads only the index's own manifest, so GitCommit and
	// GitRepo come back empty and Annotations has exactly the 2 index-level
	// keys instead of the richer per-platform set.
	if res.GitCommit != "bc5c6ab1234567890" {
		t.Errorf("GitCommit: got %q, want the linux/amd64 child manifest's revision %q (not empty, not the arm64 one)", res.GitCommit, "bc5c6ab1234567890")
	}
	if res.GitRepo != "https://github.com/acme/app" {
		t.Errorf("GitRepo: got %q, want %q", res.GitRepo, "https://github.com/acme/app")
	}
	if res.Annotations["org.opencontainers.image.base.name"] != "docker.io/library/node:22-slim" {
		t.Errorf("Annotations[base.name]: got %q, want the amd64 child's base.name annotation", res.Annotations["org.opencontainers.image.base.name"])
	}
	// Index-only annotations must not be lost by the merge.
	if res.Annotations["pokkum.dev/build-input-hash"] != "deadbeef" {
		t.Errorf("Annotations[build-input-hash]: got %q, want the index-level annotation to survive the merge", res.Annotations["pokkum.dev/build-input-hash"])
	}

	// Honesty requirement: the report must say the annotations came from a
	// child manifest, not present them as if they were the index's own.
	var full map[string]any
	if err := json.Unmarshal(dataBytes, &full); err != nil {
		t.Fatalf("failed to parse full payload: %v", err)
	}
	src, _ := full["annotations_source"].(string)
	if src != "index+child-manifest" {
		t.Errorf("annotations_source: got %q, want %q (must disclose the descend)", src, "index+child-manifest")
	}
	childDigest, _ := full["annotations_child_digest"].(string)
	if childDigest == "" {
		t.Error("annotations_child_digest: expected the child manifest's own digest to be disclosed, got empty")
	}
	if got := full["annotations_child_platform"]; got != "linux/amd64" {
		t.Errorf("annotations_child_platform: got %v, want %q", got, "linux/amd64")
	}
}

// TestHistoryCommand_IndexFallsBackToOnlyAvailablePlatform covers an
// arm64-only index (no linux/amd64 present at all): selection must still be
// deterministic and must not error out just because the preferred default
// platform is absent.
func TestHistoryCommand_IndexFallsBackToOnlyAvailablePlatform(t *testing.T) {
	srv, err := pokkumregistry.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(srv.Close)

	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	img = mutate.Annotations(img, map[string]string{
		"org.opencontainers.image.revision": "arm64-only-revision",
	}).(v1.Image)

	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add: img,
		Descriptor: v1.Descriptor{
			Platform: &v1.Platform{OS: "linux", Architecture: "arm64"},
		},
	})
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)

	repo := srv.Repo("history-arm64-only")
	tag, err := name.NewTag(repo+":v1", name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	if err := remote.WriteIndex(tag, idx); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	digest, err := idx.Digest()
	if err != nil {
		t.Fatalf("idx.Digest: %v", err)
	}

	env := runHistoryJSON(t, repo+"@"+digest.String())
	if env.Status != "success" {
		t.Fatalf("expected success, got: %+v", env)
	}
	dataBytes, _ := json.Marshal(env.Data)
	var res ports.HistoryOutput
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		t.Fatalf("failed to parse HistoryOutput: %v", err)
	}
	if res.GitCommit != "arm64-only-revision" {
		t.Errorf("GitCommit: got %q, want the sole arm64 child's revision", res.GitCommit)
	}
}
