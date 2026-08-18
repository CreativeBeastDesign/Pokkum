package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func testSignatureBundle(subject v1.Hash, repo string) ports.CosignSignatureBundle {
	payload := []byte(`{"critical":{"identity":{"docker-reference":"` + repo + `"},"image":{"docker-manifest-digest":"` + subject.String() + `"},"type":"atomic container signature"}}`)
	sig := []byte("fake-but-round-trippable-signature")
	return ports.CosignSignatureBundle{
		PayloadBytes:    payload,
		SignatureBytes:  sig,
		Base64Signature: base64.StdEncoding.EncodeToString(sig),
		Repo:            repo,
		Digest:          subject,
	}
}

func testDSSEEnvelope(t *testing.T, subject v1.Hash) ports.DSSEEnvelope {
	t.Helper()
	stmt := ports.SLSAStatement{
		Type:          ports.InTotoStatementType,
		PredicateType: ports.SLSAProvenancePredicateType,
		Subject: []ports.ResourceDescriptor{{
			Name:   "test-subject",
			Digest: map[string]string{subject.Algorithm: subject.Hex},
		}},
	}
	payload, err := json.Marshal(stmt)
	if err != nil {
		t.Fatalf("marshal statement: %v", err)
	}
	return ports.DSSEEnvelope{
		Payload:     base64.StdEncoding.EncodeToString(payload),
		PayloadType: ports.InTotoPayloadType,
		Signatures:  []ports.DSSESignature{{Sig: base64.StdEncoding.EncodeToString([]byte("fake-dsse-sig"))}},
	}
}

// TestAttachSignature_RoundTripWithReferrers exercises the full attach →
// fetch round trip against a registry with real OCI 1.1 referrers support
// (newTestRegistryWithReferrers — the capability-enabled double checklist
// row 12 requires for a referrer-path test): the .sig tag must exist in
// cosign's wire format, FetchSignature must reconstruct the exact bundle,
// and the referrers API must list the additive referrer manifest.
func TestAttachSignature_RoundTripWithReferrers(t *testing.T) {
	s, _ := newTestRegistryWithReferrers(t)
	repo := registryRepo(t, s, "app/signed")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}
	bundle := testSignatureBundle(subject, repo)

	a := NewAdapter(nil)
	res, err := a.AttachSignature(context.Background(), ports.AttachSignatureRequest{
		Repo:    repo,
		Subject: subject,
		Bundle:  bundle,
	})
	if err != nil {
		t.Fatalf("AttachSignature: %v", err)
	}
	wantTag := ports.SigTag(subject)
	if len(res.Tags) != 1 || res.Tags[0] != wantTag {
		t.Errorf("Tags = %v, want [%s]", res.Tags, wantTag)
	}

	// The tag must be readable in cosign's wire format: payload layer plus
	// the signature layer annotation.
	ref, err := name.ParseReference(repo + ":" + wantTag)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	img, err := remote.Image(ref)
	if err != nil {
		t.Fatalf("remote.Image(%s): %v", ref, err)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(m.Layers) != 1 {
		t.Fatalf("signature image layers = %d, want 1", len(m.Layers))
	}
	if got := m.Layers[0].Annotations[ports.CosignSignatureLayerAnnotation]; got != bundle.Base64Signature {
		t.Errorf("layer annotation %s = %q, want %q", ports.CosignSignatureLayerAnnotation, got, bundle.Base64Signature)
	}
	if string(m.Layers[0].MediaType) != ports.MediaTypeSimpleSigning {
		t.Errorf("layer media type = %q, want %q", m.Layers[0].MediaType, ports.MediaTypeSimpleSigning)
	}

	// FetchSignature must reconstruct the bundle exactly.
	fetched, err := a.FetchSignature(context.Background(), ports.FetchAttachmentRequest{Repo: repo, Subject: subject})
	if err != nil {
		t.Fatalf("FetchSignature: %v", err)
	}
	if !bytes.Equal(fetched.PayloadBytes, bundle.PayloadBytes) {
		t.Errorf("fetched payload = %q, want %q", fetched.PayloadBytes, bundle.PayloadBytes)
	}
	if fetched.Base64Signature != bundle.Base64Signature {
		t.Errorf("fetched signature = %q, want %q", fetched.Base64Signature, bundle.Base64Signature)
	}
	if !bytes.Equal(fetched.SignatureBytes, bundle.SignatureBytes) {
		t.Errorf("fetched raw signature bytes differ from attached ones")
	}

	// The additive referrer must be discoverable via the real Referrers API.
	subjRef, err := name.ParseReference(repo + "@" + subject.String())
	if err != nil {
		t.Fatalf("ParseReference subject: %v", err)
	}
	idx, err := remote.Referrers(subjRef.(name.Digest))
	if err != nil {
		t.Fatalf("remote.Referrers: %v", err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	if len(im.Manifests) == 0 {
		t.Errorf("referrers list is empty; the signature should also be attached as an OCI 1.1 referrer on a referrers-capable registry")
	}
}

// TestAttachSignature_ReferrersUnsupportedFallsBackToTagOnly proves the
// dual-publication contract degrades correctly on a registry WITHOUT OCI
// 1.1 referrers support (newTestRegistry's default): the attach still
// succeeds, and the tag — the load-bearing artifact every verifier reads —
// still round-trips.
func TestAttachSignature_ReferrersUnsupportedFallsBackToTagOnly(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/signed-noreferrers")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("b", 64)}
	bundle := testSignatureBundle(subject, repo)

	a := NewAdapter(nil)
	if _, err := a.AttachSignature(context.Background(), ports.AttachSignatureRequest{
		Repo:    repo,
		Subject: subject,
		Bundle:  bundle,
	}); err != nil {
		t.Fatalf("AttachSignature against a referrers-unsupported registry: %v", err)
	}

	fetched, err := a.FetchSignature(context.Background(), ports.FetchAttachmentRequest{Repo: repo, Subject: subject})
	if err != nil {
		t.Fatalf("FetchSignature: %v", err)
	}
	if !bytes.Equal(fetched.PayloadBytes, bundle.PayloadBytes) || fetched.Base64Signature != bundle.Base64Signature {
		t.Errorf("fetched bundle does not round-trip on a tag-only registry")
	}
}

// TestAttachAttestation_RoundTrip: the DSSE envelope must land under the
// .att tag in cosign's attestation wire format and decode back identically.
func TestAttachAttestation_RoundTrip(t *testing.T) {
	s, _ := newTestRegistryWithReferrers(t)
	repo := registryRepo(t, s, "app/attested")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("c", 64)}
	env := testDSSEEnvelope(t, subject)

	a := NewAdapter(nil)
	res, err := a.AttachAttestation(context.Background(), ports.AttachAttestationRequest{
		Repo:     repo,
		Subject:  subject,
		Envelope: env,
	})
	if err != nil {
		t.Fatalf("AttachAttestation: %v", err)
	}
	wantTag := ports.AttTag(subject)
	if len(res.Tags) != 1 || res.Tags[0] != wantTag {
		t.Errorf("Tags = %v, want [%s]", res.Tags, wantTag)
	}

	fetched, err := a.FetchAttestation(context.Background(), ports.FetchAttachmentRequest{Repo: repo, Subject: subject})
	if err != nil {
		t.Fatalf("FetchAttestation: %v", err)
	}
	if fetched.Payload != env.Payload || fetched.PayloadType != env.PayloadType {
		t.Errorf("fetched envelope payload does not round-trip: got %q/%q", fetched.PayloadType, fetched.Payload)
	}
	if len(fetched.Signatures) != 1 || fetched.Signatures[0].Sig != env.Signatures[0].Sig {
		t.Errorf("fetched envelope signatures do not round-trip: %+v", fetched.Signatures)
	}

	// The layer must carry the DSSE media type so cosign-family tools
	// recognize it.
	ref, err := name.ParseReference(repo + ":" + wantTag)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	img, err := remote.Image(ref)
	if err != nil {
		t.Fatalf("remote.Image: %v", err)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(m.Layers) != 1 || string(m.Layers[0].MediaType) != ports.MediaTypeDSSEEnvelope {
		t.Errorf("attestation layer media type = %v, want one %q layer", m.Layers, ports.MediaTypeDSSEEnvelope)
	}
}

// TestFetchSignature_MissingIsErrSignatureMissing: an absent attachment must
// be a hard, classifiable error — the self-verification stage depends on a
// miss never looking like an empty success.
func TestFetchSignature_MissingIsErrSignatureMissing(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/unsigned")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("d", 64)}

	a := NewAdapter(nil)
	if _, err := a.FetchSignature(context.Background(), ports.FetchAttachmentRequest{Repo: repo, Subject: subject}); !errors.Is(err, core.ErrSignatureMissing) {
		t.Fatalf("FetchSignature on unsigned subject: err = %v, want core.ErrSignatureMissing", err)
	}
	if _, err := a.FetchAttestation(context.Background(), ports.FetchAttachmentRequest{Repo: repo, Subject: subject}); !errors.Is(err, core.ErrSignatureMissing) {
		t.Fatalf("FetchAttestation on unattested subject: err = %v, want core.ErrSignatureMissing", err)
	}
}

// TestAttachSignature_Validation pins the request-shape errors.
func TestAttachSignature_Validation(t *testing.T) {
	a := NewAdapter(nil)
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("e", 64)}

	if _, err := a.AttachSignature(context.Background(), ports.AttachSignatureRequest{Subject: subject, Bundle: testSignatureBundle(subject, "r")}); !errors.Is(err, core.ErrNoDockerRepo) {
		t.Errorf("empty repo: err = %v, want core.ErrNoDockerRepo", err)
	}
	if _, err := a.AttachSignature(context.Background(), ports.AttachSignatureRequest{Repo: "example.com/app", Bundle: testSignatureBundle(subject, "r")}); !errors.Is(err, core.ErrSigningFailed) {
		t.Errorf("no subject: err = %v, want core.ErrSigningFailed", err)
	}
	if _, err := a.AttachSignature(context.Background(), ports.AttachSignatureRequest{Repo: "example.com/app", Subject: subject}); !errors.Is(err, core.ErrSigningFailed) {
		t.Errorf("empty bundle: err = %v, want core.ErrSigningFailed", err)
	}
	if _, err := a.AttachAttestation(context.Background(), ports.AttachAttestationRequest{Repo: "example.com/app", Subject: subject}); !errors.Is(err, core.ErrSigningFailed) {
		t.Errorf("empty envelope: err = %v, want core.ErrSigningFailed", err)
	}
}
