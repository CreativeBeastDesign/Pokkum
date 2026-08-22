package bunexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestPrepare_PinsVersionNameWhenAdapterAlreadyCorrect guards the case the
// version pin was originally written for and then silently did not cover: a
// project whose adapter is *already* the right one.
//
// The pin lived inside the adapter-injection path, so it only ever ran for
// misconfigured projects — a correctly configured project got no injection, no
// pin, and a Date.now() version name that renames every hashed client chunk
// between two builds of identical source. The first fix hung the pin off an
// `else if`, which chained to a different `if` than its indentation suggested
// and never executed at all; only driving Prepare end-to-end catches that.
// Asserting on VersionNamePinned alone would pass against both broken versions.
func TestPrepare_PinsVersionNameWhenAdapterAlreadyCorrect(t *testing.T) {
	svelteConfig := fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-static")
	dir := newProjectDir(t, viteBuildPackageJSON, svelteConfig)
	putFakeBunOnPath(t, `set -e
mkdir -p .svelte-kit/output/prerendered/pages .svelte-kit/output/client
printf '<h1>index</h1>' > .svelte-kit/output/prerendered/pages/index.html
exit 0
`)
	c := NewCompiler(discardLogger())

	if _, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyStatic,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}); err != nil {
		t.Fatalf("Prepare() error = %v, want nil", err)
	}

	// Assert the staged config actually carries the pin, not merely that some
	// file was written into .pokkum/ — a staged config without a version block
	// is the same non-reproducible build with extra steps.
	staged := findStagedViteConfig(t, dir)
	if staged == "" {
		t.Fatal("Prepare staged no Vite config, so kit.version.name was never pinned and the build is not reproducible")
	}
	body, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "version") {
		t.Errorf("staged config %s carries no version pin:\n%s", staged, body)
	}
}

// TestPrepare_WarnsWhenVersionNameCannotBePinned covers the other half: a build
// script that does more than `vite build` must not be taken over (that would
// skip whatever else it does), so Prepare has to leave it alone and warn.
func TestPrepare_WarnsWhenVersionNameCannotBePinned(t *testing.T) {
	pkg := strings.Replace(viteBuildPackageJSON, `"build": "vite build"`, `"build": "vite build && bun run prepack"`, 1)
	svelteConfig := fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-static")
	dir := newProjectDir(t, pkg, svelteConfig)
	putFakeBunOnPath(t, `set -e
mkdir -p .svelte-kit/output/prerendered/pages .svelte-kit/output/client
printf '<h1>index</h1>' > .svelte-kit/output/prerendered/pages/index.html
exit 0
`)
	c := NewCompiler(discardLogger())

	if _, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyStatic,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}); err != nil {
		t.Fatalf("Prepare() error = %v, want nil", err)
	}

	if staged := findStagedViteConfig(t, dir); staged != "" {
		t.Errorf("Prepare staged %s and would run `vite build` directly, skipping the rest of a multi-command build script", staged)
	}
}

// findStagedViteConfig returns the path of a Vite config Pokkum staged under
// .pokkum/, or "" when none was staged.
func findStagedViteConfig(t *testing.T, dir string) string {
	t.Helper()
	var found string
	root := filepath.Join(dir, ".pokkum")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.Contains(filepath.Base(path), "vite.config") {
			found = path
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walking %s: %v", root, err)
	}
	return found
}
