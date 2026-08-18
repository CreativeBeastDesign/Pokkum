package ports

import (
	"context"
	"errors"
	"time"
)

// ErrNoKeylessBundle means the artifact carries no keyless signature material
// at all — the Fulcio certificate and/or Rekor bundle annotations are absent.
//
// It is part of the KeylessVerifier contract rather than an implementation
// detail of whichever adapter satisfies it: it is the ONE error a caller may
// legitimately treat as "this artifact is not keyless-signed, try another
// verification mode" instead of "verification failed". Every other error a
// KeylessVerifier returns means material was present and did not verify, and
// must never be downgraded to a skip. Callers therefore have to be able to
// match this sentinel through the interface, without importing the concrete
// verifier.
var ErrNoKeylessBundle = errors.New("no keyless signature material")

// KeylessIdentity is the expected Fulcio certificate identity a keyless
// Sigstore signature must match. At least one of Issuer/IssuerRegex and one
// of SAN/SANRegex must be set — a zero value must never be treated as "trust
// any Fulcio-issued certificate"; callers must check Empty() and refuse to
// verify against an empty identity.
type KeylessIdentity struct {
	// Issuer is the exact expected OIDC issuer URL (e.g.
	// "https://accounts.google.com"). Mutually exclusive in practice with
	// IssuerRegex, but both may be set; either match is sufficient.
	Issuer string

	// IssuerRegex is a regular expression the OIDC issuer must match.
	IssuerRegex string

	// SAN is the exact expected certificate Subject Alternative Name, in
	// whichever form Fulcio embedded it (an email address or a URI).
	SAN string

	// SANRegex is a regular expression the SAN must match.
	SANRegex string
}

// Empty reports whether no identity constraint at all was expressed. A
// KeylessVerifier implementation must reject verification when the effective
// identity is Empty() rather than treat it as "match anything".
func (k KeylessIdentity) Empty() bool {
	return k.Issuer == "" && k.IssuerRegex == "" && k.SAN == "" && k.SANRegex == ""
}

// KeylessVerifyRequest carries one Cosign keyless (Sigstore) signature
// artifact's material, as read from the legacy Cosign signature-tag
// convention: a "<repo>:<alg>-<hex>.sig" manifest whose layer annotations
// carry the certificate, chain, and Rekor bundle alongside the usual
// simple-signing payload and signature.
type KeylessVerifyRequest struct {
	// PayloadBytes is the signed Simple Signing JSON payload (the artifact
	// the signature and Rekor entry cover).
	PayloadBytes []byte

	// SignatureBytes is the raw signature over PayloadBytes, as read from the
	// dev.cosignproject.cosign/signature annotation.
	SignatureBytes []byte

	// CertificatePEM is the Fulcio-issued leaf certificate PEM, from the
	// dev.sigstore.cosign/certificate annotation. Required.
	CertificatePEM []byte

	// ChainPEM is the certificate chain PEM from the
	// dev.sigstore.cosign/chain annotation. Informational only: a correct
	// KeylessVerifier resolves the trust chain from its own trusted root, not
	// from this attacker-suppliable field.
	ChainPEM []byte

	// RekorBundleJSON is the Rekor inclusion material (a cosign-format
	// "bundle" JSON: SignedEntryTimestamp + Payload{body, integratedTime,
	// logIndex, logID}) from the dev.sigstore.cosign/bundle annotation.
	// Required.
	RekorBundleJSON []byte

	// Identity is the expected certificate identity. Must not be Empty().
	Identity KeylessIdentity

	// TrustedRootJSON is the Sigstore trusted-root snapshot (Fulcio root, CT
	// log key, Rekor log key) to verify against, in the
	// application/vnd.dev.sigstore.trustedroot+json format. Empty means the
	// implementation's own embedded default (the public-good Sigstore
	// instance) should be used.
	TrustedRootJSON []byte
}

// KeylessVerifyResult is what was proven by a successful Verify call, for
// logging and provenance display.
type KeylessVerifyResult struct {
	// Issuer is the verified OIDC issuer from the certificate.
	Issuer string

	// SAN is the verified Subject Alternative Name from the certificate.
	SAN string

	// CertificateNotBefore and CertificateNotAfter are the leaf
	// certificate's validity window (short-lived, typically minutes).
	CertificateNotBefore time.Time
	CertificateNotAfter  time.Time

	// RekorLogID identifies which transparency log instance the entry was
	// verified against.
	RekorLogID string

	// RekorLogIndex is the entry's index in that log.
	RekorLogIndex int64

	// IntegratedTime is when Rekor recorded the entry.
	IntegratedTime time.Time
}

// KeylessVerifier validates a Sigstore keyless signature: it checks the
// certificate chains to a trusted Fulcio root, that the certificate's
// identity (issuer + SAN) matches the expected KeylessIdentity, and that the
// signature's Rekor transparency-log entry is valid. Implemented by
// internal/adapters/sigstore.
type KeylessVerifier interface {
	// Verify validates req and returns what was proven. It returns an error
	// if req.Identity is Empty(), if the certificate/chain/Rekor material is
	// malformed, if the certificate does not chain to a trusted root, if the
	// certificate's identity does not match req.Identity, or if the Rekor
	// entry cannot be verified.
	Verify(ctx context.Context, req KeylessVerifyRequest) (KeylessVerifyResult, error)
}
