package bunexec

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// assetsGeneratedFilename is the name @jesterkit/exe-sveltekit gives the
// generated asset-embedding entrypoint it writes alongside temp-server/index.ts
// during Prepare.
const assetsGeneratedFilename = "assets.generated.ts"

// assets.generated.ts has a fixed two-line header followed by three parallel
// sections, each one entry per discovered static asset, joined to each other
// by a shared per-asset import identifier (not by path — the import block uses
// a relative filesystem path like "../client/logo.svg" while assetMap and
// assets use the asset's public URL path like "/logo.svg"; the identifier is
// the only key common to all three):
//
//   - an `import IDENT from "IMPORT_PATH" with { type: "file" };` line per asset
//   - an `assetMap` array literal of `["PUBLIC_PATH", IDENT]` pairs
//   - an `assets` object literal re-exporting every IDENT by name (no path)
//
// @jesterkit/exe-sveltekit's discoverClientAssets walks client/ and
// prerendered/ without sorting, so the order of entries within each section is
// whatever the filesystem walk happened to yield — which is not guaranteed
// stable across otherwise-identical builds. Two entries trading position
// changes the generated file's bytes, which changes the bundle Compile embeds,
// which changes the compiled binary, defeating reproducibility even though the
// *set* of embedded assets never changed.
//
// assetsFileRe pins the exact shape above (including the header) so that any
// upstream change to the generator's output format causes normalization to
// fail loudly rather than silently doing nothing or mangling the file. It
// tolerates the generator's own irregular indentation (some lines are emitted
// with leading whitespace, most are not) but nothing else.
var assetsFileRe = regexp.MustCompile(`(?s)\A// Auto-generated asset imports\n// @ts-nocheck\n(.*?)\n\n[ \t]*export const assetMap = new Map\(\[\n(.*?)\n[ \t]*\]\);\n\n[ \t]*export const assets = \{\n(.*?)\n[ \t]*\};[ \t\n]*\z`)

var (
	importLineRe   = regexp.MustCompile(`^[ \t]*import[ \t]+(\w+)[ \t]+from[ \t]+"([^"]+)"[ \t]+with[ \t]+\{[ \t]*type:[ \t]*"file"[ \t]*\};[ \t]*$`)
	assetMapLineRe = regexp.MustCompile(`^[ \t]*\["([^"]+)",[ \t]*(\w+)\],?[ \t]*$`)
	assetsLineRe   = regexp.MustCompile(`^[ \t]*(\w+),?[ \t]*$`)
)

// assetEntry is one static asset, carrying both spellings of its location
// (the relative import path used in the import block, and the public URL path
// used in assetMap) bound together by their shared generated identifier.
type assetEntry struct {
	ident      string
	importPath string
	publicPath string
}

// normalizeGeneratedAssetsFile rewrites the assets.generated.ts file at path
// in place so that its import block, assetMap entries and assets object are
// all reordered together, sorted by public asset path. It is idempotent:
// normalizing an already-normalized file is a byte-for-byte no-op, so calling
// it twice (or on a file the walk order happened to already get right) is
// always safe.
//
// It never changes which assets are embedded, only the order they appear in —
// see normalizeGeneratedAssetsSource for the parsing and validation this
// relies on to guarantee that.
func normalizeGeneratedAssetsFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("bunexec: normalize %s: %w: %w", path, err, core.ErrPrepareFailed)
	}

	normalized, err := normalizeGeneratedAssetsSource(string(data))
	if err != nil {
		return fmt.Errorf("bunexec: normalize %s: %w: %w", path, err, core.ErrPrepareFailed)
	}

	if normalized == string(data) {
		return nil
	}

	if err := os.WriteFile(path, []byte(normalized), 0o644); err != nil {
		return fmt.Errorf("bunexec: normalize %s: write: %w: %w", path, err, core.ErrPrepareFailed)
	}
	return nil
}

// normalizeGeneratedAssetsSource parses src as an assets.generated.ts file,
// sorts its three sections together by public asset path, and returns the
// rewritten source.
//
// Parsing strategy: a single structural regex (assetsFileRe) first pins the
// overall shape — header, import block, assetMap block, assets block, in that
// order, separated by blank lines — and captures each block's raw text. Each
// block is then parsed line-by-line with a section-specific regex into
// (identifier, path) pairs. This is deliberately not a line-oriented sort: it
// never reorders raw text lines directly (which would desynchronize e.g. an
// import from its assetMap entry if the three sections ever happened to use
// different tie-breaking), it extracts identifier-keyed data from each section
// independently, cross-validates that all three sections describe the exact
// same set of identifiers, sorts once by public path, and regenerates all
// three sections from that single sorted list — so the identifier<->path
// binding cannot drift between sections.
//
// If assetsFileRe does not match at all, or any block contains a line that
// does not match its section's line pattern, or the three sections disagree on
// entry count or on which identifiers exist, normalization fails with a
// descriptive error instead of writing anything — see the package-level
// comment on assetsFileRe for why that matters more than best-effort sorting.
func normalizeGeneratedAssetsSource(src string) (string, error) {
	m := assetsFileRe.FindStringSubmatch(src)
	if m == nil {
		return "", fmt.Errorf(
			"assets.generated.ts does not match the expected @jesterkit/exe-sveltekit shape " +
				"(header + import block + assetMap block + assets block); refusing to normalize " +
				"a file whose generator format may have changed upstream",
		)
	}
	importsBlock, assetMapBlock, assetsBlock := m[1], m[2], m[3]

	imports, err := parseImportLines(importsBlock)
	if err != nil {
		return "", fmt.Errorf("assets.generated.ts: import block: %w", err)
	}
	assetMapEntries, err := parseAssetMapLines(assetMapBlock)
	if err != nil {
		return "", fmt.Errorf("assets.generated.ts: assetMap block: %w", err)
	}
	assetsIdents, err := parseIdentLines(assetsBlock)
	if err != nil {
		return "", fmt.Errorf("assets.generated.ts: assets block: %w", err)
	}

	// Entry-count assertion (before any rewriting): a parsing bug in one
	// section must not silently drop or duplicate assets relative to the
	// others.
	if len(imports) == 0 {
		return "", fmt.Errorf("assets.generated.ts: found zero asset imports; refusing to normalize an unexpected/empty file")
	}
	if len(assetMapEntries) != len(imports) {
		return "", fmt.Errorf("assets.generated.ts: assetMap has %d entries, import block has %d; refusing to normalize", len(assetMapEntries), len(imports))
	}
	if len(assetsIdents) != len(imports) {
		return "", fmt.Errorf("assets.generated.ts: assets object has %d entries, import block has %d; refusing to normalize", len(assetsIdents), len(imports))
	}

	byIdent := make(map[string]*assetEntry, len(imports))
	order := make([]string, 0, len(imports))
	for _, e := range imports {
		if _, dup := byIdent[e.ident]; dup {
			return "", fmt.Errorf("assets.generated.ts: duplicate import identifier %q", e.ident)
		}
		entry := e
		byIdent[e.ident] = &entry
		order = append(order, e.ident)
	}

	seen := make(map[string]bool, len(assetMapEntries))
	for _, e := range assetMapEntries {
		entry, ok := byIdent[e.ident]
		if !ok {
			return "", fmt.Errorf("assets.generated.ts: assetMap references identifier %q with no matching import", e.ident)
		}
		if seen[e.ident] {
			return "", fmt.Errorf("assets.generated.ts: duplicate assetMap entry for %q", e.ident)
		}
		seen[e.ident] = true
		entry.publicPath = e.publicPath
	}
	if len(seen) != len(byIdent) {
		return "", fmt.Errorf("assets.generated.ts: assetMap does not cover every imported identifier")
	}

	seen = make(map[string]bool, len(assetsIdents))
	for _, ident := range assetsIdents {
		if _, ok := byIdent[ident]; !ok {
			return "", fmt.Errorf("assets.generated.ts: assets object references identifier %q with no matching import", ident)
		}
		if seen[ident] {
			return "", fmt.Errorf("assets.generated.ts: duplicate assets entry for %q", ident)
		}
		seen[ident] = true
	}
	if len(seen) != len(byIdent) {
		return "", fmt.Errorf("assets.generated.ts: assets object does not cover every imported identifier")
	}

	sorted := make([]assetEntry, 0, len(order))
	for _, ident := range order {
		sorted = append(sorted, *byIdent[ident])
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].publicPath < sorted[j].publicPath })

	out := renderAssetsGenerated(sorted)

	// Entry-count assertion (after rewriting): re-parse our own output through
	// the same structural + line regexes used above. This catches a bug in
	// renderAssetsGenerated itself (e.g. a malformed line that would silently
	// drop an entry from the written file) before it ever reaches disk.
	verifyM := assetsFileRe.FindStringSubmatch(out)
	if verifyM == nil {
		return "", fmt.Errorf("assets.generated.ts: internal error: normalized output failed self-validation (does not match the expected shape); not writing it")
	}
	verifyImports, err := parseImportLines(verifyM[1])
	if err != nil {
		return "", fmt.Errorf("assets.generated.ts: internal error: normalized output failed self-validation: %w", err)
	}
	if len(verifyImports) != len(imports) {
		return "", fmt.Errorf("assets.generated.ts: internal error: normalized output has %d entries, expected %d; not writing it", len(verifyImports), len(imports))
	}

	return out, nil
}

// parseImportLines parses each non-blank line of an import block into an
// assetEntry with ident and importPath set.
func parseImportLines(block string) ([]assetEntry, error) {
	lines := strings.Split(block, "\n")
	entries := make([]assetEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		mm := importLineRe.FindStringSubmatch(line)
		if mm == nil {
			return nil, fmt.Errorf("unrecognized import line: %q", line)
		}
		entries = append(entries, assetEntry{ident: mm[1], importPath: mm[2]})
	}
	return entries, nil
}

// parseAssetMapLines parses each non-blank line of an assetMap block into an
// assetEntry with ident and publicPath set.
func parseAssetMapLines(block string) ([]assetEntry, error) {
	lines := strings.Split(block, "\n")
	entries := make([]assetEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		mm := assetMapLineRe.FindStringSubmatch(line)
		if mm == nil {
			return nil, fmt.Errorf("unrecognized assetMap line: %q", line)
		}
		entries = append(entries, assetEntry{publicPath: mm[1], ident: mm[2]})
	}
	return entries, nil
}

// parseIdentLines parses each non-blank line of an assets object block into a
// bare identifier.
func parseIdentLines(block string) ([]string, error) {
	lines := strings.Split(block, "\n")
	idents := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		mm := assetsLineRe.FindStringSubmatch(line)
		if mm == nil {
			return nil, fmt.Errorf("unrecognized assets line: %q", line)
		}
		idents = append(idents, mm[1])
	}
	return idents, nil
}

// renderAssetsGenerated formats sorted (already in the desired final order)
// back into a complete assets.generated.ts source, using consistent
// indentation throughout — deliberately not preserving the generator's own
// irregular leading whitespace on each section's first line.
func renderAssetsGenerated(sorted []assetEntry) string {
	var b strings.Builder
	b.WriteString("// Auto-generated asset imports\n// @ts-nocheck\n")
	for _, e := range sorted {
		fmt.Fprintf(&b, "import %s from %q with { type: \"file\" };\n", e.ident, e.importPath)
	}

	b.WriteString("\nexport const assetMap = new Map([\n")
	for _, e := range sorted {
		fmt.Fprintf(&b, "  [%q, %s],\n", e.publicPath, e.ident)
	}
	b.WriteString("]);\n")

	b.WriteString("\nexport const assets = {\n")
	for _, e := range sorted {
		fmt.Fprintf(&b, "  %s,\n", e.ident)
	}
	b.WriteString("};\n")

	return b.String()
}
