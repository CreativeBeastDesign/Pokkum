package provenance_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/dsse"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/provenance"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func startTestRegistry(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)
	host := strings.TrimPrefix(s.URL, "http://")
	return s, host
}

func createTestImage(t *testing.T, annotations map[string]string) v1.Image {
	t.Helper()
	img := empty.Image
	layer, err := tarball.LayerFromFile(writeTempTar(t, "app/package.json", []byte(`{"name":"my-app"}`)))
	if err != nil {
		t.Fatalf("create test layer: %v", err)
	}
	img, err = mutate.AppendLayers(img, layer)
	if err != nil {
		t.Fatalf("append layers: %v", err)
	}
	if len(annotations) > 0 {
		img = mutate.Annotations(img, annotations).(v1.Image)
	}
	return img
}

func TestResolveProvenance_EmptyRef_ReturnsError(t *testing.T) {
	r := provenance.NewResolver(nil)
	_, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{})
	if err == nil {
		t.Fatal("expected error on empty image ref, got nil")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestResolveProvenance_RealImage_WithAnnotations(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-ann", host)

	annotations := map[string]string{
		"org.opencontainers.image.source":   "github.com/my-org/my-sveltekit-app",
		"org.opencontainers.image.revision": "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
	}
	img := createTestImage(t, annotations)
	tagRef, err := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatalf("parse tag ref: %v", err)
	}
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("img digest: %v", err)
	}

	r := provenance.NewResolver(nil)
	ctx := context.Background()

	summary, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef: targetRepo + ":v1.0.0",
	})
	if err != nil {
		t.Fatalf("resolve provenance: %v", err)
	}

	if summary.ImageDigest != imgDigest.String() {
		t.Errorf("expected image digest %s, got %s", imgDigest.String(), summary.ImageDigest)
	}
	if summary.PinnedInputs.Repo != "github.com/my-org/my-sveltekit-app" {
		t.Errorf("expected source repo github.com/my-org/my-sveltekit-app, got %s", summary.PinnedInputs.Repo)
	}
	if summary.PinnedInputs.Commit != "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678" {
		t.Errorf("expected commit a1b2c3d4e5f60718293a4b5c6d7e8f9012345678, got %s", summary.PinnedInputs.Commit)
	}
	if summary.HasProvenance {
		t.Errorf("expected HasProvenance to be false for un-attested image")
	}
	if summary.SignatureValid {
		t.Errorf("expected SignatureValid to be false for unsigned image")
	}
}

func TestResolveProvenance_WithSLSAAttestation(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-slsa", host)

	img := createTestImage(t, nil)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, _ := img.Digest()

	// Generate SLSA statement
	slsaGen := slsa.NewGenerator(nil)
	slsaStmt, err := slsaGen.Generate(context.Background(), ports.SLSAGeneratorRequest{
		BaseImage: ports.SLSABaseImage{
			Ref:    "gcr.io/distroless/base:nonroot",
			Digest: v1.Hash{Algorithm: "sha256", Hex: "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"},
		},
		GitRepo:      "github.com/my-org/my-sveltekit-app",
		GitCommit:    "abcdef0123456789abcdef0123456789abcdef01",
		OutputDigest: imgDigest,
		Toolchain: ports.SLSAToolchain{
			BunVersion:    "1.2.18",
			BunBinaryHash: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
			GoVersion:     runtime.Version(),
			BuilderOSArch: runtime.GOOS + "/" + runtime.GOARCH,
		},
		SourceDateEpoch: time.Unix(1700000000, 0),
	})
	if err != nil {
		t.Fatalf("generate SLSA statement: %v", err)
	}

	stmtJSON, _ := json.Marshal(slsaStmt)

	// Wrap in DSSE envelope
	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	privDer, _ := x509.MarshalPKCS8PrivateKey(privKey)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDer})

	pubDer, _ := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDer})
	t.Setenv("POKKUM_SIGNING_PUBKEY", string(pubPEM))

	dsseSigner := dsse.NewSigner(nil)
	env, err := dsseSigner.Sign(context.Background(), ports.DSSESignRequest{
		PayloadBytes: stmtJSON,
		PayloadType:  ports.InTotoPayloadType,
		KeyPEM:       privPEM,
	})
	if err != nil {
		t.Fatalf("sign DSSE: %v", err)
	}

	envJSON, _ := json.Marshal(env)

	// Build .att image layer
	attLayer := static.NewLayer(envJSON, types.MediaType("application/vnd.dsse.envelope.v1+json"))
	attImg, _ := mutate.Append(empty.Image, mutate.Addendum{
		Layer: attLayer,
	})
	attImg = mutate.MediaType(attImg, types.OCIManifestSchema1)

	attTagStr := fmt.Sprintf("%s:%s-%s.att", targetRepo, imgDigest.Algorithm, imgDigest.Hex)
	attRef, _ := name.ParseReference(attTagStr, name.WeakValidation)
	if err := remote.Write(attRef, attImg); err != nil {
		t.Fatalf("push attestation image: %v", err)
	}

	// Resolve provenance
	r := provenance.NewResolver(nil)
	ctx := context.Background()

	summary, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef: targetRepo + ":v1.0.0",
	})
	if err != nil {
		t.Fatalf("resolve provenance: %v", err)
	}

	if !summary.HasProvenance {
		t.Fatal("expected HasProvenance to be true")
	}
	if summary.PinnedInputs.Repo != "github.com/my-org/my-sveltekit-app" {
		t.Errorf("expected repo github.com/my-org/my-sveltekit-app, got %s", summary.PinnedInputs.Repo)
	}
	if summary.PinnedInputs.Commit != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("expected commit abcdef0123456789abcdef0123456789abcdef01, got %s", summary.PinnedInputs.Commit)
	}
	if summary.PinnedInputs.BunVersion != "1.2.18" {
		t.Errorf("expected Bun version 1.2.18, got %s", summary.PinnedInputs.BunVersion)
	}
	if summary.PinnedInputs.GoVersion != runtime.Version() {
		t.Errorf("expected Go version %s, got %s", runtime.Version(), summary.PinnedInputs.GoVersion)
	}
	if !summary.ToolchainMatch || !summary.ExpectedL1Match {
		t.Errorf("expected ToolchainMatch and ExpectedL1Match to be true")
	}
}

func TestResolveProvenance_Adversarial_BogusDSSESignature_IsRejected(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-forged-slsa", host)

	img := createTestImage(t, nil)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, _ := img.Digest()

	// Generate SLSA statement with forged repo/commit
	slsaGen := slsa.NewGenerator(nil)
	slsaStmt, err := slsaGen.Generate(context.Background(), ports.SLSAGeneratorRequest{
		BaseImage: ports.SLSABaseImage{
			Ref:    "gcr.io/distroless/base:nonroot",
			Digest: v1.Hash{Algorithm: "sha256", Hex: "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"},
		},
		GitRepo:         "github.com/attacker/forged-repo",
		GitCommit:       "deadbeef0123456789deadbeef0123456789dead",
		OutputDigest:    imgDigest,
		SourceDateEpoch: time.Unix(1700000000, 0),
	})
	if err != nil {
		t.Fatalf("generate SLSA statement: %v", err)
	}

	stmtJSON, _ := json.Marshal(slsaStmt)
	forgedEnv := ports.DSSEEnvelope{
		PayloadType: ports.InTotoPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(stmtJSON),
		Signatures: []ports.DSSESignature{
			{
				KeyID: "forged-key-id",
				Sig:   base64.StdEncoding.EncodeToString([]byte("TOTALLY-BOGUS-SIGNATURE")),
			},
		},
	}

	forgedEnvJSON, _ := json.Marshal(forgedEnv)

	attLayer := static.NewLayer(forgedEnvJSON, types.MediaType("application/vnd.dsse.envelope.v1+json"))
	attImg, _ := mutate.Append(empty.Image, mutate.Addendum{
		Layer: attLayer,
	})
	attImg = mutate.MediaType(attImg, types.OCIManifestSchema1)

	attTagStr := fmt.Sprintf("%s:%s-%s.att", targetRepo, imgDigest.Algorithm, imgDigest.Hex)
	attRef, _ := name.ParseReference(attTagStr, name.WeakValidation)
	if err := remote.Write(attRef, attImg); err != nil {
		t.Fatalf("push attestation image: %v", err)
	}

	r := provenance.NewResolver(nil)
	ctx := context.Background()

	summary, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef: targetRepo + ":v1.0.0",
	})
	if err != nil {
		t.Fatalf("resolve provenance: %v", err)
	}

	// The bogus DSSE envelope signature MUST be rejected
	if summary.HasProvenance {
		t.Fatal("expected HasProvenance to be FALSE for forged DSSE signature")
	}
	if summary.PinnedInputs.Repo == "github.com/attacker/forged-repo" {
		t.Errorf("forged repo must NOT propagate when DSSE signature fails")
	}
	if summary.PinnedInputs.Commit == "deadbeef0123456789deadbeef0123456789dead" {
		t.Errorf("forged commit must NOT propagate when DSSE signature fails")
	}
}

func TestResolveProvenance_WithCosignSignature(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-cosign", host)

	img := createTestImage(t, nil)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, _ := img.Digest()

	// Generate ECDSA key pair
	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	privDer, _ := x509.MarshalPKCS8PrivateKey(privKey)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDer})

	pubDer, _ := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDer})

	t.Setenv("POKKUM_SIGNING_PUBKEY", string(pubPEM))

	cosignSigner := cosign.NewSigner(nil)
	bundle, err := cosignSigner.Sign(context.Background(), ports.CosignSignRequest{
		Repo:   targetRepo,
		Digest: imgDigest,
		KeyPEM: privPEM,
	})
	if err != nil {
		t.Fatalf("cosign sign: %v", err)
	}

	sigLayer := static.NewLayer(bundle.PayloadBytes, types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"))
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer: sigLayer,
		Annotations: map[string]string{
			"dev.cosignproject.cosign/signature": bundle.Base64Signature,
		},
		MediaType: types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"),
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}
	sigImg = mutate.MediaType(sigImg, types.OCIManifestSchema1)

	sigTagStr := fmt.Sprintf("%s:%s-%s.sig", targetRepo, imgDigest.Algorithm, imgDigest.Hex)
	sigRef, _ := name.ParseReference(sigTagStr, name.WeakValidation)
	if err := remote.Write(sigRef, sigImg); err != nil {
		t.Fatalf("push signature image: %v", err)
	}

	// Resolve provenance
	r := provenance.NewResolver(nil)
	ctx := context.Background()

	summary, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef: targetRepo + ":v1.0.0",
	})
	if err != nil {
		t.Fatalf("resolve provenance: %v", err)
	}

	if !summary.SignatureValid {
		t.Fatal("expected SignatureValid to be true")
	}
	if summary.SignerIdentity != "static-key" {
		t.Errorf("expected SignerIdentity static-key, got %s", summary.SignerIdentity)
	}
}

func TestResolveProvenance_Adversarial_FakeKeylessCert_IsRejected(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-forged-keyless", host)

	img := createTestImage(t, nil)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, _ := img.Digest()

	// Generate a rogue self-signed certificate (NOT chaining to Fulcio root CA)
	roguePrivKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1337),
		Subject: pkix.Name{
			CommonName: "Fake Fulcio CA",
		},
		EmailAddresses: []string{"attacker@rogue-domain.test"},
		NotBefore:      time.Now().Add(-1 * time.Hour),
		NotAfter:       time.Now().Add(1 * time.Hour),
	}
	rogueCertBytes, err := x509.CreateCertificate(rand.Reader, template, template, &roguePrivKey.PublicKey, roguePrivKey)
	if err != nil {
		t.Fatalf("create rogue cert: %v", err)
	}
	rogueCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rogueCertBytes})

	// Build a Cosign payload with matching image reference
	payloadObj := ports.CosignSimpleSigningPayload{
		Critical: ports.CosignCritical{
			Identity: ports.CosignCriticalIdentity{
				DockerReference: targetRepo,
			},
			Image: ports.CosignCriticalImage{
				DockerManifestDigest: imgDigest.String(),
			},
			Type: ports.CosignSimpleSigningType,
		},
	}
	payloadBytes, _ := json.Marshal(payloadObj)

	sigLayer := static.NewLayer(payloadBytes, types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"))
	sigImg, _ := mutate.Append(empty.Image, mutate.Addendum{
		Layer: sigLayer,
		Annotations: map[string]string{
			sigstore.CosignSignatureAnnotation:   base64.StdEncoding.EncodeToString([]byte("fake-sig")),
			sigstore.CosignCertificateAnnotation: string(rogueCertPEM),
			sigstore.CosignBundleAnnotation:      `{"SignedEntryTimestamp":"fake","Payload":{"body":"fake"}}`,
		},
		MediaType: types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"),
	})
	sigImg = mutate.MediaType(sigImg, types.OCIManifestSchema1)

	sigTagStr := fmt.Sprintf("%s:%s-%s.sig", targetRepo, imgDigest.Algorithm, imgDigest.Hex)
	sigRef, _ := name.ParseReference(sigTagStr, name.WeakValidation)
	if err := remote.Write(sigRef, sigImg); err != nil {
		t.Fatalf("push signature image: %v", err)
	}

	r := provenance.NewResolver(nil)
	ctx := context.Background()

	summary, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef: targetRepo + ":v1.0.0",
	})
	if err != nil {
		t.Fatalf("resolve provenance: %v", err)
	}

	// Self-signed cert MUST NOT be accepted as valid keyless signature
	if summary.SignatureValid {
		t.Fatal("expected SignatureValid to be FALSE for rogue self-signed certificate")
	}
}

func TestResolveProvenance_ExpectSource_MatchAndMismatch(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-expect-source", host)

	annotations := map[string]string{
		"org.opencontainers.image.source":   "github.com/my-org/my-sveltekit-app",
		"org.opencontainers.image.revision": "c0ffee1234567890abcdef1234567890abcdef12",
	}
	img := createTestImage(t, annotations)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	_ = remote.Write(tagRef, img)

	r := provenance.NewResolver(nil)
	ctx := context.Background()

	// Matching source
	_, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef:     targetRepo + ":v1.0.0",
		ExpectSource: "github.com/my-org/my-sveltekit-app@c0ffee12",
	})
	if err != nil {
		t.Fatalf("expected matching source to succeed: %v", err)
	}

	// Mismatched repo
	_, err = r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef:     targetRepo + ":v1.0.0",
		ExpectSource: "github.com/wrong-org/my-sveltekit-app@c0ffee12",
	})
	if err == nil {
		t.Fatal("expected error on repo mismatch, got nil")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}

	// Mismatched commit
	_, err = r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef:     targetRepo + ":v1.0.0",
		ExpectSource: "github.com/my-org/my-sveltekit-app@deadbeef",
	})
	if err == nil {
		t.Fatal("expected error on commit mismatch, got nil")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func writeTempTar(t *testing.T, filename string, content []byte) string {
	t.Helper()
	f, err := os.CreateTemp("", "pokkum-test-*.tar")
	if err != nil {
		t.Fatalf("create temp tar: %v", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	importTar(&buf, filename, content)

	if _, err := f.Write(buf.Bytes()); err != nil {
		t.Fatalf("write temp tar: %v", err)
	}

	return f.Name()
}

func importTar(w *bytes.Buffer, filename string, content []byte) {
	header := make([]byte, 512)
	copy(header[0:100], filename)
	copy(header[100:108], "0000644\x00")
	copy(header[108:116], "0000000\x00")
	copy(header[116:124], "0000000\x00")
	copy(header[124:136], fmt.Sprintf("%011o\x00", len(content)))
	header[156] = '0'

	// Calculate checksum
	chk := 0
	for i := 0; i < 512; i++ {
		if i >= 148 && i < 156 {
			chk += 32
		} else {
			chk += int(header[i])
		}
	}
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", chk))

	w.Write(header)
	w.Write(content)
	pad := 512 - (len(content) % 512)
	if pad < 512 {
		w.Write(make([]byte, pad))
	}
	w.Write(make([]byte, 1024))
}
