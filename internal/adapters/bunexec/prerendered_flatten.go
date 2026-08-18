package bunexec

import (
	"fmt"
	"os"
	"path/filepath"
)

// prerenderedCategoryDirs are the sibling subdirectories SvelteKit's own
// postbuild prerender step writes under .svelte-kit/output/prerendered/,
// BEFORE any adapter runs. Confirmed by reading
// @sveltejs/kit's src/core/postbuild/prerender.js verbatim (its `save`
// function: `dest = ${config.outDir}/output/prerendered/${category}/${file}`,
// where category is "pages" | "dependencies" | "data") out of this project's
// own vendored copy under testdata/fixtures/sveltekit-static/node_modules:
//
//   - "pages"        — rendered HTML for prerendered routes (what a plain
//     static site with no remote functions or build-time fetches populates).
//   - "dependencies" — non-HTML responses fetched during the prerender crawl
//     (e.g. a `load` function's `fetch()` call hitting an endpoint that also
//     got prerendered as a side effect).
//   - "data"         — remote-function query data (SvelteKit's experimental
//     remote functions), split out by the same `remote_prefix` check the
//     "dependencies" case uses.
//
// A plain static site with no remote functions and no build-time fetches
// (the common case) only ever populates "pages" — "dependencies" and "data"
// are legitimately absent, not an error.
//
// Every real adapter flattens all three into a single directory before
// shipping: @sveltejs/kit's own Builder.writePrerendered (src/core/adapt/
// builder.js) does exactly `copy(pages, dest); copy(dependencies, dest);
// copy(data, dest)` — confirmed the same way, out of the same vendored
// checkout. Pokkum's StrategyStatic packaging reads the pre-adapter
// .svelte-kit/output/prerendered staging directly (see Prepare's ApplyStatic
// branch) rather than running the adapter's own writePrerendered, so it must
// reproduce the same flattening itself — otherwise the shipped image has
// /app/prerendered/pages/index.html instead of /app/prerendered/index.html,
// and pokkum-static (which looks for index.html directly inside whichever
// root it's serving) 404s on every route.
//
// Order is fixed (not filesystem-iteration-dependent, matching the bit-for-
// bit reproducibility invariant) and only matters for the collision check
// below: dependencies and data are processed first, pages last, so that if
// the same relative path were somehow produced by more than one category —
// not expected in practice; the three are disjoint by construction per the
// doc comment above — the error is deterministic across runs rather than
// depending on directory-read order.
var prerenderedCategoryDirs = []string{"dependencies", "data", "pages"}

// FlattenPrerenderedOutput merges SvelteKit's pages/dependencies/data staging
// split directly under prerenderedDir, matching what every real adapter's
// Builder.writePrerendered call produces. Category subdirectories that don't
// exist are silently skipped — their absence is not an error, since most
// real static sites never populate "dependencies" or "data" at all. A file
// that exists at the same relative path in more than one category is a hard
// error rather than a silent pick-one: nothing in SvelteKit's own design
// should produce that (see prerenderedCategoryDirs' doc comment), so it is
// treated as a signal that an assumption here no longer holds, not as
// routine data to reconcile by silently overwriting one with the other.
func FlattenPrerenderedOutput(prerenderedDir string) error {
	for _, category := range prerenderedCategoryDirs {
		categoryDir := filepath.Join(prerenderedDir, category)
		info, err := os.Stat(categoryDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("bunexec: stat prerendered category %s: %w", categoryDir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("bunexec: prerendered category %s exists but is not a directory", categoryDir)
		}

		walkErr := filepath.WalkDir(categoryDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(categoryDir, p)
			if relErr != nil {
				return fmt.Errorf("relative path of %s under %s: %w", p, categoryDir, relErr)
			}
			dest := filepath.Join(prerenderedDir, rel)
			if _, statErr := os.Stat(dest); statErr == nil {
				return fmt.Errorf(
					"prerendered/%s and an earlier category both produced %s; refusing to silently overwrite one with the other",
					filepath.Join(category, rel), filepath.Join("prerendered", rel),
				)
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), mkErr)
			}
			if renErr := os.Rename(p, dest); renErr != nil {
				return fmt.Errorf("move %s to %s: %w", p, dest, renErr)
			}
			return nil
		})
		if walkErr != nil {
			return fmt.Errorf("bunexec: flatten prerendered category %s: %w", category, walkErr)
		}
		if err := os.RemoveAll(categoryDir); err != nil {
			return fmt.Errorf("bunexec: remove flattened prerendered category dir %s: %w", categoryDir, err)
		}
	}
	return nil
}
