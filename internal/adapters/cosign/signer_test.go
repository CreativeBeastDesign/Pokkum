package cosign

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// generateECDSAPEM generates a temporary ECDSA P-256 keypair encoded as PEM bytes.
func generateECDSAPEM(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}

	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal ec private key: %v", err)
	}
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal ec public key: %v", err)
	}
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})

	return privPEM, pubPEM
}

// generateEd25519PEM generates a temporary Ed25519 keypair encoded as PEM bytes.
func generateEd25519PEM(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal ed25519 private key: %v", err)
	}
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal ed25519 public key: %v", err)
	}
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})

	return privPEM, pubPEM
}

func TestCosignSigner_ECDSA_SignAndVerify(t *testing.T) {
	privPEM, pubPEM := generateECDSAPEM(t)

	signer := NewSigner(nil)

	digestStr := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digest, err := v1.NewHash(digestStr)
	if err != nil {
		t.Fatalf("parse digest: %v", err)
	}

	req := ports.CosignSignRequest{
		Repo:            "ghcr.io/acme/app",
		Digest:          digest,
		KeyPEM:          privPEM,
		Creator:         "pokkum-test",
		SourceDateEpoch: time.Unix(1700000000, 0).UTC(),
		Annotations: map[string]string{
			"env": "production",
		},
	}

	bundle, err := signer.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	if len(bundle.PayloadBytes) == 0 {
		t.Error("PayloadBytes is empty")
	}
	if len(bundle.SignatureBytes) == 0 {
		t.Error("SignatureBytes is empty")
	}
	if bundle.Base64Signature == "" {
		t.Error("Base64Signature is empty")
	}

	// Verify valid signature
	err = signer.Verify(context.Background(), bundle, pubPEM, "ghcr.io/acme/app", digest)
	if err != nil {
		t.Errorf("Verify failed for valid signature: %v", err)
	}
}

func TestCosignSigner_Ed25519_SignAndVerify(t *testing.T) {
	privPEM, pubPEM := generateEd25519PEM(t)

	signer := NewSigner(nil)

	digestStr := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	digest, err := v1.NewHash(digestStr)
	if err != nil {
		t.Fatalf("parse digest: %v", err)
	}

	req := ports.CosignSignRequest{
		Repo:   "ghcr.io/acme/app",
		Digest: digest,
		KeyPEM: privPEM,
	}

	bundle, err := signer.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("Sign (Ed25519) failed: %v", err)
	}

	err = signer.Verify(context.Background(), bundle, pubPEM, "ghcr.io/acme/app", digest)
	if err != nil {
		t.Errorf("Verify (Ed25519) failed: %v", err)
	}
}

func TestCosignSigner_TamperDetection(t *testing.T) {
	privPEM, pubPEM := generateECDSAPEM(t)
	_, wrongPubPEM := generateECDSAPEM(t)

	signer := NewSigner(nil)

	digestStr := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digest, err := v1.NewHash(digestStr)
	if err != nil {
		t.Fatalf("parse digest: %v", err)
	}

	otherDigestStr := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	otherDigest, err := v1.NewHash(otherDigestStr)
	if err != nil {
		t.Fatalf("parse other digest: %v", err)
	}

	req := ports.CosignSignRequest{
		Repo:   "ghcr.io/acme/app",
		Digest: digest,
		KeyPEM: privPEM,
	}

	bundle, err := signer.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	// Case 1: Wrong expected repository
	if err := signer.Verify(context.Background(), bundle, pubPEM, "ghcr.io/other/app", digest); err == nil {
		t.Error("Verify expected error for wrong repository, got nil")
	}

	// Case 2: Wrong expected digest
	if err := signer.Verify(context.Background(), bundle, pubPEM, "ghcr.io/acme/app", otherDigest); err == nil {
		t.Error("Verify expected error for wrong digest, got nil")
	}

	// Case 3: Wrong public key
	if err := signer.Verify(context.Background(), bundle, wrongPubPEM, "ghcr.io/acme/app", digest); err == nil {
		t.Error("Verify expected error for wrong public key, got nil")
	}

	// Case 4: Tampered payload bytes
	tamperedBundle := bundle
	tamperedBundle.PayloadBytes = append([]byte("tampered "), bundle.PayloadBytes...)
	if err := signer.Verify(context.Background(), tamperedBundle, pubPEM, "ghcr.io/acme/app", digest); err == nil {
		t.Error("Verify expected error for tampered payload, got nil")
	}
}
