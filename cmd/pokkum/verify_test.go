package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
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

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/dsse"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestVerifyCommand_NoRebuildJSON_UnattestedImage_Exits2(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	targetRepo := fmt.Sprintf("%s/app-unattested", host)

	annotations := map[string]string{
		"org.opencontainers.image.source":   "github.com/example/my-app",
		"org.opencontainers.image.revision": "a1b2c3d4e5f678901234567890abcdef12345678",
	}
	img := mutate.Annotations(empty.Image, annotations).(v1.Image)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}

	var exitCode int
	oldExit := exitFunc
	exitFunc = func(code int) {
		exitCode = code
	}
	defer func() { exitFunc = oldExit }()

	ctx := context.Background()
	opts := &verifyOptions{
		noRebuild: true,
		output:    "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = runVerify(ctx, nil, opts, targetRepo+":v1.0.0")

	w.Close()
	os.Stdout = oldStdout

	if exitCode != 2 {
		t.Fatalf("expected exit code 2 for unattested image, got %d", exitCode)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("verify --output=json emitted invalid JSON: %v, raw: %s", err, outBuf.String())
	}

	if env.Status != "error" {
		t.Errorf("expected status error, got %s", env.Status)
	}
	if env.Error == nil || env.Error.Code != "ERR_PROVENANCE_FAILED" {
		t.Errorf("expected ERR_PROVENANCE_FAILED error code, got %+v", env.Error)
	}
}

func TestVerifyCommand_NoRebuildJSON_WithAttestation_VerdictValidated(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	targetRepo := fmt.Sprintf("%s/app-attested", host)

	// Create test image
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	hdr := &tar.Header{
		Name: "app/server.js",
		Mode: 0644,
		Size: int64(len("console.log(1);")),
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("console.log(1);"))
	_ = tw.Close()

	layer, _ := tarball.LayerFromFile(writeTempTarFile(t, tarBuf.Bytes()))
	img, _ := mutate.AppendLayers(empty.Image, layer)

	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, _ := img.Digest()

	// Generate and sign SLSA statement
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
	attLayer := static.NewLayer(envJSON, types.MediaType("application/vnd.dsse.envelope.v1+json"))
	attImg, _ := mutate.Append(empty.Image, mutate.Addendum{Layer: attLayer})
	attImg = mutate.MediaType(attImg, types.OCIManifestSchema1)

	attTagStr := fmt.Sprintf("%s:%s-%s.att", targetRepo, imgDigest.Algorithm, imgDigest.Hex)
	attRef, _ := name.ParseReference(attTagStr, name.WeakValidation)
	if err := remote.Write(attRef, attImg); err != nil {
		t.Fatalf("push attestation image: %v", err)
	}

	var exitCode int
	oldExit := exitFunc
	exitFunc = func(code int) {
		exitCode = code
	}
	defer func() { exitFunc = oldExit }()

	ctx := context.Background()
	opts := &verifyOptions{
		noRebuild: true,
		output:    "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runVerify(ctx, nil, opts, targetRepo+":v1.0.0")

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running verify --no-rebuild: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	type EnvelopeWrapper struct {
		SchemaVersion string             `json:"schema_version"`
		Command       string             `json:"command"`
		Status        string             `json:"status"`
		Data          ports.VerifyOutput `json:"data"`
	}

	var wrapper EnvelopeWrapper
	if err := json.Unmarshal(outBuf.Bytes(), &wrapper); err != nil {
		t.Fatalf("verify --output=json emitted invalid JSON: %v, raw: %s", err, outBuf.String())
	}

	if wrapper.Status != "success" {
		t.Errorf("expected status success, got %s", wrapper.Status)
	}
	if wrapper.Data.Verdict != "ATTESTATION_VALIDATED" {
		t.Errorf("expected Verdict ATTESTATION_VALIDATED, got %s", wrapper.Data.Verdict)
	}
	if !wrapper.Data.Provenance.HasProvenance {
		t.Errorf("expected HasProvenance to be true")
	}
	if wrapper.Data.Provenance.PinnedInputs.Repo != "github.com/my-org/my-sveltekit-app" {
		t.Errorf("expected repo github.com/my-org/my-sveltekit-app, got %s", wrapper.Data.Provenance.PinnedInputs.Repo)
	}
}

func TestVerifyCommand_FullRebuildWithAgainstTarball(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	targetRepo := fmt.Sprintf("%s/app-verify-rebuild", host)

	// Create test layer
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	hdr := &tar.Header{
		Name: "app/server.js",
		Mode: 0644,
		Size: int64(len("console.log(1);")),
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("console.log(1);"))
	_ = tw.Close()

	layer, _ := tarball.LayerFromFile(writeTempTarFile(t, tarBuf.Bytes()))
	img, _ := mutate.AppendLayers(empty.Image, layer)

	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push remote image: %v", err)
	}

	localTarPath := writeTempImageTar(t, img, targetRepo+":v1.0.0")
	defer os.Remove(localTarPath)

	var exitCode int
	oldExit := exitFunc
	exitFunc = func(code int) {
		exitCode = code
	}
	defer func() { exitFunc = oldExit }()

	ctx := context.Background()
	opts := &verifyOptions{
		against: localTarPath,
		output:  "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runVerify(ctx, nil, opts, targetRepo+":v1.0.0")

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running verify with --against: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	type EnvelopeWrapper struct {
		SchemaVersion string             `json:"schema_version"`
		Command       string             `json:"command"`
		Status        string             `json:"status"`
		Data          ports.VerifyOutput `json:"data"`
	}

	var wrapper EnvelopeWrapper
	if err := json.Unmarshal(outBuf.Bytes(), &wrapper); err != nil {
		t.Fatalf("verify --output=json emitted invalid JSON: %v, raw: %s", err, outBuf.String())
	}

	if wrapper.Status != "success" {
		t.Errorf("expected status success, got %s", wrapper.Status)
	}
	if wrapper.Data.Verdict != "VERIFIED_L1" {
		t.Errorf("expected Verdict VERIFIED_L1, got %s", wrapper.Data.Verdict)
	}
	if wrapper.Data.Level != "L1" {
		t.Errorf("expected Level L1, got %s", wrapper.Data.Level)
	}
}

func writeTempTarFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp("", "pokkum-test-layer-*.tar")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()
	_, _ = f.Write(data)
	return f.Name()
}

func writeTempImageTar(t *testing.T, img v1.Image, refStr string) string {
	t.Helper()
	f, err := os.CreateTemp("", "pokkum-test-img-*.tar")
	if err != nil {
		t.Fatalf("create temp tar: %v", err)
	}
	defer f.Close()

	ref, err := name.ParseReference(refStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("parse ref: %v", err)
	}

	if err := tarball.Write(ref, img, f); err != nil {
		t.Fatalf("write tarball: %v", err)
	}

	return f.Name()
}
