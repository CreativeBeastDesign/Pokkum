package cosign

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var (
	_ ports.CosignSigner    = (*Signer)(nil)
	_ ports.ReleaseVerifier = (*Signer)(nil)
)

// Signer implements ports.CosignSigner.
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

// CreatePayload builds the canonical Red Hat Simple Signing JSON payload for a request.
func (s *Signer) CreatePayload(req ports.CosignSignRequest) ([]byte, error) {
	p, err := buildPayload(req)
	if err != nil {
		return nil, err
	}
	return marshalPayload(p)
}

// Sign creates a signed CosignSignatureBundle for a request using req.KeyPEM.
func (s *Signer) Sign(ctx context.Context, req ports.CosignSignRequest) (ports.CosignSignatureBundle, error) {
	if len(req.KeyPEM) == 0 {
		return ports.CosignSignatureBundle{}, fmt.Errorf("cosign: private key PEM is required: %w", core.ErrInvalidRequest)
	}

	privKey, err := parsePrivateKeyPEM(req.KeyPEM)
	if err != nil {
		return ports.CosignSignatureBundle{}, fmt.Errorf("cosign: parse private key: %w", err)
	}

	payloadBytes, err := s.CreatePayload(req)
	if err != nil {
		return ports.CosignSignatureBundle{}, err
	}

	sigBytes, err := signPayload(privKey, payloadBytes)
	if err != nil {
		return ports.CosignSignatureBundle{}, fmt.Errorf("cosign: sign payload: %w", err)
	}

	b64Sig := base64.StdEncoding.EncodeToString(sigBytes)

	s.log.InfoContext(ctx, "cosign: signed payload",
		"repo", req.Repo,
		"digest", req.Digest.String(),
		"signature_bytes", len(sigBytes),
	)

	return ports.CosignSignatureBundle{
		PayloadBytes:    payloadBytes,
		SignatureBytes:  sigBytes,
		Base64Signature: b64Sig,
		Repo:            req.Repo,
		Digest:          req.Digest,
	}, nil
}

// Verify validates a CosignSignatureBundle against pubKeyPEM, expectedRepo, and expectedDigest.
func (s *Signer) Verify(ctx context.Context, bundle ports.CosignSignatureBundle, pubKeyPEM []byte, expectedRepo string, expectedDigest v1.Hash) error {
	if len(pubKeyPEM) == 0 {
		return fmt.Errorf("cosign: public key PEM is required: %w", core.ErrInvalidRequest)
	}
	if len(bundle.SignatureBytes) == 0 && bundle.Base64Signature != "" {
		if decoded, err := base64.StdEncoding.DecodeString(bundle.Base64Signature); err == nil {
			bundle.SignatureBytes = decoded
		}
	}
	if len(bundle.PayloadBytes) == 0 || len(bundle.SignatureBytes) == 0 {
		return fmt.Errorf("cosign: empty signature bundle payload or signature: %w", core.ErrInvalidRequest)
	}

	pubKey, err := parsePublicKeyPEM(pubKeyPEM)
	if err != nil {
		return fmt.Errorf("cosign: parse public key: %w", err)
	}

	// 1. Unmarshal payload claims
	var payload ports.CosignSimpleSigningPayload
	if err := json.Unmarshal(bundle.PayloadBytes, &payload); err != nil {
		return fmt.Errorf("cosign: invalid JSON payload: %w", err)
	}

	// 2. Validate payload claims
	if payload.Critical.Type != ports.CosignSimpleSigningType {
		return fmt.Errorf("cosign: payload type %q != %q: %w", payload.Critical.Type, ports.CosignSimpleSigningType, core.ErrInvalidRequest)
	}

	actualRepo := payload.Critical.Identity.DockerReference
	if strings.TrimSpace(expectedRepo) != "" && actualRepo != strings.TrimSpace(expectedRepo) {
		return fmt.Errorf("cosign: payload docker-reference %q != expected %q: %w", actualRepo, expectedRepo, core.ErrInvalidRequest)
	}

	actualDigest := payload.Critical.Image.DockerManifestDigest
	if expectedDigest.Hex != "" && actualDigest != expectedDigest.String() {
		return fmt.Errorf("cosign: payload docker-manifest-digest %q != expected %q: %w", actualDigest, expectedDigest.String(), core.ErrInvalidRequest)
	}

	// 3. Verify cryptographic signature
	if err := verifyPayload(pubKey, bundle.PayloadBytes, bundle.SignatureBytes); err != nil {
		return fmt.Errorf("cosign: signature verification failed: %w", err)
	}

	s.log.DebugContext(ctx, "cosign: verified signature bundle", "repo", actualRepo, "digest", actualDigest)
	return nil
}

// VerifyArtifactSignature validates that signatureBytes is a valid signature for artifactBytes using pubKeyPEM.
func (s *Signer) VerifyArtifactSignature(ctx context.Context, req ports.VerifyReleaseArtifactRequest) error {
	if len(req.PublicKeyPEM) == 0 {
		return fmt.Errorf("cosign: public key PEM is required: %w", core.ErrInvalidRequest)
	}
	if len(req.ArtifactBytes) == 0 || len(req.SignatureBytes) == 0 {
		return fmt.Errorf("cosign: empty artifact or signature: %w", core.ErrInvalidRequest)
	}

	pubKey, err := parsePublicKeyPEM(req.PublicKeyPEM)
	if err != nil {
		return fmt.Errorf("cosign: parse public key: %w", err)
	}

	sigBytes := req.SignatureBytes
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(req.SignatureBytes))); err == nil && len(decoded) > 0 {
		sigBytes = decoded
	}

	if err := verifyPayload(pubKey, req.ArtifactBytes, sigBytes); err != nil {
		return fmt.Errorf("cosign: artifact signature verification failed: %w: %w", err, core.ErrReleaseVerificationFailed)
	}

	s.log.DebugContext(ctx, "cosign: verified artifact signature", "artifact_bytes", len(req.ArtifactBytes))
	return nil
}

// VerifyChecksum validates that content SHA256 matches expectedChecksumHex.
func (s *Signer) VerifyChecksum(content []byte, expectedChecksumHex string) error {
	h := sha256.Sum256(content)
	actualHex := fmt.Sprintf("%x", h)
	expectedHex := strings.TrimSpace(strings.ToLower(expectedChecksumHex))
	if actualHex != expectedHex {
		return fmt.Errorf("cosign: checksum mismatch: computed %s != expected %s: %w", actualHex, expectedHex, core.ErrReleaseVerificationFailed)
	}
	return nil
}

// signPayload signs payloadBytes using private key (ECDSA or Ed25519).

func signPayload(priv crypto.PrivateKey, payloadBytes []byte) ([]byte, error) {
	switch k := priv.(type) {
	case *ecdsa.PrivateKey:
		h := sha256.Sum256(payloadBytes)
		return ecdsa.SignASN1(rand.Reader, k, h[:])
	case ed25519.PrivateKey:
		return ed25519.Sign(k, payloadBytes), nil
	default:
		return nil, fmt.Errorf("unsupported key type: %T", priv)
	}
}

// verifyPayload checks signatureBytes against payloadBytes using public key.
func verifyPayload(pub crypto.PublicKey, payloadBytes, signatureBytes []byte) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		h := sha256.Sum256(payloadBytes)
		if !ecdsa.VerifyASN1(k, h[:], signatureBytes) {
			return errors.New("ecdsa signature verification failed")
		}
		return nil
	case ed25519.PublicKey:
		if !ed25519.Verify(k, payloadBytes, signatureBytes) {
			return errors.New("ed25519 signature verification failed")
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

	// 1. Try PKCS#8
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	// 2. Try EC private key (SEC1)
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	// 3. Try PKCS#1 RSA private key
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

	// 1. Try PKIX public key
	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		return pub, nil
	}
	// 2. Try Certificate public key
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		return cert.PublicKey, nil
	}

	return nil, errors.New("unsupported or malformed public key format")
}
