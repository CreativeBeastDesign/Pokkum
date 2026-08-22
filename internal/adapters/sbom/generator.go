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

	v1 "github.com/google/go-containerregistry/pkg/v1"

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

// Generate implements ports.SBOMGenerator. It describes only req.ProjectDir's
// npm dependency graph (plus the Bun runtime component, if BunVersion is
// set) — it never claims anything about a base image's OS package surface,
// because it has no image to look at. Every document it produces marks
// "pokkum:osPackagesScanned" false in both formats' metadata (see
// renderSPDXJSON/renderCycloneDXJSON), so a consumer can tell "npm-only
// because that's genuinely all there is" (impossible to represent — the
// image always has *a* base) apart from "npm-only because nobody looked at
// the base image", rather than the two being silently indistinguishable.
//
// See GenerateForImage's doc comment for why base-image OS packages are not
// merged in here directly.
func (g *Generator) Generate(ctx context.Context, req ports.SBOMRequest) (*ports.SBOMDocument, error) {
	// When the caller supplied the resolved base images, catalogue their OS
	// packages too. Routing through the one port method keeps the OS surface
	// from being an opt-in a caller can forget: before this, an SBOM that
	// omitted libc6 and libssl3 entirely was the default and looked exactly
	// like a complete one.
	if len(req.BaseImages) > 0 {
		return g.GenerateForImage(ctx, req, req.BaseImages)
	}
	return g.generate(ctx, req, nil, scannerutils.DistroInfo{}, false)
}

// GenerateForImage extends Generate with the resolved base image's OS
// package database (Debian/dpkg or Alpine/apk, via
// scannerutils.ExtractImagePackages), merged into the document alongside
// the project's own npm dependency graph, each with a purl matching its
// real ecosystem ("pkg:deb/...", "pkg:apk/...", "pkg:npm/...").
//
// images should be the resolved base image's ports.BaseImage.Images map —
// the same v1.Image values the packager builds from, already pulled by
// core.Build's BaseImageResolver.Resolve call earlier in the same build.
// This method deliberately never pulls or resolves an image itself: doing
// so would either duplicate a fetch the pipeline already paid for, or give
// SBOM generation a network dependency it has never had. Every platform
// present is scanned and the results deduped by name+version+architecture,
// since a multi-arch base image is not guaranteed to carry identical
// per-arch package builds even when it usually does in practice.
//
// A nil or empty images map is treated exactly like Generate — "not
// scanned" — rather than as a base image confirmed to carry zero packages.
// Only a real, non-empty image that scannerutils.ExtractImagePackages
// genuinely walked and found no dpkg/apk database in (a scratch or fully
// static base) produces the "scanned, zero packages" state; the two are
// marked distinctly in the output (see renderSPDXJSON/renderCycloneDXJSON)
// so "we found nothing" and "we did not look" can never be confused for
// each other downstream — the exact failure mode named in this
// package's Lessons.md entries for the scanner and secretguard adapters.
//
// GenerateForImage is not part of ports.SBOMGenerator: wiring a real
// `pokkum build` to call it instead of Generate requires threading the
// resolved ports.BaseImage through ports.SBOMRequest and
// internal/core/pipeline.go's fanOut, both outside this package's scope.
// See the package-level "OS package coverage" note below for the exact
// change needed.
func (g *Generator) GenerateForImage(ctx context.Context, req ports.SBOMRequest, images map[ports.Platform]v1.Image) (*ports.SBOMDocument, error) {
	if len(images) == 0 {
		return g.generate(ctx, req, nil, scannerutils.DistroInfo{}, false)
	}
	osPackages, distro, err := extractBaseImageOSPackages(ctx, images)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("sbom: extracting base image OS packages: %w: %w", err, core.ErrSBOMFailed)
	}
	return g.generate(ctx, req, osPackages, distro, true)
}

// extractBaseImageOSPackages scans every platform's resolved base image for
// its dpkg/apk package database via scannerutils.ExtractImagePackages and
// returns the deduplicated union, plus the distro identified for purl
// namespacing. Platforms are visited in a fixed (OS, Arch, Variant) order —
// map iteration order is not — so that which platform's DistroInfo "wins"
// when picking the first non-empty one is deterministic across runs, not
// just eventually-consistent by content.
//
// Only Deb/Apk entries are kept: ExtractImagePackages also looks for
// app/-prefixed package.json/bun.lock files, which a pure, pre-packaging
// base image never has (there is no /app yet), so this filter is a
// defensive assertion of that assumption rather than an observed real
// case — if it were ever violated (e.g. this function accidentally handed
// an already-packaged image), it fails safely by dropping the npm entries
// instead of silently double-counting the app's own dependencies.
func extractBaseImageOSPackages(ctx context.Context, images map[ports.Platform]v1.Image) ([]scannerutils.CatalogPackage, scannerutils.DistroInfo, error) {
	platforms := make([]ports.Platform, 0, len(images))
	for p := range images {
		platforms = append(platforms, p)
	}
	sort.Slice(platforms, func(i, j int) bool {
		if platforms[i].OS != platforms[j].OS {
			return platforms[i].OS < platforms[j].OS
		}
		if platforms[i].Arch != platforms[j].Arch {
			return platforms[i].Arch < platforms[j].Arch
		}
		return platforms[i].Variant < platforms[j].Variant
	})

	seen := make(map[string]scannerutils.CatalogPackage)
	var distro scannerutils.DistroInfo
	for _, p := range platforms {
		img := images[p]
		if img == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, scannerutils.DistroInfo{}, err
		}
		pkgs, d, err := scannerutils.ExtractImagePackages(ctx, img)
		if err != nil {
			return nil, scannerutils.DistroInfo{}, fmt.Errorf("platform %s: %w", p.String(), err)
		}
		if distro.ID == "" {
			distro = d
		}
		for _, pkg := range pkgs {
			if pkg.Type != scannerutils.PkgTypeDeb && pkg.Type != scannerutils.PkgTypeApk {
				continue
			}
			key := string(pkg.Type) + "@" + pkg.Name + "@" + pkg.Version + "@" + pkg.Architecture
			seen[key] = pkg
		}
	}

	result := make([]scannerutils.CatalogPackage, 0, len(seen))
	for _, pkg := range seen {
		result = append(result, pkg)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		if result[i].Version != result[j].Version {
			return result[i].Version < result[j].Version
		}
		return result[i].Architecture < result[j].Architecture
	})
	return result, distro, nil
}

// generate is the shared implementation behind Generate and
// GenerateForImage. osPackages is nil for a plain Generate call; osScanned
// is false exactly when osPackages was never populated because no base
// image was consulted at all, true whenever a real image was scanned
// (regardless of how many, or zero, packages it turned up) — see
// GenerateForImage's doc comment for why those two are not the same claim.
func (g *Generator) generate(ctx context.Context, req ports.SBOMRequest, osPackages []scannerutils.CatalogPackage, distro scannerutils.DistroInfo, osScanned bool) (*ports.SBOMDocument, error) {
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

	// Merged and re-sorted by (Type, Name, Version) rather than trusting
	// scanProject's npm-only ordering or extractBaseImageOSPackages' own
	// sort: the combined document's package order must be fully
	// deterministic (Bit-for-bit OCI Reproducibility) regardless of which
	// of the two sources found what, and grouping by Type ("apk" < "deb" <
	// "npm") keeps OS and application packages visually separated in the
	// rendered document.
	if len(osPackages) > 0 {
		packages = append(packages, osPackages...)
	}
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Type != packages[j].Type {
			return packages[i].Type < packages[j].Type
		}
		if packages[i].Name != packages[j].Name {
			return packages[i].Name < packages[j].Name
		}
		return packages[i].Version < packages[j].Version
	})

	if unresolved := countUnresolved(packages); unresolved > 0 {
		g.log.WarnContext(ctx, "sbom: some packages could not be resolved to an installed version; "+
			"recording their declared package.json range instead, marked as unresolved",
			"unresolved", unresolved, "total", len(packages))
	}

	id := contentIdentityUUID(name, version, packages, req.BunVersion, req.BunSHA256, distro, osScanned)
	created := req.CreatedAt.UTC().Truncate(time.Second).Format(time.RFC3339)

	var docBytes []byte
	switch req.Format {
	case ports.SBOMFormatSPDXJSON:
		docBytes, err = renderSPDXJSON(name, version, id, created, packages, req.BunVersion, req.BunSHA256, distro, osScanned)
	case ports.SBOMFormatCycloneDXJSON:
		docBytes, err = renderCycloneDXJSON(name, version, id, created, packages, req.BunVersion, req.BunSHA256, distro, osScanned)
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
	g.log.InfoContext(ctx, "sbom: generated", "format", req.Format, "packages", doc.PackageCount, "bytes", len(docBytes),
		"osPackagesScanned", osScanned)
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

		// node_modules is never descended into. A nested package's own
		// package.json only carries the package's OWN declared dependency
		// ranges (its own "dependencies"/"devDependencies" fields), not
		// evidence of what version of THAT package got installed -- walking
		// into it let an unrelated, deeply nested package's declared range
		// for some name (e.g. a peerDependency range) win a race against the
		// project's own lockfile/root package.json for that same name,
		// whenever node_modules was visited first (it sorts before the root
		// package.json lexically: "node_modules" < "package.json"). The
		// project's own node_modules is still consulted -- directly, by path
		// -- via sveltekitutils.ResolveVersion below; this only stops the
		// walk from treating every dependency's transitive dependency
		// declarations as if they resolved something (F6 field-test bug).
		if d.IsDir() && d.Name() == "node_modules" {
			return filepath.SkipDir
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
							Resolved:  scannerutils.IsConcreteVersion(resolved),
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
							Resolved:  scannerutils.IsConcreteVersion(resolved),
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

// countUnresolved returns how many packages carry an unresolved
// package.json range instead of a concrete, installed version.
func countUnresolved(packages []scannerutils.CatalogPackage) int {
	n := 0
	for _, p := range packages {
		if !p.Resolved {
			n++
		}
	}
	return n
}

// unresolvedVersionComment is the SPDX package "comment" and CycloneDX
// "pokkum:versionResolved" property value explaining why a package's
// version looks like a range rather than a pinned release -- a consumer
// diffing SBOMs or matching against a CVE database needs to be able to tell
// this apart from a genuinely resolved version at a glance, not infer it
// from the presence of "^"/"~"/"*" in the string.
const unresolvedVersionComment = "versionInfo is an unresolved package.json range, not an installed version: " +
	"no lockfile entry or installed copy in node_modules was found for this package"

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

func renderSPDXJSON(name, version string, id uuid.UUID, created string, packages []scannerutils.CatalogPackage, bunVersion, bunSHA256 string, distro scannerutils.DistroInfo, osScanned bool) ([]byte, error) {
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
		purl := purlFor(p, distro)

		pkgMap := map[string]any{
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
		}
		if !p.Resolved {
			pkgMap["comment"] = unresolvedVersionComment
		}
		spdxPackages = append(spdxPackages, pkgMap)

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
			// osScanComment is always present (never a silently-omitted
			// field) so "the base image's OS package database was scanned
			// and genuinely has zero entries (a scratch/static base)" can
			// never be confused with "nobody scanned it" -- see
			// GenerateForImage's doc comment.
			"comment": osScanComment(osScanned, countOSPackages(packages)),
		},
		"packages":      spdxPackages,
		"relationships": relationships,
	}

	return marshalDeterministic(doc)
}

func renderCycloneDXJSON(name, version string, id uuid.UUID, created string, packages []scannerutils.CatalogPackage, bunVersion, bunSHA256 string, distro scannerutils.DistroInfo, osScanned bool) ([]byte, error) {
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
		purl := purlFor(p, distro)
		comp := map[string]any{
			"type":    "library",
			"name":    p.Name,
			"version": p.Version,
			"purl":    purl,
		}
		if !p.Resolved {
			comp["properties"] = []map[string]any{
				{"name": "pokkum:versionResolved", "value": "false"},
			}
		}
		components = append(components, comp)
	}

	// metadataProperties always carries the OS-scan marker, never only when
	// true -- an absent property is indistinguishable from "this pokkum
	// version doesn't know the concept" to a consumer, whereas an explicit
	// "false" is unambiguous. See GenerateForImage's doc comment and the
	// matching osScanComment used for the SPDX document's creationInfo.
	metadataProperties := []map[string]any{
		{"name": "pokkum:osPackagesScanned", "value": fmt.Sprintf("%v", osScanned)},
	}
	if osScanned {
		metadataProperties = append(metadataProperties, map[string]any{
			"name": "pokkum:osPackageCount", "value": fmt.Sprintf("%d", countOSPackages(packages)),
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
			"properties": metadataProperties,
		},
		"components": components,
	}

	return marshalDeterministic(doc)
}

func contentIdentityUUID(name, version string, packages []scannerutils.CatalogPackage, bunVersion, bunSHA256 string, distro scannerutils.DistroInfo, osScanned bool) uuid.UUID {
	ids := make([]string, 0, len(packages))
	for _, p := range packages {
		ids = append(ids, fmt.Sprintf("%s@%s@%s@resolved=%v", p.Name, p.Version, p.Type, p.Resolved))
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
	// osScanned/distro are folded into identity even though every package
	// they produced is already present in ids above: a "scanned this
	// scratch base and found zero OS packages" document and a "never
	// looked at a base image" document can otherwise hash identical (same
	// packages list, same Bun component) despite making a materially
	// different claim about what was checked.
	fmt.Fprintf(&b, "osScanned=%v@distro=%s:%s\n", osScanned, distro.ID, distro.VersionID)
	return uuid.NewSHA1(pokkumSBOMNamespace, []byte(b.String()))
}

// purlFor derives the Package URL for a catalogued component. distro is the
// base image's identity (Debian/Alpine/Wolfi/...), used only for the deb/apk
// namespace segment -- it has nothing to do with npm packages and is the
// zero value whenever no base image was scanned. It is threaded through as
// one shared value per render call rather than stored per-package because
// every OS package in a single Generate/GenerateForImage call comes from
// exactly one resolved base image.
//
// This is an explicit switch over every scannerutils.PackageType, not an
// if/else keyed on the one type this codebase happened to emit before this
// change (npm): a future PackageType value reaching this function without
// an explicit case falls into the generic default instead of silently being
// mislabeled with an npm purl, matching the "positive gate, not a negative
// one" rule from this repo's Lessons.md.
func purlFor(p scannerutils.CatalogPackage, distro scannerutils.DistroInfo) string {
	switch p.Type {
	case scannerutils.PkgTypeNpm:
		return fmt.Sprintf("pkg:npm/%s@%s", p.Name, p.Version)
	case scannerutils.PkgTypeDeb:
		return osPurl("deb", "debian", distro, p)
	case scannerutils.PkgTypeApk:
		return osPurl("apk", "alpine", distro, p)
	default:
		return fmt.Sprintf("pkg:generic/%s@%s", p.Name, p.Version)
	}
}

// osPurl builds a "pkg:<purlType>/<namespace>/<name>@<version>[?arch=...]"
// purl for an OS package. defaultNamespace is used only when distro.ID is
// empty -- e.g. the base image's os-release couldn't be read even though its
// dpkg/apk database could -- so a purl is still emitted rather than dropping
// the package from the document entirely.
func osPurl(purlType, defaultNamespace string, distro scannerutils.DistroInfo, p scannerutils.CatalogPackage) string {
	ns := strings.ToLower(distro.ID)
	if ns == "" {
		ns = defaultNamespace
	}
	purl := fmt.Sprintf("pkg:%s/%s/%s@%s", purlType, ns, p.Name, p.Version)
	if p.Architecture != "" {
		purl += "?arch=" + p.Architecture
	}
	return purl
}

// osScanComment is the SPDX document creationInfo.comment recording whether
// a base image's OS package database was consulted at all, and if so how
// many packages it turned up -- see GenerateForImage's doc comment for why
// this needs to be unconditionally present rather than an optional field
// only added when true.
func osScanComment(osScanned bool, osPackageCount int) string {
	if !osScanned {
		return "pokkum:osPackagesScanned=false"
	}
	return fmt.Sprintf("pokkum:osPackagesScanned=true pokkum:osPackageCount=%d", osPackageCount)
}

// countOSPackages returns how many entries in packages are OS (deb/apk)
// packages rather than npm ones, for the scan-status marker above. Computed
// from the merged list instead of threaded as a separate parameter so it
// can never drift from what the document's own components array contains.
func countOSPackages(packages []scannerutils.CatalogPackage) int {
	n := 0
	for _, p := range packages {
		if p.Type == scannerutils.PkgTypeDeb || p.Type == scannerutils.PkgTypeApk {
			n++
		}
	}
	return n
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
