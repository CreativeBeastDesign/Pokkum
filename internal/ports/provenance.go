package ports

import (
	"context"
)

// SourceProvenance describes how PinnedBuildInputs.Repo/Commit were
// obtained, not merely what value they hold. Repo/Commit strings look
// identical to a caller regardless of their origin, but their trust
// properties are not: a value read off the image's own unsigned OCI
// annotations is fully attacker/operator-controlled (anyone able to push a
// tag to the repository can set it), while a value read out of a
// cryptographically verified SLSA statement is bound to the exact image
// digest by a signature ResolveProvenance checked. Callers that assert
// something about the source (--expect-source) MUST consult this field
// rather than assuming Repo/Commit being non-empty means anything was
// verified — see ErrUnverifiedSourceProvenance.
type SourceProvenance string

const (
	// SourceProvenanceNone means no source repo/commit was resolved from any
	// source: neither the image's own annotations nor a verified SLSA
	// statement carried one.
	SourceProvenanceNone SourceProvenance = ""

	// SourceProvenanceUnverified means Repo/Commit came from the image's own
	// unsigned OCI annotations (org.opencontainers.image.source/.revision).
	// These are not cryptographically bound to the image in any way; whoever
	// can push a tag to the repository controls them.
	SourceProvenanceUnverified SourceProvenance = "unverified"

	// SourceProvenanceVerified means Repo/Commit came from a
	// cryptographically verified SLSA provenance statement: a DSSE envelope
	// whose signature ResolveProvenance checked against a configured key,
	// carrying a "source-code" resolved dependency, whose statement subject
	// was itself checked against the image digest under verification.
	SourceProvenanceVerified SourceProvenance = "verified"
)

// PinnedBuildInputs captures all deterministic inputs recorded in SLSA provenance
// required to perform a bit-for-bit rebuild verification.
type PinnedBuildInputs struct {
	Repo   string `json:"repo"`
	Commit string `json:"commit"`

	// SourceProvenance reports how Repo/Commit above were obtained. See
	// SourceProvenance's doc comment — this is the field a caller comparing
	// Repo/Commit against an expectation (--expect-source) must check before
	// trusting the comparison at all.
	SourceProvenance SourceProvenance `json:"source_provenance,omitempty"`

	BaseImageRef   string            `json:"base_image_ref"`
	BaseImageHash  string            `json:"base_image_hash"`
	BunVersion     string            `json:"bun_version"`
	BunBinaryHash  string            `json:"bun_binary_hash"`
	GoVersion      string            `json:"go_version"`
	BuilderOSArch  string            `json:"builder_os_arch"`
	LockfileHashes map[string]string `json:"lockfile_hashes,omitempty"`
	IgnoreHash     string            `json:"ignore_hash,omitempty"`
	Platforms      []string          `json:"platforms,omitempty"`
}

// ProvenanceSummary holds the validated attestation metadata for an image reference.
type ProvenanceSummary struct {
	ImageRef        string            `json:"image_ref"`
	ImageDigest     string            `json:"image_digest"`
	HasProvenance   bool              `json:"has_provenance"`
	SignatureValid  bool              `json:"signature_valid"`
	SignerIdentity  string            `json:"signer_identity,omitempty"`
	PinnedInputs    PinnedBuildInputs `json:"pinned_inputs"`
	ToolchainMatch  bool              `json:"toolchain_match"`
	ToolchainNotes  string            `json:"toolchain_notes"`
	ExpectedL1Match bool              `json:"expected_l1_match"`
}

// ProvenanceResolverRequest represents a request to fetch and validate provenance for an image.
type ProvenanceResolverRequest struct {
	ImageRef           string
	ExpectSource       string // Optional repo@commit filter
	RegistryConfigPath string // Optional custom docker config.json path

	// KeylessIdentity is the expected Fulcio certificate identity that a
	// keyless Sigstore signature on the image must have been issued to.
	//
	// It MUST be supplied by the caller — from a flag, a config file, or an
	// explicitly-chosen default for a known publisher. It must NEVER be
	// derived from the certificate under verification: reading the expected
	// issuer/SAN out of the very certificate being checked makes the
	// comparison tautological, so every certificate Fulcio ever issued (i.e.
	// anyone with a GitHub or Google account) would satisfy it. See
	// KeylessIdentity's own doc comment and KeylessVerifier's empty-identity
	// refusal.
	//
	// An empty value does not mean "accept any signer": it means the resolver
	// has no basis on which to judge keyless material, and
	// ResolveProvenance refuses to continue if the image actually carries
	// any (see ProvenanceResolver).
	KeylessIdentity KeylessIdentity

	// PublicKeyPEM is the static Cosign public key used to verify a
	// static-key signature on the image. Empty means the implementation falls
	// back to its own configured default key — which is still a specific key,
	// never "any key", so unlike KeylessIdentity an empty value here is not a
	// fail-open condition.
	PublicKeyPEM []byte

	// TrustedRootJSON is an optional Sigstore trusted-root snapshot (the
	// application/vnd.dev.sigstore.trustedroot+json format) to verify keyless
	// material against. Empty means the KeylessVerifier's own embedded
	// public-good snapshot.
	TrustedRootJSON []byte

	// AllowUnverifiedSource is the escape hatch for ExpectSource. By default,
	// ResolveProvenance refuses (ErrUnverifiedSourceProvenance) to compare
	// ExpectSource against source information that isn't backed by a
	// cryptographically verified SLSA statement (see SourceProvenance) —
	// comparing an assertion against the image's own unsigned annotations
	// looks like a real check while proving nothing, since anyone able to
	// push a tag to the repository controls those annotations. Setting this
	// true (--allow-unverified-source) permits the comparison to proceed
	// anyway; the result is still reported with
	// PinnedInputs.SourceProvenance == SourceProvenanceUnverified so it can
	// never be mistaken for a real verification, and ResolveProvenance logs
	// a Warn when this path is taken. Mirrors the shape of
	// BuildRequest.AllowIncompleteScan/--allow-incomplete: an explicit,
	// visible downgrade rather than a silent one.
	AllowUnverifiedSource bool
}

// ProvenanceResolver defines the port for fetching and verifying image attestations and provenance.
//
// ResolveProvenance fails closed on an unconstrained keyless verification: if
// the image carries keyless Sigstore signature material that nothing else has
// already verified, and req.KeylessIdentity is Empty(), it returns an error
// naming the missing configuration rather than reporting an unverified — or,
// worse, trivially-satisfiable — result.
type ProvenanceResolver interface {
	ResolveProvenance(ctx context.Context, req ProvenanceResolverRequest) (ProvenanceSummary, error)
}

// VerifyOutput represents the structured JSON output payload for `pokkum verify`.
type VerifyOutput struct {
	ImageRef        string            `json:"image_ref"`
	Verdict         string            `json:"verdict"` // "VERIFIED_L1", "VERIFIED_L2", "MISMATCH", "INCONCLUSIVE"
	Level           string            `json:"level"`   // "L1", "L2", "L3", "NONE"
	Provenance      ProvenanceSummary `json:"provenance"`
	RebuildDuration string            `json:"rebuild_duration,omitempty"`
	MismatchDetails []string          `json:"mismatch_details,omitempty"`
}

// HistoryOutput represents the structured JSON output payload for
// `pokkum history <image>`. Every field here is read directly off the
// image's real, pulled manifest — deliberately no builder/signer/SLSA
// fields that this command doesn't actually verify: a bool field that's
// always false because nothing checked it reads identically to "checked
// and failed," which is worse than not having the field at all. Use
// `pokkum verify <ref>` for an actual cryptographic verdict.
type HistoryOutput struct {
	ImageRef       string            `json:"image_ref"`
	ImageDigest    string            `json:"image_digest"`
	BuildTimestamp string            `json:"build_timestamp,omitempty"`
	GitRepo        string            `json:"git_repo,omitempty"`
	GitCommit      string            `json:"git_commit,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
}
