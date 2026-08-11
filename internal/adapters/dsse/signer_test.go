package dsse

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"

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

func TestPAEFormat(t *testing.T) {
	payloadType := "application/vnd.in-toto+json"
	payload := []byte(`{"_type":"https://in-toto.io/Statement/v0.1"}`)

	pae := PAE(payloadType, payload)
	expectedPrefix := "DSSEv1 28 application/vnd.in-toto+json 45 "
	if string(pae[:len(expectedPrefix)]) != expectedPrefix {
		t.Errorf("PAE prefix = %q, want %q", string(pae[:len(expectedPrefix)]), expectedPrefix)
	}
}

func TestDSSESigner_ECDSA_SignAndVerify(t *testing.T) {
	privPEM, pubPEM := generateECDSAPEM(t)

	signer := NewSigner(nil)

	payloadBytes := []byte(`{"_type":"https://in-toto.io/Statement/v0.1","subject":[{"name":"ghcr.io/acme/app"}]}`)
	req := ports.DSSESignRequest{
		PayloadBytes: payloadBytes,
		PayloadType:  ports.InTotoPayloadType,
		KeyPEM:       privPEM,
		KeyID:        "key-1",
	}

	envelope, err := signer.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	if envelope.PayloadType != ports.InTotoPayloadType {
		t.Errorf("PayloadType = %q, want %q", envelope.PayloadType, ports.InTotoPayloadType)
	}
	if len(envelope.Signatures) != 1 {
		t.Fatalf("len(Signatures) = %d, want 1", len(envelope.Signatures))
	}
	if envelope.Signatures[0].KeyID != "key-1" {
		t.Errorf("KeyID = %q, want %q", envelope.Signatures[0].KeyID, "key-1")
	}

	// Verify valid signature
	gotPayload, err := signer.Verify(context.Background(), envelope, pubPEM)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if string(gotPayload) != string(payloadBytes) {
		t.Errorf("gotPayload = %q, want %q", string(gotPayload), string(payloadBytes))
	}
}

func TestDSSESigner_Ed25519_SignAndVerify(t *testing.T) {
	privPEM, pubPEM := generateEd25519PEM(t)

	signer := NewSigner(nil)

	payloadBytes := []byte(`{"spdxVersion":"SPDX-2.3","name":"pokkum-sbom"}`)
	req := ports.DSSESignRequest{
		PayloadBytes: payloadBytes,
		PayloadType:  "application/spdx+json",
		KeyPEM:       privPEM,
	}

	envelope, err := signer.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("Sign (Ed25519) failed: %v", err)
	}

	gotPayload, err := signer.Verify(context.Background(), envelope, pubPEM)
	if err != nil {
		t.Fatalf("Verify (Ed25519) failed: %v", err)
	}

	if string(gotPayload) != string(payloadBytes) {
		t.Errorf("gotPayload = %q, want %q", string(gotPayload), string(payloadBytes))
	}
}

func TestDSSESigner_TamperDetection(t *testing.T) {
	privPEM, pubPEM := generateECDSAPEM(t)
	_, wrongPubPEM := generateECDSAPEM(t)

	signer := NewSigner(nil)

	payloadBytes := []byte(`{"_type":"https://in-toto.io/Statement/v0.1"}`)
	req := ports.DSSESignRequest{
		PayloadBytes: payloadBytes,
		PayloadType:  ports.InTotoPayloadType,
		KeyPEM:       privPEM,
	}

	envelope, err := signer.Sign(context.Background(), req)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	// Case 1: Wrong public key
	if _, err := signer.Verify(context.Background(), envelope, wrongPubPEM); err == nil {
		t.Error("Verify expected error for wrong public key, got nil")
	}

	// Case 2: Tampered Base64 payload in envelope
	tamperedEnvelope := envelope
	tamperedEnvelope.Payload = "dGFtcGVyZWQ=" // base64 for "tampered"
	if _, err := signer.Verify(context.Background(), tamperedEnvelope, pubPEM); err == nil {
		t.Error("Verify expected error for tampered envelope payload, got nil")
	}

	// Case 3: Tampered PayloadType
	tamperedTypeEnvelope := envelope
	tamperedTypeEnvelope.PayloadType = "application/json"
	if _, err := signer.Verify(context.Background(), tamperedTypeEnvelope, pubPEM); err == nil {
		t.Error("Verify expected error for tampered payloadType, got nil")
	}
}
