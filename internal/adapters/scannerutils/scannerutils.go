// Package scannerutils provides lightweight, zero-dependency parsers for OS package
// databases (Debian /var/lib/dpkg/status, Alpine /lib/apk/db/installed), OS release
// metadata (/etc/os-release), and JavaScript/TypeScript lockfiles (bun.lock, package-lock.json,
// pnpm-lock.yaml), avoiding the need for heavy external catalogers like Anchore Syft.
package scannerutils

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"gopkg.in/yaml.v3"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
)

// IsUtilityPackage marks this as a reusable internal utility, not a port adapter.
const IsUtilityPackage = true

// PackageType defines the category of package cataloged.
type PackageType string

const (
	PkgTypeDeb PackageType = "deb"
	PkgTypeApk PackageType = "apk"
	PkgTypeNpm PackageType = "npm"
)

// CatalogPackage represents a discovered dependency or OS package.
type CatalogPackage struct {
	Name         string      `json:"name"`
	Version      string      `json:"version"`
	Ecosystem    string      `json:"ecosystem"`
	Type         PackageType `json:"type"`
	Architecture string      `json:"architecture,omitempty"`
	License      string      `json:"license,omitempty"`
	Source       string      `json:"source,omitempty"`
	// Resolved reports whether Version is a concrete, pinned version (from a
	// lockfile entry, an installed node_modules/<pkg>/package.json, or an OS
	// package database, all of which only ever record what is actually
	// present) rather than an unresolved package.json semver range/wildcard
	// ("^10.9.1", "~5.0.0", "*") that a consumer cannot match against a CVE
	// database. Every constructor in this file must set this explicitly —
	// the zero value (false) is only correct for genuinely unresolved
	// entries, so an omitted assignment here silently misreports a resolved
	// package as unresolved instead of failing loudly.
	Resolved bool `json:"resolved"`
}

// DistroInfo records basic Linux distribution identification parsed from os-release.
type DistroInfo struct {
	ID        string `json:"id"`
	VersionID string `json:"version_id"`
	Name      string `json:"name"`
}

// ParseOSRelease parses key-value pairs from an /etc/os-release or /usr/lib/os-release file.
func ParseOSRelease(r io.Reader) (DistroInfo, error) {
	var info DistroInfo
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), "\"'")

		switch key {
		case "ID":
			info.ID = strings.ToLower(val)
		case "VERSION_ID":
			info.VersionID = val
		case "NAME":
			info.Name = val
		}
	}
	if err := scanner.Err(); err != nil {
		return info, err
	}
	return info, nil
}

// ParseDPKGStatus parses Debian control paragraph format from /var/lib/dpkg/status.
func ParseDPKGStatus(r io.Reader) ([]CatalogPackage, error) {
	var packages []CatalogPackage
	scanner := bufio.NewScanner(r)

	// Adjust max buffer size in case of large status fields
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var (
		pkgName string
		version string
		status  string
		arch    string
		source  string
	)

	flush := func() {
		if pkgName != "" && version != "" {
			// Only include installed packages if Status field is present
			isInstalled := status == "" || strings.Contains(status, "installed")
			if isInstalled {
				packages = append(packages, CatalogPackage{
					Name:         pkgName,
					Version:      version,
					Type:         PkgTypeDeb,
					Architecture: arch,
					Source:       source,
					Resolved:     true,
				})
			}
		}
		pkgName = ""
		version = ""
		status = ""
		arch = ""
		source = ""
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			flush()
			continue
		}

		// Continuation line of previous field
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "Package":
			pkgName = val
		case "Version":
			version = val
		case "Status":
			status = val
		case "Architecture":
			arch = val
		case "Source":
			source = strings.Fields(val)[0]
		}
	}

	flush()

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return packages, nil
}

// ParseAPKInstalled parses Alpine /lib/apk/db/installed single-character tag lines.
func ParseAPKInstalled(r io.Reader) ([]CatalogPackage, error) {
	var packages []CatalogPackage
	scanner := bufio.NewScanner(r)

	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var (
		pkgName string
		version string
		arch    string
		license string
		origin  string
	)

	flush := func() {
		if pkgName != "" && version != "" {
			packages = append(packages, CatalogPackage{
				Name:         pkgName,
				Version:      version,
				Type:         PkgTypeApk,
				Architecture: arch,
				License:      license,
				Source:       origin,
				Resolved:     true,
			})
		}
		pkgName = ""
		version = ""
		arch = ""
		license = ""
		origin = ""
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			flush()
			continue
		}

		if len(line) < 2 || line[1] != ':' {
			continue
		}

		tag := line[0]
		val := strings.TrimSpace(line[2:])

		switch tag {
		case 'P':
			pkgName = val
		case 'V':
			version = val
		case 'A':
			arch = val
		case 'L':
			license = val
		case 'o':
			origin = val
		}
	}

	flush()

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return packages, nil
}

// ParseBunLock parses bun.lock v1 JSON format.
//
// Real bun.lock files are JSONC, not strict JSON: `bun install` always
// writes a trailing comma before the closing '}' of "packages" (and
// routinely elsewhere), which encoding/json's strict parser rejects
// outright ("invalid character '}' looking for beginning of object key
// string"). Without stripJSONTrailingCommas here, json.Unmarshal fails on
// essentially every real-world bun.lock and this function silently returns
// zero packages to every caller that swallows its error (both
// ExtractProjectDependencies and sbom.Generator.scanProject do) — the
// project's dependency versions then fall back to whatever a
// package.json-only path can resolve instead (F6 field-test bug).
func ParseBunLock(data []byte) ([]CatalogPackage, error) {
	var parsed struct {
		Packages map[string]any `json:"packages"`
	}
	if err := json.Unmarshal(stripJSONTrailingCommas(data), &parsed); err != nil {
		return nil, err
	}

	var packages []CatalogPackage
	for k, v := range parsed.Packages {
		if k == "" {
			continue
		}
		name, version := parseBunPackageEntry(k, v)
		if name != "" && version != "" {
			packages = append(packages, CatalogPackage{
				Name:      name,
				Version:   version,
				Type:      PkgTypeNpm,
				Ecosystem: "npm",
				// A bun.lock "packages" entry always records the version bun
				// actually resolved and installed, never a range.
				Resolved: true,
			})
		}
	}

	return packages, nil
}

// stripJSONTrailingCommas returns a copy of data with any comma removed
// when the next non-whitespace byte is a closing '}' or ']', tolerating the
// trailing commas that JSONC-flavored formats (like Bun's bun.lock) allow
// but encoding/json does not. Commas inside quoted strings are left alone —
// tracked via an in-string flag that itself respects backslash escapes —
// so nothing inside a string value (a sha512 integrity hash, say) is ever
// mistaken for structural JSON.
func stripJSONTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		if inString {
			out = append(out, b)
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			}
			continue
		}
		if b == '"' {
			inString = true
			out = append(out, b)
			continue
		}
		if b == ',' {
			j := i + 1
			for j < len(data) && isJSONSpace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue // drop the trailing comma
			}
		}
		out = append(out, b)
	}
	return out
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// IsConcreteVersion reports whether v looks like a single pinned version
// (e.g. "10.9.6") rather than a semver range or wildcard a package manager
// would still need to resolve (e.g. "^10.9.1", "~5.0.0", "*", ">=1.2.0",
// "1.x", "1.2.3 || 2.0.0"). It is a heuristic, not a full semver-range
// parser — false negatives (an exact version misclassified as a range) are
// the safe failure direction here, since callers use this to decide whether
// a version is trustworthy for CVE matching, and treating an ambiguous
// value as unresolved is always safer than trusting a range as if it were
// pinned.
func IsConcreteVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "*" {
		return false
	}
	if strings.ContainsAny(v, "^~<>=| ") {
		return false
	}
	lower := strings.ToLower(v)
	if lower == "x" || strings.Contains(lower, ".x") {
		return false
	}
	return true
}

func parseBunPackageEntry(key string, entry any) (name, version string) {
	// bun.lock format: "packages": { "pkgName": ["pkgName@1.2.3", "", {}, "..."] }
	// or "packages": { "@scope/pkg": ["@scope/pkg@1.2.3", ...] }
	if arr, ok := entry.([]any); ok && len(arr) > 0 {
		if idStr, ok := arr[0].(string); ok {
			lastAt := strings.LastIndex(idStr, "@")
			if lastAt > 0 {
				return idStr[:lastAt], idStr[lastAt+1:]
			}
		}
	}
	// Fallback to key
	lastAt := strings.LastIndex(key, "@")
	if lastAt > 0 {
		return key[:lastAt], key[lastAt+1:]
	}
	return key, ""
}

// ParsePackageLock parses package-lock.json v1, v2, and v3.
func ParsePackageLock(data []byte) ([]CatalogPackage, error) {
	var parsed struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]any `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	var packages []CatalogPackage
	seen := make(map[string]string)

	if len(parsed.Packages) > 0 {
		for path, pkg := range parsed.Packages {
			if path == "" || pkg.Version == "" {
				continue
			}
			// Extract package name from node_modules/ path
			idx := strings.LastIndex(path, "node_modules/")
			if idx >= 0 {
				name := path[idx+len("node_modules/"):]
				seen[name] = pkg.Version
			}
		}
	} else if len(parsed.Dependencies) > 0 {
		extractV1Dependencies(parsed.Dependencies, seen)
	}

	for name, ver := range seen {
		packages = append(packages, CatalogPackage{
			Name:      name,
			Version:   ver,
			Type:      PkgTypeNpm,
			Ecosystem: "npm",
			// package-lock.json only ever records the version npm actually
			// installed, never a range.
			Resolved: true,
		})
	}

	return packages, nil
}

func extractV1Dependencies(deps map[string]any, seen map[string]string) {
	for name, val := range deps {
		if obj, ok := val.(map[string]any); ok {
			if v, ok := obj["version"].(string); ok && v != "" {
				seen[name] = v
			}
			if sub, ok := obj["dependencies"].(map[string]any); ok {
				extractV1Dependencies(sub, seen)
			}
		}
	}
}

// ParsePnpmLock parses pnpm-lock.yaml packages section.
func ParsePnpmLock(data []byte) ([]CatalogPackage, error) {
	var parsed struct {
		Packages map[string]any `yaml:"packages"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}

	var packages []CatalogPackage
	for key := range parsed.Packages {
		name, version := parsePnpmKey(key)
		if name != "" && version != "" {
			packages = append(packages, CatalogPackage{
				Name:      name,
				Version:   version,
				Type:      PkgTypeNpm,
				Ecosystem: "npm",
				// pnpm-lock.yaml keys only ever encode the resolved version,
				// never a range.
				Resolved: true,
			})
		}
	}

	return packages, nil
}

func parsePnpmKey(key string) (name, version string) {
	// Keys in pnpm-lock.yaml look like:
	//   /svelte@5.0.0
	//   /@sveltejs/kit@2.31.0
	//   /esbuild@0.25.12(peer@1.0)
	//   @sveltejs/kit@2.31.0
	clean := strings.TrimPrefix(key, "/")
	if paren := strings.Index(clean, "("); paren >= 0 {
		clean = clean[:paren]
	}
	lastAt := strings.LastIndex(clean, "@")
	if lastAt > 0 {
		return clean[:lastAt], clean[lastAt+1:]
	}
	return clean, ""
}

// ExtractProjectDependencies scans projectDir for lockfiles and package.json to catalog dependencies.
func ExtractProjectDependencies(projectDir string) ([]CatalogPackage, error) {
	seen := make(map[string]CatalogPackage)

	// 1. Try bun.lock
	if bunData, err := os.ReadFile(filepath.Join(projectDir, "bun.lock")); err == nil {
		if pkgs, err := ParseBunLock(bunData); err == nil && len(pkgs) > 0 {
			for _, p := range pkgs {
				seen[p.Name] = p
			}
		}
	}

	// 2. Try package-lock.json
	if npmData, err := os.ReadFile(filepath.Join(projectDir, "package-lock.json")); err == nil {
		if pkgs, err := ParsePackageLock(npmData); err == nil && len(pkgs) > 0 {
			for _, p := range pkgs {
				if _, ok := seen[p.Name]; !ok {
					seen[p.Name] = p
				}
			}
		}
	}

	// 3. Try pnpm-lock.yaml
	if pnpmData, err := os.ReadFile(filepath.Join(projectDir, "pnpm-lock.yaml")); err == nil {
		if pkgs, err := ParsePnpmLock(pnpmData); err == nil && len(pkgs) > 0 {
			for _, p := range pkgs {
				if _, ok := seen[p.Name]; !ok {
					seen[p.Name] = p
				}
			}
		}
	}

	// 4. Always read package.json as base / fallback
	pkgJSON, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err == nil {
		for name, ver := range pkgJSON.Dependencies {
			resolved := sveltekitutils.ResolveVersion(projectDir, name, pkgJSON)
			if resolved == "" {
				resolved = ver
			}
			if _, ok := seen[name]; !ok {
				seen[name] = CatalogPackage{
					Name:      name,
					Version:   resolved,
					Type:      PkgTypeNpm,
					Ecosystem: "npm",
					Resolved:  IsConcreteVersion(resolved),
				}
			}
		}
		for name, ver := range pkgJSON.DevDependencies {
			resolved := sveltekitutils.ResolveVersion(projectDir, name, pkgJSON)
			if resolved == "" {
				resolved = ver
			}
			if _, ok := seen[name]; !ok {
				seen[name] = CatalogPackage{
					Name:      name,
					Version:   resolved,
					Type:      PkgTypeNpm,
					Ecosystem: "npm",
					Resolved:  IsConcreteVersion(resolved),
				}
			}
		}
	}

	var results []CatalogPackage
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

// MapDistroEcosystem computes the OSV.dev ecosystem string for a package given distro metadata.
func MapDistroEcosystem(distro DistroInfo, pkgType PackageType) string {
	id := strings.ToLower(distro.ID)
	switch pkgType {
	case PkgTypeDeb:
		if id == "ubuntu" {
			if distro.VersionID != "" {
				return "Ubuntu:" + distro.VersionID
			}
			return "Ubuntu"
		}
		if distro.VersionID != "" {
			major := strings.Split(distro.VersionID, ".")[0]
			return "Debian:" + major
		}
		return "Debian"
	case PkgTypeApk:
		if id == "wolfi" {
			return "Wolfi"
		}
		if id == "chainguard" {
			return "Chainguard"
		}
		if distro.VersionID != "" {
			parts := strings.Split(distro.VersionID, ".")
			if len(parts) >= 2 {
				return "Alpine:v" + parts[0] + "." + parts[1]
			}
		}
		return "Alpine"
	case PkgTypeNpm:
		return "npm"
	default:
		return ""
	}
}

// ExtractImagePackages extracts all OS and language packages from an OCI container image.
func ExtractImagePackages(ctx context.Context, img v1.Image) ([]CatalogPackage, DistroInfo, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, DistroInfo{}, fmt.Errorf("reading image layers: %w", err)
	}

	var (
		distroInfo DistroInfo
		osPackages []CatalogPackage
		// vendorPackages holds each vendored dependency's own declared identity
		// (name+version read from *its own* package.json, e.g.
		// /app/vendor/express/package.json), keyed by name. This is the
		// ground truth for what is actually installed in the image — see
		// ports.AppVendorDirPrefix ("/app/vendor") and how the packager lays
		// out one directory per vendored npm package.
		vendorPackages = make(map[string]CatalogPackage)
		// otherAppPackages holds packages discovered indirectly: declared
		// dependency ranges from non-vendor package.json files (e.g. the
		// app's own /app/package.json, or an image not built by Pokkum) and
		// resolved entries from bun.lock files found under app/. These are a
		// fallback for when we have no installed-version ground truth, and
		// are dropped in favor of a vendorPackages entry of the same name to
		// avoid double-counting/overriding an exact installed version with an
		// unresolved semver range.
		otherAppPackages []CatalogPackage
		dpkgStatusData   []byte
		apkData          []byte
		osReleaseData    []byte
	)

	// Scan layers from bottom to top
	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			return nil, DistroInfo{}, err
		}

		r, err := layer.Uncompressed()
		if err != nil {
			return nil, DistroInfo{}, fmt.Errorf("reading uncompressed layer: %w", err)
		}

		tr := tar.NewReader(r)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = r.Close()
				return nil, DistroInfo{}, fmt.Errorf("tar read error: %w", err)
			}

			cleanName := filepath.ToSlash(filepath.Clean(hdr.Name))
			cleanName = strings.TrimPrefix(cleanName, "./")
			cleanName = strings.TrimPrefix(cleanName, "/")

			switch {
			case cleanName == "etc/os-release" || cleanName == "usr/lib/os-release" || strings.HasSuffix(cleanName, "/os-release"):
				data, _ := io.ReadAll(tr)
				if len(data) > 0 {
					osReleaseData = data
				}
			case cleanName == "var/lib/dpkg/status" || strings.HasPrefix(cleanName, "var/lib/dpkg/status.d/"):
				data, _ := io.ReadAll(tr)
				if len(data) > 0 {
					dpkgStatusData = append(dpkgStatusData, '\n')
					dpkgStatusData = append(dpkgStatusData, data...)
					dpkgStatusData = append(dpkgStatusData, '\n')
				}
			case cleanName == "lib/apk/db/installed" || strings.HasSuffix(cleanName, "apk/db/installed"):
				data, _ := io.ReadAll(tr)
				if len(data) > 0 {
					apkData = append(apkData, '\n')
					apkData = append(apkData, data...)
					apkData = append(apkData, '\n')
				}
			case strings.HasSuffix(cleanName, "package.json") && strings.Contains(cleanName, "app/"):
				data, _ := io.ReadAll(tr)
				var p struct {
					Name            string            `json:"name"`
					Version         string            `json:"version"`
					Dependencies    map[string]string `json:"dependencies"`
					DevDependencies map[string]string `json:"devDependencies"`
				}
				if json.Unmarshal(data, &p) == nil {
					if p.Name != "" && p.Version != "" && strings.Contains(cleanName, "vendor/") {
						// This package.json belongs to a vendored dependency
						// itself (e.g. app/vendor/express/package.json) — its
						// own name+version is the actual installed identity,
						// not just a range someone else declared.
						vendorPackages[p.Name] = CatalogPackage{Name: p.Name, Version: p.Version, Type: PkgTypeNpm, Ecosystem: "npm", Resolved: true}
					} else {
						for k, v := range p.Dependencies {
							otherAppPackages = append(otherAppPackages, CatalogPackage{Name: k, Version: v, Type: PkgTypeNpm, Ecosystem: "npm", Resolved: IsConcreteVersion(v)})
						}
					}
				}
			case strings.HasSuffix(cleanName, "bun.lock") && strings.Contains(cleanName, "app/"):
				data, _ := io.ReadAll(tr)
				if pkgs, err := ParseBunLock(data); err == nil {
					otherAppPackages = append(otherAppPackages, pkgs...)
				}
			}
		}
		_ = r.Close()
	}

	if len(osReleaseData) > 0 {
		distroInfo, _ = ParseOSRelease(bytes.NewReader(osReleaseData))
	}

	if len(dpkgStatusData) > 0 {
		pkgs, err := ParseDPKGStatus(bytes.NewReader(dpkgStatusData))
		if err == nil {
			for i := range pkgs {
				pkgs[i].Ecosystem = MapDistroEcosystem(distroInfo, PkgTypeDeb)
			}
			osPackages = append(osPackages, pkgs...)
		}
	}

	if len(apkData) > 0 {
		pkgs, err := ParseAPKInstalled(bytes.NewReader(apkData))
		if err == nil {
			for i := range pkgs {
				pkgs[i].Ecosystem = MapDistroEcosystem(distroInfo, PkgTypeApk)
			}
			osPackages = append(osPackages, pkgs...)
		}
	}

	appPackages := make([]CatalogPackage, 0, len(vendorPackages)+len(otherAppPackages))
	for _, pkg := range vendorPackages {
		appPackages = append(appPackages, pkg)
	}
	for _, pkg := range otherAppPackages {
		if _, ok := vendorPackages[pkg.Name]; ok {
			// Already recorded with its exact installed version from its own
			// vendored package.json; skip the declared-range/lockfile dup.
			continue
		}
		appPackages = append(appPackages, pkg)
	}
	sort.Slice(appPackages, func(i, j int) bool {
		if appPackages[i].Name == appPackages[j].Name {
			return appPackages[i].Version < appPackages[j].Version
		}
		return appPackages[i].Name < appPackages[j].Name
	})

	all := append(osPackages, appPackages...)
	return all, distroInfo, nil
}
