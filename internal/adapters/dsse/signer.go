package dsse

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Signer implements ports.DSSESigner.
type Signer struct {
	log *slog.Logger
}

// NewSigner constructs a Signer. A nil logger defaults to slog.Default().
func NewSigner(log *slog.Logger) *Signer {
	if log == nil {
		log = slog.Default()
	}
	return &Signer{log: log}
}

// CreatePAE computes the canonical Pre-Authentication Encoding for payloadType and payload.
func (s *Signer) CreatePAE(payloadType string, payload []byte) []byte {
	return PAE(payloadType, payload)
}

// Sign wraps payloadBytes into a DSSEEnvelope signed by req.KeyPEM.
func (s *Signer) Sign(ctx context.Context, req ports.DSSESignRequest) (ports.DSSEEnvelope, error) {
	if len(req.PayloadBytes) == 0 {
		return ports.DSSEEnvelope{}, fmt.Errorf("dsse: empty payload bytes: %w", core.ErrInvalidRequest)
	}
	if req.PayloadType == "" {
		return ports.DSSEEnvelope{}, fmt.Errorf("dsse: empty payload type: %w", core.ErrInvalidRequest)
	}
	if len(req.KeyPEM) == 0 {
		return ports.DSSEEnvelope{}, fmt.Errorf("dsse: private key PEM is required: %w", core.ErrInvalidRequest)
	}

	privKey, err := parsePrivateKeyPEM(req.KeyPEM)
	if err != nil {
		return ports.DSSEEnvelope{}, fmt.Errorf("dsse: parse private key: %w", err)
	}

	paeBytes := s.CreatePAE(req.PayloadType, req.PayloadBytes)

	sigBytes, err := signPAE(privKey, paeBytes)
	if err != nil {
		return ports.DSSEEnvelope{}, fmt.Errorf("dsse: sign PAE: %w", err)
	}

	b64Payload := base64.StdEncoding.EncodeToString(req.PayloadBytes)
	b64Sig := base64.StdEncoding.EncodeToString(sigBytes)

	s.log.InfoContext(ctx, "dsse: signed payload envelope",
		"payloadType", req.PayloadType,
		"payload_size", len(req.PayloadBytes),
		"signature_bytes", len(sigBytes),
	)

	return ports.DSSEEnvelope{
		Payload:     b64Payload,
		PayloadType: req.PayloadType,
		Signatures: []ports.DSSESignature{
			{
				KeyID: req.KeyID,
				Sig:   b64Sig,
			},
		},
	}, nil
}

// Verify decodes envelope, validates its PAE signature against pubKeyPEM, and returns the raw payload bytes.
func (s *Signer) Verify(ctx context.Context, envelope ports.DSSEEnvelope, pubKeyPEM []byte) ([]byte, error) {
	if len(pubKeyPEM) == 0 {
		return nil, fmt.Errorf("dsse: public key PEM is required: %w", core.ErrInvalidRequest)
	}
	if envelope.Payload == "" || envelope.PayloadType == "" || len(envelope.Signatures) == 0 {
		return nil, fmt.Errorf("dsse: invalid envelope structure: %w", core.ErrInvalidRequest)
	}

	pubKey, err := parsePublicKeyPEM(pubKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("dsse: parse public key: %w", err)
	}

	payloadBytes, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return nil, fmt.Errorf("dsse: decode base64 payload: %w", err)
	}

	paeBytes := s.CreatePAE(envelope.PayloadType, payloadBytes)

	// Verify at least one valid signature matching pubKey
	verified := false
	for _, sigEntry := range envelope.Signatures {
		sigBytes, err := base64.StdEncoding.DecodeString(sigEntry.Sig)
		if err != nil {
			continue
		}
		if err := verifyPAE(pubKey, paeBytes, sigBytes); err == nil {
			verified = true
			break
		}
	}

	if !verified {
		return nil, errors.New("dsse: signature verification failed for all envelope signatures")
	}

	s.log.DebugContext(ctx, "dsse: verified envelope signature", "payloadType", envelope.PayloadType, "bytes", len(payloadBytes))
	return payloadBytes, nil
}

// signPAE signs PAE encoded bytes using ECDSA or Ed25519 private key.
func signPAE(priv crypto.PrivateKey, paeBytes []byte) ([]byte, error) {
	switch k := priv.(type) {
	case *ecdsa.PrivateKey:
		h := sha256.Sum256(paeBytes)
		return ecdsa.SignASN1(rand.Reader, k, h[:])
	case ed25519.PrivateKey:
		return ed25519.Sign(k, paeBytes), nil
	default:
		return nil, fmt.Errorf("unsupported key type: %T", priv)
	}
}

// verifyPAE verifies PAE signature bytes using ECDSA or Ed25519 public key.
func verifyPAE(pub crypto.PublicKey, paeBytes, sigBytes []byte) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		h := sha256.Sum256(paeBytes)
		if !ecdsa.VerifyASN1(k, h[:], sigBytes) {
			return errors.New("ecdsa PAE signature verification failed")
		}
		return nil
	case ed25519.PublicKey:
		if !ed25519.Verify(k, paeBytes, sigBytes) {
			return errors.New("ed25519 PAE signature verification failed")
		}
		return nil
	default:
		return fmt.Errorf("unsupported public key type: %T", pub)
	}
}

// parsePrivateKeyPEM parses PKCS#8, SEC1, or PKCS#1 PEM encoded private keys.
func parsePrivateKeyPEM(pemBytes []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM data found in private key")
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	return nil, errors.New("unsupported or malformed private key format")
}

// parsePublicKeyPEM parses PKIX or certificate PEM encoded public keys.
func parsePublicKeyPEM(pemBytes []byte) (crypto.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM data found in public key")
	}

	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		return pub, nil
	}
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		return cert.PublicKey, nil
	}

	return nil, errors.New("unsupported or malformed public key format")
}
