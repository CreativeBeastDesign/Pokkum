package ports

import (
	"context"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// SBOMFormat selects the serialisation of the generated software bill of
// materials. It is a string type so it round-trips through flags and config.
type SBOMFormat string

const (
	// SBOMFormatSPDXJSON is SPDX 2.3 in JSON, the default. It is what
	// `cosign download sbom` and most policy engines expect first.
	SBOMFormatSPDXJSON SBOMFormat = "spdx-json"

	// SBOMFormatCycloneDXJSON is CycloneDX in JSON, for toolchains standardised
	// on it (Dependency-Track and friends).
	SBOMFormatCycloneDXJSON SBOMFormat = "cyclonedx-json"

	// SBOMFormatNone disables SBOM generation entirely. It is a format value
	// rather than a separate boolean so that "off" is representable in one
	// field and cannot contradict a second one.
	SBOMFormatNone SBOMFormat = "none"
)

// DefaultSBOMFormat is used when a BuildRequest leaves the SBOM format at its
// zero value.
const DefaultSBOMFormat = SBOMFormatSPDXJSON

// SBOMAttachMode selects how the SBOM artifact is attached to the OCI image.
type SBOMAttachMode string

const (
	// SBOMAttachReferrer attaches the SBOM as an OCI 1.1 referrer artifact,
	// explicitly — no fallback if the registry doesn't support it, the push
	// fails outright, since the caller asked for this mode by name.
	SBOMAttachReferrer SBOMAttachMode = "referrer"

	// SBOMAttachTag attaches the SBOM using the legacy .sbom tag convention.
	SBOMAttachTag SBOMAttachMode = "tag"

	// SBOMAttachAuto attempts the OCI 1.1 Referrers API first and falls back
	// to the legacy .sbom tag convention if the registry doesn't support it
	// (ECR, older Harbor, and older Artifactory still lack it) — the
	// default, so a caller doesn't need to know their registry's capability
	// in advance to get a real SBOM attachment.
	SBOMAttachAuto SBOMAttachMode = "auto"
)

// DefaultSBOMAttachMode is used when a BuildRequest leaves the SBOM attach mode at its
// zero value.
const DefaultSBOMAttachMode = SBOMAttachAuto

// Valid reports whether m is a known attach mode.
func (m SBOMAttachMode) Valid() bool {
	switch m {
	case SBOMAttachReferrer, SBOMAttachTag, SBOMAttachAuto:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (m SBOMAttachMode) String() string { return string(m) }

// Media types for the attached SBOM layer, matching the cosign convention.
const (
	MediaTypeSPDXJSON      = "application/spdx+json"
	MediaTypeCycloneDXJSON = "application/vnd.cyclonedx+json"
)

// Valid reports whether f is a known format. The zero value ("") is NOT valid;
// core normalises it to DefaultSBOMFormat before validating.
func (f SBOMFormat) Valid() bool {
	switch f {
	case SBOMFormatSPDXJSON, SBOMFormatCycloneDXJSON, SBOMFormatNone:
		return true
	default:
		return false
	}
}

// Enabled reports whether an SBOM should be generated at all.
func (f SBOMFormat) Enabled() bool { return f.Valid() && f != SBOMFormatNone }

// String implements fmt.Stringer.
func (f SBOMFormat) String() string { return string(f) }

// MediaType returns the OCI layer media type for the format, and reports false
// for SBOMFormatNone and unknown formats.
func (f SBOMFormat) MediaType() (string, bool) {
	switch f {
	case SBOMFormatSPDXJSON:
		return MediaTypeSPDXJSON, true
	case SBOMFormatCycloneDXJSON:
		return MediaTypeCycloneDXJSON, true
	default:
		return "", false
	}
}

// SBOMRequest asks for a bill of materials for the application.
type SBOMRequest struct {
	// ProjectDir is the absolute SvelteKit project root to scan. Required.
	//
	// The scan target is the PROJECT, not the compiled binary, and this is the
	// central design decision of this port. A Bun-compiled executable is a
	// single opaque blob with the entire dependency tree bundled and minified
	// into it; scanning it yields nothing useful. Scanning the project
	// directory reads package.json and the lockfile and produces the real npm
	// dependency graph — which is what a vulnerability scanner needs and what
	// a consumer of the SBOM actually wants to know.
	//
	// The consequence downstream: the SBOM describes the source project, so it
	// is generated once per build and attached to the index digest, not once
	// per platform. The dependency set does not vary by architecture.
	ProjectDir string

	// BaseImages maps each built platform to its resolved base image, so the
	// generator can catalogue the OS packages the image actually ships
	// (Debian's /var/lib/dpkg/status.d, Alpine's /lib/apk/db/installed)
	// alongside the project's npm dependencies.
	//
	// Optional, and the distinction matters: nil means "no image was offered,
	// so no OS scan was attempted", which the document records as
	// osPackagesScanned=false. An image that genuinely ships no package
	// database records osPackagesScanned=true with a count of zero. Those two
	// are different claims and must not share a representation — an SBOM that
	// silently omits libc6 and libssl3 looks like coverage that isn't there.
	BaseImages map[Platform]v1.Image

	// Format is the output format. Required and must be Valid and Enabled;
	// core must not call Generate at all when the format is SBOMFormatNone.
	Format SBOMFormat

	// Name is the component name recorded as the document's subject, normally
	// the image repository. Empty means the generator derives one from
	// package.json.
	Name string

	// Version is the component version recorded in the document. Empty means
	// the generator derives one from package.json.
	Version string

	// CreatedAt is the deterministic build timestamp from SOURCE_DATE_EPOCH.
	// Required (non-zero). It is written as the document's creation time.
	//
	// This is the one field most likely to be forgotten, and forgetting it
	// silently breaks reproducibility: an SBOM stamped with time.Now changes
	// the SBOM layer digest on every run even when nothing else did. The
	// generator must also suppress any other nondeterminism the scanner
	// introduces — notably document namespaces and element IDs derived from a
	// random UUID, which must be derived from the content instead.
	CreatedAt time.Time

	// ExcludePaths are glob patterns, relative to ProjectDir, to skip while
	// scanning. Nil means the generator's defaults, which should already skip
	// .svelte-kit, build output and .git.
	ExcludePaths []string

	// BunVersion is the resolved version of the Bun runtime embedded in the
	// image (StrategyLayered) or that compiled it (StrategyExe). Empty means
	// no Bun component is added to the document — e.g. for a static build,
	// which ships no Bun runtime at all.
	//
	// This is deliberately a different source than the project's own
	// dependency graph: Bun is a Pokkum-embedded runtime artifact, not
	// something declared in package.json/bun.lock, so it can't be discovered
	// by scanning ProjectDir the way npm packages are.
	BunVersion string

	// BunSHA256 is the lowercase hex SHA256 of the resolved Bun artifact
	// (ports.BunResolverResult.SHA256). Empty omits the hash from the
	// document's Bun component while still recording its version.
	BunSHA256 string
}

// SBOMDocument is a generated bill of materials, held in memory. SBOMs are
// small relative to an image layer, and keeping the bytes in a value rather
// than a file keeps the attach path free of temporary files whose mtimes would
// themselves need to be made deterministic.
type SBOMDocument struct {
	// Format is the format the document was serialised in. Required.
	Format SBOMFormat

	// MediaType is the OCI media type of the document, from Format.MediaType().
	// Required — the registry adapter uses it verbatim as the layer media type.
	MediaType string

	// Content is the serialised document. Required and non-empty.
	Content []byte

	// SHA256 is the lowercase hex digest of Content, with no "sha256:" prefix.
	// Empty means the generator did not compute it. Used for the build summary
	// and for verifying that two runs really did produce identical documents.
	SHA256 string

	// PackageCount is the number of packages catalogued. Informational, for the
	// build summary; zero is legal but suspicious and worth warning about.
	PackageCount int
}

// SBOMGenerator produces a bill of materials for the image. It is implemented
// by internal/adapters/sbom, which catalogues packages with this project's own
// parsers in internal/adapters/scannerutils — npm lockfiles plus the base
// image's dpkg/apk database — and depends on no external cataloguer.
//
// This comment previously said the adapter "wraps anchore/syft", and went on to
// explain at length why no syft type may appear in the signature. Syft is not
// and has never been a dependency: it appears nowhere in go.mod or go.sum. The
// reasoning was sound and still applies to any future external cataloguer —
// keep third-party SBOM types out of this signature so a dependency's minor
// release cannot force a change on the application service — but the premise
// was false, and a doc comment naming a dependency the project does not have
// sends the next reader looking for code that does not exist.
//
// Error expectations: core.ErrSBOMFailed for a scan or serialisation failure,
// core.ErrInvalidSBOMFormat if Format is not Valid or is SBOMFormatNone.
//
// Implementations must be safe for concurrent use.
type SBOMGenerator interface {
	// Generate scans req.ProjectDir and returns the serialised document.
	Generate(ctx context.Context, req SBOMRequest) (*SBOMDocument, error)
}
