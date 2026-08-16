package bunexec

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
// so several known variable-name/quoting variants are tried. Unlike a soft,
// warn-only outcome, a build whose handler doesn't match any of them is
// failed outright: an unpatched handler still builds and runs, but silently
// serves prerendered pages from a directory Pokkum no longer mounts, which
// 404s in production with nothing but a log line to explain it.
func patchPrerenderedEnv(handlerPath, pokkumDir string, log *slog.Logger) error {
	data, err := os.ReadFile(handlerPath)
	if err != nil {
		return fmt.Errorf("bunexec: read handler %s: %w", handlerPath, err)
	}
	src := string(data)

	patched := false
	// Try the common variable spellings for the handler's own directory. The
	// exact variable name differs across adapter-node versions, so attempt each.
	for _, dirVar := range []string{"dir", "__dirname", "server_dir", "serverDir"} {
		for _, pat := range []string{prerenderedDirPattern(dirVar), jsonPrerenderedDirPattern(dirVar)} {
			if !strings.Contains(src, pat) {
				continue
			}
			repl := `(process.env.POKKUM_PRERENDERED_DIR || ` + pat + `)`
			src = strings.ReplaceAll(src, pat, repl)
			patched = true
		}
	}

	if !patched {
		return fmt.Errorf("bunexec: handler %s has no recognizable prerendered path pattern; prerendered pages would silently resolve from the adapter default instead of /app/prerendered", handlerPath)
	}

	if err := os.MkdirAll(pokkumDir, 0o700); err != nil {
		return fmt.Errorf("bunexec: create sandbox dir %s: %w", pokkumDir, err)
	}
	staged := filepath.Join(pokkumDir, "handler.js")
	if err := os.WriteFile(staged, []byte(src), 0o600); err != nil {
		return fmt.Errorf("bunexec: write staged handler %s: %w", staged, err)
	}

	if err := os.WriteFile(handlerPath, []byte(src), 0o600); err != nil {
		return fmt.Errorf("bunexec: write patched handler %s: %w", handlerPath, err)
	}
	log.Debug("bunexec: handler patched to honor POKKUM_PRERENDERED_DIR", "handler", handlerPath, "staged", staged)
	return nil
}

// patchPrerenderedHandler locates the generated adapter-node handler.js under
// outputDir and applies the POKKUM_PRERENDERED_DIR patch, staging the
// transform under projectDir's .pokkum/ sandbox (see patchPrerenderedEnv).
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
