// Command gen-schema renders schema/pokkum.schema.json — a JSON Schema
// (draft 2020-12) describing the .pokkum.yaml configuration file — from
// ports.ProjectConfig and ports.BuildProfile (internal/ports/config.go) by
// reflection.
//
// Why this exists: docs/roadmap/developer-experience.yaml's
// pokkum-yaml-json-schema item wants editors to offer inline validation and
// completion for .pokkum.yaml instead of only failing at `pokkum config
// validate` time. A hand-written schema would drift from
// internal/ports/config.go the same way the eight hand-maintained markdown
// files scripts/gen-docs replaced drifted from the code (see that command's
// doc comment, and Lessons.md) — so, same pattern, the schema is generated
// from the actual Go types instead of hand-authored.
//
// Enum values (strategy, runtime, fail_on_cve, sbom.format, sbom.attach,
// cache.verify_mode, deploy.target, deploy.method, and the profile-only
// output) are not copied from any example file: they come from the typed
// constants in internal/ports and internal/core that the real loader and
// `pokkum config validate` use. See fieldMeta in reflectwalk.go for exactly
// where each one is sourced. base is deliberately NOT an enum — it accepts
// the named presets or an arbitrary custom image reference; see
// baseDescription in reflectwalk.go.
//
// Usage:
//
//	go run ./scripts/gen-schema      # regenerate schema/pokkum.schema.json
//	make schema                      # same, via the Makefile target
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-schema:", err)
		os.Exit(1)
	}
}

func run() error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}

	data, err := RenderSchema()
	if err != nil {
		return fmt.Errorf("gen-schema: rendering schema: %w", err)
	}

	outPath := filepath.Join(repoRoot, "schema", "pokkum.schema.json")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("gen-schema: creating %s: %w", filepath.Dir(outPath), err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("gen-schema: writing %s: %w", outPath, err)
	}
	return nil
}

// findRepoRoot walks up from the working directory looking for go.mod, so
// this tool behaves the same whether invoked as `make schema` (from repo
// root) or `go run ./scripts/gen-schema` from any subdirectory. Copied from
// scripts/gen-docs/main.go's function of the same name — small enough, and
// specific enough to each command's own package, that sharing it isn't worth
// a new internal package.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("gen-schema: getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("gen-schema: could not locate repo root (no go.mod found above %s)", dir)
		}
		dir = parent
	}
}
