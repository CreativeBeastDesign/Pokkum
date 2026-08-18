package packager

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/attestutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// recomputeLayeredDigest independently re-derives the expected attestation
// digest, modeling pokkum-init's own runtime view — a single, layer-squashed
// /app filesystem, not a bag of per-layer records — rather than the
// packager's own bookkeeping. Walks each host root and keys every regular
// file by its path relative to /app into ONE map, so a path contributed by
// two roots (AssetOverlayDir and AppClientDir both target the real /app/client)
// naturally collapses to a single entry, exactly like a real container's
// merged filesystem only ever has one file at a given path — never counting
// its bytes twice, unlike attestutils.RootDigest, which has no dedup of its
// own and just hashes whatever list it's given.
//
// Root order matters and is NOT arbitrary: it mirrors packager.go's real
// addenda append order (server, overlay, client, vendor, native,
// prerendered), the same order OCI layers are applied in — so AppClientDir
// is walked AFTER AssetOverlayDir here, meaning a colliding path's map entry
// ends up holding the CURRENT build's bytes, matching what a later-appended
// layer actually does to an earlier one's file on disk at runtime. Getting
// this order backwards would make the oracle agree with a packager bug that
// let a stale prior generation's bytes win instead of the current build's.
func recomputeLayeredDigest(t *testing.T, req ports.PackageRequest) string {
	t.Helper()
	roots := []struct{ host, prefix string }{
		{req.AppServerDir, ports.AppServerDirPrefix},
		// AssetOverlayDir mirrors to the SAME in-image prefix as AppClientDir
		// (it's a second, separate layer targeting /app/client, not a
		// separate directory) and is walked BEFORE AppClientDir so a
		// colliding path's final map entry holds the current build's bytes.
		{req.AssetOverlayDir, ports.AppClientDirPrefix},
		{req.AppClientDir, ports.AppClientDirPrefix},
		{req.AppVendorDir, ports.AppVendorDirPrefix},
		{req.AppNativeDir, ports.AppNativeDirPrefix},
		{req.AppPrerenderedDir, ports.AppPrerenderedDirPrefix},
	}
	byRel := make(map[string]string) // Rel -> SHA, last root visited wins
	for _, r := range roots {
		if r.host == "" {
			continue
		}
		fi, err := os.Stat(r.host)
		if err != nil || !fi.IsDir() {
			continue // absent root contributes nothing, mirroring the packager
		}
		baseRel := strings.TrimPrefix(r.prefix, "/app/")
		_ = filepath.WalkDir(r.host, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil || !info.Mode().IsRegular() {
				return nil
			}
			sub, rerr := filepath.Rel(r.host, p)
			if rerr != nil {
				return nil
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			rel := filepath.ToSlash(filepath.Join(baseRel, sub))
			byRel[rel] = sha256Hex(data)
			return nil
		})
	}
	recs := make([]attestutils.Record, 0, len(byRel))
	for rel, sha := range byRel {
		recs = append(recs, attestutils.Record{Rel: rel, SHA: sha})
	}
	return attestutils.RootDigest(recs)
}

func TestAttestationEnv_StampedForLayered(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry", "handler.js": "handler"})
	req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "var x=1"})
	req.AppVendorDir = writeStrategyDir(t, map[string]string{"module.js": "export 1"})
	req.AppNativeDir = writeStrategyDir(t, map[string]string{"addon.node": "\x7fELFdata"})
	req.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": "<h1>home</h1>"})
	req.NoPrecompress = true // isolate attestation logic from sidecar generation

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	cfg := configOf(t, img)
	got, ok := envValue(cfg.Config.Env, ports.EnvAttestationDigest)
	if !ok {
		t.Fatalf("layered image is missing %s in config env", ports.EnvAttestationDigest)
	}
	if len(got) != 64 || !isLowerHex(got) {
		t.Fatalf("attestation digest %q is not a 64-hex lowercase SHA-256", got)
	}

	// The stamped digest must exactly match an independent re-derivation from
	// the same staged directories — the packager↔runtime parity guarantee.
	if want := recomputeLayeredDigest(t, req); got != want {
		t.Errorf("stamped attestation digest = %s, want re-derived %s", got, want)
	}
}

// TestAttestationEnv_IncludesAssetOverlayLayer is the regression guard for
// this feature's single most important correctness requirement: overlay
// files added under AppClientDirPrefix MUST be folded into the stamped
// attestation digest (appendAssetOverlayLayer's returned records into
// attestRecords, packager.go's Build), or every --asset-overlay image would
// fail pokkum-init's independent re-derivation at container startup —
// refusing to exec, exit 126 — despite having built and pushed successfully.
// If that fold-in is ever reverted, this test fails by construction: the
// packager's stamped digest would stop matching recomputeLayeredDigest's
// independent oracle (which does include AssetOverlayDir) the moment
// AssetOverlayDir is non-empty.
func TestAttestationEnv_IncludesAssetOverlayLayer(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
	req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "current generation"})
	req.AssetOverlayDir = writeStrategyDir(t, map[string]string{
		"orphaned-abc123.js": "content only a prior generation's browsers still need",
	})
	req.NoPrecompress = true

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	cfg := configOf(t, img)
	got, ok := envValue(cfg.Config.Env, ports.EnvAttestationDigest)
	if !ok {
		t.Fatalf("layered image with an asset overlay is missing %s in config env", ports.EnvAttestationDigest)
	}

	want := recomputeLayeredDigest(t, req)
	if got != want {
		t.Errorf("stamped attestation digest = %s, want %s (re-derived INCLUDING AssetOverlayDir) — if this fails, appendAssetOverlayLayer's records are no longer being folded into attestRecords, and every --asset-overlay image will fail to start", got, want)
	}

	// Same assertion from the other direction: prove the overlay content is
	// what actually changes the digest, so a test that accidentally didn't
	// wire AssetOverlayDir through at all couldn't pass by coincidence.
	reqWithoutOverlay := req
	reqWithoutOverlay.AssetOverlayDir = ""
	digestWithoutOverlay := recomputeLayeredDigest(t, reqWithoutOverlay)
	if digestWithoutOverlay == want {
		t.Fatal("test premise broken: the overlay directory's content did not change the recomputed digest at all")
	}
}

// TestAttestationEnv_AssetOverlayOverlapWithClientDeduped is the regression
// guard for the OTHER real bug this feature's own end-to-end test caught
// (see Lessons.md's 2026-08-18 entry): the asset overlay layer and the
// current build's own client layer both target /app/client, so a path
// present in BOTH — the ordinary case, since content-hashed filenames only
// change when their content does — used to make the packager count that
// file's contribution to the attestation digest TWICE (once per layer's
// records), while pokkum-init's runtime walk of the real, layer-squashed
// /app/client tree sees the file exactly ONCE. That mismatch would fail
// EVERY --asset-overlay image with any unchanged carried-forward asset —
// i.e. nearly all of them — at container startup, not at build time.
//
// This test uses the SAME relative path ("entry.js") in both
// AppClientDir and AssetOverlayDir, with DIFFERENT byte content, to prove
// two things at once: (1) the stamped digest counts that path only once
// (matches recomputeLayeredDigest's own deduped-by-Rel oracle), and (2) the
// CURRENT build's bytes are what's actually merged into the real image at
// that path (packager.go appends the overlay layer BEFORE the client layer,
// so client — the later, "closer to top" layer — physically wins on disk).
func TestAttestationEnv_AssetOverlayOverlapWithClientDeduped(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
	req.AppClientDir = writeStrategyDir(t, map[string]string{"entry.js": "current generation bytes"})
	req.AssetOverlayDir = writeStrategyDir(t, map[string]string{"entry.js": "PRIOR generation bytes — must not win"})
	req.NoPrecompress = true

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	cfg := configOf(t, img)
	got, ok := envValue(cfg.Config.Env, ports.EnvAttestationDigest)
	if !ok {
		t.Fatalf("layered image is missing %s in config env", ports.EnvAttestationDigest)
	}
	if want := recomputeLayeredDigest(t, req); got != want {
		t.Errorf("stamped attestation digest = %s, want %s (deduped-by-Rel oracle) — the overlay/client overlap is not being deduped correctly", got, want)
	}

	// Prove the real merged image actually contains the CURRENT build's
	// bytes at the colliding path, not the stale overlay's — extract every
	// layer and confirm there is exactly one "app/client/entry.js"
	// entry across the whole image with the current generation's content.
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	var found int
	var lastContent string
	for _, l := range layers {
		rc, err := l.Uncompressed()
		if err != nil {
			t.Fatalf("layer.Uncompressed: %v", err)
		}
		tr := tar.NewReader(rc)
		for {
			hdr, terr := tr.Next()
			if terr == io.EOF {
				break
			}
			if terr != nil {
				t.Fatalf("tar.Next: %v", terr)
			}
			if hdr.Name != "app/client/entry.js" || hdr.Typeflag != tar.TypeReg {
				continue
			}
			data, rerr := io.ReadAll(tr)
			if rerr != nil {
				t.Fatalf("read tar entry: %v", rerr)
			}
			found++
			lastContent = string(data)
		}
		rc.Close()
	}
	if lastContent != "current generation bytes" {
		t.Errorf("app/client/entry.js final content = %q, want %q (current build must win over the stale overlay)", lastContent, "current generation bytes")
	}
	_ = found // multiple layers legitimately both carry the tar entry; only the LAST one applied (highest layer index) is authoritative at runtime, which lastContent captures.
}

func TestAttestationEnv_NotStampedForExeOrStatic(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*testing.T, *ports.PackageRequest)
	}{
		{
			name: "exe",
			set: func(t *testing.T, r *ports.PackageRequest) {
				r.Strategy = ports.StrategyExe
			},
		},
		{
			name: "static",
			set: func(t *testing.T, r *ports.PackageRequest) {
				r.Strategy = ports.StrategyStatic
				r.StaticServer = []byte("fake-pokkum-static")
				r.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "client"})
				r.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": "page"})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(t, ports.LinuxAMD64)
			tc.set(t, &req)
			img, err := NewPackager(testLogger()).Build(context.Background(), req)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			cfg := configOf(t, img)
			if val, ok := envValue(cfg.Config.Env, ports.EnvAttestationDigest); ok {
				t.Fatalf("strategy %s unexpectedly stamped %s=%q; attestation applies only to layered", req.Strategy, ports.EnvAttestationDigest, val)
			}
		})
	}
}

// TestBuildDirectoryTreeLayerWithPruning_Records pins the authoritative
// attestation records a tree layer produces: rel paths relative to /app,
// content-bound hashes, and junk-file exclusion under pruning.
func TestBuildDirectoryTreeLayerWithPruning_Records(t *testing.T) {
	dir := writeStrategyDir(t, map[string]string{
		"lib.js":    "module",
		"README.md": "junk doc",  // isDocFile → pruned
		"x.d.ts":    "type junk", // DefaultJunkPatterns → pruned
	})
	layer, pruned, recs, err := BuildDirectoryTreeLayerWithPruning(
		context.Background(), ports.LinuxAMD64, dir, ports.AppVendorDirPrefix, buildEpoch, ports.CompressionGzip, pruneutils.PruneOptions{})
	if err != nil {
		t.Fatalf("build layer with pruning: %v", err)
	}
	if layer == nil {
		t.Fatal("expected a non-nil layer")
	}
	if pruned.FilesPruned != 2 {
		t.Fatalf("FilesPruned = %d, want 2 (README.md + x.d.ts)", pruned.FilesPruned)
	}

	// Only lib.js should survive into the records, keyed relative to /app.
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1 (junk files excluded); got %+v", len(recs), recs)
	}
	if recs[0].Rel != "vendor/lib.js" {
		t.Errorf("record rel = %q, want %q", recs[0].Rel, "vendor/lib.js")
	}
	if recs[0].SHA != sha256Hex([]byte("module")) {
		t.Errorf("record SHA = %q, want sha256 of the file content", recs[0].SHA)
	}
}

func TestBuildDirectoryTreeLayer_RecordsRelToApp(t *testing.T) {
	// A non-vendor tree (no pruning) must produce records keyed relative to /app
	// for every regular file.
	dir := writeStrategyDir(t, map[string]string{
		"index.js": "server entry",
		"app.css":  "body{}",
	})
	_, _, recs, err := BuildDirectoryTreeLayerWithPruning(
		context.Background(), ports.LinuxAMD64, dir, ports.AppServerDirPrefix, buildEpoch, ports.CompressionGzip, pruneutils.PruneOptions{NoPrune: true})
	if err != nil {
		t.Fatalf("build layer: %v", err)
	}
	want := map[string]bool{
		"server/index.js": false,
		"server/app.css":  false,
	}
	if len(recs) != len(want) {
		t.Fatalf("records = %d, want %d; got %+v", len(recs), len(want), recs)
	}
	for _, r := range recs {
		if seen, ok := want[r.Rel]; !ok || seen {
			t.Errorf("unexpected or duplicate record rel %q", r.Rel)
		}
		want[r.Rel] = true
	}
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
