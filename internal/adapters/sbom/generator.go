// Package sbom implements ports.SBOMGenerator by scanning a SvelteKit project directory
// and generating deterministic SPDX 2.3 or CycloneDX 1.5 documents directly without external
// cataloging dependencies.
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

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scannerutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// defaultScanExcludes are applied when SBOMRequest.ExcludePaths is nil.
var defaultScanExcludes = []string{
	".svelte-kit/",
	"dist/",
	".git/",
}

// pokkumSBOMNamespace is a fixed namespace UUID (RFC 4122) used as the base
// for deriving version-5 UUIDs deterministically from SBOM content.
var pokkumSBOMNamespace = uuid.MustParse("2f6e6b6b-756d-4b3c-9c1e-706f6b6b756d")

// Generator implements ports.SBOMGenerator.
type Generator struct {
	log *slog.Logger
}

var _ ports.SBOMGenerator = (*Generator)(nil)

// NewGenerator constructs a Generator.
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
	if version == "" {
		version = "0.0.0"
	}

	matcher, err := g.buildMatcher(req)
	if err != nil {
		return nil, fmt.Errorf("sbom: %s: %w: %w", req.ProjectDir, err, core.ErrSBOMFailed)
	}

	packages, err := g.scanProject(ctx, req.ProjectDir, matcher)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("sbom: scanning %s: %w: %w", req.ProjectDir, err, core.ErrSBOMFailed)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	id := contentIdentityUUID(name, version, packages, req.BunVersion, req.BunSHA256)
	created := req.CreatedAt.UTC().Truncate(time.Second).Format(time.RFC3339)

	var docBytes []byte
	switch req.Format {
	case ports.SBOMFormatSPDXJSON:
		docBytes, err = renderSPDXJSON(name, version, id, created, packages, req.BunVersion, req.BunSHA256)
	case ports.SBOMFormatCycloneDXJSON:
		docBytes, err = renderCycloneDXJSON(name, version, id, created, packages, req.BunVersion, req.BunSHA256)
	default:
		return nil, fmt.Errorf("sbom: unsupported format %q: %w", req.Format, core.ErrInvalidSBOMFormat)
	}
	if err != nil {
		return nil, fmt.Errorf("sbom: rendering %s: %w: %w", req.Format, err, core.ErrSBOMFailed)
	}

	mediaType, ok := req.Format.MediaType()
	if !ok {
		return nil, fmt.Errorf("sbom: format %q has no media type: %w", req.Format, core.ErrInvalidSBOMFormat)
	}

	packageCount := len(packages)
	if req.BunVersion != "" {
		packageCount++
	}

	sum := sha256.Sum256(docBytes)
	doc := &ports.SBOMDocument{
		Format:       req.Format,
		MediaType:    mediaType,
		Content:      docBytes,
		SHA256:       hex.EncodeToString(sum[:]),
		PackageCount: packageCount,
	}
	g.log.InfoContext(ctx, "sbom: generated", "format", req.Format, "packages", doc.PackageCount, "bytes", len(docBytes))
	return doc, nil
}

func (g *Generator) scanProject(ctx context.Context, root string, m *ignoreutils.Matcher) ([]scannerutils.CatalogPackage, error) {
	seen := make(map[string]scannerutils.CatalogPackage)

	// Lockfile reads inside the walk callback below go through a root handle
	// scoped to the project directory instead of through the walked path
	// directly, so a lockfile-named symlink pointing out of the project tree
	// is refused rather than followed — the project is the user's own
	// directory, but a dependency's postinstall script can write into it, so
	// the containment needs to be structural rather than incidental. Relative
	// symlinks that stay inside the project are still followed, exactly as
	// os.ReadFile would; an absolute target is refused, since openat-based
	// resolution cannot prove it stays inside. Either way the read error is
	// swallowed exactly as before, so an unreadable lockfile contributes no
	// packages instead of failing the scan. See gosec G122 and
	// mem:self_review_checklist row 22.
	projectRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("opening project dir %s: %w", root, err)
	}
	defer func() { _ = projectRoot.Close() }()

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		// rel stays in native separator form because it doubles as the
		// root-relative path handed to projectRoot below; relSlash is the
		// slash-separated form the ignore matcher's gitignore-style patterns
		// are written against.
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)

		if m.Match(relSlash, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		filename := d.Name()
		dir := filepath.Dir(p)

		switch filename {
		case "bun.lock":
			data, rErr := projectRoot.ReadFile(rel)
			if rErr == nil {
				if pkgs, pErr := scannerutils.ParseBunLock(data); pErr == nil {
					for _, pkg := range pkgs {
						seen[pkg.Name] = pkg
					}
				}
			}
		case "package-lock.json":
			data, rErr := projectRoot.ReadFile(rel)
			if rErr == nil {
				if pkgs, pErr := scannerutils.ParsePackageLock(data); pErr == nil {
					for _, pkg := range pkgs {
						if _, ok := seen[pkg.Name]; !ok {
							seen[pkg.Name] = pkg
						}
					}
				}
			}
		case "pnpm-lock.yaml":
			data, rErr := projectRoot.ReadFile(rel)
			if rErr == nil {
				if pkgs, pErr := scannerutils.ParsePnpmLock(data); pErr == nil {
					for _, pkg := range pkgs {
						if _, ok := seen[pkg.Name]; !ok {
							seen[pkg.Name] = pkg
						}
					}
				}
			}
		case "package.json":
			pkgJSON, pErr := sveltekitutils.ReadPackageJSON(dir)
			if pErr == nil {
				for name, ver := range pkgJSON.Dependencies {
					resolved := sveltekitutils.ResolveVersion(dir, name, pkgJSON)
					if resolved == "" {
						resolved = ver
					}
					if _, ok := seen[name]; !ok {
						seen[name] = scannerutils.CatalogPackage{
							Name:      name,
							Version:   resolved,
							Type:      scannerutils.PkgTypeNpm,
							Ecosystem: "npm",
						}
					}
				}
				for name, ver := range pkgJSON.DevDependencies {
					resolved := sveltekitutils.ResolveVersion(dir, name, pkgJSON)
					if resolved == "" {
						resolved = ver
					}
					if _, ok := seen[name]; !ok {
						seen[name] = scannerutils.CatalogPackage{
							Name:      name,
							Version:   resolved,
							Type:      scannerutils.PkgTypeNpm,
							Ecosystem: "npm",
						}
					}
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	var results []scannerutils.CatalogPackage
	for _, p := range seen {
		results = append(results, p)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Name == results[j].Name {
			return results[i].Version < results[j].Version
		}
		return results[i].Name < results[j].Name
	})

	return results, nil
}

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

func renderSPDXJSON(name, version string, id uuid.UUID, created string, packages []scannerutils.CatalogPackage, bunVersion, bunSHA256 string) ([]byte, error) {
	spdxPackages := make([]map[string]any, 0, len(packages)+2)
	relationships := make([]map[string]any, 0, len(packages)+2)

	// The root package describes the project/application itself (its own name and
	// version), distinct from its dependencies below. SPDX documents are expected to
	// DESCRIBES a single top-level element, with that element's dependencies attached
	// via DEPENDS_ON -- so the document version isn't lost and each dependency package
	// still carries its own versionInfo.
	rootSPDXID := fmt.Sprintf("SPDXRef-RootPackage-%s-%s", sanitizeSPDXID(name), sanitizeSPDXID(version))
	spdxPackages = append(spdxPackages, map[string]any{
		"SPDXID":           rootSPDXID,
		"name":             name,
		"versionInfo":      version,
		"downloadLocation": "NOASSERTION",
		"filesAnalyzed":    false,
		"licenseConcluded": "NOASSERTION",
		"licenseDeclared":  "NOASSERTION",
		"copyrightText":    "NOASSERTION",
	})
	relationships = append(relationships, map[string]any{
		"spdxElementId":      "SPDXRef-DOCUMENT",
		"relatedSpdxElement": rootSPDXID,
		"relationshipType":   "DESCRIBES",
	})

	// Bun is a Pokkum-embedded runtime artifact, not a project dependency
	// discovered by scanning ProjectDir — it gets its own package entry,
	// distinct from the npm packages loop below, placed right after the
	// root package so document order is deterministic regardless of
	// whether npm packages were found.
	if bunVersion != "" {
		bunSPDXID := fmt.Sprintf("SPDXRef-Package-bun-%s", sanitizeSPDXID(bunVersion))
		bunPkg := map[string]any{
			"SPDXID":           bunSPDXID,
			"name":             "bun",
			"versionInfo":      bunVersion,
			"downloadLocation": "NOASSERTION",
			"filesAnalyzed":    false,
			"licenseConcluded": "NOASSERTION",
			"licenseDeclared":  "NOASSERTION",
			"copyrightText":    "NOASSERTION",
			"externalRefs": []map[string]any{
				{
					"referenceCategory": "PACKAGE-MANAGER",
					"referenceLocator":  "pkg:generic/bun@" + bunVersion,
					"referenceType":     "purl",
				},
			},
		}
		if bunSHA256 != "" {
			bunPkg["checksums"] = []map[string]any{
				{"algorithm": "SHA256", "checksumValue": bunSHA256},
			}
		}
		spdxPackages = append(spdxPackages, bunPkg)
		relationships = append(relationships, map[string]any{
			"spdxElementId":      rootSPDXID,
			"relatedSpdxElement": bunSPDXID,
			"relationshipType":   "DEPENDS_ON",
		})
	}

	for _, p := range packages {
		pkgSPDXID := fmt.Sprintf("SPDXRef-Package-%s-%s", sanitizeSPDXID(p.Name), sanitizeSPDXID(p.Version))
		purl := fmt.Sprintf("pkg:npm/%s@%s", p.Name, p.Version)

		spdxPackages = append(spdxPackages, map[string]any{
			"SPDXID":           pkgSPDXID,
			"name":             p.Name,
			"versionInfo":      p.Version,
			"downloadLocation": "NOASSERTION",
			"filesAnalyzed":    false,
			"licenseConcluded": "NOASSERTION",
			"licenseDeclared":  "NOASSERTION",
			"copyrightText":    "NOASSERTION",
			"externalRefs": []map[string]any{
				{
					"referenceCategory": "PACKAGE-MANAGER",
					"referenceLocator":  purl,
					"referenceType":     "purl",
				},
			},
		})

		relationships = append(relationships, map[string]any{
			"spdxElementId":      rootSPDXID,
			"relatedSpdxElement": pkgSPDXID,
			"relationshipType":   "DEPENDS_ON",
		})
	}

	doc := map[string]any{
		"spdxVersion":       "SPDX-2.3",
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              name,
		"documentNamespace": fmt.Sprintf("https://pokkum.dev/sbom/%s-%s", sanitizeNamespaceName(name), id.String()),
		"creationInfo": map[string]any{
			"created": created,
			"creators": []string{
				"Tool: Pokkum",
			},
		},
		"packages":      spdxPackages,
		"relationships": relationships,
	}

	return marshalDeterministic(doc)
}

func renderCycloneDXJSON(name, version string, id uuid.UUID, created string, packages []scannerutils.CatalogPackage, bunVersion, bunSHA256 string) ([]byte, error) {
	components := make([]map[string]any, 0, len(packages)+1)

	// Bun is a Pokkum-embedded runtime artifact, not a project dependency
	// discovered by scanning ProjectDir — placed first, before the npm
	// packages loop, so document order is deterministic regardless of
	// whether npm packages were found.
	if bunVersion != "" {
		bunComponent := map[string]any{
			"type":    "application",
			"name":    "bun",
			"version": bunVersion,
			"purl":    "pkg:generic/bun@" + bunVersion,
		}
		if bunSHA256 != "" {
			bunComponent["hashes"] = []map[string]any{
				{"alg": "SHA-256", "content": bunSHA256},
			}
		}
		components = append(components, bunComponent)
	}

	for _, p := range packages {
		purl := fmt.Sprintf("pkg:npm/%s@%s", p.Name, p.Version)
		components = append(components, map[string]any{
			"type":    "library",
			"name":    p.Name,
			"version": p.Version,
			"purl":    purl,
		})
	}

	doc := map[string]any{
		"bomFormat":    "CycloneDX",
		"specVersion":  "1.5",
		"serialNumber": "urn:uuid:" + id.String(),
		"version":      1,
		"metadata": map[string]any{
			"timestamp": created,
			"tools": []map[string]any{
				{
					"vendor": "Pokkum",
					"name":   "pokkum",
				},
			},
			"component": map[string]any{
				"type":    "application",
				"name":    name,
				"version": version,
			},
		},
		"components": components,
	}

	return marshalDeterministic(doc)
}

func contentIdentityUUID(name, version string, packages []scannerutils.CatalogPackage, bunVersion, bunSHA256 string) uuid.UUID {
	ids := make([]string, 0, len(packages))
	for _, p := range packages {
		ids = append(ids, fmt.Sprintf("%s@%s@%s", p.Name, p.Version, p.Type))
	}
	sort.Strings(ids)

	var b strings.Builder
	fmt.Fprintf(&b, "pokkum-sbom\n%s@%s\n", name, version)
	for _, id := range ids {
		b.WriteString(id)
		b.WriteByte('\n')
	}
	if bunVersion != "" {
		// Appended after the sorted npm ids at a fixed position, so a change
		// in the resolved Bun version/hash changes the document identity
		// (matching how an npm package version bump does) without needing
		// to fold "bun" into the same sort as npm package names.
		fmt.Fprintf(&b, "bun@%s@%s\n", bunVersion, bunSHA256)
	}
	return uuid.NewSHA1(pokkumSBOMNamespace, []byte(b.String()))
}

func sanitizeSPDXID(s string) string {
	s = strings.ReplaceAll(s, "@", "")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "#", "-")
	s = strings.ReplaceAll(s, "+", "-")
	s = strings.ReplaceAll(s, "^", "")
	s = strings.ReplaceAll(s, "~", "")
	return s
}

func sanitizeNamespaceName(name string) string {
	name = strings.ReplaceAll(name, "#", "-")
	name = strings.ReplaceAll(name, ":", "-")
	name = strings.ReplaceAll(name, "/", "-")
	return name
}

func marshalDeterministic(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

type minimalPackageJSON struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

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
