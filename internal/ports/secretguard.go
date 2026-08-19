package ports

import "context"

// SecretMatch describes a detected secret or sensitive token leak in a file.
type SecretMatch struct {
	FilePath      string `json:"file_path"`
	LineNumber    int    `json:"line_number"`
	RuleName      string `json:"rule_name"`
	SecretSnippet string `json:"secret_snippet"`
}

// SecretSkip records a text-eligible file that ScanDirectory did not
// actually inspect — currently only because it exceeded MaxFileSizeBytes.
// This is distinct from a file silently skipped for being recognized
// binary content (images, fonts, wasm, precompressed .gz/.br/.zst
// sidecars): those are never scannable text in the first place, so
// skipping them is not a coverage gap. A SecretSkip IS a coverage gap —
// a file that could contain shipped, human-readable code was not looked
// at — which is why ScanDirectory fails closed (Passed=false) whenever
// Skipped is non-empty, rather than silently reporting a clean scan for a
// file it never opened past its size check.
type SecretSkip struct {
	FilePath string `json:"file_path"`
	Reason   string `json:"reason"`
}

// SecretScanRequest configures directory secret scanning options.
type SecretScanRequest struct {
	ProjectDir    string   `json:"project_dir"`
	AllowPatterns []string `json:"allow_patterns,omitempty"`

	// MaxFileSizeBytes overrides the adapter's default per-file text-scan
	// size ceiling. Zero means "use the adapter's default". A file whose
	// size exceeds this AND is not recognized binary content is recorded
	// as a SecretSkip rather than silently dropped — see SecretSkip and
	// SecretScanResult.Passed.
	MaxFileSizeBytes int64 `json:"max_file_size_bytes,omitempty"`

	// ScanSourcemaps disables the adapter's default "*.map" exclusion.
	// Source maps are excluded by default because a build's output does
	// not normally ship them (pokkum's packager strips them unless
	// --sourcemap is set) — but when a build genuinely ships them, a
	// sourcemap can embed the ORIGINAL, un-minified source, including
	// anything baked in via $env/static/* or a build-time `define`
	// replacement. A caller scanning a tree where sourcemaps are known to
	// be packaged (req.Compile.Sourcemap) should set this so they are
	// covered instead of silently exempted.
	ScanSourcemaps bool `json:"scan_sourcemaps,omitempty"`
}

// SecretScanResult reports secret scan findings.
type SecretScanResult struct {
	Matches []SecretMatch `json:"matches"`

	// Skipped lists text-eligible files ScanDirectory did not actually
	// inspect (currently: exceeded MaxFileSizeBytes). Recognized-binary
	// files skipped for not being scannable text at all are NOT recorded
	// here — see SecretSkip's doc comment for the distinction. A non-empty
	// Skipped always forces Passed=false.
	Skipped []SecretSkip `json:"skipped,omitempty"`
	Passed  bool         `json:"passed"`
}

// SecretGuard defines the boundary port for build-time secret leak detection.
type SecretGuard interface {
	ScanDirectory(ctx context.Context, req SecretScanRequest) (SecretScanResult, error)
}

// AllowSecretMarker is the inline annotation that exempts a single line from
// secret scanning. It may sit on the flagged line itself or on the line directly
// above it, in any comment syntax — it is matched as a substring, so //, #,
// /* */ and <!-- --> all work without the scanner needing to know the language.
//
// Why a marker and not only a regex: an allow pattern matches a whole *line*, so
// writing one means describing the offending content, and if the finding is a
// genuine secret that regex ends up in a committed config file. A marker needs no
// description of the content at all. It is also line-scoped and travels with the
// code, so unlike a file:line suppression it cannot go stale when lines shift.
//
// It lives here rather than in the adapter because core names it when telling an
// operator how to accept a finding, and internal/core cannot import an adapter.
//
// Threat model, stated so it is not mistaken for an oversight: anyone able to add
// this marker can equally well add the secret itself, so it grants no capability
// an editor of the file did not already have. It records "this is deliberate"; it
// is not a security boundary.
// the opt-out marker itself, a fixed public token with no credential in it.
//
//nolint:gosec // G101 fires because the identifier contains "secret"; this is
const AllowSecretMarker = "pokkum:allow-secret"
