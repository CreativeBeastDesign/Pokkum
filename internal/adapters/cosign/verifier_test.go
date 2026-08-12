package cosign

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestReleaseVerifier_ArtifactSignature_ECDSA(t *testing.T) {
	privPEM, pubPEM := generateECDSAPEM(t)
	privKey, err := parsePrivateKeyPEM(privPEM)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}

	artifactData := []byte("checksum123  pokkum_1.0.0_darwin_arm64.tar.gz\n")
	sigBytes, err := signPayload(privKey, artifactData)
	if err != nil {
		t.Fatalf("sign artifact: %v", err)
	}

	verifier := NewSigner(nil)

	// Raw signature verification
	err = verifier.VerifyArtifactSignature(context.Background(), ports.VerifyReleaseArtifactRequest{
		ArtifactBytes:  artifactData,
		SignatureBytes: sigBytes,
		PublicKeyPEM:   pubPEM,
	})
	if err != nil {
		t.Fatalf("VerifyArtifactSignature (raw) failed: %v", err)
	}

	// Base64 signature verification
	b64Sig := []byte(base64.StdEncoding.EncodeToString(sigBytes))
	err = verifier.VerifyArtifactSignature(context.Background(), ports.VerifyReleaseArtifactRequest{
		ArtifactBytes:  artifactData,
		SignatureBytes: b64Sig,
		PublicKeyPEM:   pubPEM,
	})
	if err != nil {
		t.Fatalf("VerifyArtifactSignature (base64) failed: %v", err)
	}

	// Tampered data should fail
	err = verifier.VerifyArtifactSignature(context.Background(), ports.VerifyReleaseArtifactRequest{
		ArtifactBytes:  []byte("tampered data"),
		SignatureBytes: sigBytes,
		PublicKeyPEM:   pubPEM,
	})
	if err == nil {
		t.Fatal("expected failure on tampered artifact data, got nil")
	}

	// Wrong key should fail
	_, wrongPubPEM := generateECDSAPEM(t)
	err = verifier.VerifyArtifactSignature(context.Background(), ports.VerifyReleaseArtifactRequest{
		ArtifactBytes:  artifactData,
		SignatureBytes: sigBytes,
		PublicKeyPEM:   wrongPubPEM,
	})
	if err == nil {
		t.Fatal("expected failure on wrong public key, got nil")
	}
}

func TestReleaseVerifier_ArtifactSignature_Ed25519(t *testing.T) {
	privPEM, pubPEM := generateEd25519PEM(t)
	privKey, err := parsePrivateKeyPEM(privPEM)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}

	artifactData := []byte("checksum456  pokkum_1.0.0_linux_amd64.tar.gz\n")
	sigBytes, err := signPayload(privKey, artifactData)
	if err != nil {
		t.Fatalf("sign artifact: %v", err)
	}

	verifier := NewSigner(nil)

	err = verifier.VerifyArtifactSignature(context.Background(), ports.VerifyReleaseArtifactRequest{
		ArtifactBytes:  artifactData,
		SignatureBytes: sigBytes,
		PublicKeyPEM:   pubPEM,
	})
	if err != nil {
		t.Fatalf("VerifyArtifactSignature (Ed25519) failed: %v", err)
	}
}

func TestReleaseVerifier_VerifyChecksum(t *testing.T) {
	verifier := NewSigner(nil)
	content := []byte("hello world binary content")
	hash := sha256.Sum256(content)
	hexStr := fmt.Sprintf("%x", hash)

	if err := verifier.VerifyChecksum(content, hexStr); err != nil {
		t.Fatalf("VerifyChecksum failed: %v", err)
	}

	if err := verifier.VerifyChecksum(content, "badchecksumhex"); err == nil {
		t.Fatal("expected checksum failure for bad hex, got nil")
	}
}
