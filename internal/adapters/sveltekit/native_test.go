package sveltekit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckNativeModules_NoNative(t *testing.T) {
	dir := t.TempDir()
	pkg := PackageJSON{
		Name: "clean-app",
		Dependencies: map[string]string{
			"@sveltejs/kit": "^2.5.0",
		},
		DevDependencies: map[string]string{
			"@jesterkit/exe-sveltekit": "^0.4.0",
		},
	}

	res := CheckNativeModules(dir, pkg)
	if res.HasNativeModules {
		t.Errorf("HasNativeModules = true, want false; detected: %v", res.DetectedModules)
	}
}

func TestCheckNativeModules_DeclaredNativePackage(t *testing.T) {
	dir := t.TempDir()
	pkg := PackageJSON{
		Name: "sqlite-app",
		Dependencies: map[string]string{
			"better-sqlite3": "^9.0.0",
			"@sveltejs/kit":  "^2.5.0",
		},
	}

	res := CheckNativeModules(dir, pkg)
	if !res.HasNativeModules {
		t.Fatal("HasNativeModules = false, want true")
	}
	if len(res.DetectedModules) != 1 || res.DetectedModules[0] != "better-sqlite3" {
		t.Errorf("DetectedModules = %v, want ['better-sqlite3']", res.DetectedModules)
	}
}

func TestCheckNativeModules_DotNodeFileInNodeModules(t *testing.T) {
	dir := t.TempDir()
	pkg := PackageJSON{
		Name: "app-with-node-file",
		Dependencies: map[string]string{
			"custom-pkg": "1.0.0",
		},
	}

	// Create a dummy .node file inside node_modules
	addonDir := filepath.Join(dir, "node_modules", "custom-pkg", "build", "Release")
	if err := os.MkdirAll(addonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nodeFile := filepath.Join(addonDir, "addon.node")
	if err := os.WriteFile(nodeFile, []byte("fake binary"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := CheckNativeModules(dir, pkg)
	if !res.HasNativeModules {
		t.Fatal("HasNativeModules = false, want true")
	}
	if len(res.DetectedModules) == 0 {
		t.Fatal("expected detected modules list to contain the .node file path")
	}
}
