package sigstore

import (
	"context"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// oidFulcioIssuer is Fulcio's OIDC issuer extension, the value sigstore-go's
// verify.IssuerMatcher actually compares against (via
// certificate.Summary.Issuer, populated from this OID and its .1.8 successor).
var oidFulcioIssuer = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}

// TestKeylessIdentity_MustNotBeDerivedFromTheCertificate pins the two facts
// that together made a real bug possible in
// internal/adapters/provenance/resolver.go, which built its "expected" identity
// out of the certificate it was about to verify:
//
//		identity := ports.KeylessIdentity{Issuer: cert.Issuer.CommonName}
//		if len(cert.EmailAddresses) > 0 { identity.SAN = cert.EmailAddresses[0] }
//
//	 1. cert.Issuer.CommonName is the *CA's* Common Name ("sigstore-intermediate"
//	    for the public-good Fulcio), which is a completely different string from
//	    the OIDC issuer sigstore-go matches on. So that comparison could never
//	    match and the whole keyless branch was dead code.
//
//	 2. Deriving the expectation from the certificate at all is the far more
//	    dangerous half. Had the bug been "fixed" by reading the OIDC extension
//	    out of the certificate instead, the check would compare the certificate
//	    against itself and succeed for every certificate Fulcio has ever issued —
//	    i.e. for anyone with a GitHub or Google account. The assertion below that
//	    the extension-derived identity *does* verify is therefore not a nice
//	    property; it is the trap, recorded so nobody walks into it again.
//
// The expected identity must always come from the operator (a flag, a config
// file, an explicitly chosen publisher default), never from the material under
// verification.
func TestKeylessIdentity_MustNotBeDerivedFromTheCertificate(t *testing.T) {
	req := loadFixture(t)

	block, _ := pem.Decode(req.CertificatePEM)
	if block == nil {
		t.Fatal("fixture certificate.pem is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse fixture certificate: %v", err)
	}

	var oidcIssuer string
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oidFulcioIssuer) {
			oidcIssuer = string(ext.Value)
		}
	}
	if oidcIssuer == "" {
		t.Fatal("fixture certificate carries no 1.3.6.1.4.1.57264.1.1 OIDC issuer extension")
	}

	// Fact 1: the CA CN and the OIDC issuer are different strings.
	if cert.Issuer.CommonName == oidcIssuer {
		t.Fatalf("expected cert.Issuer.CommonName (%q) to differ from the OIDC issuer extension (%q)",
			cert.Issuer.CommonName, oidcIssuer)
	}
	if got, want := cert.Issuer.CommonName, "sigstore-intermediate"; got != want {
		t.Errorf("fixture CA CommonName = %q, want %q (public-good Fulcio intermediate)", got, want)
	}
	if got, want := oidcIssuer, ports.DistrolessKeylessIssuer; got != want {
		t.Errorf("fixture OIDC issuer = %q, want %q", got, want)
	}

	sanFromCert := ""
	switch {
	case len(cert.URIs) > 0:
		sanFromCert = cert.URIs[0].String()
	case len(cert.EmailAddresses) > 0:
		sanFromCert = cert.EmailAddresses[0]
	}

	v := NewVerifier(nil)

	// Fact 1, empirically: an identity whose issuer is the CA CN is rejected
	// as an identity mismatch, so the old code's keyless path could never
	// succeed for any genuine Fulcio certificate.
	caCNDerived := req
	caCNDerived.Identity = ports.KeylessIdentity{Issuer: cert.Issuer.CommonName, SAN: sanFromCert}
	if _, err := v.Verify(context.Background(), caCNDerived); err == nil {
		t.Fatal("expected a CA-CommonName-derived identity to be rejected, but verification succeeded")
	} else if !errors.Is(err, ErrIdentityMismatch) {
		t.Errorf("CA-CommonName-derived identity: got %v, want ErrIdentityMismatch", err)
	}

	// Fact 2, empirically: the self-derived identity taken from the correct
	// extension verifies, which is precisely why self-derivation must never be
	// used as the expectation.
	selfDerived := req
	selfDerived.Identity = ports.KeylessIdentity{Issuer: oidcIssuer, SAN: sanFromCert}
	if _, err := v.Verify(context.Background(), selfDerived); err != nil {
		t.Fatalf("self-derived identity unexpectedly failed (%v) — if this now fails for a "+
			"reason other than a broken fixture, the tautology this test documents may have "+
			"changed shape; re-derive the reasoning before relaxing it", err)
	}

	// And an unrelated operator-supplied identity is still rejected, proving
	// the check has real content when the expectation comes from outside.
	foreign := req
	foreign.Identity = ports.KeylessIdentity{
		Issuer: "https://token.actions.githubusercontent.com",
		SAN:    "https://github.com/attacker/repo/.github/workflows/build.yml@refs/heads/main",
	}
	if _, err := v.Verify(context.Background(), foreign); err == nil {
		t.Fatal("expected a foreign operator-supplied identity to be rejected")
	} else if !errors.Is(err, ErrIdentityMismatch) {
		t.Errorf("foreign identity: got %v, want ErrIdentityMismatch", err)
	}
}
