package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/dsse"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// generateSigningKeyPair produces a real Ed25519 key pair as PEM, the same
// shapes cmd/pokkum accepts via --signing-key/POKKUM_SIGNING_KEY.
func generateSigningKeyPair(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
}

// signVerifyDeps wires REAL signing adapters (cosign, dsse, slsa) and a REAL
// registry adapter against the in-memory registry harness — the mocks cover
// only the toolchain stages (compile, base image, supervisor) that would
// otherwise need bun and a network.
func signVerifyDeps(t *testing.T, cache ports.RemoteCacher) core.Deps {
	t.Helper()
	return core.Deps{
		Compiler:        &mockCompiler{},
		BaseImages:      &mockBaseResolver{t: t},
		Supervisor:      &mockSupervisorProvider{},
		Packager:        packager.NewPackager(testLogger()),
		Registry:        registry.NewAdapter(testLogger()),
		SBOM:            nil,
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		SLSAGenerator:   slsa.NewGenerator(testLogger()),
		CosignSigner:    cosign.NewSigner(testLogger()),
		DSSESigner:      dsse.NewSigner(testLogger()),
		RemoteCache:     cache,
		Logger:          testLogger(),
		Version:         "0.1.0-sign-roundtrip-test",
	}
}

// fetchRawSignature pulls repo:<alg>-<hex>.sig straight off the registry —
// deliberately NOT through the registry adapter's own FetchSignature, so the
// assertion is independent of the code under test: this is exactly what
// `cosign verify` and the remote cache verifier read.
func fetchRawSignature(t *testing.T, repo string, subject v1.Hash) (payload []byte, b64sig string) {
	t.Helper()
	ref, err := name.ParseReference(repo + ":" + ports.SigTag(subject))
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	img, err := remote.Image(ref)
	if err != nil {
		t.Fatalf("remote.Image(%s): %v — the pushed image carries no signature at the cosign tag location", ref, err)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(m.Layers) != 1 {
		t.Fatalf("signature image layers = %d, want 1", len(m.Layers))
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	rc, err := layers[0].Uncompressed()
	if err != nil {
		t.Fatalf("Uncompressed: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read signature payload: %v", err)
	}
	return data, m.Layers[0].Annotations["dev.cosignproject.cosign/signature"]
}

// TestSignVerifyRoundTrip_PushedImageCarriesVerifiableSignature is the
// integration proof for the original signing bug: a real core.Build push
// with a real Ed25519 key must leave a signature and a DSSE-wrapped SLSA
// attestation in the registry that verify cryptographically — fetched back
// raw, not through the pipeline's own code paths — for the published index
// digest AND every per-platform manifest digest (Roadmap 2d's "attach to
// both" answer).
func TestSignVerifyRoundTrip_PushedImageCarriesVerifiableSignature(t *testing.T) {
	harness := NewRegistryHarness(t)
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	repoName := harness.Repo("signed-app")
	privPEM, pubPEM := generateSigningKeyPair(t)

	deps := signVerifyDeps(t, nil)
	req := core.BuildRequest{
		ProjectDir:      fixtureDir,
		Repo:            repoName,
		Tags:            []string{"v1"},
		Platforms:       []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
		Compile:         core.CompileOptions{Strategy: core.StrategyExe},
		Output:          core.OutputOptions{Mode: core.OutputPush},
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
		Insecure:        true,
		Sign:            true,
		Signing:         core.SigningOptions{KeyPEM: privPEM, PublicKeyPEM: pubPEM},
		SourceDateEpoch: testEpoch,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	if res.Signing == nil || !res.Signing.Signed {
		t.Fatalf("BuildResult.Signing = %+v, want Signed=true", res.Signing)
	}

	// Collect every digest that must carry attachments: the index plus each
	// per-platform child.
	idx := harness.FetchIndex(t, repoName+":v1")
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	subjects := []v1.Hash{res.Image.Digest}
	for _, m := range im.Manifests {
		subjects = append(subjects, m.Digest)
	}
	if len(subjects) != 3 {
		t.Fatalf("subjects = %d, want 3 (index + 2 platforms)", len(subjects))
	}

	verifier := cosign.NewSigner(testLogger())
	dsseVerifier := dsse.NewSigner(testLogger())

	for _, subject := range subjects {
		// Signature: fetched raw, verified against the public key with the
		// REAL cosign verifier — the same call `pokkum verify` and the
		// remote cache verifier make.
		payload, b64sig := fetchRawSignature(t, repoName, subject)
		sigBytes, err := base64.StdEncoding.DecodeString(b64sig)
		if err != nil {
			t.Fatalf("decode signature for %s: %v", subject, err)
		}
		bundle := ports.CosignSignatureBundle{PayloadBytes: payload, SignatureBytes: sigBytes, Base64Signature: b64sig}
		if err := verifier.Verify(context.Background(), bundle, pubPEM, repoName, subject); err != nil {
			t.Errorf("signature for %s does not verify: %v", subject, err)
		}

		// Attestation: fetched raw from the .att tag, DSSE-verified, and its
		// SLSA statement's subject must name this exact digest.
		attRef, err := name.ParseReference(repoName + ":" + ports.AttTag(subject))
		if err != nil {
			t.Fatalf("ParseReference att: %v", err)
		}
		attImg, err := remote.Image(attRef)
		if err != nil {
			t.Fatalf("no attestation at %s: %v", attRef, err)
		}
		layers, err := attImg.Layers()
		if err != nil || len(layers) != 1 {
			t.Fatalf("attestation layers for %s: %v, err=%v", subject, layers, err)
		}
		rc, err := layers[0].Uncompressed()
		if err != nil {
			t.Fatalf("Uncompressed: %v", err)
		}
		envBytes, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read attestation envelope: %v", err)
		}
		var env ports.DSSEEnvelope
		if err := json.Unmarshal(envBytes, &env); err != nil {
			t.Fatalf("attestation for %s is not a DSSE envelope: %v", subject, err)
		}
		stmtBytes, err := dsseVerifier.Verify(context.Background(), env, pubPEM)
		if err != nil {
			t.Errorf("attestation envelope for %s does not verify: %v", subject, err)
			continue
		}
		var stmt ports.SLSAStatement
		if err := json.Unmarshal(stmtBytes, &stmt); err != nil {
			t.Fatalf("attestation payload for %s is not a SLSA statement: %v", subject, err)
		}
		found := false
		for _, s := range stmt.Subject {
			if s.Digest[subject.Algorithm] == subject.Hex {
				found = true
			}
		}
		if !found {
			t.Errorf("SLSA statement fetched from %s's .att does not name %s as a subject", subject, subject)
		}
	}
}

// TestSignVerifyRoundTrip_SignatureAttachFailureFailsBuild: when the .sig
// cannot be attached (here: the registry rejects writes after the image
// push, simulated by pointing signing at a repo the harness will 404 —
// enforced via a registry adapter against a closed server), the build must
// fail rather than report a signed-looking success. Uses the pipeline mocks
// plus a real registry adapter whose attach targets an unreachable host.
func TestSignVerifyRoundTrip_SignatureAttachFailureFailsBuild(t *testing.T) {
	harness := NewRegistryHarness(t)
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	privPEM, pubPEM := generateSigningKeyPair(t)

	deps := signVerifyDeps(t, nil)
	// failingAttachRegistry pushes normally but refuses signature attach —
	// the precise partial-failure shape a flaky registry produces.
	deps.Registry = &failingAttachRegistry{Registry: registry.NewAdapter(testLogger())}

	req := core.BuildRequest{
		ProjectDir:      fixtureDir,
		Repo:            harness.Repo("attach-fails"),
		Tags:            []string{"v1"},
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Compile:         core.CompileOptions{Strategy: core.StrategyExe},
		Output:          core.OutputOptions{Mode: core.OutputPush},
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
		Insecure:        true,
		Sign:            true,
		Signing:         core.SigningOptions{KeyPEM: privPEM, PublicKeyPEM: pubPEM},
		SourceDateEpoch: testEpoch,
	}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err == nil {
		t.Fatalf("core.Build reported success although the signature could not be attached")
	}
}

// failingAttachRegistry delegates everything to the real adapter except
// signature attachment, which always fails.
type failingAttachRegistry struct {
	ports.Registry
}

func (f *failingAttachRegistry) AttachSignature(ctx context.Context, req ports.AttachSignatureRequest) (ports.PublishResult, error) {
	return ports.PublishResult{}, core.ErrSigningFailed
}

// TestSignVerifyRoundTrip_SignedCacheHitVerifies is the R4 proof: the
// remote build cache's cache-hit signature verification — previously a
// guaranteed miss, because nothing ever created the .sig it requires — must
// genuinely succeed against a signed push. Build once with a real key, then
// run an identical build with the real remotecacheutils cacher wired: it
// must come back as a VERIFIED cache hit, exercising the real
// <repo>:<alg>-<hex>.sig fetch and real cosign verification end to end.
func TestSignVerifyRoundTrip_SignedCacheHitVerifies(t *testing.T) {
	harness := NewRegistryHarness(t)
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	repoName := harness.Repo("cached-signed-app")
	privPEM, pubPEM := generateSigningKeyPair(t)

	cacher := remotecacheutils.New(
		remotecacheutils.WithLogger(testLogger()),
		remotecacheutils.WithCosignSigner(cosign.NewSigner(testLogger())),
	)

	newReq := func() core.BuildRequest {
		return core.BuildRequest{
			ProjectDir:      fixtureDir,
			Repo:            repoName,
			Tags:            []string{"release"},
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			Compile:         core.CompileOptions{Strategy: core.StrategyExe},
			Output:          core.OutputOptions{Mode: core.OutputPush},
			SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
			Insecure:        true,
			Sign:            true,
			Signing:         core.SigningOptions{KeyPEM: privPEM, PublicKeyPEM: pubPEM},
			CacheVerify:     core.RemoteCacheVerifyOptions{PublicKeyPEM: pubPEM},
			SourceDateEpoch: testEpoch,
		}
	}

	first, err := core.Build(context.Background(), signVerifyDeps(t, cacher), newReq(), core.BuildOptions{})
	if err != nil {
		t.Fatalf("first core.Build: %v", err)
	}
	if first.Cached {
		t.Fatalf("first build unexpectedly hit the cache")
	}
	if first.Signing == nil || !first.Signing.Signed {
		t.Fatalf("first build was not signed: %+v", first.Signing)
	}

	second, err := core.Build(context.Background(), signVerifyDeps(t, cacher), newReq(), core.BuildOptions{})
	if err != nil {
		t.Fatalf("second core.Build: %v", err)
	}
	if !second.Cached {
		t.Fatalf("second identical build did not hit the remote cache — cache-hit verification failed against a genuinely signed cache entry (R4 regression)")
	}
	if second.Image.Digest != first.Image.Digest {
		t.Errorf("cache hit digest %s != originally pushed digest %s", second.Image.Digest, first.Image.Digest)
	}
}
