package main

import (
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
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// F10: `pokkum verify --no-rebuild` reported the byte-identical message
// "image ... has neither a valid SLSA provenance attestation nor a verified
// signature" for two very different situations: an image nobody ever signed,
// and an image that carries a real Cosign signature verified against the
// WRONG public key. The distinguishing information exists deep inside
// internal/adapters/provenance.Resolver (it even gets logged at
// slog.LevelDebug), but was discarded before reaching the message built at
// verify.go's HasProvenance/SignatureValid check. cosign itself gets this
// right ("no signatures found" vs "invalid signature ..."); this test proves
// pokkum now does too, at the default log level, without needing
// --log-level=debug.
//
// runVerifyJSONMessage runs `pokkum verify --no-rebuild --output=json`
// against ref and returns the JSON envelope's error message.
func runVerifyJSONMessage(t *testing.T, ref string, opts *verifyOptions) string {
	t.Helper()
	opts.noRebuild = true
	opts.output = "json"

	oldExit := exitFunc
	exitFunc = func(int) {}
	defer func() { exitFunc = oldExit }()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = runVerify(context.Background(), nil, opts, ref)

	_ = w.Close()
	os.Stdout = oldStdout

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("verify --output=json emitted invalid JSON: %v, raw: %s", err, outBuf.String())
	}
	if env.Status != "error" {
		t.Fatalf("expected status=error, got: %+v", env)
	}
	if env.Error == nil {
		t.Fatalf("expected an error payload, got none: %+v", env)
	}
	return env.Error.Message
}

// pushUnsignedImage pushes a plain image with no .sig or .att tag at all --
// genuinely, completely unsigned.
func pushUnsignedImage(t *testing.T, host, repoName string) string {
	t.Helper()
	targetRepo := fmt.Sprintf("%s/%s", host, repoName)
	img := mutate.Annotations(empty.Image, map[string]string{
		"org.opencontainers.image.source":   "github.com/example/my-app",
		"org.opencontainers.image.revision": "a1b2c3d4e5f678901234567890abcdef12345678",
	}).(v1.Image)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	return targetRepo + ":v1.0.0"
}

// pushImageSignedWithWrongKey pushes a real image carrying a REAL Cosign
// signature (signed with privKey), then tells the caller which, different,
// public key to verify against -- so SignatureValid comes back false not
// because nothing was ever signed, but because the wrong key was checked
// against a real signature. Mirrors
// internal/adapters/provenance/resolver_test.go's
// TestResolveProvenance_WithCosignSignature fixture.
func pushImageSignedWithWrongKey(t *testing.T, host, repoName string) (ref string, wrongPubKeyPEM []byte) {
	t.Helper()
	targetRepo := fmt.Sprintf("%s/%s", host, repoName)

	img := mutate.Annotations(empty.Image, map[string]string{
		"org.opencontainers.image.source":   "github.com/example/my-app",
		"org.opencontainers.image.revision": "a1b2c3d4e5f678901234567890abcdef12345678",
	}).(v1.Image)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	// Real signing key.
	signKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	signDER, _ := x509.MarshalPKCS8PrivateKey(signKey)
	signPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: signDER})

	// A completely different key pair -- what verification will (wrongly) be
	// checked against.
	wrongKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wrongPubDER, _ := x509.MarshalPKIXPublicKey(&wrongKey.PublicKey)
	wrongPubKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: wrongPubDER})

	cosignSigner := cosign.NewSigner(nil)
	bundle, err := cosignSigner.Sign(context.Background(), ports.CosignSignRequest{
		Repo:   targetRepo,
		Digest: imgDigest,
		KeyPEM: signPEM,
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

	return targetRepo + ":v1.0.0", wrongPubKeyPEM
}

// TestVerifyNoRebuild_UnsignedVsBadSignature_ProduceDifferentMessages is the
// fail-first proof for F10: a genuinely unsigned image and an image signed
// with the wrong key both leave HasProvenance/SignatureValid false with no
// Go error, so verify.go's message construction is the only place that can
// distinguish them. Before the fix, both cases produce the byte-identical
// generic sentence; after the fix, each names its own, individually
// meaningful condition.
func TestVerifyNoRebuild_UnsignedVsBadSignature_ProduceDifferentMessages(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	unsignedRef := pushUnsignedImage(t, host, "app-genuinely-unsigned")
	unsignedMsg := runVerifyJSONMessage(t, unsignedRef, &verifyOptions{})

	badSigRef, wrongPubKeyPEM := pushImageSignedWithWrongKey(t, host, "app-wrong-key")
	badSigMsg := runVerifyJSONMessage(t, badSigRef, &verifyOptions{publicKey: string(wrongPubKeyPEM)})

	if unsignedMsg == badSigMsg {
		t.Fatalf("unsigned and bad-signature cases produced the IDENTICAL message: %q", unsignedMsg)
	}

	// Not just "different" -- each message must actually name its own
	// condition, so a test asserting only inequality couldn't pass by luck
	// (e.g. by embedding a random image ref in one but not the other).
	lowerUnsigned := strings.ToLower(unsignedMsg)
	if !strings.Contains(lowerUnsigned, "unsigned") && !strings.Contains(lowerUnsigned, "no cosign signature") && !strings.Contains(lowerUnsigned, "no signature") {
		t.Errorf("unsigned-image message does not say the image is unsigned: %q", unsignedMsg)
	}
	if strings.Contains(lowerUnsigned, "did not verify") || strings.Contains(lowerUnsigned, "invalid signature") {
		t.Errorf("unsigned-image message wrongly claims signature material was present: %q", unsignedMsg)
	}

	lowerBadSig := strings.ToLower(badSigMsg)
	if !strings.Contains(lowerBadSig, "did not verify") && !strings.Contains(lowerBadSig, "invalid") {
		t.Errorf("bad-signature message does not say verification failed on present material: %q", badSigMsg)
	}
	if strings.Contains(lowerBadSig, "never signed") || strings.Contains(lowerBadSig, "no cosign signature or slsa") {
		t.Errorf("bad-signature message wrongly claims the image was never signed: %q", badSigMsg)
	}
}
