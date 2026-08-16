package packager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// digest from the staged host directories the same way the packager does: walk
// each /app tree, hash every regular file's content, key by its path relative
// to /app, and aggregate via attestutils.RootDigest. It is the test's
// independent oracle for what Packager.Build should have stamped.
func recomputeLayeredDigest(t *testing.T, req ports.PackageRequest) string {
	t.Helper()
	roots := []struct{ host, prefix string }{
		{req.AppServerDir, ports.AppServerDirPrefix},
		{req.AppClientDir, ports.AppClientDirPrefix},
		{req.AppPrerenderedDir, ports.AppPrerenderedDirPrefix},
		{req.AppVendorDir, ports.AppVendorDirPrefix},
		{req.AppNativeDir, ports.AppNativeDirPrefix},
	}
	var recs []attestutils.Record
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
			recs = append(recs, attestutils.Record{
				Rel: filepath.ToSlash(filepath.Join(baseRel, sub)),
				SHA: sha256Hex(data),
			})
			return nil
		})
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
