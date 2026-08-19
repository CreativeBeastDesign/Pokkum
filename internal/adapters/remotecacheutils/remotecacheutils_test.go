package remotecacheutils_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Port interface conformance assertions for mock implementations.
var (
	_ ports.KeylessVerifier = (*mockKeylessVerifier)(nil)
)

func TestComputeSourceTreeHash(t *testing.T) {
	tmpDir := t.TempDir()

	srcDir := filepath.Join(tmpDir, "src")
	_ = os.MkdirAll(srcDir, 0o755)
	_ = os.WriteFile(filepath.Join(srcDir, "app.js"), []byte("console.log('hi');"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(`{"name":"test"}`), 0o644)

	// Create ignored directory
	nodeModules := filepath.Join(tmpDir, "node_modules", "some-pkg")
	_ = os.MkdirAll(nodeModules, 0o755)
	_ = os.WriteFile(filepath.Join(nodeModules, "index.js"), []byte("ignored"), 0o644)

	// Compute initial hash
	h1, err := remotecacheutils.ComputeSourceTreeHash(tmpDir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash failed: %v", err)
	}
	if h1 == "" {
		t.Fatalf("expected non-empty hash")
	}

	// Re-compute without changes -> must match
	h2, err := remotecacheutils.ComputeSourceTreeHash(tmpDir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash 2 failed: %v", err)
	}
	if h1 != h2 {
		t.Errorf("expected deterministic hash, got %s != %s", h1, h2)
	}

	// Modify node_modules -> hash should NOT change
	_ = os.WriteFile(filepath.Join(nodeModules, "index.js"), []byte("modified-ignored"), 0o644)
	h3, _ := remotecacheutils.ComputeSourceTreeHash(tmpDir)
	if h1 != h3 {
		t.Errorf("expected node_modules changes to be ignored, got %s != %s", h1, h3)
	}

	// Modify src file -> hash MUST change
	_ = os.WriteFile(filepath.Join(srcDir, "app.js"), []byte("console.log('updated');"), 0o644)
	h4, _ := remotecacheutils.ComputeSourceTreeHash(tmpDir)
	if h1 == h4 {
		t.Errorf("expected hash to change when src/app.js changes")
	}
}

// TestComputeSourceTreeHash_FramingCannotBeForged reproduces the exact
// second-preimage collision the pre-fix raw-concatenation framing was
// vulnerable to: with "path\x00content" written directly into the hash for
// each sorted file, a two-file tree {aaa.js: "BENIGN", bbb.js: "SECOND"}
// hashed to the identical byte stream as a one-file tree whose single file's
// content is crafted to contain "bbb.js\x00SECOND" — content that mimics a
// second file's own path-plus-NUL-plus-content boundary. Framing each file's
// contribution with a fixed-length (64 hex char, NUL-free) content digest
// instead of raw content removes that ambiguity, so these two genuinely
// different trees must now hash differently.
func TestComputeSourceTreeHash_FramingCannotBeForged(t *testing.T) {
	treeA := t.TempDir()
	if err := os.WriteFile(filepath.Join(treeA, "aaa.js"), []byte("BENIGN"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(treeA, "bbb.js"), []byte("SECOND"), 0o644); err != nil {
		t.Fatal(err)
	}

	treeB := t.TempDir()
	forged := "BENIGN" + "bbb.js" + "\x00" + "SECOND"
	if err := os.WriteFile(filepath.Join(treeB, "aaa.js"), []byte(forged), 0o644); err != nil {
		t.Fatal(err)
	}

	hashA, err := remotecacheutils.ComputeSourceTreeHash(treeA)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash(treeA): %v", err)
	}
	hashB, err := remotecacheutils.ComputeSourceTreeHash(treeB)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash(treeB): %v", err)
	}

	if hashA == hashB {
		t.Fatalf("BUG: genuinely different trees (2 files vs. 1 crafted file) collided on the same hash %s — the framing is forgeable again", hashA)
	}
}

// TestComputeSourceTreeHash_ExecBitAndSymlinkAreCaptured proves two of the
// framing fix's secondary properties: a file's owner-executable bit is part
// of its identity (chmod +x must change the hash even though the bytes
// don't), and a symlink is hashed by its target string, not dereferenced
// (repointing a symlink must change the hash without touching the file it
// used to point at).
func TestComputeSourceTreeHash_ExecBitAndSymlinkAreCaptured(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash before chmod: %v", err)
	}
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatal(err)
	}
	afterChmod, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash after chmod: %v", err)
	}
	if before == afterChmod {
		t.Errorf("expected hash to change when run.sh's executable bit is set")
	}

	targetA := filepath.Join(dir, "target-a.txt")
	targetB := filepath.Join(dir, "target-b.txt")
	if err := os.WriteFile(targetA, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetB, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link.txt")
	if err := os.Symlink(targetA, linkPath); err != nil {
		t.Fatal(err)
	}
	beforeRelink, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash with symlink: %v", err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetB, linkPath); err != nil {
		t.Fatal(err)
	}
	afterRelink, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash after relink: %v", err)
	}
	if beforeRelink == afterRelink {
		t.Errorf("expected hash to change when the symlink is repointed, even though both targets have byte-identical content")
	}
}

func TestComputeInputHash(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(`{"name":"test"}`), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "bun.lock"), []byte(`lockfile-contents`), 0o644)

	params1 := remotecacheutils.InputParams{
		ProjectDir:      tmpDir,
		BaseImageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		BunVersion:      "1.2.2",
		BunVariant:      "standard",
		Platforms:       []string{"linux/amd64", "linux/arm64"},
		Strategy:        "layered",
		Compression:     "gzip",
	}

	hash1, err := remotecacheutils.ComputeInputHash(params1)
	if err != nil {
		t.Fatalf("ComputeInputHash failed: %v", err)
	}
	if len(hash1) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %d chars: %s", len(hash1), hash1)
	}

	// Verify CacheTag
	tag := remotecacheutils.CacheTag(hash1)
	if tag != "cache-"+hash1 {
		t.Errorf("CacheTag mismatch: %s", tag)
	}

	// Changing base image changes the input hash
	params2 := params1
	params2.BaseImageDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	hash2, _ := remotecacheutils.ComputeInputHash(params2)
	if hash1 == hash2 {
		t.Errorf("expected hash to change on base image change")
	}

	// Platform order independence
	params3 := params1
	params3.Platforms = []string{"linux/arm64", "linux/amd64"} // reversed
	hash3, _ := remotecacheutils.ComputeInputHash(params3)
	if hash1 != hash3 {
		t.Errorf("expected hash to be independent of platform order")
	}
}

// TestComputeInputHash_CoversPreviouslyMissingFields proves the cache-key
// completeness fix: two builds that differ ONLY in a field that used to be
// absent from InputParams (runtime port, signing-adjacent build config,
// telemetry, SBOM attach settings) must never collide on the same hash — a
// collision here is exactly what let one build's cached image get silently
// promoted to a different build's release tags.
// TestCacher_ComputeInputHash_AppRuntimeFlowsThroughPort proves the port
// request's AppRuntime field is actually wired into the hash by the Cacher
// adapter — a field existing on ports.RemoteCacheInputRequest is not
// evidence its value reaches InputParams (mem:self_review_checklist row 16),
// and this exact wiring is what keeps a bun-built cache entry from
// satisfying a node-requested build.
func TestCacher_ComputeInputHash_AppRuntimeFlowsThroughPort(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(`{"name":"test"}`), 0o644)

	c := remotecacheutils.New()
	req := ports.RemoteCacheInputRequest{
		ProjectDir:      tmpDir,
		BaseImageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Strategy:        "layered",
		AppRuntime:      "bun",
	}
	bunHash, err := c.ComputeInputHash(context.Background(), req)
	if err != nil {
		t.Fatalf("ComputeInputHash(bun): %v", err)
	}
	req.AppRuntime = "node"
	nodeHash, err := c.ComputeInputHash(context.Background(), req)
	if err != nil {
		t.Fatalf("ComputeInputHash(node): %v", err)
	}
	if bunHash == nodeHash {
		t.Fatalf("bun and node builds collided on the same cache hash %s — the runtime dimension is not keyed", bunHash)
	}
}

// TestComputeInputHash_AssetOverlaySourceDigestsOrderIndependent guards the
// slices.Sort call ComputeInputHash applies to AssetOverlaySourceDigests:
// two requests naming the identical resolved predecessor set in a different
// order (order depends only on registry response timing/chain-walk order,
// not on anything meaningful) must hash identically, or a cache tag would
// churn on every build purely from harmless non-determinism in the order
// digests were resolved.
func TestComputeInputHash_AssetOverlaySourceDigestsOrderIndependent(t *testing.T) {
	base := remotecacheutils.InputParams{
		BaseImageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Strategy:        "layered",
		AssetOverlaySourceDigests: []string{
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	reversed := base
	reversed.AssetOverlaySourceDigests = []string{
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	h1, err := remotecacheutils.ComputeInputHash(base)
	if err != nil {
		t.Fatalf("ComputeInputHash(base): %v", err)
	}
	h2, err := remotecacheutils.ComputeInputHash(reversed)
	if err != nil {
		t.Fatalf("ComputeInputHash(reversed): %v", err)
	}
	if h1 != h2 {
		t.Errorf("hash differs by AssetOverlaySourceDigests order alone: %s vs %s", h1, h2)
	}
}

func TestComputeInputHash_CoversPreviouslyMissingFields(t *testing.T) {
	base := remotecacheutils.InputParams{
		BaseImageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		BunVersion:      "1.2.2",
		BunVariant:      "standard",
		Platforms:       []string{"linux/amd64"},
		Strategy:        "layered",
		Compression:     "gzip",
	}
	baseHash, err := remotecacheutils.ComputeInputHash(base)
	if err != nil {
		t.Fatalf("ComputeInputHash(base): %v", err)
	}

	cases := []struct {
		name   string
		modify func(p *remotecacheutils.InputParams)
	}{
		// The --runtime dimension: a bun-built and a node-built image of
		// identical source are different images (runtime layer, entrypoint),
		// so a hash collision between them would let one silently serve a
		// cache hit for the other — the exact bug class this test exists for.
		{"app runtime", func(p *remotecacheutils.InputParams) { p.AppRuntime = "node" }},
		{"runtime port", func(p *remotecacheutils.InputParams) { p.Runtime.Port = 8080 }},
		{"runtime user", func(p *remotecacheutils.InputParams) { p.Runtime.User = "1000" }},
		{"runtime env", func(p *remotecacheutils.InputParams) { p.Runtime.Env = map[string]string{"FOO": "bar"} }},
		{"runtime require-env", func(p *remotecacheutils.InputParams) { p.Runtime.RequireEnv = []string{"DATABASE_URL"} }},
		{"telemetry enabled", func(p *remotecacheutils.InputParams) { p.Telemetry.Enabled = true }},
		{"telemetry endpoint", func(p *remotecacheutils.InputParams) { p.Telemetry.TracesEndpoint = "http://collector:4318" }},
		{"no-minify", func(p *remotecacheutils.InputParams) { p.NoMinify = true }},
		{"no-inject", func(p *remotecacheutils.InputParams) { p.NoInject = true }},
		{"min bun version", func(p *remotecacheutils.InputParams) { p.MinBunVersion = "1.3.0" }},
		{"compile env", func(p *remotecacheutils.InputParams) { p.CompileEnv = []string{"NODE_ENV=production"} }},
		{"hermetic", func(p *remotecacheutils.InputParams) { p.Hermetic = true }},
		{"source date epoch", func(p *remotecacheutils.InputParams) { p.SourceDateEpochUnix = 1700000000 }},
		{"labels", func(p *remotecacheutils.InputParams) { p.Labels = map[string]string{"org.example": "v2"} }},
		{"annotations", func(p *remotecacheutils.InputParams) { p.Annotations = map[string]string{"pokkum.dev/note": "x"} }},
		{"sbom format", func(p *remotecacheutils.InputParams) { p.SBOMFormat = "cyclonedx-json" }},
		{"sbom attach mode", func(p *remotecacheutils.InputParams) { p.SBOMAttachMode = "tag" }},
		{"sbom no-attach", func(p *remotecacheutils.InputParams) { p.SBOMNoAttach = true }},
		{"bun custom binary", func(p *remotecacheutils.InputParams) { p.BunCustomBinaryPath = "/opt/bun/bin/bun" }},
		{"stub launcher", func(p *remotecacheutils.InputParams) { p.StubLauncher = true }},
		{"asset overlay source digests", func(p *remotecacheutils.InputParams) {
			p.AssetOverlaySourceDigests = []string{"sha256:2222222222222222222222222222222222222222222222222222222222222222"}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.modify(&p)
			h, err := remotecacheutils.ComputeInputHash(p)
			if err != nil {
				t.Fatalf("ComputeInputHash: %v", err)
			}
			if h == baseHash {
				t.Fatalf("BUG: changing %s did not change the input hash — two builds differing only in this field would collide on the same cache tag", tc.name)
			}
		})
	}
}

// TestCacher_ComputeInputHash_DoesNotMutateCallerRuntime guards against a
// subtle aliasing bug at the ports.RemoteCacher boundary: the underlying
// ComputeInputHash(InputParams) sorts some slice fields in place for
// order-independence, and req.Runtime (a plain struct, copied by value) still
// shares its slice fields' backing arrays with whatever the caller — the
// build pipeline — passed in. Cacher.ComputeInputHash must clone those slices
// before sorting, or a hash computation would have the side effect of
// silently reordering state the pipeline goes on to use later in the same
// build (e.g. RuntimeConfig.RequireEnv baked into the image config).
func TestCacher_ComputeInputHash_DoesNotMutateCallerRuntime(t *testing.T) {
	requireEnv := []string{"ZEBRA", "ALPHA", "MIKE"}
	original := append([]string(nil), requireEnv...)

	c := remotecacheutils.New()
	req := ports.RemoteCacheInputRequest{
		BaseImageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Runtime:         ports.RuntimeConfig{RequireEnv: requireEnv},
	}
	if _, err := c.ComputeInputHash(context.Background(), req); err != nil {
		t.Fatalf("ComputeInputHash: %v", err)
	}

	for i, v := range requireEnv {
		if v != original[i] {
			t.Fatalf("BUG: ComputeInputHash mutated the caller's RequireEnv slice in place: got %v, want %v", requireEnv, original)
		}
	}
}

// tagDenyingRegistry wraps an in-memory OCI registry and rejects any tag PUT
// whose path contains one of deniedTags, while still serving every other
// request normally. It simulates a real, common permission split: a CI
// credential that can pull/read a repo's cache-* entries but cannot push its
// release tags — a narrower, less-trusted credential than release authority.
type tagDenyingRegistry struct {
	inner      http.Handler
	deniedTags []string
}

func (r *tagDenyingRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPut && strings.Contains(req.URL.Path, "/manifests/") {
		for _, tag := range r.deniedTags {
			if strings.HasSuffix(req.URL.Path, "/manifests/"+tag) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"push access denied for this tag"}]}`))
				return
			}
		}
	}
	r.inner.ServeHTTP(w, req)
}

// TestCacher_Check_ReconcileTagsFailureIsNotReportedAsHit proves the F7 fix:
// previously, a ReconcileTags failure on a cache hit was silently discarded
// (`_ = ReconcileTags(...)`), so Check would report Hit: true — telling the
// build pipeline the requested release tags now point at the cache-hit
// digest, when in fact they were never moved at all. That combination (build
// reported successful, tags silently stale or missing) is exactly the kind
// of fail-open this audit exists to catch. This test populates a real cache
// entry, then denies the PUT for the release tag specifically, and asserts
// Check now reports Hit: false with a non-nil error — which makes the build
// pipeline fall through to a real build+publish instead of a false success.
func TestCacher_Check_ReconcileTagsFailureIsNotReportedAsHit(t *testing.T) {
	reg := &tagDenyingRegistry{inner: registry.New(), deniedTags: []string{"v1.0.0"}}
	s := httptest.NewServer(reg)
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	inputHash := strings.Repeat("a", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	c := remotecacheutils.New()
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
	})

	if err == nil {
		t.Fatal("expected a non-nil error when the release tag PUT is denied")
	}
	if res.Hit {
		t.Fatalf("BUG: Check reported Hit: true despite ReconcileTags failing — the release tag %q was never actually moved to the cache-hit digest, but the caller was told the build succeeded", "v1.0.0")
	}
}

func generateTestKey(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privDER})

	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return privPEM, pubPEM
}

func pushTestCosignSignature(t *testing.T, repo string, digest v1.Hash, privPEM []byte, corrupt bool) {
	t.Helper()

	signer := cosign.NewSigner(nil)
	bundle, err := signer.Sign(context.Background(), ports.CosignSignRequest{
		Repo:   repo,
		Digest: digest,
		KeyPEM: privPEM,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	b64Sig := bundle.Base64Signature
	if corrupt {
		raw, err := base64.StdEncoding.DecodeString(b64Sig)
		if err != nil {
			t.Fatalf("decode signature: %v", err)
		}
		raw[0] ^= 0xFF
		b64Sig = base64.StdEncoding.EncodeToString(raw)
	}

	layer := static.NewLayer(bundle.PayloadBytes, "application/vnd.dev.cosign.simplesigning.v1+json")
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:       layer,
		Annotations: map[string]string{"dev.cosignproject.cosign/signature": b64Sig},
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}

	sigTagStr := repo + ":" + digest.Algorithm + "-" + digest.Hex + ".sig"
	tag, err := name.NewTag(sigTagStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", sigTagStr, err)
	}
	if err := remote.Write(tag, sigImg); err != nil {
		t.Fatalf("Write signature: %v", err)
	}
}

// TestCacher_Check_VerifiedCacheHit_StaticKey verifies that a candidate cache-hit image
// with a valid Cosign signature is verified and promotes release tags.
func TestCacher_Check_VerifiedCacheHit_StaticKey(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	privPEM, pubPEM := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("b", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// Push genuine Cosign signature
	pushTestCosignSignature(t, repo, digest, privPEM, false)

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
		},
	})
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !res.Hit {
		t.Fatalf("expected Hit: true for verified cache candidate")
	}
	if !res.Verified {
		t.Fatalf("expected Verified: true")
	}
	if res.SignerIdentity != "static-key" {
		t.Errorf("expected SignerIdentity 'static-key', got %q", res.SignerIdentity)
	}

	// Check release tag was created and points to digest
	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	relDesc, err := remote.Get(relTag)
	if err != nil {
		t.Fatalf("remote.Get release tag: %v", err)
	}
	if relDesc.Digest != digest {
		t.Errorf("release tag digest %s != expected %s", relDesc.Digest, digest)
	}
}

// TestCacher_Check_NoKeyConfigured_FailsClosed proves that a genuinely,
// validly signed cache candidate is refused — not silently promoted as an
// unverified hit — when static-key verification is explicitly requested but
// no public key is configured anywhere (Verify.PublicKeyPEM,
// POKKUM_CACHE_PUBKEY, POKKUM_SIGNING_PUBKEY, POKKUM_BASE_IMAGE_PUBKEY all
// unset).
//
// This is the regression guard for the deleted cosign.DefaultPublicKeyPEM
// fallback (docs/archive/Roadmap.md item 2h): a shared, unattributed placeholder public
// key used to stand in here so verification always had *something* to check
// against, but nothing ever signed with its (nonexistent) private half. The
// setup is otherwise identical to TestCacher_Check_VerifiedCacheHit_StaticKey
// (a real key pair, a real signature pushed to a real in-process registry)
// so the only variable under test is whether a key was configured.
func TestCacher_Check_NoKeyConfigured_FailsClosed(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app-nokey"

	privPEM, _ := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("d", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// Push a genuine Cosign signature over the real candidate.
	pushTestCosignSignature(t, repo, digest, privPEM, false)

	// Deliberately NOT setting Verify.PublicKeyPEM and clearing every env
	// var this path checks — the "no key configured anywhere" case under
	// test, isolated from whatever sibling tests in this file may have set.
	t.Setenv("POKKUM_CACHE_PUBKEY", "")
	t.Setenv("POKKUM_SIGNING_PUBKEY", "")
	t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", "")

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
		},
	})
	if err != nil {
		t.Fatalf("expected nil error on default non-strict check (fail closed via cache-miss, not a hard error), got: %v", err)
	}
	if res.Hit {
		t.Fatal("BUG: Check reported Hit: true for a candidate verified against no configured key — an unowned trust anchor accepted a signature nothing configured it to check")
	}

	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(relTag); err == nil {
		t.Fatal("BUG: release tag was promoted despite no verification key being configured")
	}

	// Strict mode must surface this as an explicit, actionable error naming
	// the missing configuration, distinct from a signature that was checked
	// and found invalid.
	_, strictErr := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			Strict:          true,
		},
	})
	if strictErr == nil {
		t.Fatal("expected a non-nil error in strict mode when no verification key is configured")
	}
	if !strings.Contains(strictErr.Error(), "no key is configured") {
		t.Errorf("error should distinguish 'no key configured' from a signature that was checked and found invalid, got: %v", strictErr)
	}
}

// TestCacher_Check_SigningPublicKeyIsTheLastResortInTheKeyChain proves the
// bottom of the static-key verification chain end-to-end, against a real
// in-process registry and a real Cosign signature: with no
// Verify.PublicKeyPEM and every POKKUM_*_PUBKEY unset, a build's own signing
// public key (Verify.SigningPublicKeyPEM) is used, so a builder configured
// with --signing-key alone can verify and reuse the cache entries it signed
// itself.
//
// The two negative cases are the reason this field exists separately from
// PublicKeyPEM rather than being written over it upstream: an explicitly
// configured cache-verify key must always win, and the "nothing configured
// anywhere" case must keep failing closed exactly as it did before.
func TestCacher_Check_SigningPublicKeyIsTheLastResortInTheKeyChain(t *testing.T) {
	// Two independent key pairs: A signs the cache entry, B stands in for an
	// operator who deliberately configured a DIFFERENT cache-verification
	// key. Two differing keys (not one) is what makes "explicit wins"
	// falsifiable — with a single key every precedence order passes.
	privA, pubA := generateTestKey(t)
	_, pubB := generateTestKey(t)

	// One env-var key too, so the "explicit sources win" claim is tested at
	// the env layer and not only at the struct-field layer.
	_, pubEnv := generateTestKey(t)

	newCandidate := func(t *testing.T, repoSuffix string) (repo, inputHash string) {
		t.Helper()
		s := httptest.NewServer(registry.New())
		t.Cleanup(s.Close)

		host := strings.TrimPrefix(s.URL, "http://")
		repo = host + "/acme/" + repoSuffix

		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatalf("random.Image: %v", err)
		}
		digest, err := img.Digest()
		if err != nil {
			t.Fatalf("img.Digest: %v", err)
		}

		inputHash = strings.Repeat("e", 64)
		cacheRef, err := name.NewTag(repo+":"+remotecacheutils.CacheTag(inputHash), name.WeakValidation)
		if err != nil {
			t.Fatalf("NewTag(cache tag): %v", err)
		}
		if err := remote.Write(cacheRef, img); err != nil {
			t.Fatalf("push cache entry: %v", err)
		}
		// Signed by key A — the builder's own signing key.
		pushTestCosignSignature(t, repo, digest, privA, false)
		return repo, inputHash
	}

	// check returns the result plus everything the cacher logged, so the
	// derived-key disclosure can be asserted rather than assumed: an
	// implicitly derived trust anchor that is never announced is exactly the
	// silent-substitution shape mem:self_review_checklist rows 38/41 exist to
	// catch.
	check := func(t *testing.T, repo, inputHash string, verify ports.RemoteCacheVerifyOptions) (ports.RemoteCacheResult, string) {
		t.Helper()
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
		c := remotecacheutils.New(
			remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)),
			remotecacheutils.WithLogger(logger),
		)
		res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
			Repo:      repo,
			InputHash: inputHash,
			Tags:      []string{"v1.0.0"},
			Verify:    verify,
		})
		if err != nil {
			t.Fatalf("Check returned a hard error on a non-strict check: %v", err)
		}
		return res, logs.String()
	}

	t.Run("used when nothing else in the chain is configured", func(t *testing.T) {
		t.Setenv("POKKUM_CACHE_PUBKEY", "")
		t.Setenv("POKKUM_SIGNING_PUBKEY", "")
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", "")
		repo, inputHash := newCandidate(t, "app-signingkey-fallback")

		res, logs := check(t, repo, inputHash, ports.RemoteCacheVerifyOptions{
			VerifySignature:     true,
			VerifyMode:          ports.CacheVerifyStaticKey,
			SigningPublicKeyPEM: pubA,
		})
		if !res.Hit || !res.Verified {
			t.Fatalf("Check = {Hit:%v Verified:%v}, want a verified hit — with no cache-verify key configured, the build's own signing public key must be used so it can verify the entries it signed itself", res.Hit, res.Verified)
		}
		if res.SignerIdentity != "static-key" {
			t.Errorf("SignerIdentity = %q, want %q", res.SignerIdentity, "static-key")
		}
		if !strings.Contains(logs, "signing key's public half") {
			t.Errorf("engaging the derived-key fallback logged nothing naming it; an implicit trust derivation must be announced, not silent. logs:\n%s", logs)
		}
	})

	t.Run("an explicit Verify.PublicKeyPEM is never overridden by it", func(t *testing.T) {
		t.Setenv("POKKUM_CACHE_PUBKEY", "")
		t.Setenv("POKKUM_SIGNING_PUBKEY", "")
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", "")
		repo, inputHash := newCandidate(t, "app-explicit-wins")

		// The entry is signed by A, but the operator explicitly configured B.
		// A hit here would prove the derived key silently replaced their
		// choice.
		res, logs := check(t, repo, inputHash, ports.RemoteCacheVerifyOptions{
			VerifySignature:     true,
			VerifyMode:          ports.CacheVerifyStaticKey,
			PublicKeyPEM:        pubB,
			SigningPublicKeyPEM: pubA,
		})
		if strings.Contains(logs, "signing key's public half") {
			t.Errorf("the derived-key fallback announced itself even though an explicit key was configured; it must not have been consulted at all. logs:\n%s", logs)
		}
		if res.Hit {
			t.Fatal("BUG: an explicitly configured --cache-verify-key was superseded by the signing key's public half; an explicit trust choice must always win over a derived one")
		}
	})

	t.Run("an explicit POKKUM_CACHE_PUBKEY is never overridden by it", func(t *testing.T) {
		t.Setenv("POKKUM_CACHE_PUBKEY", string(pubEnv))
		t.Setenv("POKKUM_SIGNING_PUBKEY", "")
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", "")
		repo, inputHash := newCandidate(t, "app-env-wins")

		res, _ := check(t, repo, inputHash, ports.RemoteCacheVerifyOptions{
			VerifySignature:     true,
			VerifyMode:          ports.CacheVerifyStaticKey,
			SigningPublicKeyPEM: pubA,
		})
		if res.Hit {
			t.Fatal("BUG: POKKUM_CACHE_PUBKEY sits ABOVE the derived signing key in the chain, but the candidate verified against the derived key instead")
		}
	})

	t.Run("a signing key belonging to someone else still fails closed", func(t *testing.T) {
		t.Setenv("POKKUM_CACHE_PUBKEY", "")
		t.Setenv("POKKUM_SIGNING_PUBKEY", "")
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", "")
		repo, inputHash := newCandidate(t, "app-foreign-signer")

		// The fallback narrows trust to "entries I signed myself": an entry
		// signed by A is not accepted by a builder whose own signing key is B.
		res, _ := check(t, repo, inputHash, ports.RemoteCacheVerifyOptions{
			VerifySignature:     true,
			VerifyMode:          ports.CacheVerifyStaticKey,
			SigningPublicKeyPEM: pubB,
		})
		if res.Hit {
			t.Fatal("BUG: a cache entry signed by a key other than this builder's own signing key was accepted — the fallback must narrow trust to self-signed entries, never widen it")
		}
	})
}

// TestCacher_Check_PoisonedCacheEntry_UnsignedRejected verifies that an unsigned cache candidate
// is rejected when verification is active, and release tags are NOT promoted.
func TestCacher_Check_PoisonedCacheEntry_UnsignedRejected(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	_, pubPEM := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}

	inputHash := strings.Repeat("c", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	// Poisoned / unverified cache tag pushed without signature
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
		},
	})
	if err != nil {
		t.Fatalf("expected nil error (fallback to rebuild), got: %v", err)
	}
	if res.Hit {
		t.Fatalf("BUG: Check reported Hit: true on an unsigned cache entry — cache poisoning vulnerability open")
	}

	// Verify release tag was NEVER promoted
	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(relTag); err == nil {
		t.Fatalf("BUG: release tag v1.0.0 was promoted to unverified cache digest!")
	}
}

// TestCacher_Check_PoisonedCacheEntry_InvalidSignatureRejected verifies that a candidate
// with an invalid/tampered signature is rejected and tags are not promoted.
func TestCacher_Check_PoisonedCacheEntry_InvalidSignatureRejected(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	privPEM, pubPEM := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("d", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// Push corrupted / tampered signature
	pushTestCosignSignature(t, repo, digest, privPEM, true)

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
		},
	})
	if err != nil {
		t.Fatalf("expected nil error on default non-strict check, got: %v", err)
	}
	if res.Hit {
		t.Fatalf("BUG: Check reported Hit: true on corrupted signature candidate")
	}

	// Verify release tag was NEVER promoted
	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(relTag); err == nil {
		t.Fatalf("BUG: release tag was promoted on corrupted signature!")
	}
}

// TestCacher_Check_StrictEnforcementFailsOnError verifies that strict mode returns an error
// when a candidate fails signature verification.
func TestCacher_Check_StrictEnforcementFailsOnError(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	privPEM, pubPEM := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("e", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// Push corrupted signature
	pushTestCosignSignature(t, repo, digest, privPEM, true)

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
			Strict:          true,
		},
	})
	if err == nil {
		t.Fatalf("expected non-nil error in strict mode")
	}
	if res.Hit {
		t.Fatalf("expected Hit: false in strict mode error")
	}
}

// TestCacher_Check_OptOutNoVerifyStillHits verifies that when verification is explicitly disabled
// via CacheVerifyNone, an unsigned cache hit still works.
func TestCacher_Check_OptOutNoVerifyStillHits(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("f", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	c := remotecacheutils.New()
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifyMode: ports.CacheVerifyNone,
		},
	})
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !res.Hit {
		t.Fatalf("expected Hit: true when verification is disabled")
	}

	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	relDesc, err := remote.Get(relTag)
	if err != nil {
		t.Fatalf("remote.Get release tag: %v", err)
	}
	if relDesc.Digest != digest {
		t.Errorf("release tag digest %s != expected %s", relDesc.Digest, digest)
	}
}

// TestCacher_Check_UnrecognizedVerifyModeFailsClosed proves the mode switch's
// new `default` arm inside verifyCandidate. The switch just above it
// normalizes an empty/"auto" VerifyMode to CacheVerifyKeyless/StaticKey, so
// the only way to reach default is an explicit, unhandled mode value
// surviving alongside VerifySignature: true — e.g. ports.CacheVerifyNone set
// without also disabling VerifySignature, the exact inconsistent combination
// "--cache-verify-mode=none" set without the paired "--no-cache-verify" flag
// produces (cmd/pokkum/build.go only keeps those two fields consistent when
// --no-cache-verify itself is used). Before this fix, that combination hit no
// case, appended no error, and Check silently reported "not verified, no
// error" for a genuinely signed image — i.e. it happened to look identical to
// a correctly-verified hit purely by accident, not because anything actually
// verified it. This test pushes a REAL, validly-signed cache candidate (same
// fixture shape as TestCacher_Check_VerifiedCacheHit_StaticKey) specifically
// so a pre-fix run of this test would have reported Hit: true — proving the
// fix changed real behavior, not just satisfied a narrower assertion.
func TestCacher_Check_UnrecognizedVerifyModeFailsClosed(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	privPEM, pubPEM := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("6", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// A genuine, validly-signed candidate — the signature itself is not what's
	// under test, the unrecognized mode is.
	pushTestCosignSignature(t, repo, digest, privPEM, false)

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))

	// VerifySignature: true (this build wants its cache hits verified) but
	// VerifyMode explicitly carries CacheVerifyNone instead of a real mode —
	// the inconsistent-config shape described above.
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyNone,
			PublicKeyPEM:    pubPEM,
		},
	})
	if err != nil {
		t.Fatalf("expected nil error on default non-strict check, got: %v", err)
	}
	if res.Hit {
		t.Fatalf("BUG: Check reported Hit: true for VerifyMode=CacheVerifyNone alongside VerifySignature=true — an unrecognized/unhandled verify mode must fail closed (reject the candidate), not silently accept it as if verification had actually run")
	}

	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(relTag); err == nil {
		t.Fatalf("BUG: release tag was promoted despite the verify mode never actually being checked")
	}

	// Strict mode must surface this as an explicit, actionable error rather
	// than the same silent-reject shape.
	_, strictErr := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyNone,
			PublicKeyPEM:    pubPEM,
			Strict:          true,
		},
	})
	if strictErr == nil {
		t.Fatal("expected a non-nil error in strict mode for an unrecognized verify mode")
	}
	if !strings.Contains(strictErr.Error(), "not a recognized") {
		t.Errorf("expected the strict-mode error to name the unrecognized verify mode, got: %v", strictErr)
	}
}

type mockKeylessVerifier struct {
	verifyFn func(ctx context.Context, req ports.KeylessVerifyRequest) (ports.KeylessVerifyResult, error)
}

func (m *mockKeylessVerifier) Verify(ctx context.Context, req ports.KeylessVerifyRequest) (ports.KeylessVerifyResult, error) {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, req)
	}
	return ports.KeylessVerifyResult{}, errors.New("unimplemented mock keyless verifier")
}

// TestCacher_Check_ReplayAttack_DifferentRepoOrDigestClaimsRejected tests the scenario
// where an attacker attempts a replay attack by attaching a signature from a different repository
// or a different image digest. Even with a cryptographically valid signature, checkSimpleSigningClaims
// must reject the claim mismatch and refuse to promote release tags.
func TestCacher_Check_ReplayAttack_DifferentRepoOrDigestClaimsRejected(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/legit-app"

	privPEM, pubPEM := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("1", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// 1. Replay attack with different repository claim in payload
	otherRepo := host + "/attacker/malicious"
	signer := cosign.NewSigner(nil)
	bundleOtherRepo, err := signer.Sign(context.Background(), ports.CosignSignRequest{
		Repo:   otherRepo,
		Digest: digest,
		KeyPEM: privPEM,
	})
	if err != nil {
		t.Fatalf("Sign otherRepo: %v", err)
	}

	layer := static.NewLayer(bundleOtherRepo.PayloadBytes, "application/vnd.dev.cosign.simplesigning.v1+json")
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:       layer,
		Annotations: map[string]string{"dev.cosignproject.cosign/signature": bundleOtherRepo.Base64Signature},
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}

	sigTagStr := repo + ":" + digest.Algorithm + "-" + digest.Hex + ".sig"
	sigTag, err := name.NewTag(sigTagStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", sigTagStr, err)
	}
	if err := remote.Write(sigTag, sigImg); err != nil {
		t.Fatalf("Write signature: %v", err)
	}

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
		},
	})
	if err != nil {
		t.Fatalf("expected nil error on default non-strict check, got: %v", err)
	}
	if res.Hit {
		t.Fatalf("BUG: Check reported Hit: true on replay attack with mismatched repository claim!")
	}

	// Verify release tag was NOT created
	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(relTag); err == nil {
		t.Fatalf("BUG: release tag was promoted on repository claim mismatch!")
	}

	// Check that strict mode returns explicit error
	_, strictErr := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
			Strict:          true,
		},
	})
	if strictErr == nil {
		t.Fatalf("expected error in strict mode for repository claim mismatch")
	}
	if !strings.Contains(strictErr.Error(), "payload repo") && !strings.Contains(strictErr.Error(), "invalid payload claims") {
		t.Errorf("expected claim mismatch error message, got: %v", strictErr)
	}
}

// TestCacher_Check_MalformedSignatureLayerPayload_GracefullyRejected tests that
// corrupt or non-JSON payloads inside the signature layer do not cause panics or false hits.
func TestCacher_Check_MalformedSignatureLayerPayload_GracefullyRejected(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	_, pubPEM := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("2", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// Push signature image with malformed (non-JSON) layer content
	garbageLayer := static.NewLayer([]byte("not-json-garbage-bytes"), "application/vnd.dev.cosign.simplesigning.v1+json")
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:       garbageLayer,
		Annotations: map[string]string{"dev.cosignproject.cosign/signature": "QUFBQQ=="},
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}

	sigTagStr := repo + ":" + digest.Algorithm + "-" + digest.Hex + ".sig"
	sigTag, err := name.NewTag(sigTagStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", sigTagStr, err)
	}
	if err := remote.Write(sigTag, sigImg); err != nil {
		t.Fatalf("Write signature: %v", err)
	}

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
		},
	})
	if err != nil {
		t.Fatalf("expected nil error on default non-strict check, got: %v", err)
	}
	if res.Hit {
		t.Fatalf("BUG: Check reported Hit: true on malformed signature payload!")
	}

	// In strict mode, an error must be returned
	_, strictErr := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
			Strict:          true,
		},
	})
	if strictErr == nil {
		t.Fatalf("expected error in strict mode for malformed payload")
	}
}

// TestCacher_Check_KeylessVerification_IdentityMismatchRejected tests keyless
// verification handling, asserting that certificate identity mismatches reject cache hits,
// while matching identities succeed and promote release tags.
func TestCacher_Check_KeylessVerification_IdentityMismatchRejected(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"

	privPEM, _ := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("3", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// Create valid Simple Signing payload
	signer := cosign.NewSigner(nil)
	bundle, err := signer.Sign(context.Background(), ports.CosignSignRequest{
		Repo:   repo,
		Digest: digest,
		KeyPEM: privPEM,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Push signature image with dummy keyless annotations
	layer := static.NewLayer(bundle.PayloadBytes, "application/vnd.dev.cosign.simplesigning.v1+json")
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer: layer,
		Annotations: map[string]string{
			"dev.cosignproject.cosign/signature": bundle.Base64Signature,
			"dev.sigstore.cosign/certificate":    "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
			"dev.sigstore.cosign/bundle":         `{"SignedEntryTimestamp":"test"}`,
		},
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}

	sigTagStr := repo + ":" + digest.Algorithm + "-" + digest.Hex + ".sig"
	sigTag, err := name.NewTag(sigTagStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", sigTagStr, err)
	}
	if err := remote.Write(sigTag, sigImg); err != nil {
		t.Fatalf("Write signature: %v", err)
	}

	// 1. Mock verifier configured to reject due to identity mismatch
	mockKeyless := &mockKeylessVerifier{
		verifyFn: func(ctx context.Context, req ports.KeylessVerifyRequest) (ports.KeylessVerifyResult, error) {
			if req.Identity.SAN != "ci@acme.com" {
				return ports.KeylessVerifyResult{}, fmt.Errorf("certificate SAN %q != expected %q", "attacker@evil.com", req.Identity.SAN)
			}
			return ports.KeylessVerifyResult{
				Issuer: req.Identity.Issuer,
				SAN:    req.Identity.SAN,
			}, nil
		},
	}

	c := remotecacheutils.New(
		remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)),
		remotecacheutils.WithKeylessVerifier(mockKeyless),
	)

	// Mismatched expected identity -> reject
	resMismatch, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyKeyless,
			KeylessIdentity: ports.KeylessIdentity{
				SAN:    "other-developer@acme.com",
				Issuer: "https://token.actions.githubusercontent.com",
			},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error on default non-strict check, got: %v", err)
	}
	if resMismatch.Hit {
		t.Fatalf("BUG: Check reported Hit: true on keyless identity mismatch!")
	}

	// Matching expected identity -> succeed and promote tags
	resMatch, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyKeyless,
			KeylessIdentity: ports.KeylessIdentity{
				SAN:    "ci@acme.com",
				Issuer: "https://token.actions.githubusercontent.com",
			},
		},
	})
	if err != nil {
		t.Fatalf("Check failed on matching identity: %v", err)
	}
	if !resMatch.Hit {
		t.Fatalf("expected Hit: true for matching keyless identity")
	}
	if !resMatch.Verified {
		t.Fatalf("expected Verified: true")
	}
	if resMatch.SignerIdentity != "keyless:ci@acme.com" {
		t.Errorf("expected SignerIdentity 'keyless:ci@acme.com', got %q", resMatch.SignerIdentity)
	}

	// Verify release tag was promoted
	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	relDesc, err := remote.Get(relTag)
	if err != nil {
		t.Fatalf("remote.Get release tag: %v", err)
	}
	if relDesc.Digest != digest {
		t.Errorf("release tag digest %s != expected %s", relDesc.Digest, digest)
	}
}

// TestCacher_Check_KeylessVerification_SuffixRepoClaimRejected proves
// checkSimpleSigningClaims' repo comparison is an exact match, not a suffix
// match. The prior implementation accepted claimRepo whenever it ended with
// "/"+expectedRepo (or vice versa) — so a payload claiming
// "attacker.example/acme/app" was wrongly treated as matching expected repo
// "acme/app". For the keyless path this check is the ONLY thing standing
// between a validly-signed-by-the-trusted-identity payload for a different
// repo and a false accept: sigstore-go's Verify proves identity + payload
// integrity, never which repo the payload claims to describe (see
// verifyCandidate's static-key path, which is separately protected by
// cosign.Signer.Verify's own exact-match check — the keyless path has no
// such second check). The mock keyless verifier here always succeeds, so
// the repo claim is the only thing that can reject this candidate.
func TestCacher_Check_KeylessVerification_SuffixRepoClaimRejected(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/app"
	// Chosen so the OLD buggy check's "claimRepo ends with /+expectedRepo"
	// branch would have accepted it: attackerClaim = "evil/" + repo.
	attackerClaimedRepo := host + "/evil/" + repo

	privPEM, _ := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("5", 64)
	cacheTag := remotecacheutils.CacheTag(inputHash)
	cacheRef, err := name.NewTag(repo+":"+cacheTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}

	// A genuine Simple Signing payload, but signed for attackerClaimedRepo,
	// not repo — simulating a real signature the trusted identity made for
	// some other, differently-named artifact.
	signer := cosign.NewSigner(nil)
	bundle, err := signer.Sign(context.Background(), ports.CosignSignRequest{
		Repo:   attackerClaimedRepo,
		Digest: digest,
		KeyPEM: privPEM,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	layer := static.NewLayer(bundle.PayloadBytes, "application/vnd.dev.cosign.simplesigning.v1+json")
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer: layer,
		Annotations: map[string]string{
			"dev.cosignproject.cosign/signature": bundle.Base64Signature,
			"dev.sigstore.cosign/certificate":    "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
			"dev.sigstore.cosign/bundle":         `{"SignedEntryTimestamp":"test"}`,
		},
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}

	sigTagStr := repo + ":" + digest.Algorithm + "-" + digest.Hex + ".sig"
	sigTag, err := name.NewTag(sigTagStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", sigTagStr, err)
	}
	if err := remote.Write(sigTag, sigImg); err != nil {
		t.Fatalf("Write signature: %v", err)
	}

	// Always-succeeds keyless verifier: identity/crypto is not what's under
	// test here, only the repo claim comparison is.
	alwaysOK := &mockKeylessVerifier{
		verifyFn: func(_ context.Context, req ports.KeylessVerifyRequest) (ports.KeylessVerifyResult, error) {
			return ports.KeylessVerifyResult{Issuer: req.Identity.Issuer, SAN: req.Identity.SAN}, nil
		},
	}

	c := remotecacheutils.New(remotecacheutils.WithKeylessVerifier(alwaysOK))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyKeyless,
			KeylessIdentity: ports.KeylessIdentity{
				SAN:    "ci@acme.com",
				Issuer: "https://token.actions.githubusercontent.com",
			},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error on default non-strict check, got: %v", err)
	}
	if res.Hit {
		t.Fatalf("BUG: Check reported Hit: true for a payload claiming repo %q against expected %q — the suffix-match repo comparison is forgeable", attackerClaimedRepo, repo)
	}

	relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(relTag); err == nil {
		t.Fatalf("BUG: release tag was promoted despite the repo claim mismatch")
	}
}

// TestCacher_Check_NoVerifierInjected_FailsClosed proves a genuinely, validly
// signed cache candidate is refused — not promoted as an unverified hit — when
// the verifier the requested mode needs was never injected.
//
// This is the regression guard for removing the point-of-use defaults. Both
// arms of verifyCandidate's mode switch used to construct their verifier
// themselves (`if signer == nil { signer = cosign.NewSigner(c.log) }`, and the
// same for sigstore.NewVerifier) — the adapter-imports-adapter edges
// internal/architecture_test.go used to allowlist. With those gone, "no
// verifier" is reachable, and continuing past it would promote a cache tag to a
// release tag without anything having verified the bytes: the exact fail-open
// 812662e closed on this switch's default arm.
//
// The setup is otherwise identical to
// TestCacher_Check_VerifiedCacheHit_StaticKey — a real key pair, a real
// signature pushed to a real in-process registry, the correct public key
// supplied — so the only variable is whether a verifier was injected.
func TestCacher_Check_NoVerifierInjected_FailsClosed(t *testing.T) {
	newSignedCandidate := func(t *testing.T, repoName string) (repo, inputHash string, pubPEM []byte, digest v1.Hash) {
		t.Helper()
		s := httptest.NewServer(registry.New())
		t.Cleanup(s.Close)

		host := strings.TrimPrefix(s.URL, "http://")
		repo = host + "/" + repoName

		privPEM, pubPEM := generateTestKey(t)

		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatalf("random.Image: %v", err)
		}
		digest, err = img.Digest()
		if err != nil {
			t.Fatalf("img.Digest: %v", err)
		}

		inputHash = strings.Repeat("c", 64)
		cacheRef, err := name.NewTag(repo+":"+remotecacheutils.CacheTag(inputHash), name.WeakValidation)
		if err != nil {
			t.Fatalf("NewTag(cache tag): %v", err)
		}
		if err := remote.Write(cacheRef, img); err != nil {
			t.Fatalf("push cache entry: %v", err)
		}
		pushTestCosignSignature(t, repo, digest, privPEM, false)
		return repo, inputHash, pubPEM, digest
	}

	assertNoRelease := func(t *testing.T, repo string) {
		t.Helper()
		relTag, err := name.NewTag(repo+":v1.0.0", name.WeakValidation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remote.Get(relTag); err == nil {
			t.Fatal("BUG: release tag was created from an unverified cache candidate")
		}
	}

	t.Run("static-key mode without a CosignSigner", func(t *testing.T) {
		repo, inputHash, pubPEM, _ := newSignedCandidate(t, "acme/no-signer")

		// Deliberately no WithCosignSigner. A correct key IS supplied, so "no
		// key configured" cannot be the reason for the refusal.
		c := remotecacheutils.New(remotecacheutils.WithKeylessVerifier(sigstore.NewVerifier(nil)))
		res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
			Repo:      repo,
			InputHash: inputHash,
			Tags:      []string{"v1.0.0"},
			Verify: ports.RemoteCacheVerifyOptions{
				VerifySignature: true,
				VerifyMode:      ports.CacheVerifyStaticKey,
				PublicKeyPEM:    pubPEM,
			},
		})
		if err != nil {
			t.Fatalf("Check (non-strict) should bypass the cache, not error: %v", err)
		}
		if res.Hit {
			t.Fatal("BUG: reported a cache Hit with no ports.CosignSigner injected — an unverified image would be promoted")
		}
		if res.Verified {
			t.Fatal("BUG: reported Verified with no ports.CosignSigner injected")
		}
		assertNoRelease(t, repo)
	})

	t.Run("static-key mode without a CosignSigner, strict", func(t *testing.T) {
		repo, inputHash, pubPEM, _ := newSignedCandidate(t, "acme/no-signer-strict")

		c := remotecacheutils.New()
		_, err := c.Check(context.Background(), ports.RemoteCacheRequest{
			Repo:      repo,
			InputHash: inputHash,
			Tags:      []string{"v1.0.0"},
			Verify: ports.RemoteCacheVerifyOptions{
				VerifySignature: true,
				VerifyMode:      ports.CacheVerifyStaticKey,
				PublicKeyPEM:    pubPEM,
				Strict:          true,
			},
		})
		if err == nil {
			t.Fatal("BUG: strict Check succeeded with no ports.CosignSigner injected")
		}
		if !strings.Contains(err.Error(), "WithCosignSigner") {
			t.Errorf("error should name the missing injection point, got: %v", err)
		}
		assertNoRelease(t, repo)
	})

	t.Run("keyless mode without a KeylessVerifier, strict", func(t *testing.T) {
		repo, inputHash, _, _ := newSignedCandidate(t, "acme/no-keyless-strict")

		// A full expected identity IS supplied, so "keyless identity is
		// required" cannot be the reason for the refusal.
		c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
		_, err := c.Check(context.Background(), ports.RemoteCacheRequest{
			Repo:      repo,
			InputHash: inputHash,
			Tags:      []string{"v1.0.0"},
			Verify: ports.RemoteCacheVerifyOptions{
				VerifySignature: true,
				VerifyMode:      ports.CacheVerifyKeyless,
				KeylessIdentity: ports.KeylessIdentity{
					Issuer: "https://token.actions.githubusercontent.com",
					SAN:    "https://github.com/my-org/my-app/.github/workflows/release.yml@refs/heads/main",
				},
				Strict: true,
			},
		})
		if err == nil {
			t.Fatal("BUG: strict Check succeeded with no ports.KeylessVerifier injected")
		}
		if !strings.Contains(err.Error(), "WithKeylessVerifier") {
			t.Errorf("error should name the missing injection point, got: %v", err)
		}
		assertNoRelease(t, repo)
	})
}
