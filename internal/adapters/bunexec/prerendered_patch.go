package bunexec

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// prerenderedPattern variants that the generated adapter-node handler.js may
// use to join a "prerendered" directory onto the handler's own directory. In
// recent adapter-node (v3/v5) the handler builds the prerendered tree path as
// path.join(dir, "prerendered"), where dir is the handler's own directory
// (fileURLToPath(import.meta.url)). This is exactly the constant Pokkum overrides:
// the prerendered tree lives in its own /app/prerendered layer, not under the
// server dir, so the handler must read POKKUM_PRERENDERED_DIR instead.
func prerenderedDirPattern(dirVar string) string {
	return `path.join(` + dirVar + `, "prerendered")`
}

// jsonPrerenderedDirPattern matches the same join expressed with the other
// quoting style some adapter versions emit.
func jsonPrerenderedDirPattern(dirVar string) string {
	return `path.join(` + dirVar + `, 'prerendered')`
}

// prerenderedDirVars are the variable spellings a generated handler may use
// for its own directory; the exact name differs across adapter-node versions,
// so each is attempted.
var prerenderedDirVars = []string{"dir", "__dirname", "server_dir", "serverDir"}

// applyPrerenderedPatch wraps every known prerendered-path join found in src
// as (process.env.POKKUM_PRERENDERED_DIR || <original-expr>), returning the
// rewritten source and whether anything matched.
func applyPrerenderedPatch(src string) (string, bool) {
	patched := false
	for _, dirVar := range prerenderedDirVars {
		for _, pat := range []string{prerenderedDirPattern(dirVar), jsonPrerenderedDirPattern(dirVar)} {
			if !strings.Contains(src, pat) {
				continue
			}
			src = strings.ReplaceAll(src, pat, `(process.env.POKKUM_PRERENDERED_DIR || `+pat+`)`)
			patched = true
		}
	}
	return src, patched
}

// handlerReExportPattern matches a bundled handler.js that is only a
// re-export barrel for the real implementation, e.g. the shape Vite/Rollup
// emits for adapter-node 5.x today:
//
//	export { h as handler } from './server/chunks/handler-Cl6LqmpI.js';
//
// Neither the local identifier (`h`) nor the content hash in the chunk
// filename is stable — both are assigned by Rollup per build — so only the
// exported name `handler` and the relative specifier are relied upon. A plain
// `export { handler } from '...'` re-export matches too.
var handlerReExportPattern = regexp.MustCompile(`export\s*\{[^}]*\bhandler\b[^}]*\}\s*from\s*['"]([^'"]+)['"]`)

// handlerReExportTargets resolves every relative module specifier that
// handlerSrc re-exports `handler` from, against handlerPath's own directory.
// Bare package specifiers and absolute paths are skipped: only files emitted
// alongside the handler in the same build output are candidates for patching.
func handlerReExportTargets(handlerPath, handlerSrc string) []string {
	baseDir := filepath.Dir(handlerPath)
	var targets []string
	seen := make(map[string]bool)
	for _, m := range handlerReExportPattern.FindAllStringSubmatch(handlerSrc, -1) {
		spec := m[1]
		if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
			continue
		}
		resolved := filepath.Join(baseDir, filepath.FromSlash(spec))
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		targets = append(targets, resolved)
	}
	return targets
}

// writePatchedHandler materializes patched source for the file it was read
// from, staging a copy under pokkumDir first (see patchPrerenderedEnv). The
// staged copy is named after the file that was actually transformed, so the
// sandbox records which build artifact changed — that is not always
// handler.js itself (see the re-export fallback).
func writePatchedHandler(targetPath, pokkumDir, src string, log *slog.Logger) error {
	if err := os.MkdirAll(pokkumDir, 0o700); err != nil {
		return fmt.Errorf("bunexec: create sandbox dir %s: %w", pokkumDir, err)
	}
	staged := filepath.Join(pokkumDir, filepath.Base(targetPath))
	if err := os.WriteFile(staged, []byte(src), 0o600); err != nil {
		return fmt.Errorf("bunexec: write staged handler %s: %w", staged, err)
	}
	if err := os.WriteFile(targetPath, []byte(src), 0o600); err != nil {
		return fmt.Errorf("bunexec: write patched handler %s: %w", targetPath, err)
	}
	log.Debug("bunexec: handler patched to honor POKKUM_PRERENDERED_DIR", "handler", targetPath, "staged", staged)
	return nil
}

// patchPrerenderedEnv rewrites a generated adapter-node handler.js so the
// prerendered tree is resolved from POKKUM_PRERENDERED_DIR when set, falling
// back to the handler's default path otherwise. This lets the image serve
// prerendered pages from their own /app/prerendered layer.
//
// The transform itself is staged under pokkumDir (<projectDir>/.pokkum), the
// same sandbox convention used for the virtual svelte.config.js injection —
// the injected content is decided and written there first. It is then
// materialized into handlerPath because the packager reads handler.js
// directly from the real build output when assembling the image layer, so
// the patched bytes must exist there for prerendered pages to actually serve.
//
// adapter-node is an external artifact whose internals vary across versions,
// so several known variable-name/quoting variants are tried.
//
// Two handler shapes are handled, in order:
//
//  1. The join appears directly in handler.js — the shape adapter-node's own
//     source template has, and which some versions/build configs still emit
//     inlined into the build output.
//  2. handler.js is only a re-export barrel (what Vite/Rollup emits for
//     adapter-node 5.x today), with the real implementation in a
//     content-hashed chunk under build/server/chunks/. In that case the
//     re-export is followed and the chunk is patched instead; handler.js
//     itself is left untouched, since it contains nothing to patch.
//
// Unlike a soft, warn-only outcome, a build whose handler matches neither
// shape is failed outright: an unpatched handler still builds and runs, but
// silently serves prerendered pages from a directory Pokkum no longer mounts,
// which 404s in production with nothing but a log line to explain it.
func patchPrerenderedEnv(handlerPath, pokkumDir string, log *slog.Logger) error {
	data, err := os.ReadFile(handlerPath)
	if err != nil {
		return fmt.Errorf("bunexec: read handler %s: %w", handlerPath, err)
	}
	src := string(data)

	if patchedSrc, ok := applyPrerenderedPatch(src); ok {
		return writePatchedHandler(handlerPath, pokkumDir, patchedSrc, log)
	}

	// No direct match: handler.js may be a re-export barrel for a bundled
	// chunk holding the real implementation. Follow it and retry there.
	for _, target := range handlerReExportTargets(handlerPath, src) {
		chunk, err := os.ReadFile(target)
		if err != nil {
			log.Debug("bunexec: handler re-export target not readable, skipping", "handler", handlerPath, "target", target, "err", err)
			continue
		}
		patchedSrc, ok := applyPrerenderedPatch(string(chunk))
		if !ok {
			continue
		}
		log.Debug("bunexec: patching prerendered path in re-exported handler chunk", "handler", handlerPath, "chunk", target)
		return writePatchedHandler(target, pokkumDir, patchedSrc, log)
	}

	return fmt.Errorf("bunexec: handler %s has no recognizable prerendered path pattern; prerendered pages would silently resolve from the adapter default instead of /app/prerendered", handlerPath)
}

// patchPrerenderedHandler locates the generated adapter-node handler.js under
// outputDir and applies the POKKUM_PRERENDERED_DIR patch, staging the
// transform under projectDir's .pokkum/ sandbox (see patchPrerenderedEnv).
// When handler.js is a re-export barrel, the patched file is the bundled
// chunk it re-exports from, not handler.js itself.
// Returns an error if no handler.js can be found, or if the patch itself
// fails — either means prerendered pages would silently 404 in the shipped
// image, so this is not a soft/optional step.
func (c *Compiler) patchPrerenderedHandler(outputDir, projectDir string) error {
	log := c.logger
	candidates := []string{
		filepath.Join(outputDir, "handler.js"),
		filepath.Join(outputDir, "server", "handler.js"),
	}
	var found string
	for _, cand := range candidates {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			found = cand
			break
		}
	}
	if found == "" {
		// Some adapter-node versions nest the handler deeper; do a shallow
		// walk so we still catch it without over-searching the whole tree.
		_ = filepath.WalkDir(outputDir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "handler.js" {
				return nil
			}
			if found == "" {
				found = p
			}
			return nil
		})
	}
	if found == "" {
		return fmt.Errorf("bunexec: no adapter-node handler.js found under %s to patch for POKKUM_PRERENDERED_DIR", outputDir)
	}
	if err := patchPrerenderedEnv(found, filepath.Join(projectDir, ".pokkum"), log); err != nil {
		return fmt.Errorf("bunexec: patch handler %s for POKKUM_PRERENDERED_DIR: %w", found, err)
	}
	return nil
}
