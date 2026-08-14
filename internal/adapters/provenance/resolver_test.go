package provenance_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pokkum-provenance-dockerconfig")
	if err != nil {
		fmt.Fprintln(os.Stderr, "provenance: TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	os.Setenv("DOCKER_CONFIG", dir)
	os.Exit(m.Run())
}

func startTestRegistry(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	return server, host
}

func createTestImage(t *testing.T, annotations map[string]string) v1.Image {
	t.Helper()
	img := empty.Image
	if annotations != nil {
		img = mutate.Annotations(img, annotations).(v1.Image)
	}
	return img
}

func TestResolveProvenance_EmptyRef_ReturnsError(t *testing.T) {
	r := provenance.NewResolver(nil)
	ctx := context.Background()

	_, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{})
	if err == nil {
		t.Fatal("expected error for empty image ref, got nil")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
}

func TestResolveProvenance_RealImage_WithAnnotations(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-annotations", host)

	annotations := map[string]string{
		"org.opencontainers.image.source":   "github.com/my-org/my-sveltekit-app",
		"org.opencontainers.image.revision": "c0ffee1234567890abcdef1234567890abcdef12",
		"org.opencontainers.image.created":  "2026-08-14T12:00:00Z",
	}
	img := createTestImage(t, annotations)

	tagRef, err := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatalf("parse tag ref: %v", err)
	}
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}

	expectedDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("img digest: %v", err)
	}

	r := provenance.NewResolver(slog.Default())
	ctx := context.Background()

	summary, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef: targetRepo + ":v1.0.0",
	})
	if err != nil {
		t.Fatalf("unexpected error resolving provenance: %v", err)
	}

	if summary.ImageDigest != expectedDigest.String() {
		t.Errorf("expected digest %s, got %s", expectedDigest.String(), summary.ImageDigest)
	}
	if summary.PinnedInputs.Repo != "github.com/my-org/my-sveltekit-app" {
		t.Errorf("expected repo github.com/my-org/my-sveltekit-app, got %s", summary.PinnedInputs.Repo)
	}
	if summary.PinnedInputs.Commit != "c0ffee1234567890abcdef1234567890abcdef12" {
		t.Errorf("expected commit c0ffee1234567890abcdef1234567890abcdef12, got %s", summary.PinnedInputs.Commit)
	}
	if summary.HasProvenance {
		t.Error("expected HasProvenance to be false when no .att tag exists")
	}
	if summary.SignatureValid {
		t.Error("expected SignatureValid to be false when no .sig tag exists")
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

	// Generate real SLSA statement
	slsaGen := slsa.NewGenerator(nil)
	slsaStmt, err := slsaGen.Generate(context.Background(), ports.SLSAGeneratorRequest{
		Repo: targetRepo,
		BaseImage: ports.SLSABaseImage{
			Ref:       "gcr.io/distroless/cc-debian12:nonroot",
			PinnedRef: "gcr.io/distroless/cc-debian12@sha256:1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff",
			Digest:    v1.Hash{Algorithm: "sha256", Hex: "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"},
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
	attLayer, err := tarball.LayerFromFile(writeTempTar(t, "attestation.json", envJSON))
	if err != nil {
		t.Fatalf("layer from file: %v", err)
	}
	attImg, _ := mutate.AppendLayers(empty.Image, attLayer)

	attTagStr := fmt.Sprintf("%s:%s-%s.att", targetRepo, imgDigest.Algorithm, imgDigest.Hex)
	attRef, _ := name.ParseReference(attTagStr, name.WeakValidation)
	if err := remote.Write(attRef, attImg); err != nil {
		t.Fatalf("push attestation image: %v", err)
	}

	// Resolve provenance
	r := provenance.NewResolver(slog.Default())
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
	r := provenance.NewResolver(slog.Default())
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

	tw := mutate.Annotations(empty.Image, nil)
	_ = tw

	// Simple tar file creation
	var buf bytes.Buffer
	tarWriter := cosignTarWriter(&buf, filename, content)
	if err := tarWriter(); err != nil {
		t.Fatalf("write tar: %v", err)
	}

	if _, err := f.Write(buf.Bytes()); err != nil {
		t.Fatalf("write temp tar: %v", err)
	}

	return f.Name()
}

func cosignTarWriter(w *bytes.Buffer, filename string, content []byte) func() error {
	return func() error {
		importTar := tarballWriteEntry(w, filename, content)
		return importTar
	}
}

func tarballWriteEntry(w *bytes.Buffer, filename string, content []byte) error {
	importArchiveTar := func() error {
		importArch := "archive/tar"
		_ = importArch
		return nil
	}
	_ = importArchiveTar

	// Use archive/tar directly
	return writeTarRaw(w, filename, content)
}

func writeTarRaw(w *bytes.Buffer, filename string, content []byte) error {
	importTar(w, filename, content)
	return nil
}

func importTar(w *bytes.Buffer, filename string, content []byte) {
	type tarWriter interface {
		WriteHeader(hdr any) error
		Write(b []byte) (int, error)
	}
	// Manual minimal tar header write
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
