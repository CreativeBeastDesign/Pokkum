package ports

import "context"

// NativeInspectionResult reports what a NativeInspector found when inspecting
// a project directory for native Node C++ addons and computed dynamic import() calls.
type NativeInspectionResult struct {
	// HasNativeModules is true if any native module, .node binary, or native package
	// dependency was detected.
	HasNativeModules bool

	// HasUnsupportedDynamicImports is true if any computed/dynamic import() call was found.
	HasUnsupportedDynamicImports bool

	// DetectedModules contains the names of detected native packages or relative paths of .node binaries.
	DetectedModules []string

	// DetectedDynamicImports contains file:line locations of untraceable dynamic imports.
	DetectedDynamicImports []string

	// RequiredSOLibs lists shared library .so filenames required by target ELF binaries
	// beyond what the base image surface provides.
	RequiredSOLibs []string

	// GlibcVersionMax is the highest GLIBC_X.YY symbol version detected across target ELF binaries.
	GlibcVersionMax string

	// Reasons provides human-readable explanations for all detected items.
	Reasons []string
}

// NativeInspector verifies whether a project can be safely compiled into a
// single-executable container image without native addon or dynamic import failures.
type NativeInspector interface {
	Inspect(ctx context.Context, projectDir string, targetPlatform Platform) (NativeInspectionResult, error)
}
