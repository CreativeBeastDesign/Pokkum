package nativeinspect

import (
	"context"
	"fmt"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// StrictNativeAdapter implements ports.NativeInspector by performing a strict
// pre-flight check. If native Node C++ addons or untraceable dynamic imports are
// detected, it returns an error wrapping core.ErrNativeModulesUnsupported to halt
// the build early with actionable diagnostics.
type StrictNativeAdapter struct{}

// NewStrictAdapter creates a new StrictNativeAdapter.
func NewStrictAdapter() *StrictNativeAdapter {
	return &StrictNativeAdapter{}
}

// Inspect checks projectDir for native Node dependencies and computed dynamic imports.
func (a *StrictNativeAdapter) Inspect(_ context.Context, projectDir string, targetPlatform ports.Platform) (ports.NativeInspectionResult, error) {
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
	}

	if result.HasNativeModules || result.HasUnsupportedDynamicImports {
		var details []string
		if len(result.Reasons) > 0 {
			details = append(details, result.Reasons...)
		}
		return result, fmt.Errorf("nativeinspect strict preflight %s: %s: %w", projectDir, strings.Join(details, "; "), core.ErrNativeModulesUnsupported)
	}

	return result, nil
}
