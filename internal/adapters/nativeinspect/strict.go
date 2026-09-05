package nativeinspect

import (
	"context"
	"fmt"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var _ ports.NativeInspector = (*StrictNativeAdapter)(nil)

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
//
// It fails the build on three conditions, not two: a native module, a computed
// dynamic import, and — new — a file the dynamic-import scan could not actually
// read. The third exists because this adapter GATES the build on the second: a
// file that was never inspected must not be reported as one that was inspected
// and found clean. That is the same distinction secretguard draws between
// core.ErrSecretInlined ("I looked and found something") and
// core.ErrSecretScanIncomplete ("I could not look"), and it is what stops a
// coverage gap from reading as a pass.
//
// ctx is honoured by both scans below, so a cancelled build stops walking
// instead of running the traversal to completion.
func (a *StrictNativeAdapter) Inspect(ctx context.Context, projectDir string, targetPlatform ports.Platform) (ports.NativeInspectionResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.NativeInspectionResult{}, err
	}

	pkg, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		pkg = sveltekitutils.PackageJSON{}
	}

	scan, err := sveltekitutils.ScanNativeModules(ctx, projectDir, pkg)
	if err != nil {
		return ports.NativeInspectionResult{}, fmt.Errorf("nativeinspect strict: scan node_modules under %s: %w", projectDir, err)
	}
	nativeRes := scan.Native

	dynamicRes, err := sveltekitutils.ScanDynamicImports(ctx, projectDir, 0)
	if err != nil {
		return ports.NativeInspectionResult{}, fmt.Errorf("nativeinspect strict: scan dynamic imports under %s: %w", projectDir, err)
	}

	result := ports.NativeInspectionResult{
		HasNativeModules:             nativeRes.HasNativeModules,
		HasUnsupportedDynamicImports: dynamicRes.HasUnsupportedDynamicImports,
		DetectedModules:              nativeRes.DetectedModules,
		DetectedDynamicImports:       dynamicRes.DetectedLocations,
		Reasons:                      append(append([]string{}, nativeRes.Reasons...), dynamicRes.Reasons...),
	}

	if result.HasNativeModules || result.HasUnsupportedDynamicImports || len(dynamicRes.SkippedFiles) > 0 {
		var details []string
		if len(result.Reasons) > 0 {
			details = append(details, result.Reasons...)
		}
		return result, fmt.Errorf("nativeinspect strict preflight %s: %s: %w", projectDir, strings.Join(details, "; "), core.ErrNativeModulesUnsupported)
	}

	return result, nil
}
