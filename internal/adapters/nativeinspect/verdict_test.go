package nativeinspect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// writeFile writes rel (project-relative) with content, creating parents.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStrictNativeAdapter_VerdictMatrix pins the build-gating verdict for every
// project shape the merged single-pass walk touches.
//
// The single-pass refactor must not move any row here. The one row that DID
// move is called out in its own comment: a computed import that exists ONLY in
// generated output is no longer a build failure, because generated output is
// now pruned from the scan.
func TestStrictNativeAdapter_VerdictMatrix(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, dir string)
		wantErr     bool
		wantNative  bool
		wantDynamic bool
	}{
		{
			name: "clean project",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"clean","dependencies":{"@sveltejs/kit":"^2.5.0"}}`)
				writeFile(t, dir, "src/routes/+page.server.ts", "import x from './x.js';\nexport const load = () => x;\n")
			},
		},
		{
			name: "static dynamic import only",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"static-import"}`)
				writeFile(t, dir, "src/lib/loader.js", "export const m = await import('./helper.js');\n")
			},
		},
		{
			name: "declared native dependency",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"native","dependencies":{"better-sqlite3":"^9.0.0"}}`)
			},
			wantErr:    true,
			wantNative: true,
		},
		{
			name: "dot-node binary in node_modules",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"addon"}`)
				writeFile(t, dir, "node_modules/custom/build/Release/addon.node", "fake binary")
			},
			wantErr:    true,
			wantNative: true,
		},
		{
			name: "shared object without any dot-node is not a native verdict",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"so-only"}`)
				writeFile(t, dir, "node_modules/sharp/vendor/lib/libvips.so.42", "fake binary")
			},
		},
		{
			name: "computed import in source",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"computed"}`)
				writeFile(t, dir, "src/routes/loader.js", "export const load = (n) => import('./routes/' + n);\n")
			},
			wantErr:     true,
			wantDynamic: true,
		},
		{
			name: "computed import on a minified line over 64KiB",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"minified"}`)
				var b strings.Builder
				for b.Len() < 100*1024 {
					b.WriteString("var _pad=1;")
				}
				b.WriteString("const m=await import('./routes/'+n);")
				writeFile(t, dir, "src/lib/vendor.bundle.js", b.String())
			},
			// This row is the whole point of the fix: before it, the strict
			// adapter passed this project.
			wantErr:     true,
			wantDynamic: true,
		},
		{
			name: "computed import only in generated output is pruned",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"generated-only"}`)
				writeFile(t, dir, "build/server/chunks/0.js", "const m = await import('./c/' + h);\n")
				writeFile(t, dir, ".svelte-kit/output/server/index.js", "const m = await import('./n/' + i);\n")
			},
			// INTENTIONAL behavior change: generated output is now pruned, so a
			// computed import that exists only there no longer gates the build.
			// A developer cannot act on it (the fix is "rewrite the import in
			// source"), and .svelte-kit/ and build/ are concurrently written by
			// Prepare in the same errgroup, which made this verdict a race.
		},
		{
			name: "native dependency and computed import together",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "package.json", `{"name":"both","dependencies":{"sharp":"^0.33.0"}}`)
				writeFile(t, dir, "src/lib/loader.ts", "export const load = (n) => import(n);\n")
			},
			wantErr:     true,
			wantNative:  true,
			wantDynamic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			res, err := NewStrictAdapter().Inspect(context.Background(), dir, ports.LinuxAMD64)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Inspect() error = nil, want a preflight failure; result = %+v", res)
				}
				if !errors.Is(err, core.ErrNativeModulesUnsupported) {
					t.Errorf("error = %v, want errors.Is(core.ErrNativeModulesUnsupported)", err)
				}
			} else if err != nil {
				t.Fatalf("Inspect() error = %v, want nil", err)
			}

			if res.HasNativeModules != tt.wantNative {
				t.Errorf("HasNativeModules = %v, want %v (reasons: %v)", res.HasNativeModules, tt.wantNative, res.Reasons)
			}
			if res.HasUnsupportedDynamicImports != tt.wantDynamic {
				t.Errorf("HasUnsupportedDynamicImports = %v, want %v (locations: %v)", res.HasUnsupportedDynamicImports, tt.wantDynamic, res.DetectedDynamicImports)
			}
		})
	}
}

// sparseOversizedJS creates a .js file larger than the dynamic-import scan
// ceiling without writing that many real bytes: a text head (so the binary
// sniff correctly classifies it as text) followed by a sparse tail.
func sparseOversizedJS(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	head := strings.Repeat("// generated padding\n", 64) // > the 512-byte sniff window
	if _, err := f.WriteString(head); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

// TestStrictNativeAdapter_UnscannableFileFailsPreflight is the guard for the
// second half of the bug: an adapter that GATES a build on "no dynamic imports
// found" must not treat a file it never managed to read as a file it read and
// found clean.
func TestStrictNativeAdapter_UnscannableFileFailsPreflight(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"oversized"}`)
	sparseOversizedJS(t, filepath.Join(dir, "src", "lib", "huge.js"), 17*1024*1024)

	res, err := NewStrictAdapter().Inspect(context.Background(), dir, ports.LinuxAMD64)
	if err == nil {
		t.Fatalf("Inspect() error = nil, want a preflight failure: a file the scan could not read was reported as clean; result = %+v", res)
	}
	if !errors.Is(err, core.ErrNativeModulesUnsupported) {
		t.Errorf("error = %v, want errors.Is(core.ErrNativeModulesUnsupported)", err)
	}
	if !strings.Contains(err.Error(), "not scanned for dynamic imports") {
		t.Errorf("error = %v, want it to name the file that was never inspected", err)
	}
	if res.HasUnsupportedDynamicImports {
		t.Errorf("HasUnsupportedDynamicImports = true, want false: nothing was found — the file was never read, which is a distinct condition")
	}
}

// TestClosuredNativeAdapter_ReportsUnscannableFile — the closured adapter does
// not gate the build, but it must still SAY that a file went uninspected.
func TestClosuredNativeAdapter_ReportsUnscannableFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"oversized"}`)
	sparseOversizedJS(t, filepath.Join(dir, "src", "lib", "huge.js"), 17*1024*1024)

	res, err := NewClosuredAdapter().Inspect(context.Background(), dir, ports.LinuxAMD64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found bool
	for _, r := range res.Reasons {
		if strings.Contains(r, "not scanned for dynamic imports") {
			found = true
		}
	}
	if !found {
		t.Errorf("Reasons = %v, want a line naming the file that was never inspected", res.Reasons)
	}
}

// TestAdapters_CancelledContextStopsInspection covers both adapters: a
// cancelled build must abort the traversal instead of walking node_modules to
// completion and discarding the answer.
func TestAdapters_CancelledContextStopsInspection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"big"}`)
	for i := 0; i < 40; i++ {
		writeFile(t, dir, fmt.Sprintf("node_modules/pkg%02d/build/Release/addon.node", i), "x")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("closured", func(t *testing.T) {
		res, err := NewClosuredAdapter().Inspect(ctx, dir, ports.LinuxAMD64)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if len(res.DetectedModules) != 0 {
			t.Errorf("DetectedModules = %v, want none: a cancelled inspection must not return a partial verdict", res.DetectedModules)
		}
	})

	t.Run("strict", func(t *testing.T) {
		res, err := NewStrictAdapter().Inspect(ctx, dir, ports.LinuxAMD64)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, core.ErrNativeModulesUnsupported) {
			t.Errorf("err = %v, want a cancellation, not a native-module verdict", err)
		}
		if len(res.DetectedModules) != 0 {
			t.Errorf("DetectedModules = %v, want none", res.DetectedModules)
		}
	})

	// Fixture floor: uncancelled, this project fails loudly, so "no verdict"
	// above means the walk stopped rather than the fixture being empty.
	if _, err := NewStrictAdapter().Inspect(context.Background(), dir, ports.LinuxAMD64); !errors.Is(err, core.ErrNativeModulesUnsupported) {
		t.Fatalf("baseline err = %v, want core.ErrNativeModulesUnsupported", err)
	}
}
