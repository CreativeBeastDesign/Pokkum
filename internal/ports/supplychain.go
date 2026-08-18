package ports

import (
	"context"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// InTotoStatementType is the in-toto spec statement type URI.
const InTotoStatementType = "https://in-toto.io/Statement/v0.1"

// SLSAProvenancePredicateType is the SLSA v1.0 provenance predicate type URI.
const SLSAProvenancePredicateType = "https://slsa.dev/provenance/v1"

// SLSABuildType is Pokkum's SLSA build type URI.
const SLSABuildType = "https://pokkum.dev/slsa/v1"

// SLSABuilderID is Pokkum's builder identifier URI.
const SLSABuilderID = "https://pokkum.dev/builder/v0.1"

// ResourceDescriptor represents an artifact or dependency in an in-toto statement.
type ResourceDescriptor struct {
	Name      string            `json:"name,omitempty"`
	URI       string            `json:"uri,omitempty"`
	Digest    map[string]string `json:"digest,omitempty"`
	MediaType string            `json:"mediaType,omitempty"`
}

// SLSABuildDefinition describes the build process per the SLSA v1.0 spec.
type SLSABuildDefinition struct {
	BuildType            string               `json:"buildType"`
	ExternalParameters   map[string]any       `json:"externalParameters"`
	InternalParameters   map[string]any       `json:"internalParameters,omitempty"`
	ResolvedDependencies []ResourceDescriptor `json:"resolvedDependencies,omitempty"`
}

// SLSABuilder identifies the builder that performed the build.
type SLSABuilder struct {
	ID string `json:"id"`
}

// SLSABuildMetadata records build run timestamps and invocation details.
type SLSABuildMetadata struct {
	InvocationID string     `json:"invocationId,omitempty"`
	StartedOn    *time.Time `json:"startedOn,omitempty"`
	FinishedOn   *time.Time `json:"finishedOn,omitempty"`
}

// SLSARunDetails describes the execution environment and metadata.
type SLSARunDetails struct {
	Builder  SLSABuilder       `json:"builder"`
	Metadata SLSABuildMetadata `json:"metadata,omitempty"`
}

// SLSAPredicate is the payload of a SLSA v1.0 statement.
type SLSAPredicate struct {
	BuildDefinition SLSABuildDefinition `json:"buildDefinition"`
	RunDetails      SLSARunDetails      `json:"runDetails"`
}

// SLSAStatement is an in-toto Statement carrying a SLSA v1.0 predicate.
type SLSAStatement struct {
	Type          string               `json:"_type"`
	Subject       []ResourceDescriptor `json:"subject"`
	PredicateType string               `json:"predicateType"`
	Predicate     SLSAPredicate        `json:"predicate"`
}

// SLSAToolchain records the tool versions involved in the build for SLSA provenance.
type SLSAToolchain struct {
	PokkumVersion       string
	PokkumCommit        string
	GoVersion           string
	BuilderOSArch       string
	BunVersion          string
	BunBinaryHash       string
	SupervisorVersion   string
	StaticServerVersion string
}

// SLSABaseImage records the base image information for SLSA provenance.
type SLSABaseImage struct {
	Preset    BaseImagePreset
	Ref       string
	PinnedRef string
	Digest    v1.Hash
}

// SLSAGeneratorRequest supplies the context and build inputs necessary to build a SLSA provenance statement.
type SLSAGeneratorRequest struct {
	// ProjectDir is the root directory of the project.
	ProjectDir string

	// Repo is the target image repository name (e.g. "ghcr.io/acme/app").
	Repo string

	// TargetTags are the tags requested for the output image.
	Tags []string

	// TargetPlatforms are the platforms built.
	Platforms []Platform

	// OutputMode is the destination mode (e.g. "push", "local", "tarball").
	OutputMode string

	// BaseImage holds the resolved base image info and digest.
	BaseImage SLSABaseImage

	// OutputDigest is the digest of the produced image manifest or index.
	OutputDigest v1.Hash

	// Toolchain records the tool versions involved in the build.
	Toolchain SLSAToolchain

	// SourceDateEpoch is the deterministic build timestamp used.
	SourceDateEpoch time.Time

	// GitRepo is the source repository URL (optional/discovered).
	GitRepo string

	// GitCommit is the source commit SHA (optional/discovered).
	GitCommit string

	// LockfileHashes maps lockfile names to SHA256 hashes (optional).
	LockfileHashes map[string]string

	// Hermetic reports whether --hermetic was requested for this build.
	Hermetic bool

	// HermeticEnforcement names *how* Hermetic was actually enforced, so
	// provenance doesn't claim a uniform guarantee "--hermetic" doesn't
	// uniformly provide: "kernel-enforced-netns" (Linux, a real unshared
	// network namespace — see internal/adapters/bunexec/hermetic_linux.go)
	// or "advisory-env-only" (every other platform — BUN_OFFLINE=1 and
	// friends, which a malicious build script can simply ignore). Empty when
	// Hermetic is false. A consumer of this attestation checking for a real
	// hermetic guarantee must check this field, not just Hermetic — a
	// signed statement asserting "hermetic: true" without it would be
	// indistinguishable from a real kernel-enforced build even when nothing
	// was actually enforced.
	HermeticEnforcement string
}

// SLSAGenerator produces SLSA v1.0 in-toto provenance statements.
type SLSAGenerator interface {
	// Generate creates an in-toto SLSA v1.0 provenance statement for a build.
	Generate(ctx context.Context, req SLSAGeneratorRequest) (SLSAStatement, error)
}

// CosignSimpleSigningType is the Red Hat Simple Signing payload type string.
const CosignSimpleSigningType = "atomic container signature"

// CosignContainerImageSignatureType is the payload type string used by
// real upstream Cosign keyless signatures (e.g. on the stock
// distroless/chainguard base image presets), as distinct from
// CosignSimpleSigningType which is what Pokkum's own static-key signer
// writes. Both are valid "type" claims a Simple Signing payload may carry.
const CosignContainerImageSignatureType = "cosign container image signature"

// CosignCriticalIdentity names the container repository reference claim.
type CosignCriticalIdentity struct {
	DockerReference string `json:"docker-reference"`
}

// CosignCriticalImage names the container manifest digest claim.
type CosignCriticalImage struct {
	DockerManifestDigest string `json:"docker-manifest-digest"`
}

// CosignCritical holds the mandatory claims of a Red Hat Simple Signing payload.
type CosignCritical struct {
	Identity CosignCriticalIdentity `json:"identity"`
	Image    CosignCriticalImage    `json:"image"`
	Type     string                 `json:"type"`
}

// CosignOptional holds the optional claims of a Red Hat Simple Signing payload.
type CosignOptional struct {
	Creator   string            `json:"creator,omitempty"`
	Timestamp int64             `json:"timestamp,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// CosignSimpleSigningPayload is the canonical JSON structure for Cosign container signatures.
type CosignSimpleSigningPayload struct {
	Critical CosignCritical `json:"critical"`
	Optional CosignOptional `json:"optional,omitempty"`
}

// CosignSignRequest supplies the image reference, manifest digest, and signing key for Cosign signing.
type CosignSignRequest struct {
	// Repo is the target container repository (e.g. "ghcr.io/acme/app"). Required.
	Repo string

	// Digest is the output image manifest digest (e.g. "sha256:1111..."). Required.
	Digest v1.Hash

	// KeyPEM is the PEM-encoded private key bytes (ECDSA P-256 or Ed25519).
	KeyPEM []byte

	// Creator is an optional creator tool identifier.
	Creator string

	// SourceDateEpoch is the deterministic timestamp to record in the payload.
	SourceDateEpoch time.Time

	// Annotations are additional key-value claims recorded in optional.extra.
	Annotations map[string]string
}

// Cosign's legacy signature-tag annotation keys — the OCI annotation names a
// "<repo>:<alg>-<hex>.sig" manifest (or one of its layers) uses to carry
// signature material.
//
// They live here, in the ports vocabulary, rather than in the Sigstore
// adapter that verifies them, because reading these annotations off a manifest
// is not the verifier's job: several adapters (base-image resolution, remote
// cache verification, provenance resolution) have to extract the material
// before they can hand it to a CosignSigner or KeylessVerifier, and none of
// them may import a concrete adapter to learn a wire-format constant. Naming
// them also lets error messages say which annotation the bad material came
// from — during an incident, "the bundle annotation's logID is not hex" is
// actionable and "verification failed" is not.
const (
	// CosignSignatureAnnotation carries the base64 signature over the payload.
	CosignSignatureAnnotation = "dev.cosignproject.cosign/signature"

	// CosignCertificateAnnotation carries the Fulcio leaf certificate, as
	// plain PEM text.
	CosignCertificateAnnotation = "dev.sigstore.cosign/certificate"

	// CosignChainAnnotation carries the intermediate/root certificates as
	// plain PEM text. It is never trusted for path building — a correct
	// KeylessVerifier resolves the chain from its own trusted root.
	CosignChainAnnotation = "dev.sigstore.cosign/chain"

	// CosignBundleAnnotation carries the Rekor inclusion material as plain
	// JSON text.
	CosignBundleAnnotation = "dev.sigstore.cosign/bundle"
)

// CosignSignatureBundle contains the generated Simple Signing payload and signature details.
type CosignSignatureBundle struct {
	// PayloadBytes is the canonical Simple Signing JSON payload.
	PayloadBytes []byte

	// SignatureBytes is the raw cryptographic signature bytes (DER or raw Ed25519).
	SignatureBytes []byte

	// Base64Signature is the standard Base64-encoded signature string.
	Base64Signature string

	// Repo is the repository reference recorded in the payload.
	Repo string

	// Digest is the manifest digest recorded in the payload.
	Digest v1.Hash
}

// CosignSigner generates Simple Signing payloads, signs image digests with ECDSA/Ed25519 keys, and verifies signatures.
type CosignSigner interface {
	// CreatePayload builds the canonical Red Hat Simple Signing JSON payload for a request.
	CreatePayload(req CosignSignRequest) ([]byte, error)

	// Sign creates a signed CosignSignatureBundle for a request using keyPEM.
	Sign(ctx context.Context, req CosignSignRequest) (CosignSignatureBundle, error)

	// Verify validates a CosignSignatureBundle against a public key PEM, expected repo, and expected digest.
	Verify(ctx context.Context, bundle CosignSignatureBundle, pubKeyPEM []byte, expectedRepo string, expectedDigest v1.Hash) error
}

// InTotoPayloadType is the DSSE payloadType for in-toto statements (SLSA provenance & SBOM attestations).
const InTotoPayloadType = "application/vnd.in-toto+json"

// DSSESignature represents a single signature entry in a DSSE envelope.
type DSSESignature struct {
	KeyID string `json:"keyid,omitempty"`
	Sig   string `json:"sig"`
}

// DSSEEnvelope is a Dead Simple Signing Envelope (application/vnd.dsse.envelope.v1+json).
type DSSEEnvelope struct {
	Payload     string          `json:"payload"`
	PayloadType string          `json:"payloadType"`
	Signatures  []DSSESignature `json:"signatures"`
}

// DSSESignRequest supplies an arbitrary payload (e.g. SLSA Statement JSON) and key for DSSE signing.
type DSSESignRequest struct {
	// PayloadBytes is the unencoded payload bytes to wrap and sign. Required.
	PayloadBytes []byte

	// PayloadType is the media type of PayloadBytes (e.g. "application/vnd.in-toto+json"). Required.
	PayloadType string

	// KeyPEM is the PEM-encoded private key bytes (ECDSA P-256 or Ed25519). Required.
	KeyPEM []byte

	// KeyID is an optional identifier associated with the key.
	KeyID string
}

// DSSESigner wraps payloads into DSSE envelopes, signs PAE encodings, and verifies signatures.
type DSSESigner interface {
	// CreatePAE computes the canonical Pre-Authentication Encoding for payloadType and payload.
	CreatePAE(payloadType string, payload []byte) []byte

	// Sign wraps payloadBytes into a DSSEEnvelope signed by req.KeyPEM.
	Sign(ctx context.Context, req DSSESignRequest) (DSSEEnvelope, error)

	// Verify decodes envelope, validates its PAE signature against pubKeyPEM, and returns the raw payload bytes.
	Verify(ctx context.Context, envelope DSSEEnvelope, pubKeyPEM []byte) ([]byte, error)
}

// VerifyReleaseArtifactRequest contains parameters for verifying a release artifact signature.
type VerifyReleaseArtifactRequest struct {
	// ArtifactBytes is the raw byte content of the artifact (e.g. checksums.txt content). Required.
	ArtifactBytes []byte

	// SignatureBytes is the signature bytes (raw DER/Ed25519 or Base64 encoded). Required.
	SignatureBytes []byte

	// PublicKeyPEM is the PEM-encoded ECDSA or Ed25519 public key bytes. Required.
	PublicKeyPEM []byte
}

// ReleaseVerifier verifies release signatures and checksum integrity.
type ReleaseVerifier interface {
	// VerifyArtifactSignature verifies that SignatureBytes is a valid signature for ArtifactBytes using PublicKeyPEM.
	VerifyArtifactSignature(ctx context.Context, req VerifyReleaseArtifactRequest) error

	// VerifyChecksum validates that content SHA256 matches expectedChecksumHex.
	VerifyChecksum(content []byte, expectedChecksumHex string) error
}
