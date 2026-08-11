package nativeinspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestStrictNativeAdapter_CleanProject(t *testing.T) {
	dir := t.TempDir()
	pkgContent := `{"name": "clean-app", "dependencies": {"@sveltejs/kit": "^2.5.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := NewStrictAdapter()
	res, err := adapter.Inspect(context.Background(), dir, ports.LinuxAMD64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.HasNativeModules || res.HasUnsupportedDynamicImports {
		t.Errorf("expected clean result, got native=%v dynamic=%v", res.HasNativeModules, res.HasUnsupportedDynamicImports)
	}
}

func TestStrictNativeAdapter_NativeDependencyFails(t *testing.T) {
	dir := t.TempDir()
	pkgContent := `{"name": "native-app", "dependencies": {"better-sqlite3": "^9.0.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := NewStrictAdapter()
	_, err := adapter.Inspect(context.Background(), dir, ports.LinuxAMD64)
	if err == nil {
		t.Fatal("expected error for native dependency, got nil")
	}

	if !errors.Is(err, core.ErrNativeModulesUnsupported) {
		t.Errorf("error = %v, want errors.Is(core.ErrNativeModulesUnsupported)", err)
	}
}

func TestStrictNativeAdapter_ComputedDynamicImportFails(t *testing.T) {
	dir := t.TempDir()
	pkgContent := `{"name": "dynamic-app"}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	jsContent := `async function load(name) { return import('./routes/' + name); }`
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(jsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := NewStrictAdapter()
	_, err := adapter.Inspect(context.Background(), dir, ports.LinuxAMD64)
	if err == nil {
		t.Fatal("expected error for computed dynamic import, got nil")
	}

	if !errors.Is(err, core.ErrNativeModulesUnsupported) {
		t.Errorf("error = %v, want errors.Is(core.ErrNativeModulesUnsupported)", err)
	}
}
