package nativeinspect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestCompareGlibcVersions(t *testing.T) {
	tests := []struct {
		v1, v2 string
		want   int
	}{
		{"2.36", "2.17", 1},
		{"2.17", "2.36", -1},
		{"2.36", "2.36", 0},
		{"2.38", "2.36", 2},
	}

	for _, tt := range tests {
		t.Run(tt.v1+"_vs_"+tt.v2, func(t *testing.T) {
			got := CompareGlibcVersions(tt.v1, tt.v2)
			if (got > 0 && tt.want <= 0) || (got < 0 && tt.want >= 0) || (got == 0 && tt.want != 0) {
				t.Errorf("CompareGlibcVersions(%q, %q) = %d, want sign matching %d", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}

func TestClosuredNativeAdapter_CleanProject(t *testing.T) {
	dir := t.TempDir()
	pkgContent := `{"name": "clean-app", "dependencies": {"@sveltejs/kit": "^2.5.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := NewClosuredAdapter()
	res, err := adapter.Inspect(context.Background(), dir, ports.LinuxAMD64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.HasNativeModules || res.HasUnsupportedDynamicImports {
		t.Errorf("expected clean result, got native=%v dynamic=%v", res.HasNativeModules, res.HasUnsupportedDynamicImports)
	}
	if len(res.RequiredSOLibs) > 0 {
		t.Errorf("RequiredSOLibs = %v, want empty", res.RequiredSOLibs)
	}
}
