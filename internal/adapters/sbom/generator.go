// Package sbom implements ports.SBOMGenerator by wrapping anchore/syft's
// library API to scan a SvelteKit PROJECT DIRECTORY, not the compiled Bun
// binary. package.json and the lockfile (bun.lock, package-lock.json, ...)
// are where the real npm dependency inventory lives; the compiled binary is
// a single, minified, opaque blob with everything already baked in, and
// scanning it would yield nothing a vulnerability scanner could use. See
// ports.SBOMRequest.ProjectDir for the full rationale.
//
// # The reproducibility trap
//
// syft's SPDX and CycloneDX JSON encoders both stamp two nondeterministic
// values into every document on every call:
//
//   - a document identifier generated from a fresh
//     github.com/google/uuid.NewRandom() — DocumentNamespace for SPDX
//     (github.com/anchore/syft/syft/format/common/spdxhelpers.
//     ToFormatModel, via internal helpers.DocumentNamespace) and
//     SerialNumber for CycloneDX
//     (.../cyclonedxhelpers/to_format_model.go: cdxBOM.SerialNumber =
//     uuid.New().URN())
//   - a creation timestamp hardcoded to time.Now().UTC() in the same two
//     files (CreationInfo.Created for SPDX, Metadata.Timestamp for
//     CycloneDX)
//
// Neither value is reachable through spdxjson.EncoderConfig,
// cyclonedxjson.EncoderConfig, or syft.CreateSBOMConfig — they are internal
// implementation details of format encoders syft does not expose a hook
// for, confirmed by reading the vendored source rather than assumed. Left
// alone, every build would produce a byte-different SBOM even with an
// identical dependency set and an identical SOURCE_DATE_EPOCH, silently
// breaking Pokkum's reproducibility promise while every single build still
// looks correct.
//
// There being no config hook, Generate encodes once with syft's own
// encoder and then POST-PROCESSES the marshalled JSON: rewriteSPDX and
// rewriteCycloneDX overwrite the namespace/serial with a value derived
// deterministically from the component identity and the sorted package set
// (see contentIdentityUUID), and overwrite the timestamp with
// req.CreatedAt. This is a deliberate, documented fallback — not an
// oversight — because syft's public API gives no cleaner path.
//
// Package and relationship ORDER does not need the same treatment: syft
// already sorts both before encoding (pkg.Collection.Sorted in
// syft/pkg/collection.go, called from spdxhelpers.toPackages;
// internal/relationship.Index, which documents "always return sorted for
// SBOM stability"), which this package verified by reading that source
// rather than assuming it.
//
// # .pokkumignore
//
// Generate applies internal/adapters/ignore for the .pokkumignore exclude
// mechanism. syft's own directory-source exclusion
// (source.Config.Exclude.Paths, wired through
// directorysource.GetDirectoryExclusionFunctions) only understands a flat
// list of doublestar globs ORed together — it has no concept of a later
// pattern overriding an earlier one, so gitignore's negation ("!pattern")
// cannot be expressed in it directly. To still honour negation, Generate
// walks req.ProjectDir itself first, resolving every path's ignore
// decision through ignore.Matcher (which does understand negation and
// precedence), and hands syft only the resulting flat list of already
// negation-resolved, literal exclusion paths. Once a directory is excluded
// this way its subtree is never walked or handed to syft at all — matching
// both git's own behaviour (a negation cannot reach inside an excluded
// directory, see the ignore package doc) and the requirement that an
// excluded file must genuinely never appear in the SBOM, not merely be
// filtered out of it afterwards.
package sbom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/cataloging"
	"github.com/anchore/syft/syft/format"
	"github.com/anchore/syft/syft/format/cyclonedxjson"
	"github.com/anchore/syft/syft/format/spdxjson"
	"github.com/anchore/syft/syft/pkg"
	"github.com/anchore/syft/syft/pkg/cataloger/javascript"
	syftsbom "github.com/anchore/syft/syft/sbom"
	"github.com/anchore/syft/syft/source"
	"github.com/anchore/syft/syft/source/directorysource"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// defaultScanExcludes are applied when SBOMRequest.ExcludePaths is nil, per
// its documented contract: "the generator's defaults, which should already
// skip .svelte-kit, build output and .git." These are gitignore-syntax
// patterns fed through the same ignore.Matcher as .pokkumignore, so a
// trailing slash restricts them to directories exactly as it would there.
var defaultScanExcludes = []string{
	".svelte-kit/",
	"dist/",
	".git/",
}

// pokkumSBOMNamespace is a fixed namespace UUID (RFC 4122, generated once
// and hardcoded below) used as the base for deriving version-5
// (SHA1 name-based) UUIDs deterministically from SBOM content — see
// contentIdentityUUID. It must never change: doing so would change every
// namespace/serial number Generate has ever produced for identical input.
var pokkumSBOMNamespace = uuid.MustParse("2f6e6b6b-756d-4b3c-9c1e-706f6b6b756d")

// Generator implements ports.SBOMGenerator against anchore/syft's library
// API. The zero value is not usable; construct with NewGenerator.
//
// Generator holds no mutable state and is safe for concurrent use.
type Generator struct {
	log *slog.Logger
}

var _ ports.SBOMGenerator = (*Generator)(nil)

// NewGenerator constructs a Generator. A nil logger defaults to
// slog.Default().
//
// syft's own internal logger (github.com/anchore/syft/internal/log)
// defaults to a discard logger and its event bus
// (github.com/anchore/syft/internal/bus) is a no-op until something calls
// bus.Set — both confirmed by reading that source — so a library caller
// that never calls syft's clio/CLI setup, as this package deliberately does
// not, gets a silent syft by construction. NewGenerator does not need to,
// and does not, wire slog into syft's logger: doing so would require
// depending on syft's internal logger adapter interface for no benefit,
// since the two log statements this package itself emits (via log) are
// enough to explain what happened.
func NewGenerator(log *slog.Logger) *Generator {
	if log == nil {
		log = slog.Default()
	}
	return &Generator{log: log}
}

// Generate implements ports.SBOMGenerator.
func (g *Generator) Generate(ctx context.Context, req ports.SBOMRequest) (*ports.SBOMDocument, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !req.Format.Valid() || !req.Format.Enabled() {
		return nil, fmt.Errorf("sbom: format %q: %w", req.Format, core.ErrInvalidSBOMFormat)
	}
	if strings.TrimSpace(req.ProjectDir) == "" {
		return nil, fmt.Errorf("sbom: project directory is required: %w", core.ErrSBOMFailed)
	}
	if req.CreatedAt.IsZero() {
		return nil, fmt.Errorf("sbom: CreatedAt is required (SOURCE_DATE_EPOCH): %w", core.ErrSBOMFailed)
	}

	// Resolve symlinks once, up front, and use the canonical path
	// everywhere below. This matters on macOS in particular, where the
	// system temp directory (and therefore every t.TempDir() in this
	// package's own tests) lives under /var, itself a symlink to
	// /private/var: syft's directory file-resolver canonicalises the path
	// it walks via symlink resolution internally, but
	// directorysource.GetDirectoryExclusionFunctions builds its exclusion
	// prefix from filepath.Abs alone, which does NOT resolve symlinks. Left
	// alone, that mismatch means an exclude pattern silently never matches
	// anything — doublestar.Match compares "/var/folders/.../excluded"
	// against the walker's "/private/var/folders/.../excluded" and the
	// exclusion is a silent no-op, which is exactly the failure mode this
	// package cannot afford to have. Canonicalising ProjectDir before it
	// reaches either buildExcludePatterns or directorysource.New keeps both
	// sides working from the same string.
	projectDir, err := filepath.EvalSymlinks(req.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("sbom: resolving %s: %w: %w", req.ProjectDir, err, core.ErrSBOMFailed)
	}
	req.ProjectDir = projectDir

	name, version := req.Name, req.Version
	if name == "" || version == "" {
		pkgName, pkgVersion := readPackageIdentity(req.ProjectDir)
		if name == "" {
			name = pkgName
		}
		if version == "" {
			version = pkgVersion
		}
	}
	if name == "" {
		name = filepath.Base(req.ProjectDir)
	}

	matcher, err := g.buildMatcher(req)
	if err != nil {
		return nil, fmt.Errorf("sbom: %s: %w: %w", req.ProjectDir, err, core.ErrSBOMFailed)
	}

	excludePatterns, err := buildExcludePatterns(ctx, req.ProjectDir, matcher)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("sbom: computing excludes for %s: %w: %w", req.ProjectDir, err, core.ErrSBOMFailed)
	}
	g.log.DebugContext(ctx, "sbom: resolved excludes", "project_dir", req.ProjectDir, "count", len(excludePatterns))

	src, err := directorysource.New(directorysource.Config{
		Path: req.ProjectDir,
		Exclude: source.ExcludeConfig{
			Paths: excludePatterns,
		},
		Alias: source.Alias{
			Name:    name,
			Version: version,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("sbom: opening %s: %w: %w", req.ProjectDir, err, core.ErrSBOMFailed)
	}
	defer src.Close()

	s, err := syft.CreateSBOM(ctx, src, createSBOMConfig())
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("sbom: scanning %s: %w: %w", req.ProjectDir, err, core.ErrSBOMFailed)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	raw, err := encode(*s, req.Format)
	if err != nil {
		return nil, fmt.Errorf("sbom: encoding %s: %w: %w", req.Format, err, core.ErrSBOMFailed)
	}

	packages := s.Artifacts.Packages.Sorted()
	final, err := makeDeterministic(req.Format, raw, name, version, req.CreatedAt, packages)
	if err != nil {
		return nil, fmt.Errorf("sbom: %s: %w: %w", req.Format, err, core.ErrSBOMFailed)
	}

	mediaType, ok := req.Format.MediaType()
	if !ok {
		// Unreachable given the Valid/Enabled check above, but guarded
		// rather than assumed.
		return nil, fmt.Errorf("sbom: format %q has no media type: %w", req.Format, core.ErrInvalidSBOMFormat)
	}

	sum := sha256.Sum256(final)
	doc := &ports.SBOMDocument{
		Format:       req.Format,
		MediaType:    mediaType,
		Content:      final,
		SHA256:       hex.EncodeToString(sum[:]),
		PackageCount: len(packages),
	}
	g.log.InfoContext(ctx, "sbom: generated", "format", req.Format, "packages", doc.PackageCount, "bytes", len(final))
	return doc, nil
}

// createSBOMConfig is syft's default CreateSBOMConfig with the RPM
// database cataloger removed. That cataloger is part of syft's default
// task set for a directory source regardless of whether the directory
// contains anything RPM-related, and instantiating it requires a
// database/sql driver registered under the name "sqlite"
// (modernc.org/sqlite, per syft's own examples) that this module does not
// otherwise need and does not import; without it CreateSBOM fails outright
// with "sqlite driver is required ... none registered" even though the
// scan target is an npm project directory that could never contain an RPM
// database. Removing the one cataloger that needs it is simpler and more
// honest than importing a database driver this package will never use a
// database with.
func createSBOMConfig() *syft.CreateSBOMConfig {
	cfg := syft.DefaultCreateSBOMConfig().WithCatalogerSelection(
		cataloging.NewSelectionRequest().WithRemovals("rpm-db-cataloger"),
	)

	// syft's javascript cataloger excludes devDependencies (and any
	// dev-only transitive package) by default. That default fits a
	// vulnerability scan of a running application, but not Pokkum's SBOM:
	// the whole reason to scan the project directory rather than the
	// compiled binary is to see the *complete* dependency graph, and a
	// SvelteKit project's build tooling (svelte, vite, @sveltejs/kit) lives
	// entirely in devDependencies. Silently dropping them would make the
	// SBOM materially incomplete for exactly the packages a caller is most
	// likely to search for.
	cfg.Packages = cfg.Packages.WithJavascriptConfig(
		javascript.DefaultCatalogerConfig().WithIncludeDevDependencies(true),
	)

	return cfg
}

// buildMatcher composes the effective exclude rule set for req: the
// built-in .pokkumignore defaults, then either req.ExcludePaths or, if that
// is empty, this package's own defaultScanExcludes, then finally the
// project's own .pokkumignore file — applied last so a project's own rules
// (including a negation) always have the final say over everything before
// it, matching ignoreutils.Matcher's documented last-match-wins precedence.
func (g *Generator) buildMatcher(req ports.SBOMRequest) (*ignoreutils.Matcher, error) {
	patterns := ignoreutils.DefaultPatterns()

	extra := req.ExcludePaths
	if len(extra) == 0 {
		extra = defaultScanExcludes
	}
	patterns = append(patterns, extra...)

	filePatterns, err := ignoreutils.ReadPatterns(req.ProjectDir)
	if err != nil {
		return nil, err
	}
	patterns = append(patterns, filePatterns...)

	return ignoreutils.New(patterns)
}

// buildExcludePatterns builds a flat list of exclusion patterns that Syft
// understands, by resolving every entry's ignore decision through m first.
// way (see directorysource.GetDirectoryExclusionFunctions).
func buildExcludePatterns(ctx context.Context, root string, m *ignoreutils.Matcher) ([]string, error) {
	var patterns []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if m.Match(rel, d.IsDir()) {
			patterns = append(patterns, "./"+rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return patterns, nil
}

// encode runs syft's own encoder for format. Both encoders are configured
// non-pretty (compact JSON), matching their own DefaultEncoderConfig.
func encode(s syftsbom.SBOM, f ports.SBOMFormat) ([]byte, error) {
	var enc syftsbom.FormatEncoder
	var err error
	switch f {
	case ports.SBOMFormatSPDXJSON:
		enc, err = spdxjson.NewFormatEncoderWithConfig(spdxjson.DefaultEncoderConfig())
	case ports.SBOMFormatCycloneDXJSON:
		enc, err = cyclonedxjson.NewFormatEncoderWithConfig(cyclonedxjson.DefaultEncoderConfig())
	default:
		return nil, fmt.Errorf("unsupported format %q", f)
	}
	if err != nil {
		return nil, fmt.Errorf("building encoder: %w", err)
	}
	return format.Encode(s, enc)
}

// makeDeterministic is the reproducibility fix-up described in the package
// doc: it overwrites the nondeterministic namespace/serial and timestamp
// fields syft's encoders stamp into raw, with values derived from name,
// version, the sorted package set, and createdAt.
func makeDeterministic(f ports.SBOMFormat, raw []byte, name, version string, createdAt time.Time, packages []pkg.Package) ([]byte, error) {
	id := contentIdentityUUID(name, version, packages)
	created := createdAt.UTC().Truncate(time.Second).Format(time.RFC3339)

	switch f {
	case ports.SBOMFormatSPDXJSON:
		return rewriteSPDX(raw, name, id, created)
	case ports.SBOMFormatCycloneDXJSON:
		return rewriteCycloneDX(raw, id, created)
	default:
		return nil, fmt.Errorf("unsupported format %q", f)
	}
}

// contentIdentityUUID derives a version-5 (SHA1 name-based, RFC 4122) UUID
// from the component's identity and its resolved package set. Being
// name-based rather than random, calling it twice for the same input always
// returns the same UUID — that determinism is the entire point, since it
// replaces syft's uuid.NewRandom() as the source of the document
// namespace/serial number. Two SBOMs describing genuinely different
// dependency sets legitimately want different identifiers, so the package
// set is folded in rather than deriving the UUID from name/version alone.
func contentIdentityUUID(name, version string, packages []pkg.Package) uuid.UUID {
	ids := make([]string, 0, len(packages))
	for _, p := range packages {
		ids = append(ids, fmt.Sprintf("%s@%s@%s", p.Name, p.Version, p.Type))
	}
	// packages is already sorted (pkg.Collection.Sorted), but sorting again
	// here means contentIdentityUUID's own determinism does not depend on
	// that caller-side guarantee holding forever.
	sort.Strings(ids)

	var b strings.Builder
	fmt.Fprintf(&b, "pokkum-sbom\n%s@%s\n", name, version)
	for _, id := range ids {
		b.WriteString(id)
		b.WriteByte('\n')
	}
	return uuid.NewSHA1(pokkumSBOMNamespace, []byte(b.String()))
}

// rewriteSPDX overwrites the SPDX document's namespace and creation
// timestamp in place. See the package doc for why this is a post-processing
// step rather than an encoder option.
func rewriteSPDX(raw []byte, name string, id uuid.UUID, created string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decoding spdx document: %w", err)
	}

	// Mirrors the shape syft itself builds
	// (spdxhelpers.DocumentNamespace: https://anchore.com/<input>/<name>-<uuid>),
	// substituting our own host and the content-derived UUID in place of
	// uuid.NewRandom().
	doc["documentNamespace"] = fmt.Sprintf("https://pokkum.dev/sbom/%s-%s", sanitizeNamespaceName(name), id.String())

	if ci, ok := doc["creationInfo"].(map[string]any); ok {
		ci["created"] = created
	} else {
		return nil, fmt.Errorf("spdx document missing creationInfo")
	}

	return marshalDeterministic(doc)
}

// rewriteCycloneDX overwrites the CycloneDX document's serial number and
// metadata timestamp in place. See the package doc for why this is a
// post-processing step rather than an encoder option.
func rewriteCycloneDX(raw []byte, id uuid.UUID, created string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decoding cyclonedx document: %w", err)
	}

	// Matches the "urn:uuid:<uuid>" shape of uuid.New().URN(), which is
	// what syft's own encoder produces (cyclonedxhelpers.ToFormatModel:
	// cdxBOM.SerialNumber = uuid.New().URN()).
	doc["serialNumber"] = "urn:uuid:" + id.String()

	if md, ok := doc["metadata"].(map[string]any); ok {
		md["timestamp"] = created
	} else {
		return nil, fmt.Errorf("cyclonedx document missing metadata")
	}

	return marshalDeterministic(doc)
}

// sanitizeNamespaceName mirrors spdxhelpers.cleanName closely enough for a
// namespace URI path segment: '#' and ':' cannot appear in a URI path the
// way this document embeds it.
func sanitizeNamespaceName(name string) string {
	name = strings.ReplaceAll(name, "#", "-")
	name = strings.ReplaceAll(name, ":", "-")
	name = strings.ReplaceAll(name, "/", "-")
	return name
}

// marshalDeterministic serialises v as compact JSON with HTML-escaping
// disabled, matching syft's own encoders (both call
// json.Encoder.SetEscapeHTML(false)). Marshalling a map[string]any is
// deterministic on its own terms — encoding/json always emits a map's keys
// in sorted order — which is what makes generating the same input twice
// produce byte-identical output, independent of whatever field order
// syft's own struct-based encoder happened to use.
func marshalDeterministic(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// json.Encoder.Encode appends a trailing newline that json.Marshal does
	// not; trim it so Content has no observer-visible dependence on which
	// of the two encoding/json entry points was used.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// minimalPackageJSON is the subset of package.json Generate needs to fill
// in a default component name/version. It intentionally does not reuse
// internal/adapters/sveltekit.PackageJSON, which has no Version field —
// that package answers "is @jesterkit/exe-sveltekit configured", a
// different question than the one asked here, so duplicating a four-line
// struct is clearer than bending that package's contract to fit.
type minimalPackageJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// readPackageIdentity best-effort reads projectDir/package.json for a
// default component name/version. Any failure — missing file, malformed
// JSON — yields two empty strings rather than an error: Generate treats an
// unresolvable name/version as informational, the same contract
// internal/adapters/sveltekit documents for its own version lookups, and
// falls back to the project directory's basename for the name.
func readPackageIdentity(projectDir string) (name, version string) {
	data, err := os.ReadFile(filepath.Join(projectDir, "package.json"))
	if err != nil {
		return "", ""
	}
	var p minimalPackageJSON
	if err := json.Unmarshal(data, &p); err != nil {
		return "", ""
	}
	return p.Name, p.Version
}
