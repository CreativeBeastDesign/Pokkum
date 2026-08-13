package sigstore

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	rekorv1 "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	sigstorebundle "github.com/sigstore/sigstore-go/pkg/bundle"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// bundleVersion is the Sigstore bundle schema version the legacy Cosign
// signature-tag material maps onto. Cosign's `dev.sigstore.cosign/bundle`
// annotation carries a Rekor SignedEntryTimestamp ("inclusion promise") and
// no inclusion proof, which is exactly what a v0.1 bundle is defined to
// carry: sigstore-go's Bundle.validate() *requires* an inclusion promise for
// v0.1 and *requires* an inclusion proof for v0.2+, so v0.1 is the only
// version this material can legally be expressed as.
const bundleVersion = "v0.1"

// cosignRekorBundle is the shape of the JSON in Cosign's
// `dev.sigstore.cosign/bundle` layer annotation. The field casing is
// load-bearing and matches what Cosign writes: the two top-level keys are
// capitalized, the keys inside Payload are lower-camel (and it is "logID",
// not "logId").
type cosignRekorBundle struct {
	// SignedEntryTimestamp is base64-encoded ASN.1 DER: Rekor's signature
	// over the canonicalized entry, a.k.a. the inclusion promise.
	SignedEntryTimestamp string `json:"SignedEntryTimestamp"`

	Payload struct {
		// Body is the base64-encoded canonicalized Rekor entry JSON.
		Body string `json:"body"`
		// IntegratedTime is Unix seconds.
		IntegratedTime int64 `json:"integratedTime"`
		// LogIndex is the entry's index in the Rekor log.
		LogIndex int64 `json:"logIndex"`
		// LogID is the hex-encoded (not base64) SHA-256 of the Rekor log's
		// public key.
		LogID string `json:"logID"`
	} `json:"Payload"`
}

// rekorEntryBody is the subset of the decoded canonicalized Rekor entry body
// this package needs: the entry's kind and schema version. These are read at
// runtime rather than hardcoded to "hashedrekord"/"0.0.1" because
// tlog.ParseTransparencyLogEntry cross-checks the KindVersion supplied in the
// protobuf against the kind/version it parses out of the canonicalized body
// and rejects a mismatch. Hardcoding would turn "this signature uses a Rekor
// entry type we have not seen before" into a confusing kind/version mismatch
// error instead of either working or failing for the real reason.
type rekorEntryBody struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

// legacyMaterial is the parsed, structurally valid form of a legacy Cosign
// keyless signature: a Sigstore bundle ready to hand to a verify.Verifier,
// plus the parsed leaf certificate and Rekor entry metadata that the caller
// reports back in a ports.KeylessVerifyResult once verification succeeds.
//
// Nothing in here is verified — assembleV01Bundle only proves the material
// is well-formed. Do not read any field of this struct as a trusted fact
// before verify.Verifier.Verify has returned success for Bundle.
type legacyMaterial struct {
	// Bundle is the assembled v0.1 Sigstore bundle.
	Bundle *sigstorebundle.Bundle

	// LeafCertificate is the parsed Fulcio leaf, used for reporting the
	// certificate validity window.
	LeafCertificate *x509.Certificate

	// RekorLogID, RekorLogIndex and IntegratedTime mirror the Rekor bundle
	// annotation's Payload fields, decoded.
	RekorLogID     string
	RekorLogIndex  int64
	IntegratedTime time.Time
}

// assembleV01Bundle converts the legacy Cosign signature-tag material in req
// into a Sigstore v0.1 bundle.
//
// Security note on the certificate chain: only the leaf certificate from
// req.CertificatePEM is placed in the bundle's X509CertificateChain. The
// `dev.sigstore.cosign/chain` annotation (req.ChainPEM) is deliberately
// ignored — it is attacker-suppliable data fetched from the same registry as
// the signature, so trusting it for path building would let a forged
// intermediate substitute for Fulcio's. The trust chain is instead built by
// verify.Verifier against the trusted root's own Fulcio CAs. (sigstore-go
// would ignore extra certificates here anyway: Bundle.VerificationContent()
// only parses Certificates[0] unless bundle.AllowCertificateChain() is
// passed, and that option is itself rejected for bundles below v0.3.)
//
// All returned errors wrap ErrMalformedMaterial.
func assembleV01Bundle(req ports.KeylessVerifyRequest) (*legacyMaterial, error) {
	if len(req.PayloadBytes) == 0 {
		return nil, fmt.Errorf("%w: signature payload (the .sig layer blob) is empty", ErrMalformedMaterial)
	}
	if len(req.SignatureBytes) == 0 {
		return nil, fmt.Errorf("%w: signature (annotation %s) is empty", ErrMalformedMaterial, CosignSignatureAnnotation)
	}

	leaf, err := parseLeafCertificate(req.CertificatePEM)
	if err != nil {
		return nil, err
	}

	rekor, err := parseRekorBundle(req.RekorBundleJSON)
	if err != nil {
		return nil, err
	}

	logIDBytes, err := hex.DecodeString(rekor.Payload.LogID)
	if err != nil || len(logIDBytes) == 0 {
		return nil, fmt.Errorf("%w: annotation %s has a Payload.logID that is not a non-empty hex string (got %q): %v",
			ErrMalformedMaterial, CosignBundleAnnotation, rekor.Payload.LogID, err)
	}

	setBytes, err := base64.StdEncoding.DecodeString(rekor.SignedEntryTimestamp)
	if err != nil || len(setBytes) == 0 {
		return nil, fmt.Errorf("%w: annotation %s has a SignedEntryTimestamp that is not non-empty base64: %v",
			ErrMalformedMaterial, CosignBundleAnnotation, err)
	}

	canonicalBody, err := base64.StdEncoding.DecodeString(rekor.Payload.Body)
	if err != nil || len(canonicalBody) == 0 {
		return nil, fmt.Errorf("%w: annotation %s has a Payload.body that is not non-empty base64: %v",
			ErrMalformedMaterial, CosignBundleAnnotation, err)
	}

	var body rekorEntryBody
	if err := json.Unmarshal(canonicalBody, &body); err != nil {
		return nil, fmt.Errorf("%w: annotation %s Payload.body does not decode to a Rekor entry JSON object: %w",
			ErrMalformedMaterial, CosignBundleAnnotation, err)
	}
	if body.Kind == "" || body.APIVersion == "" {
		return nil, fmt.Errorf("%w: annotation %s Payload.body is missing kind and/or apiVersion (kind=%q apiVersion=%q)",
			ErrMalformedMaterial, CosignBundleAnnotation, body.Kind, body.APIVersion)
	}

	mediaType, err := sigstorebundle.MediaTypeString(bundleVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot build Sigstore bundle media type for version %s: %w",
			ErrMalformedMaterial, bundleVersion, err)
	}

	payloadDigest := sha256.Sum256(req.PayloadBytes)

	pb := &protobundle.Bundle{
		MediaType: mediaType,
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_X509CertificateChain{
				X509CertificateChain: &protocommon.X509CertificateChain{
					Certificates: []*protocommon.X509Certificate{
						// Leaf only, on purpose. See the security note above.
						{RawBytes: leaf.Raw},
					},
				},
			},
			TlogEntries: []*rekorv1.TransparencyLogEntry{
				{
					LogIndex:          rekor.Payload.LogIndex,
					LogId:             &protocommon.LogId{KeyId: logIDBytes},
					KindVersion:       &rekorv1.KindVersion{Kind: body.Kind, Version: body.APIVersion},
					IntegratedTime:    rekor.Payload.IntegratedTime,
					CanonicalizedBody: canonicalBody,
					InclusionPromise:  &rekorv1.InclusionPromise{SignedEntryTimestamp: setBytes},
				},
			},
		},
		Content: &protobundle.Bundle_MessageSignature{
			MessageSignature: &protocommon.MessageSignature{
				MessageDigest: &protocommon.HashOutput{
					Algorithm: protocommon.HashAlgorithm_SHA2_256,
					Digest:    payloadDigest[:],
				},
				Signature: req.SignatureBytes,
			},
		},
	}

	b, err := sigstorebundle.NewBundle(pb)
	if err != nil {
		return nil, fmt.Errorf("%w: assembled Sigstore %s bundle (Rekor %s/%s entry, log index %d) was rejected as invalid: %w",
			ErrMalformedMaterial, bundleVersion, body.Kind, body.APIVersion, rekor.Payload.LogIndex, err)
	}

	return &legacyMaterial{
		Bundle:          b,
		LeafCertificate: leaf,
		RekorLogID:      rekor.Payload.LogID,
		RekorLogIndex:   rekor.Payload.LogIndex,
		IntegratedTime:  time.Unix(rekor.Payload.IntegratedTime, 0).UTC(),
	}, nil
}

// parseLeafCertificate decodes the first CERTIFICATE block out of the
// `dev.sigstore.cosign/certificate` annotation's PEM text and parses it.
// Cosign writes exactly one leaf there; any trailing blocks are ignored
// rather than silently promoted to trust anchors.
func parseLeafCertificate(certPEM []byte) (*x509.Certificate, error) {
	rest := certPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("%w: annotation %s contains no PEM CERTIFICATE block (%d bytes of PEM input)",
				ErrMalformedMaterial, CosignCertificateAnnotation, len(certPEM))
		}
		if block.Type != "CERTIFICATE" {
			// Skip non-certificate blocks rather than failing outright.
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: annotation %s leaf certificate does not parse as X.509 DER: %w",
				ErrMalformedMaterial, CosignCertificateAnnotation, err)
		}
		return leaf, nil
	}
}

// parseRekorBundle decodes the `dev.sigstore.cosign/bundle` annotation JSON
// and checks that every field the bundle assembly needs is present.
func parseRekorBundle(bundleJSON []byte) (*cosignRekorBundle, error) {
	var rekor cosignRekorBundle
	if err := json.Unmarshal(bundleJSON, &rekor); err != nil {
		return nil, fmt.Errorf("%w: annotation %s is not valid JSON: %w",
			ErrMalformedMaterial, CosignBundleAnnotation, err)
	}

	switch {
	case rekor.SignedEntryTimestamp == "":
		return nil, fmt.Errorf("%w: annotation %s is missing SignedEntryTimestamp (the Rekor inclusion promise); without it there is no proof the signature was logged",
			ErrMalformedMaterial, CosignBundleAnnotation)
	case rekor.Payload.Body == "":
		return nil, fmt.Errorf("%w: annotation %s is missing Payload.body (the canonicalized Rekor entry)",
			ErrMalformedMaterial, CosignBundleAnnotation)
	case rekor.Payload.LogID == "":
		return nil, fmt.Errorf("%w: annotation %s is missing Payload.logID (which Rekor instance recorded the entry)",
			ErrMalformedMaterial, CosignBundleAnnotation)
	case rekor.Payload.IntegratedTime <= 0:
		return nil, fmt.Errorf("%w: annotation %s has a non-positive Payload.integratedTime (%d); the Rekor timestamp is the only time source for validating a short-lived Fulcio certificate",
			ErrMalformedMaterial, CosignBundleAnnotation, rekor.Payload.IntegratedTime)
	case rekor.Payload.LogIndex < 0:
		return nil, fmt.Errorf("%w: annotation %s has a negative Payload.logIndex (%d)",
			ErrMalformedMaterial, CosignBundleAnnotation, rekor.Payload.LogIndex)
	}

	return &rekor, nil
}
