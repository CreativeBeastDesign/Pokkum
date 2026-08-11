package nativeinspect

import (
	"context"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// List of shared libraries provided by gcr.io/distroless/cc-debian12:nonroot
var standardBaseLibs = map[string]bool{
	"libc.so.6":             true,
	"libm.so.6":             true,
	"libpthread.so.0":       true,
	"libdl.so.2":            true,
	"libstdc++.so.6":        true,
	"libgcc_s.so.1":         true,
	"librt.so.1":            true,
	"ld-linux-x86-64.so.2":  true,
	"ld-linux-aarch64.so.1": true,
}

// Regex to parse GLIBC_2.XX version strings
var glibcVersionPattern = regexp.MustCompile(`GLIBC_(\d+)\.(\d+)`)

// ClosuredNativeAdapter implements ports.NativeInspector by inspecting ELF headers of
// native .node binaries and shared libraries to calculate dynamic library closures (.so dependencies)
// and validate glibc version requirements against the base image surface.
type ClosuredNativeAdapter struct {
	MaxGlibcVersion string // Max supported glibc, defaults to "2.36" (Debian 12 Bookworm)
}

// NewClosuredAdapter creates a ClosuredNativeAdapter configured for distroless/cc-debian12.
func NewClosuredAdapter() *ClosuredNativeAdapter {
	return &ClosuredNativeAdapter{
		MaxGlibcVersion: "2.36",
	}
}

// Inspect scans projectDir, inspects target ELF binaries, builds the .so closure,
// and checks glibc symbol versions.
func (a *ClosuredNativeAdapter) Inspect(ctx context.Context, projectDir string, targetPlatform ports.Platform) (ports.NativeInspectionResult, error) {
	pkg, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		pkg = sveltekitutils.PackageJSON{}
	}

	nativeRes := sveltekitutils.CheckNativeModules(projectDir, pkg)
	dynamicRes := sveltekitutils.CheckDynamicImports(projectDir)

	result := ports.NativeInspectionResult{
		HasNativeModules:             nativeRes.HasNativeModules,
		HasUnsupportedDynamicImports: dynamicRes.HasUnsupportedDynamicImports,
		DetectedModules:              nativeRes.DetectedModules,
		DetectedDynamicImports:       dynamicRes.DetectedLocations,
		Reasons:                      append(append([]string{}, nativeRes.Reasons...), dynamicRes.Reasons...),
		GlibcVersionMax:              "2.17",
	}

	seenLibs := make(map[string]bool)

	nodeModulesDir := filepath.Join(projectDir, "node_modules")
	if info, err := os.Stat(nodeModulesDir); err == nil && info.IsDir() {
		_ = filepath.Walk(nodeModulesDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}

			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".node" || ext == ".so" || strings.Contains(info.Name(), ".so.") {
				needed, maxGlibc, err := InspectELFBinary(path)
				if err != nil {
					return nil
				}

				for _, lib := range needed {
					if !standardBaseLibs[lib] && !seenLibs[lib] {
						seenLibs[lib] = true
						result.RequiredSOLibs = append(result.RequiredSOLibs, lib)
					}
				}

				if CompareGlibcVersions(maxGlibc, result.GlibcVersionMax) > 0 {
					result.GlibcVersionMax = maxGlibc
				}

				if a.MaxGlibcVersion != "" && CompareGlibcVersions(maxGlibc, a.MaxGlibcVersion) > 0 {
					relPath, _ := filepath.Rel(projectDir, path)
					result.Reasons = append(result.Reasons, fmt.Sprintf("%s requires GLIBC %s, which exceeds base image limit (%s)", relPath, maxGlibc, a.MaxGlibcVersion))
				}
			}
			return nil
		})
	}

	return result, nil
}

// InspectELFBinary opens an ELF binary and extracts its DT_NEEDED libraries and max GLIBC version.
func InspectELFBinary(path string) (needed []string, maxGlibc string, err error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	needed, err = f.ImportedLibraries()
	if err != nil {
		needed = nil
	}

	maxGlibc = "2.17"

	symbols, err := f.ImportedSymbols()
	if err == nil {
		for _, sym := range symbols {
			if m := glibcVersionPattern.FindStringSubmatch(sym.Version); len(m) == 3 {
				verStr := fmt.Sprintf("%s.%s", m[1], m[2])
				if CompareGlibcVersions(verStr, maxGlibc) > 0 {
					maxGlibc = verStr
				}
			}
		}
	}

	return needed, maxGlibc, nil
}

// CompareGlibcVersions compares two semver-like version strings ("2.36", "2.17").
// Returns >0 if v1 > v2, <0 if v1 < v2, 0 if equal.
func CompareGlibcVersions(v1, v2 string) int {
	p1 := strings.Split(v1, ".")
	p2 := strings.Split(v2, ".")

	if len(p1) < 2 || len(p2) < 2 {
		return strings.Compare(v1, v2)
	}

	major1, _ := strconv.Atoi(p1[0])
	major2, _ := strconv.Atoi(p2[0])
	if major1 != major2 {
		return major1 - major2
	}

	minor1, _ := strconv.Atoi(p1[1])
	minor2, _ := strconv.Atoi(p2[1])
	return minor1 - minor2
}
