package sveltekitutils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsStaticImportLiteral(t *testing.T) {
	tests := []struct {
		arg  string
		want bool
	}{
		{"'./foo'", true},
		{`"/bar/baz"`, true},
		{"`./routes/page`", true},
		{"moduleName", false},
		{"'./modules/' + name", false},
		{"`./routes/${page}`", false},
		{"path.join(__dirname, file)", false},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			if got := IsStaticImportLiteral(tt.arg); got != tt.want {
				t.Errorf("IsStaticImportLiteral(%q) = %v, want %v", tt.arg, got, tt.want)
			}
		})
	}
}

func TestCheckDynamicImports_DetectsComputedImports(t *testing.T) {
	dir := t.TempDir()

	fileContent := `
import staticMod from './static.js';

async function loadRoute(routeName) {
    const staticPage = await import('./pages/home.js');
    const dynamicPage = await import('./pages/' + routeName);
    const templatePage = await import(` + "`./pages/${routeName}`" + `);
    return { staticPage, dynamicPage, templatePage };
}
`
	filePath := filepath.Join(dir, "server.js")
	if err := os.WriteFile(filePath, []byte(fileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	res := CheckDynamicImports(dir)
	if !res.HasUnsupportedDynamicImports {
		t.Fatal("HasUnsupportedDynamicImports = false, want true")
	}

	if len(res.DetectedLocations) != 2 {
		t.Errorf("DetectedLocations length = %d, want 2; locations: %v", len(res.DetectedLocations), res.DetectedLocations)
	}
}

func TestCheckDynamicImports_CleanFiles(t *testing.T) {
	dir := t.TempDir()

	fileContent := `
import adapter from '@jesterkit/exe-sveltekit';

export async function load() {
    const mod = await import('./helper.js');
    return mod;
}
`
	filePath := filepath.Join(dir, "index.js")
	if err := os.WriteFile(filePath, []byte(fileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	res := CheckDynamicImports(dir)
	if res.HasUnsupportedDynamicImports {
		t.Errorf("HasUnsupportedDynamicImports = true, want false; detected: %v", res.DetectedLocations)
	}
}
