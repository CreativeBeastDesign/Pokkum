package ports

import (
	"context"
)

// PinnedBuildInputs captures all deterministic inputs recorded in SLSA provenance
// required to perform a bit-for-bit rebuild verification.
type PinnedBuildInputs struct {
	Repo           string            `json:"repo"`
	Commit         string            `json:"commit"`
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
	ImageRef     string
	ExpectSource string // Optional repo@commit filter
}

// ProvenanceResolver defines the port for fetching and verifying image attestations and provenance.
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
