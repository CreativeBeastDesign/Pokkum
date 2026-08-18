package provenance_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/provenance"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// The identity values used throughout these tests. They deliberately do not
// appear anywhere in the certificate the tests build, so a resolver that
// re-derived the expectation from the certificate could not produce them.
const (
	expectedIssuer = "https://token.actions.githubusercontent.com"
	expectedSAN    = "https://github.com/acme/app/.github/workflows/release.yml@refs/heads/main"

	// What a certificate-derived expectation would have produced instead.
	certCACommonName = "test-fulcio-intermediate"
	certOIDCIssuer   = "https://accounts.example.test"
	certSANEmail     = "signer@somebody-elses-project.test"
)

var oidFulcioIssuer = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}

// recordingKeylessVerifier is a ports.KeylessVerifier test double that records
// every identity it is asked to verify against. It exists to answer one
// question the real verifier cannot: *what expectation did the resolver pass
// in* — which is exactly where the bug lived.
type recordingKeylessVerifier struct {
	mu       sync.Mutex
	seen     []ports.KeylessIdentity
	result   ports.KeylessVerifyResult
	failWith error
}

func (f *recordingKeylessVerifier) Verify(_ context.Context, req ports.KeylessVerifyRequest) (ports.KeylessVerifyResult, error) {
	f.mu.Lock()
	f.seen = append(f.seen, req.Identity)
	f.mu.Unlock()

	// Mirror the real verifier's non-negotiable refusal, so a resolver that
	// ever passes an empty identity fails here too rather than being papered
	// over by a permissive double.
	if req.Identity.Empty() {
		return ports.KeylessVerifyResult{}, errors.New("fake verifier: refusing an unconstrained identity")
	}
	if f.failWith != nil {
		return ports.KeylessVerifyResult{}, f.failWith
	}
	return f.result, nil
}

func (f *recordingKeylessVerifier) calls() []ports.KeylessIdentity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.KeylessIdentity(nil), f.seen...)
}

// fulcioLikeCertPEM builds a certificate shaped like a real Fulcio leaf: a CA
// Common Name that is NOT the OIDC issuer, a real 1.3.6.1.4.1.57264.1.1 OIDC
// issuer extension, and an email SAN. The three values are mutually distinct so
// a test can tell exactly which field a wrong implementation read.
func fulcioLikeCertPEM(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:   big.NewInt(4242),
		Subject:        pkix.Name{},
		Issuer:         pkix.Name{CommonName: certCACommonName, Organization: []string{"sigstore.dev"}},
		EmailAddresses: []string{certSANEmail},
		NotBefore:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:       time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC),
		ExtraExtensions: []pkix.Extension{{
			Id:    oidFulcioIssuer,
			Value: []byte(certOIDCIssuer),
		}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// keylessSignedImage pushes an image plus a `.sig` tag carrying keyless
// material (certificate + Rekor bundle annotations). payloadRepo/payloadDigest
// let a test make the signed simple-signing payload describe a *different*
// image than the one being verified.
type keylessImage struct {
	repo   string
	ref    string
	digest v1.Hash
}

func pushKeylessSignedImage(t *testing.T, host, repoName, payloadType, payloadRepo, payloadDigest string) keylessImage {
	t.Helper()

	targetRepo := fmt.Sprintf("%s/%s", host, repoName)
	img := createTestImage(t, nil)
	tagRef, err := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatalf("parse tag: %v", err)
	}
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	if payloadRepo == "" {
		payloadRepo = targetRepo
	}
	if payloadDigest == "" {
		payloadDigest = imgDigest.String()
	}
	if payloadType == "" {
		payloadType = ports.CosignContainerImageSignatureType
	}

	payloadBytes, err := json.Marshal(ports.CosignSimpleSigningPayload{
		Critical: ports.CosignCritical{
			Identity: ports.CosignCriticalIdentity{DockerReference: payloadRepo},
			Image:    ports.CosignCriticalImage{DockerManifestDigest: payloadDigest},
			Type:     payloadType,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	sigLayer := static.NewLayer(payloadBytes, types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"))
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer: sigLayer,
		Annotations: map[string]string{
			sigstore.CosignSignatureAnnotation:   base64.StdEncoding.EncodeToString([]byte("a-signature")),
			sigstore.CosignCertificateAnnotation: string(fulcioLikeCertPEM(t)),
			sigstore.CosignBundleAnnotation:      `{"SignedEntryTimestamp":"c2ln","Payload":{"body":"Ym9keQ==","integratedTime":1,"logIndex":2,"logID":"ab"}}`,
		},
		MediaType: types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"),
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}
	sigImg = mutate.MediaType(sigImg, types.OCIManifestSchema1)

	sigTagStr := fmt.Sprintf("%s:%s-%s.sig", targetRepo, imgDigest.Algorithm, imgDigest.Hex)
	sigRef, err := name.ParseReference(sigTagStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("parse sig tag: %v", err)
	}
	if err := remote.Write(sigRef, sigImg); err != nil {
		t.Fatalf("push signature image: %v", err)
	}

	return keylessImage{repo: targetRepo, ref: targetRepo + ":v1.0.0", digest: imgDigest}
}

// TestResolveProvenance_Keyless_IdentityComesFromRequestNotCertificate is the
// regression test for the core bug: the resolver derived its "expected"
// identity from the certificate it was verifying
// (cert.Issuer.CommonName + cert.EmailAddresses[0]).
//
// Deriving it from the certificate is wrong in two ways at once, and this test
// pins both:
//
//   - cert.Issuer.CommonName is the CA's CN, not the OIDC issuer sigstore-go
//     matches on, so the comparison never matched and the keyless path was dead.
//   - had it been "fixed" by reading the OIDC extension instead, the check would
//     compare the certificate against itself and accept every Fulcio-issued
//     certificate in existence.
//
// So the assertion is not "some identity was passed" but specifically: the
// identity passed is the one the *caller* supplied, and matches none of the
// three values that a certificate-derived expectation would have produced.
func TestResolveProvenance_Keyless_IdentityComesFromRequestNotCertificate(t *testing.T) {
	_, host := startTestRegistry(t)
	img := pushKeylessSignedImage(t, host, "app-keyless-identity", "", "", "")

	fake := &recordingKeylessVerifier{
		result: ports.KeylessVerifyResult{Issuer: expectedIssuer, SAN: expectedSAN},
	}
	r := provenance.NewResolver(nil, provenance.WithKeylessVerifier(fake))

	summary, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{
		ImageRef: img.ref,
		KeylessIdentity: ports.KeylessIdentity{
			Issuer: expectedIssuer,
			SAN:    expectedSAN,
		},
	})
	if err != nil {
		t.Fatalf("resolve provenance: %v", err)
	}

	calls := fake.calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 keyless verification, got %d", len(calls))
	}
	got := calls[0]

	if got.Issuer != expectedIssuer {
		t.Errorf("Issuer passed to the verifier = %q, want %q (the caller's value)", got.Issuer, expectedIssuer)
	}
	if got.SAN != expectedSAN {
		t.Errorf("SAN passed to the verifier = %q, want %q (the caller's value)", got.SAN, expectedSAN)
	}

	// The three values a certificate-derived expectation would have yielded.
	for _, forbidden := range []string{certCACommonName, certOIDCIssuer, certSANEmail} {
		if got.Issuer == forbidden || got.SAN == forbidden {
			t.Errorf("identity %+v contains %q, which could only have come from the certificate "+
				"under verification — the expectation must come from the caller", got, forbidden)
		}
	}

	if !summary.SignatureValid {
		t.Error("expected SignatureValid=true once the expected identity verified")
	}
	if !strings.HasPrefix(summary.SignerIdentity, "keyless:") {
		t.Errorf("SignerIdentity = %q, want a keyless:... value", summary.SignerIdentity)
	}
	if !strings.Contains(summary.SignerIdentity, expectedSAN) || !strings.Contains(summary.SignerIdentity, expectedIssuer) {
		t.Errorf("SignerIdentity = %q, want it to report the verified SAN and issuer", summary.SignerIdentity)
	}
}

// TestResolveProvenance_Keyless_UnconstrainedFailsClosed proves the resolver
// refuses to judge keyless material it has no expectation for, instead of
// either silently reporting SignatureValid=false (indistinguishable from a
// forgery) or fabricating an expectation from the certificate (which would
// accept anyone). The verifier must not be called at all.
func TestResolveProvenance_Keyless_UnconstrainedFailsClosed(t *testing.T) {
	_, host := startTestRegistry(t)
	img := pushKeylessSignedImage(t, host, "app-keyless-unconstrained", "", "", "")

	fake := &recordingKeylessVerifier{
		result: ports.KeylessVerifyResult{Issuer: certOIDCIssuer, SAN: certSANEmail},
	}
	r := provenance.NewResolver(nil, provenance.WithKeylessVerifier(fake))

	summary, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{
		ImageRef: img.ref,
		// Deliberately no KeylessIdentity.
	})
	if err == nil {
		t.Fatalf("expected a hard failure for an unconstrained keyless verify, got summary %+v", summary)
	}
	if !errors.Is(err, provenance.ErrKeylessIdentityRequired) {
		t.Errorf("got %v, want ErrKeylessIdentityRequired", err)
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("got %v, want it to also wrap core.ErrInvalidRequest", err)
	}
	// The message must tell the operator what to do.
	for _, want := range []string{"--keyless-identity", "--keyless-issuer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
	if summary.SignatureValid {
		t.Error("SignatureValid must never be true on the fail-closed path")
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("the keyless verifier was called %d time(s) with no configured identity; it must not be called at all", n)
	}
}

// TestResolveProvenance_Keyless_IncompleteIdentityRefused covers the half-configured
// case, which Sigstore cannot express and which a caller almost never means.
func TestResolveProvenance_Keyless_IncompleteIdentityRefused(t *testing.T) {
	tests := []struct {
		name string
		id   ports.KeylessIdentity
	}{
		{"issuer without SAN", ports.KeylessIdentity{Issuer: expectedIssuer}},
		{"SAN without issuer", ports.KeylessIdentity{SAN: expectedSAN}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, host := startTestRegistry(t)
			img := pushKeylessSignedImage(t, host, "app-keyless-incomplete", "", "", "")

			fake := &recordingKeylessVerifier{}
			r := provenance.NewResolver(nil, provenance.WithKeylessVerifier(fake))

			_, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{
				ImageRef:        img.ref,
				KeylessIdentity: tc.id,
			})
			if err == nil {
				t.Fatal("expected an error for a half-specified identity")
			}
			if !errors.Is(err, provenance.ErrKeylessIdentityIncomplete) {
				t.Errorf("got %v, want ErrKeylessIdentityIncomplete", err)
			}
			if n := len(fake.calls()); n != 0 {
				t.Errorf("verifier called %d time(s); a half-specified identity must be refused before any verification", n)
			}
		})
	}
}

// TestResolveProvenance_Keyless_ClaimsStillChecked proves that proving *who*
// signed is not the same as proving *what* they signed. The fake verifier
// reports success for the expected identity in every subtest; only the
// simple-signing claims differ.
func TestResolveProvenance_Keyless_ClaimsStillChecked(t *testing.T) {
	otherDigest := "sha256:" + strings.Repeat("7", 64)

	tests := []struct {
		name        string
		payloadType string
		payloadRepo string
		payloadDgst string
		wantValid   bool
	}{
		{name: "matching claims accepted", wantValid: true},
		{name: "cross-repo payload rejected", payloadRepo: "ghcr.io/evil/app", wantValid: false},
		{name: "cross-digest payload rejected", payloadDgst: otherDigest, wantValid: false},
		{name: "foreign payload type rejected", payloadType: "cosign attestation", wantValid: false},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, host := startTestRegistry(t)
			img := pushKeylessSignedImage(t, host, fmt.Sprintf("app-keyless-claims-%d", i),
				tc.payloadType, tc.payloadRepo, tc.payloadDgst)

			fake := &recordingKeylessVerifier{
				result: ports.KeylessVerifyResult{Issuer: expectedIssuer, SAN: expectedSAN},
			}
			r := provenance.NewResolver(nil, provenance.WithKeylessVerifier(fake))

			summary, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{
				ImageRef:        img.ref,
				KeylessIdentity: ports.KeylessIdentity{Issuer: expectedIssuer, SAN: expectedSAN},
			})
			// A configured-but-unsatisfied identity is a normal negative
			// result, not the fail-closed refusal — that only fires when
			// nothing was configured.
			if err != nil {
				t.Fatalf("resolve provenance: %v", err)
			}
			if summary.SignatureValid != tc.wantValid {
				t.Errorf("SignatureValid = %v, want %v", summary.SignatureValid, tc.wantValid)
			}
		})
	}
}

// TestResolveProvenance_Keyless_VerifierRejectionIsNotFailClosedError covers the
// boundary between the two negative outcomes: when an identity *was* configured
// and the verifier rejected the certificate, the result is SignatureValid=false,
// not ErrKeylessIdentityRequired. Conflating the two would make a genuine
// forgery look like a configuration mistake.
func TestResolveProvenance_Keyless_VerifierRejectionIsNotFailClosedError(t *testing.T) {
	_, host := startTestRegistry(t)
	img := pushKeylessSignedImage(t, host, "app-keyless-rejected", "", "", "")

	fake := &recordingKeylessVerifier{failWith: sigstore.ErrIdentityMismatch}
	r := provenance.NewResolver(nil, provenance.WithKeylessVerifier(fake))

	summary, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{
		ImageRef:        img.ref,
		KeylessIdentity: ports.KeylessIdentity{Issuer: expectedIssuer, SAN: expectedSAN},
	})
	if err != nil {
		t.Fatalf("resolve provenance: %v", err)
	}
	if summary.SignatureValid {
		t.Fatal("expected SignatureValid=false when the verifier rejected the certificate")
	}
	if len(fake.calls()) != 1 {
		t.Errorf("expected the verifier to be called once, got %d", len(fake.calls()))
	}
}

// TestResolveProvenance_NoKeylessMaterial_DoesNotRequireIdentity guards against
// the fail-closed refusal over-firing: an image with no keyless material at all
// (unsigned, or static-key signed) must resolve without any keyless flags.
func TestResolveProvenance_NoKeylessMaterial_DoesNotRequireIdentity(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-no-keyless", host)

	img := createTestImage(t, nil)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}

	fake := &recordingKeylessVerifier{}
	r := provenance.NewResolver(nil, provenance.WithKeylessVerifier(fake))

	summary, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{
		ImageRef: targetRepo + ":v1.0.0",
	})
	if err != nil {
		t.Fatalf("an image with no keyless material must not require keyless flags: %v", err)
	}
	if summary.SignatureValid {
		t.Error("expected SignatureValid=false for an unsigned image")
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("verifier called %d time(s) for an image with no keyless material", n)
	}
}

// pushMultiLayerKeylessSig pushes a `.sig` manifest with TWO annotated layers:
// the first carries keyless material whose payload describes the WRONG image,
// the second carries correct claims. Both layers reference the same expected
// identity.
//
// This exists for checklist rows 3 and 4: verifyCosignSignature loops over
// layers and now accumulates state across iterations (keylessMaterialSeen,
// keylessErr), so a single-layer fixture cannot show that a failure on a
// non-first item is handled correctly, nor that a first-item failure does not
// poison a later success.
func pushMultiLayerKeylessSig(t *testing.T, host, repoName string, goodLayerLast bool) keylessImage {
	t.Helper()

	targetRepo := fmt.Sprintf("%s/%s", host, repoName)
	img := createTestImage(t, nil)
	tagRef, err := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err != nil {
		t.Fatalf("parse tag: %v", err)
	}
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	payload := func(repo, dgst string) []byte {
		b, merr := json.Marshal(ports.CosignSimpleSigningPayload{
			Critical: ports.CosignCritical{
				Identity: ports.CosignCriticalIdentity{DockerReference: repo},
				Image:    ports.CosignCriticalImage{DockerManifestDigest: dgst},
				Type:     ports.CosignContainerImageSignatureType,
			},
		})
		if merr != nil {
			t.Fatalf("marshal payload: %v", merr)
		}
		return b
	}

	bad := payload("ghcr.io/evil/app", "sha256:"+strings.Repeat("3", 64))
	good := payload(targetRepo, imgDigest.String())

	ordered := [][]byte{bad, good}
	if !goodLayerLast {
		ordered = [][]byte{good, bad}
	}

	annotations := map[string]string{
		sigstore.CosignSignatureAnnotation:   base64.StdEncoding.EncodeToString([]byte("a-signature")),
		sigstore.CosignCertificateAnnotation: string(fulcioLikeCertPEM(t)),
		sigstore.CosignBundleAnnotation:      `{"SignedEntryTimestamp":"c2ln","Payload":{"body":"Ym9keQ==","integratedTime":1,"logIndex":2,"logID":"ab"}}`,
	}

	sigImg := empty.Image
	for _, p := range ordered {
		sigImg, err = mutate.Append(sigImg, mutate.Addendum{
			Layer:       static.NewLayer(p, types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json")),
			Annotations: annotations,
			MediaType:   types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"),
		})
		if err != nil {
			t.Fatalf("mutate.Append: %v", err)
		}
	}
	sigImg = mutate.MediaType(sigImg, types.OCIManifestSchema1)

	sigTagStr := fmt.Sprintf("%s:%s-%s.sig", targetRepo, imgDigest.Algorithm, imgDigest.Hex)
	sigRef, err := name.ParseReference(sigTagStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("parse sig tag: %v", err)
	}
	if err := remote.Write(sigRef, sigImg); err != nil {
		t.Fatalf("push signature image: %v", err)
	}

	return keylessImage{repo: targetRepo, ref: targetRepo + ":v1.0.0", digest: imgDigest}
}

// TestResolveProvenance_Keyless_MultiLayerNonFirstItem covers the two-layer
// cases in both orders: a bad first layer must not prevent a good second layer
// from verifying, and a good first layer must short-circuit without the bad
// second one mattering. Either order must reach the same verdict.
func TestResolveProvenance_Keyless_MultiLayerNonFirstItem(t *testing.T) {
	for _, goodLast := range []bool{true, false} {
		name := "good layer last"
		if !goodLast {
			name = "good layer first"
		}
		t.Run(name, func(t *testing.T) {
			_, host := startTestRegistry(t)
			img := pushMultiLayerKeylessSig(t, host, fmt.Sprintf("app-keyless-multi-%v", goodLast), goodLast)

			fake := &recordingKeylessVerifier{
				result: ports.KeylessVerifyResult{Issuer: expectedIssuer, SAN: expectedSAN},
			}
			r := provenance.NewResolver(nil, provenance.WithKeylessVerifier(fake))

			summary, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{
				ImageRef:        img.ref,
				KeylessIdentity: ports.KeylessIdentity{Issuer: expectedIssuer, SAN: expectedSAN},
			})
			if err != nil {
				t.Fatalf("resolve provenance: %v", err)
			}
			if !summary.SignatureValid {
				t.Fatal("expected SignatureValid=true: one layer carries correct claims for the expected identity")
			}
			// Every identity the verifier was handed must still be the
			// caller's, on every iteration — not just the first.
			for i, id := range fake.calls() {
				if id.Issuer != expectedIssuer || id.SAN != expectedSAN {
					t.Errorf("call %d used identity %+v, want the caller's", i, id)
				}
			}
		})
	}
}

// TestResolveProvenance_Keyless_MultiLayerAllBadFailsClosedNotValid pairs with
// the above: when NO layer has correct claims, the accumulated state must not
// leak into a positive verdict just because the verifier itself said yes.
func TestResolveProvenance_Keyless_MultiLayerAllBadFailsClosedNotValid(t *testing.T) {
	_, host := startTestRegistry(t)
	targetRepo := fmt.Sprintf("%s/app-keyless-multi-allbad", host)

	img := createTestImage(t, nil)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}
	imgDigest, _ := img.Digest()

	annotations := map[string]string{
		sigstore.CosignSignatureAnnotation:   base64.StdEncoding.EncodeToString([]byte("a-signature")),
		sigstore.CosignCertificateAnnotation: string(fulcioLikeCertPEM(t)),
		sigstore.CosignBundleAnnotation:      `{"SignedEntryTimestamp":"c2ln","Payload":{"body":"Ym9keQ==","integratedTime":1,"logIndex":2,"logID":"ab"}}`,
	}

	sigImg := empty.Image
	for _, wrongRepo := range []string{"ghcr.io/evil/one", "ghcr.io/evil/two"} {
		p, _ := json.Marshal(ports.CosignSimpleSigningPayload{
			Critical: ports.CosignCritical{
				Identity: ports.CosignCriticalIdentity{DockerReference: wrongRepo},
				Image:    ports.CosignCriticalImage{DockerManifestDigest: imgDigest.String()},
				Type:     ports.CosignContainerImageSignatureType,
			},
		})
		var err error
		sigImg, err = mutate.Append(sigImg, mutate.Addendum{
			Layer:       static.NewLayer(p, types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json")),
			Annotations: annotations,
			MediaType:   types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"),
		})
		if err != nil {
			t.Fatalf("mutate.Append: %v", err)
		}
	}
	sigImg = mutate.MediaType(sigImg, types.OCIManifestSchema1)

	sigRef, _ := name.ParseReference(
		fmt.Sprintf("%s:%s-%s.sig", targetRepo, imgDigest.Algorithm, imgDigest.Hex), name.WeakValidation)
	if err := remote.Write(sigRef, sigImg); err != nil {
		t.Fatalf("push signature image: %v", err)
	}

	fake := &recordingKeylessVerifier{
		result: ports.KeylessVerifyResult{Issuer: expectedIssuer, SAN: expectedSAN},
	}
	r := provenance.NewResolver(nil, provenance.WithKeylessVerifier(fake))

	summary, err := r.ResolveProvenance(context.Background(), ports.ProvenanceResolverRequest{
		ImageRef:        targetRepo + ":v1.0.0",
		KeylessIdentity: ports.KeylessIdentity{Issuer: expectedIssuer, SAN: expectedSAN},
	})
	if err != nil {
		t.Fatalf("resolve provenance: %v", err)
	}
	if summary.SignatureValid {
		t.Fatal("expected SignatureValid=false when no layer's claims match the image")
	}
	if n := len(fake.calls()); n != 2 {
		t.Errorf("expected both layers to be attempted, got %d call(s)", n)
	}
}
