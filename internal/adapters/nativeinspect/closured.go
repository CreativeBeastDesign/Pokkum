package nativeinspect

import (
	"context"
	"debug/elf"
	"fmt"
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

var _ ports.NativeInspector = (*ClosuredNativeAdapter)(nil)

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
//
// node_modules is traversed exactly ONCE: sveltekitutils.ScanNativeModules
// classifies every entry for both the native-module verdict and the ELF
// candidate list in a single filepath.WalkDir pass. This function previously
// called CheckNativeModules (one full filepath.Walk of node_modules) and then
// ran a second, independent filepath.Walk of the same tree for ELF binaries.
//
// ctx is honoured: both the node_modules traversal and the per-binary ELF
// inspection loop below stop on cancellation, so a cancelled build no longer
// walks a 40k-file tree to completion. sbom.Generator and secretguard.Adapter
// already did this; this adapter was the outlier.
func (a *ClosuredNativeAdapter) Inspect(ctx context.Context, projectDir string, targetPlatform ports.Platform) (ports.NativeInspectionResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.NativeInspectionResult{}, err
	}

	pkg, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		pkg = sveltekitutils.PackageJSON{}
	}

	scan, err := sveltekitutils.ScanNativeModules(ctx, projectDir, pkg)
	if err != nil {
		return ports.NativeInspectionResult{}, fmt.Errorf("nativeinspect closured: scan node_modules under %s: %w", projectDir, err)
	}
	nativeRes := scan.Native

	dynamicRes, err := sveltekitutils.ScanDynamicImports(ctx, projectDir, 0)
	if err != nil {
		return ports.NativeInspectionResult{}, fmt.Errorf("nativeinspect closured: scan dynamic imports under %s: %w", projectDir, err)
	}

	result := ports.NativeInspectionResult{
		HasNativeModules:             nativeRes.HasNativeModules,
		HasUnsupportedDynamicImports: dynamicRes.HasUnsupportedDynamicImports,
		DetectedModules:              nativeRes.DetectedModules,
		DetectedDynamicImports:       dynamicRes.DetectedLocations,
		// dynamicRes.Reasons already carries a line per skipped file, so a file
		// the scan could not read is reported here rather than silently folded
		// into "found nothing".
		Reasons:         append(append([]string{}, nativeRes.Reasons...), dynamicRes.Reasons...),
		GlibcVersionMax: "2.17",
	}

	seenLibs := make(map[string]bool)

	for _, path := range scan.ELFCandidates {
		if err := ctx.Err(); err != nil {
			return ports.NativeInspectionResult{}, err
		}

		needed, maxGlibc, err := InspectELFBinary(path)
		if err != nil {
			continue
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
