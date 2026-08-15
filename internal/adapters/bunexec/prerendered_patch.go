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

// patchPrerenderedEnv rewrites a generated adapter-node handler.js (in place,
// in the .pokkum/ build sandbox) so the prerendered tree is resolved from
// POKKUM_PRERENDERED_DIR when set, falling back to the handler's default path
// otherwise. This lets the image serve prerendered pages from their own
// /app/prerendered layer.
//
// The patch is deliberately defensive: adapter-node is an external artifact
// whose internals vary across versions, so if the expected pattern is not found
// the file is left untouched and a warning is returned rather than an error —
// an unpatched handler still builds and runs, it just resolves prerendered
// pages from the adapter's default location (which Pokkum no longer mounts).
func patchPrerenderedEnv(handlerPath string, log *slog.Logger) error {
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
		log.Warn("bunexec: adapter-node handler has no recognizable prerendered path; prerendered pages will resolve from the adapter default (not /app/prerendered)", "handler", handlerPath)
		return nil
	}

	if err := os.WriteFile(handlerPath, []byte(src), 0o600); err != nil {
		return fmt.Errorf("bunexec: write patched handler %s: %w", handlerPath, err)
	}
	log.Debug("bunexec: handler patched to honor POKKUM_PRERENDERED_DIR", "handler", handlerPath)
	return nil
}

// patchPrerenderedHandler locates the generated adapter-node handler.js under
// outputDir and applies the POKKUM_PRERENDERED_DIR patch defensively. Missing
// handler is not an error (still version-sensitive); a warning is logged.
func (c *Compiler) patchPrerenderedHandler(outputDir string) {
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
		log.Warn("bunexec: no adapter-node handler.js found to patch for POKKUM_PRERENDERED_DIR; prerendered pages may 404", "outputDir", outputDir)
		return
	}
	if err := patchPrerenderedEnv(found, log); err != nil {
		log.Warn("bunexec: failed to patch handler for POKKUM_PRERENDERED_DIR", "handler", found, "error", err)
	}
}
