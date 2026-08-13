package sigstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// fixtureDir holds a real, captured keyless signature from
// gcr.io/distroless/cc-debian12:nonroot. See its README.md for provenance.
const fixtureDir = "testdata/distroless-nonroot"

// loadFixture reads the captured distroless keyless signature material into a
// request with the expected distroless identity and the embedded trust root.
//
// The captured Fulcio certificate expired minutes after it was issued, which
// is exactly why this fixture keeps working offline and indefinitely:
// certificate path validation is performed at the Rekor entry's integrated
// time, not at the current time. A test here that starts failing because of
// wall-clock time would indicate a real regression in how this adapter
// establishes the signing time.
func loadFixture(t *testing.T) ports.KeylessVerifyRequest {
	t.Helper()

	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(fixtureDir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		return b
	}

	return ports.KeylessVerifyRequest{
		PayloadBytes:    read("payload.json"),
		SignatureBytes:  read("signature.bin"),
		CertificatePEM:  read("certificate.pem"),
		ChainPEM:        read("chain.pem"),
		RekorBundleJSON: read("bundle.json"),
		Identity: ports.KeylessIdentity{
			Issuer: ports.DistrolessKeylessIssuer,
			SAN:    ports.DistrolessKeylessSAN,
		},
	}
}

// TestVerify_RealDistrolessSignature is the positive case: a real keyless
// signature from the distroless project verifies end to end against the
// embedded public-good trust root, with no network access.
func TestVerify_RealDistrolessSignature(t *testing.T) {
	v := NewVerifier(nil)

	got, err := v.Verify(context.Background(), loadFixture(t))
	if err != nil {
		t.Fatalf("Verify() on real distroless signature failed: %v", err)
	}

	if got.Issuer != ports.DistrolessKeylessIssuer {
		t.Errorf("Issuer = %q, want %q", got.Issuer, ports.DistrolessKeylessIssuer)
	}
	if got.SAN != ports.DistrolessKeylessSAN {
		t.Errorf("SAN = %q, want %q", got.SAN, ports.DistrolessKeylessSAN)
	}
	if got.RekorLogID == "" {
		t.Error("RekorLogID is empty")
	}
	if got.RekorLogIndex <= 0 {
		t.Errorf("RekorLogIndex = %d, want > 0", got.RekorLogIndex)
	}
	if got.IntegratedTime.IsZero() {
		t.Error("IntegratedTime is zero")
	}
	if got.CertificateNotBefore.IsZero() || got.CertificateNotAfter.IsZero() {
		t.Errorf("certificate validity window incomplete: %v .. %v", got.CertificateNotBefore, got.CertificateNotAfter)
	}
	if !got.CertificateNotAfter.After(got.CertificateNotBefore) {
		t.Errorf("certificate NotAfter %v is not after NotBefore %v", got.CertificateNotAfter, got.CertificateNotBefore)
	}
	// The Rekor entry must fall inside the certificate's validity window;
	// this is the property that lets an expired short-lived cert still be
	// trusted, so assert the fixture actually exercises it.
	if got.IntegratedTime.Before(got.CertificateNotBefore) || got.IntegratedTime.After(got.CertificateNotAfter) {
		t.Errorf("IntegratedTime %v outside certificate validity %v .. %v",
			got.IntegratedTime, got.CertificateNotBefore, got.CertificateNotAfter)
	}

	t.Logf("verified: issuer=%s san=%s rekor_log_id=%s rekor_log_index=%d integrated_time=%s cert_valid=%s..%s",
		got.Issuer, got.SAN, got.RekorLogID, got.RekorLogIndex, got.IntegratedTime,
		got.CertificateNotBefore, got.CertificateNotAfter)
}

// TestVerify_TamperedSignatureFailsClosed flips one byte of the signature and
// one byte of the payload and confirms verification fails with one of this
// package's sentinels rather than passing or panicking.
func TestVerify_TamperedSignatureFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		tamper  func(*ports.KeylessVerifyRequest)
		wantAny []error
	}{
		{
			name: "flipped signature byte",
			tamper: func(r *ports.KeylessVerifyRequest) {
				r.SignatureBytes[len(r.SignatureBytes)-1] ^= 0x01
			},
			// The Rekor entry commits to the signature bytes, so the tlog
			// cross-check rejects this before signature verification does.
			wantAny: []error{ErrTlogInvalid, ErrChainInvalid},
		},
		{
			name: "flipped payload byte",
			tamper: func(r *ports.KeylessVerifyRequest) {
				r.PayloadBytes[len(r.PayloadBytes)/2] ^= 0x01
			},
			// The Rekor entry commits to the payload digest too.
			wantAny: []error{ErrTlogInvalid, ErrChainInvalid},
		},
	}

	v := NewVerifier(nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := loadFixture(t)
			tt.tamper(&req)

			got, err := v.Verify(context.Background(), req)
			if err == nil {
				t.Fatalf("Verify() accepted tampered material and returned %+v", got)
			}
			for _, want := range tt.wantAny {
				if errors.Is(err, want) {
					t.Logf("rejected as expected: %v", err)
					return
				}
			}
			t.Fatalf("Verify() error %v matches none of the expected sentinels %v", err, tt.wantAny)
		})
	}
}

// TestVerify_EmptyIdentityRefused pins the invariant that an unconstrained
// identity is refused before any Sigstore code runs.
func TestVerify_EmptyIdentityRefused(t *testing.T) {
	req := loadFixture(t)
	req.Identity = ports.KeylessIdentity{}

	_, err := NewVerifier(nil).Verify(context.Background(), req)
	if err == nil {
		t.Fatal("Verify() accepted an empty identity; it must refuse to verify against an unconstrained identity")
	}
	// It must not be reported as a signature problem — this is a refusal.
	for _, sentinel := range []error{ErrNoBundle, ErrMalformedMaterial, ErrChainInvalid, ErrIdentityMismatch, ErrTlogInvalid} {
		if errors.Is(err, sentinel) {
			t.Errorf("empty-identity refusal wrongly classified as %v: %v", sentinel, err)
		}
	}
}

// TestVerify_MissingMaterialIsErrNoBundle pins the ErrNoBundle signal the
// resolver uses to tell "not keyless-signed" apart from "failed to verify".
func TestVerify_MissingMaterialIsErrNoBundle(t *testing.T) {
	tests := []struct {
		name  string
		strip func(*ports.KeylessVerifyRequest)
	}{
		{"no certificate annotation", func(r *ports.KeylessVerifyRequest) { r.CertificatePEM = nil }},
		{"no bundle annotation", func(r *ports.KeylessVerifyRequest) { r.RekorBundleJSON = nil }},
		{"neither", func(r *ports.KeylessVerifyRequest) { r.CertificatePEM, r.RekorBundleJSON = nil, nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := loadFixture(t)
			tt.strip(&req)

			_, err := NewVerifier(nil).Verify(context.Background(), req)
			if !errors.Is(err, ErrNoBundle) {
				t.Fatalf("Verify() error = %v, want one wrapping ErrNoBundle", err)
			}
		})
	}
}

// TestVerify_MalformedMaterial covers the assembleV01Bundle failure paths that
// must be reported as malformed material rather than as a failed verification.
func TestVerify_MalformedMaterial(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*ports.KeylessVerifyRequest)
	}{
		{"certificate is not PEM", func(r *ports.KeylessVerifyRequest) { r.CertificatePEM = []byte("not a pem block") }},
		{"certificate PEM is not X.509", func(r *ports.KeylessVerifyRequest) {
			r.CertificatePEM = []byte("-----BEGIN CERTIFICATE-----\nZGVhZGJlZWY=\n-----END CERTIFICATE-----\n")
		}},
		{"bundle is not JSON", func(r *ports.KeylessVerifyRequest) { r.RekorBundleJSON = []byte("{oops") }},
		{"bundle logID is not hex", func(r *ports.KeylessVerifyRequest) {
			r.RekorBundleJSON = []byte(`{"SignedEntryTimestamp":"MEQ=","Payload":{"body":"e30=","integratedTime":1,"logIndex":1,"logID":"zzzz"}}`)
		}},
		{"bundle missing SignedEntryTimestamp", func(r *ports.KeylessVerifyRequest) {
			r.RekorBundleJSON = []byte(`{"Payload":{"body":"e30=","integratedTime":1,"logIndex":1,"logID":"ab"}}`)
		}},
		{"empty payload", func(r *ports.KeylessVerifyRequest) { r.PayloadBytes = nil }},
		{"empty signature", func(r *ports.KeylessVerifyRequest) { r.SignatureBytes = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := loadFixture(t)
			tt.corrupt(&req)

			_, err := NewVerifier(nil).Verify(context.Background(), req)
			if !errors.Is(err, ErrMalformedMaterial) {
				t.Fatalf("Verify() error = %v, want one wrapping ErrMalformedMaterial", err)
			}
		})
	}
}

// TestVerify_WrongIdentityIsIdentityMismatch confirms a cryptographically
// valid signature signed by somebody else is rejected, and is distinguishable
// from a broken signature. It covers both halves of the identity: a wrong SAN
// with the correct issuer, and a wrong issuer with the correct SAN. These are
// deliberately distinct cases — NewShortCertificateIdentity ANDs issuer and
// SAN together, so a verifier that only ever varied one field in testing
// could hide a bug where the other field was silently ignored.
func TestVerify_WrongIdentityIsIdentityMismatch(t *testing.T) {
	tests := []struct {
		name     string
		identity ports.KeylessIdentity
	}{
		{
			name: "wrong SAN, correct issuer",
			identity: ports.KeylessIdentity{
				Issuer: ports.DistrolessKeylessIssuer,
				SAN:    "attacker@example.com",
			},
		},
		{
			name: "correct SAN, wrong issuer",
			identity: ports.KeylessIdentity{
				Issuer: "https://attacker.example.com",
				SAN:    ports.DistrolessKeylessSAN,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := loadFixture(t)
			req.Identity = tt.identity

			_, err := NewVerifier(nil).Verify(context.Background(), req)
			if !errors.Is(err, ErrIdentityMismatch) {
				t.Fatalf("Verify() error = %v, want one wrapping ErrIdentityMismatch", err)
			}
		})
	}
}

// TestVerify_ChainAnnotationIsNotTrusted asserts the ChainPEM field is inert:
// a signature that verifies must still verify with the chain annotation
// removed, and must still verify with garbage in it. If either changes, the
// adapter has started depending on attacker-suppliable chain material.
func TestVerify_ChainAnnotationIsNotTrusted(t *testing.T) {
	v := NewVerifier(nil)

	for _, tt := range []struct {
		name  string
		chain []byte
	}{
		{"absent", nil},
		{"garbage", []byte("-----BEGIN CERTIFICATE-----\nZGVhZGJlZWY=\n-----END CERTIFICATE-----\n")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := loadFixture(t)
			req.ChainPEM = tt.chain

			if _, err := v.Verify(context.Background(), req); err != nil {
				t.Fatalf("Verify() with %s chain annotation failed: %v", tt.name, err)
			}
		})
	}
}

// TestVerify_CorruptedRekorSETFailsClosed corrupts the Rekor
// SignedEntryTimestamp (the inclusion promise) directly, as distinct from
// TestVerify_TamperedSignatureFailsClosed which mutates SignatureBytes /
// PayloadBytes. Those mutate data the Rekor entry's CanonicalizedBody
// commits to, so they exercise the digest cross-check inside tlog entry
// verification failing — not SET cryptographic verification itself. This
// test leaves the canonicalized body (and therefore the digests it commits
// to) untouched and only breaks the SET bytes, so the only thing that can
// fail is "does this SET verify against the trusted root's Rekor log key".
//
// The corrupted SET no longer decodes to a valid ASN.1 DER ECDSA signature
// (flipping a byte in the middle of a DER-encoded signature almost always
// breaks its structure), so what actually gets exercised is
// VerifyTransparencyLogInclusion's signature-parsing/verification path
// rejecting malformed signature bytes — still squarely ErrTlogInvalid, and
// still distinct from the digest cross-check the tamper tests exercise.
// Observed failure text: "not enough verified log entries from transparency
// log: 0 < 1" (sigstore-go does not expose a typed error for this, hence the
// ErrTlogInvalid sentinel classification in verifier.go's dedicated
// VerifyTransparencyLogInclusion call rather than pattern-matching text).
func TestVerify_CorruptedRekorSETFailsClosed(t *testing.T) {
	req := loadFixture(t)

	// Pull the SignedEntryTimestamp value out of the fixture's bundle.json so
	// the mutation tracks the real fixture rather than a hardcoded copy of it.
	var parsed struct {
		SignedEntryTimestamp string `json:"SignedEntryTimestamp"`
	}
	if err := json.Unmarshal(req.RekorBundleJSON, &parsed); err != nil {
		t.Fatalf("parse fixture bundle.json: %v", err)
	}
	if parsed.SignedEntryTimestamp == "" {
		t.Fatal("fixture bundle.json has an empty SignedEntryTimestamp; cannot corrupt it")
	}

	setBytes, err := base64.StdEncoding.DecodeString(parsed.SignedEntryTimestamp)
	if err != nil {
		t.Fatalf("decode fixture SignedEntryTimestamp as base64: %v", err)
	}
	setBytes[len(setBytes)/2] ^= 0xFF
	corrupted := base64.StdEncoding.EncodeToString(setBytes)

	// Substring replace rather than re-marshaling the whole struct, so every
	// other byte of the fixture (field order, the canonicalized body, etc.)
	// is left exactly as captured.
	mutated := bytes.Replace(req.RekorBundleJSON, []byte(parsed.SignedEntryTimestamp), []byte(corrupted), 1)
	if bytes.Equal(mutated, req.RekorBundleJSON) {
		t.Fatal("substring replace did not change bundle.json; SignedEntryTimestamp value was not found verbatim")
	}
	req.RekorBundleJSON = mutated

	_, err = NewVerifier(nil).Verify(context.Background(), req)
	if !errors.Is(err, ErrTlogInvalid) {
		t.Fatalf("Verify() error = %v, want one wrapping ErrTlogInvalid", err)
	}
	t.Logf("rejected as expected: %v", err)
}

// TestVerify_WrongTrustedRootFailsClosed proves the TrustedRootJSON override
// is actually read and enforced rather than the request field being silently
// ignored in favor of the embedded default: no other test in this package
// ever sets it to a non-empty value.
//
// testdata/trusted-root-wrong-keys.json is a copy of the embedded public-good
// trust root with the Rekor tlog's public key swapped for an unrelated,
// freshly generated P-256 key (same DER shape, so the root still parses as
// structurally valid — this is "wrong root", not the already-covered
// "malformed root" case). Its log ID was deliberately left unchanged, so
// lookup-by-log-ID still finds the (now wrong) key; see
// testdata/README.md for exactly how it was derived.
//
// Because the Rekor key no longer matches, the same SET-verification step
// TestVerify_CorruptedRekorSETFailsClosed exercises is what trips here too —
// confirmed by observation, not assumed, since a mismatched Fulcio CA could
// just as plausibly have surfaced as ErrChainInvalid first.
func TestVerify_WrongTrustedRootFailsClosed(t *testing.T) {
	wrongRoot, err := os.ReadFile(filepath.Join("testdata", "trusted-root-wrong-keys.json"))
	if err != nil {
		t.Fatalf("read testdata/trusted-root-wrong-keys.json: %v", err)
	}

	req := loadFixture(t)
	req.TrustedRootJSON = wrongRoot

	_, err = NewVerifier(nil).Verify(context.Background(), req)
	if err == nil {
		t.Fatal("Verify() accepted the fixture against a trust root with a substituted Rekor log key")
	}
	if !errors.Is(err, ErrTlogInvalid) {
		t.Fatalf("Verify() error = %v, want one wrapping ErrTlogInvalid (the Rekor SET no longer verifies against the substituted log key)", err)
	}
	t.Logf("rejected as expected: %v", err)
}

// TestVerify_ExplicitDefaultTrustedRootStillSucceeds is the positive half of
// the TrustedRootJSON override proof: passing the real embedded trust root
// explicitly through the request field (instead of relying on the
// empty-means-embedded-default fallback) must still verify. Without this,
// TestVerify_WrongTrustedRootFailsClosed alone would not distinguish "the
// override path works and this root is wrong" from "the override path is
// broken in a way that always fails".
func TestVerify_ExplicitDefaultTrustedRootStillSucceeds(t *testing.T) {
	req := loadFixture(t)
	req.TrustedRootJSON = DefaultTrustedRootJSON()

	if _, err := NewVerifier(nil).Verify(context.Background(), req); err != nil {
		t.Fatalf("Verify() with the embedded trust root passed explicitly via TrustedRootJSON failed: %v", err)
	}
}
