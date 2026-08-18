package main

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
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http/httptest"
	"os"
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
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestVerifyCommand_SignatureVerificationFlagsRegistered guards the CLI surface
// that makes `pokkum verify` able to check a keyless signature at all.
//
// Before these flags existed, ports.ProvenanceResolverRequest carried no
// identity fields, so the resolver had nowhere to get an expected signer from
// and derived one from the certificate it was verifying — a check with no
// content. The flags are therefore not a convenience: they are the only
// legitimate source of the expectation, which is why their absence is a
// regression worth a test.
func TestVerifyCommand_SignatureVerificationFlagsRegistered(t *testing.T) {
	cmd := newVerifyCommand(context.Background(), slog.Default())

	for _, name := range []string{
		"keyless-identity",
		"keyless-issuer",
		"public-key",
		"sigstore-trusted-root",
		// Pre-existing flags, asserted so a refactor cannot quietly drop them.
		"no-rebuild",
		"expect-source",
		"against",
		"registry-config",
		// The --expect-source escape hatch: see
		// TestVerifyCommand_ExpectSourceWithoutVerifiedProvenance_Exits2.
		"allow-unverified-source",
	} {
		if flag := cmd.Flags().Lookup(name); flag == nil {
			t.Errorf("expected --%s flag to be registered on `pokkum verify`", name)
		}
	}
}

// TestVerifyCommand_KeylessFlagNamingMatchesBuild pins the naming relationship
// with `pokkum build`'s two existing keyless pairs. `verify` has exactly one
// verification subject, so it takes the unprefixed form of the same names.
func TestVerifyCommand_KeylessFlagNamingMatchesBuild(t *testing.T) {
	verifyCmd := newVerifyCommand(context.Background(), slog.Default())
	buildCmd := newBuildCommand(context.Background(), slog.Default())

	pairs := [][2]string{
		{"keyless-identity", "cache-keyless-identity"},
		{"keyless-issuer", "cache-keyless-issuer"},
	}
	for _, p := range pairs {
		if verifyCmd.Flags().Lookup(p[0]) == nil {
			t.Errorf("`pokkum verify` is missing --%s", p[0])
		}
		if buildCmd.Flags().Lookup(p[1]) == nil {
			t.Errorf("`pokkum build` is missing --%s; the naming precedent this pair follows is gone", p[1])
		}
	}
}

// TestVerifyCommand_KeylessWithoutIdentity_Exits2 is the caller-chain test
// (self-review checklist row 13) for the fail-closed refusal. The refusal is
// implemented in the provenance resolver and unit-tested there, but what
// actually matters to a CI pipeline is `pokkum verify`'s exit code: a
// keyless-signed image verified without --keyless-identity/--keyless-issuer
// must exit non-zero, not print a reassuring report and exit 0.
func TestVerifyCommand_KeylessWithoutIdentity_Exits2(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	targetRepo := fmt.Sprintf("%s/app-keyless-cli", host)

	img := mutate.Annotations(empty.Image, map[string]string{
		"org.opencontainers.image.source":   "github.com/example/my-app",
		"org.opencontainers.image.revision": "a1b2c3d4e5f678901234567890abcdef12345678",
	}).(v1.Image)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, _ := img.Digest()

	// A minimal but structurally complete keyless `.sig`: a Fulcio-shaped
	// certificate plus a Rekor bundle annotation is all it takes for the
	// resolver to recognise keyless material it cannot judge.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:   big.NewInt(7),
		Issuer:         pkix.Name{CommonName: "some-fulcio-intermediate"},
		EmailAddresses: []string{"whoever@example.test"},
		NotBefore:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:       time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	payload, _ := json.Marshal(ports.CosignSimpleSigningPayload{
		Critical: ports.CosignCritical{
			Identity: ports.CosignCriticalIdentity{DockerReference: targetRepo},
			Image:    ports.CosignCriticalImage{DockerManifestDigest: imgDigest.String()},
			Type:     ports.CosignContainerImageSignatureType,
		},
	})

	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer: static.NewLayer(payload, types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json")),
		Annotations: map[string]string{
			"dev.cosignproject.cosign/signature": base64.StdEncoding.EncodeToString([]byte("sig")),
			"dev.sigstore.cosign/certificate":    string(certPEM),
			"dev.sigstore.cosign/bundle":         `{"SignedEntryTimestamp":"c2ln","Payload":{"body":"Ym9keQ==","integratedTime":1,"logIndex":2,"logID":"ab"}}`,
		},
		MediaType: types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"),
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}
	sigImg = mutate.MediaType(sigImg, types.OCIManifestSchema1)

	sigRef, _ := name.ParseReference(
		fmt.Sprintf("%s:%s-%s.sig", targetRepo, imgDigest.Algorithm, imgDigest.Hex), name.WeakValidation)
	if err := remote.Write(sigRef, sigImg); err != nil {
		t.Fatalf("push signature image: %v", err)
	}

	var exitCode int
	oldExit := exitFunc
	exitFunc = func(code int) { exitCode = code }
	defer func() { exitFunc = oldExit }()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = runVerify(context.Background(), nil, &verifyOptions{noRebuild: true, output: "json"}, targetRepo+":v1.0.0")

	w.Close()
	os.Stdout = oldStdout

	if exitCode != 2 {
		t.Fatalf("expected exit code 2 for a keyless-signed image with no expected identity, got %d", exitCode)
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
	if env.Error == nil || !strings.Contains(env.Error.Message, "--keyless-identity") {
		t.Errorf("the error must tell the operator which flag to supply, got %+v", env.Error)
	}
}

// TestVerifyCommand_ExpectSourceWithoutVerifiedProvenance_Exits2 is the
// caller-chain test (self-review checklist row 13) for the --expect-source
// fail-closed gate: the refusal is implemented and unit-tested at the
// resolver layer (internal/adapters/provenance), but what actually matters
// to a CI pipeline invoking `pokkum verify --expect-source ...` is the
// process's own exit code and the message printed to stdout/stderr — this
// proves the gate actually reaches runVerify's error path rather than being
// silently absorbed or bypassed by a check elsewhere in the same call chain
// (e.g. ResolveProvenance's error being swallowed before exitFunc is called).
func TestVerifyCommand_ExpectSourceWithoutVerifiedProvenance_Exits2(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	targetRepo := fmt.Sprintf("%s/app-expect-source-cli", host)

	// Annotation-only image: exactly the shape the original fail-open bug
	// exploited (unsigned org.opencontainers.image.source/.revision, no
	// SLSA attestation). Its values deliberately MATCH the --expect-source
	// assertion below — proving the refusal fires because nothing verified
	// them, not because they happen to disagree.
	img := mutate.Annotations(empty.Image, map[string]string{
		"org.opencontainers.image.source":   "github.com/example/my-app",
		"org.opencontainers.image.revision": "a1b2c3d4e5f678901234567890abcdef12345678",
	}).(v1.Image)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}

	var exitCode int
	oldExit := exitFunc
	exitFunc = func(code int) { exitCode = code }
	defer func() { exitFunc = oldExit }()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = runVerify(context.Background(), nil, &verifyOptions{
		noRebuild:    true,
		expectSource: "github.com/example/my-app@a1b2c3d4",
		output:       "json",
	}, targetRepo+":v1.0.0")

	w.Close()
	os.Stdout = oldStdout

	if exitCode != 2 {
		t.Fatalf("expected exit code 2 for --expect-source against unverified source information, got %d", exitCode)
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
	if env.Error == nil || !strings.Contains(env.Error.Message, "--allow-unverified-source") {
		t.Errorf("the error must tell the operator about the escape hatch, got %+v", env.Error)
	}
}
