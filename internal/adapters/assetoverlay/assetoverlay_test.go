package assetoverlay

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// buildClientLayerTar builds a real tar stream shaped like a client layer
// the packager would produce: entries under app/client/_app/immutable/...,
// no leading slash, matching BuildDirectoryTreeLayer's own path.Clean/
// TrimPrefix convention (packager/layer.go).
func buildClientLayerTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Size:     int64(len(content)),
			Mode:     0o644,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write content %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return buf.Bytes()
}

// TestExtractImmutableAssets_OnlyImmutablePrefixExtracted proves the
// extraction filter: only paths under app/client/_app/immutable/ are pulled
// out — a non-hashed file (index.html, service-worker.js) living alongside
// the immutable tree in the same real client layer must never be copied
// into the overlay, since it isn't safe to serve stale.
//
// It also proves the extracted path retains "_app/immutable/..." relative
// to destDir (not just "chunks/..."), since packager.go's
// appendAssetOverlayLayer repackages destDir under AppClientDirPrefix
// unchanged — stripping the immutable prefix here would silently repackage
// every overlay asset one directory level too high in the final image
// (a real regression this test previously missed; see
// TestRealBuild_AssetOverlay_TwoGenerations in tests/integration).
func TestExtractImmutableAssets_OnlyImmutablePrefixExtracted(t *testing.T) {
	destDir := t.TempDir()
	tarBytes := buildClientLayerTar(t, map[string]string{
		"app/client/_app/immutable/chunks/abc123.js": "immutable content",
		"app/client/index.html":                      "not immutable — must not be extracted",
		"app/client/service-worker.js":               "not immutable — must not be extracted",
	})

	if err := extractImmutableAssets(bytes.NewReader(tarBytes), destDir, "test-ref"); err != nil {
		t.Fatalf("extractImmutableAssets: %v", err)
	}

	wantPath := filepath.Join(destDir, "_app", "immutable", "chunks", "abc123.js")
	if got, err := os.ReadFile(wantPath); err != nil {
		t.Fatalf("expected _app/immutable/chunks/abc123.js to be extracted: %v", err)
	} else if string(got) != "immutable content" {
		t.Errorf("_app/immutable/chunks/abc123.js content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(destDir, "..", "index.html")); err == nil {
		t.Error("index.html should not have been extracted anywhere reachable")
	}
	entries, _ := os.ReadDir(destDir)
	if len(entries) != 1 {
		t.Errorf("destDir has %d top-level entries, want exactly 1 (_app/): %v", len(entries), entries)
	}
}

// TestExtractImmutableAssets_IdenticalContentAcrossGenerationsSkipped proves
// the expected, ordinary case: the same content-hashed filename appearing in
// two generations' extraction passes (because it genuinely didn't change
// between builds) is silently accepted, not treated as a conflict — a
// content hash existing in both is only meaningful if the bytes actually
// match, which is exactly what real content-hashing guarantees.
func TestExtractImmutableAssets_IdenticalContentAcrossGenerationsSkipped(t *testing.T) {
	destDir := t.TempDir()
	genA := buildClientLayerTar(t, map[string]string{"app/client/_app/immutable/chunks/stable.js": "unchanged"})
	genB := buildClientLayerTar(t, map[string]string{"app/client/_app/immutable/chunks/stable.js": "unchanged"})

	if err := extractImmutableAssets(bytes.NewReader(genA), destDir, "gen-a"); err != nil {
		t.Fatalf("extract gen A: %v", err)
	}
	if err := extractImmutableAssets(bytes.NewReader(genB), destDir, "gen-b"); err != nil {
		t.Fatalf("extract gen B (identical content) should not error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "_app", "immutable", "chunks", "stable.js"))
	if err != nil || string(got) != "unchanged" {
		t.Errorf("_app/immutable/chunks/stable.js = %q, %v", got, err)
	}
}

// TestExtractImmutableAssets_RejectsPathTraversal is the regression guard
// for a real path-traversal vulnerability found during adversarial review:
// extractImmutableAssets used to filter tar entries against
// immutableAssetPrefix as a RAW STRING PREFIX check, before any path
// cleaning — so an entry name that still literally starts with
// "app/client/_app/immutable/" but is followed by enough "../" segments to
// escape it (e.g. "app/client/_app/immutable/../../../../pwned.txt") passed
// the filter, and filepath.Join(destDir, rel) only resolved those ".."
// segments AFTER the entry had already been accepted — writing attacker-
// chosen bytes outside destDir. The content this filter gates is not
// locally-produced: it comes from a remote registry's layer tar stream,
// reachable via an automatically-walked pokkum.dev/predecessor chain
// (anyone with push access to the target repo) or an arbitrary
// --asset-overlay-from ref (fully caller-controlled, no repo-push
// precondition at all) — so this is a real, externally reachable attack
// surface, not a hypothetical.
func TestExtractImmutableAssets_RejectsPathTraversal(t *testing.T) {
	destDir := t.TempDir()
	outsideDir := t.TempDir()
	evilTarget := filepath.Join(outsideDir, "pwned.txt")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Deliberately crafted so the RAW string still starts with
	// immutableAssetPrefix ("app/client/_app/immutable/") — this is exactly
	// the shape that passed the old, pre-clean prefix check.
	// A generous, depth-independent "../" count: filepath.Join/Clean drops
	// excess ".." once a path reaches "/" rather than erroring, so any count
	// at least as deep as destDir's real depth reliably lands exactly on
	// evilTarget once fully resolved — no need to compute destDir's exact
	// depth (which varies by OS/test-runner temp dir layout).
	traversalName := "app/client/_app/immutable/" + strings.Repeat("../", 30) + strings.TrimPrefix(evilTarget, string(filepath.Separator))
	content := []byte("PWNED")
	if err := tw.WriteHeader(&tar.Header{Name: traversalName, Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o644}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}

	// Not asserting on the error itself (the traversal filter and the
	// immutableAssetPrefix filter can legitimately reject this via either
	// path, both are correct) — asserting on the one thing that actually
	// matters: nothing was ever written outside destDir.
	_ = extractImmutableAssets(bytes.NewReader(buf.Bytes()), destDir, "malicious-ref")

	if _, err := os.Stat(evilTarget); err == nil {
		t.Fatalf("path traversal succeeded: %q was written outside destDir", evilTarget)
	}
	entries, _ := os.ReadDir(outsideDir)
	if len(entries) != 0 {
		t.Fatalf("outsideDir has unexpected entries after traversal attempt: %v", entries)
	}
}

// TestExtractImmutableAssets_ConflictHardFails is the core proof of this
// feature's stated conflict policy: the same content-hashed path appearing
// in two generations with DIFFERENT bytes must hard-fail the whole
// operation with core.ErrAssetOverlayConflict, never silently pick either
// generation's content. A real collision here means an upstream invariant
// broke (a hash collision, or a non-immutable file wrongly living under the
// immutable prefix) and needs a human, not an automatic resolution.
func TestExtractImmutableAssets_ConflictHardFails(t *testing.T) {
	destDir := t.TempDir()
	genA := buildClientLayerTar(t, map[string]string{"app/client/_app/immutable/chunks/collide.js": "version A"})
	genB := buildClientLayerTar(t, map[string]string{"app/client/_app/immutable/chunks/collide.js": "version B — different!"})

	if err := extractImmutableAssets(bytes.NewReader(genA), destDir, "gen-a"); err != nil {
		t.Fatalf("extract gen A: %v", err)
	}
	err := extractImmutableAssets(bytes.NewReader(genB), destDir, "gen-b")
	if err == nil {
		t.Fatal("expected a hard failure for conflicting content at the same hashed path, got nil error")
	}
	if !errors.Is(err, core.ErrAssetOverlayConflict) {
		t.Errorf("err = %v, want core.ErrAssetOverlayConflict", err)
	}

	// The original, first-written content must survive a rejected conflict —
	// a failed merge must not leave a partially-overwritten file behind.
	got, readErr := os.ReadFile(filepath.Join(destDir, "_app", "immutable", "chunks", "collide.js"))
	if readErr != nil || string(got) != "version A" {
		t.Errorf("_app/immutable/chunks/collide.js after rejected conflict = %q, %v — original content must be preserved", got, readErr)
	}
}

// TestBuildOverlayDir_EmptyDigestsReturnsEmptyString proves the "feature
// resolved zero generations" signal: BuildOverlayDir must return ("", nil)
// — not create a directory, not error — when digests is empty, since the
// caller (core.Build) uses an empty string as "no overlay layer" via
// ports.PackageRequest.AssetOverlayDir's own optionality contract.
func TestBuildOverlayDir_EmptyDigestsReturnsEmptyString(t *testing.T) {
	dir, err := BuildOverlayDir(t.Context(), "example.com/repo", nil, "", false)
	if err != nil {
		t.Fatalf("BuildOverlayDir(nil digests): %v", err)
	}
	if dir != "" {
		t.Errorf("dir = %q, want empty string for zero resolved generations", dir)
	}
}

// TestClientLayerIndex_CrossChecksHistoryAgainstDiffIDs guards against
// trusting a misaligned History/Layers mapping — mirrors
// cmd/pokkum/explain.go's layerPurposes discipline (see that function's own
// doc comment for the incident this pattern originally closed).
func TestClientLayerIndex_CrossChecksHistoryAgainstDiffIDs(t *testing.T) {
	t.Run("matches when lengths agree", func(t *testing.T) {
		cfg := &v1.ConfigFile{
			History: []v1.History{
				{CreatedBy: "pokkum: add /usr/local/bin/bun"},
				{CreatedBy: "pokkum: add /pokkum/init"},
				{CreatedBy: "pokkum: add /app/client"},
			},
			RootFS: v1.RootFS{DiffIDs: make([]v1.Hash, 3)},
		}
		idx, ok := clientLayerIndex(cfg, 3)
		if !ok || idx != 2 {
			t.Errorf("clientLayerIndex = %d, %v, want 2, true", idx, ok)
		}
	})

	t.Run("no match when History/DiffIDs lengths disagree", func(t *testing.T) {
		cfg := &v1.ConfigFile{
			History: []v1.History{
				{CreatedBy: "pokkum: add /app/client"},
			},
			RootFS: v1.RootFS{DiffIDs: make([]v1.Hash, 2)}, // deliberately mismatched
		}
		if _, ok := clientLayerIndex(cfg, 1); ok {
			t.Error("expected no match when History/DiffIDs lengths disagree — trusting it anyway risks pulling the wrong layer's content")
		}
	})

	t.Run("no match when no client layer present", func(t *testing.T) {
		cfg := &v1.ConfigFile{
			History: []v1.History{
				{CreatedBy: "pokkum: add /pokkum/init"},
				{CreatedBy: "pokkum: add /app/server"},
			},
			RootFS: v1.RootFS{DiffIDs: make([]v1.Hash, 2)},
		}
		if _, ok := clientLayerIndex(cfg, 2); ok {
			t.Error("expected no match — this generation (e.g. a --strategy=exe build) has no /app/client layer at all")
		}
	})

	t.Run("empty-layer history entries are skipped when counting", func(t *testing.T) {
		cfg := &v1.ConfigFile{
			History: []v1.History{
				{CreatedBy: "some intermediate base layer step", EmptyLayer: true},
				{CreatedBy: "pokkum: add /app/client"},
			},
			RootFS: v1.RootFS{DiffIDs: make([]v1.Hash, 1)},
		}
		idx, ok := clientLayerIndex(cfg, 1)
		if !ok || idx != 0 {
			t.Errorf("clientLayerIndex = %d, %v, want 0, true", idx, ok)
		}
	})
}
