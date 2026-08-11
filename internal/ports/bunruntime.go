package ports

import (
	"context"
	"time"
)

// DefaultBunVersion is the default pinned Bun release version used for runtime layering when none is specified.
const DefaultBunVersion = "1.2.2"

// BunVariant specifies CPU/build variants of the Bun runtime binary.
type BunVariant string

const (
	// BunVariantStandard targets modern x86-64 / arm64 CPUs (AVX2 required on x86-64).
	BunVariantStandard BunVariant = "standard"

	// BunVariantBaseline targets older x86-64 CPUs (pre-AVX2 fallback).
	BunVariantBaseline BunVariant = "baseline"
)

// BunResolverRequest specifies the criteria for resolving a Bun runtime binary.
type BunResolverRequest struct {
	// Platform is the target execution platform (e.g., linux/amd64 or linux/arm64). Required.
	Platform Platform

	// Version is the requested Bun semver string (e.g. "1.2.2"). Empty defaults to DefaultBunVersion.
	Version string

	// Variant is the requested CPU variant ("standard" or "baseline"). Empty defaults to BunVariantStandard.
	Variant BunVariant

	// CustomBinaryPath is an optional local filesystem escape hatch path to a bun executable (--bun-binary).
	// If set, resolution skips network/cache lookups and uses this binary directly.
	CustomBinaryPath string

	// SourceDateEpoch is the deterministic timestamp for file headers/mtime derivation. Required.
	SourceDateEpoch time.Time
}

// BunResolverResult reports the verified Bun runtime binary artifact details.
type BunResolverResult struct {
	// BinaryPath is the absolute local filesystem path to the verified bun executable.
	BinaryPath string

	// Version is the resolved Bun version string (e.g., "1.2.2").
	Version string

	// Variant is the resolved CPU variant ("standard" or "baseline").
	Variant BunVariant

	// Platform is the target platform.
	Platform Platform

	// SHA256 is the lowercase hex digest of the uncompressed bun executable.
	SHA256 string

	// Size is the size of the binary in bytes.
	Size int64
}

// BunRuntimeResolver provides pinned Bun runtime binaries for layer packaging.
// Implementations must handle local caching (~/.cache/pokkum/bun/), SHA256 validation,
// and custom local binary overrides.
type BunRuntimeResolver interface {
	// Resolve returns a verified Bun runtime binary matching req.
	Resolve(ctx context.Context, req BunResolverRequest) (BunResolverResult, error)
}
