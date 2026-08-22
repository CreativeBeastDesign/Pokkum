package sveltekitutils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// LegacyAdapters is the list of common SvelteKit adapter packages that pokkum replaces.
var LegacyAdapters = []string{
	"@sveltejs/adapter-node",
	"@sveltejs/adapter-vercel",
	"@sveltejs/adapter-auto",
	"@sveltejs/adapter-cloudflare",
	"@sveltejs/adapter-static",
	"@sveltejs/adapter-bun",
}

// DockerFilesToCheck is the list of Docker-related files removed with --remove-dockerfile.
var DockerFilesToCheck = []string{
	"Dockerfile",
	"Dockerfile.dev",
	"Dockerfile.prod",
	"Dockerfile.local",
	".dockerignore",
	"docker-compose.yml",
	"docker-compose.yaml",
}

// AdoptOptions configures the behavior of the pokkum adopt codemod.
type AdoptOptions struct {
	// Dir is the root directory of the SvelteKit project.
	Dir string

	// DryRun reports proposed changes without writing or deleting any files.
	DryRun bool

	// RemoveDockerfile deletes legacy Dockerfile and docker-compose files.
	RemoveDockerfile bool

	// WriteConfig permanently rewrites svelte.config.js on disk. Off by
	// default: pokkum build already injects the adapter and
	// SOURCE_DATE_EPOCH pin into a virtual copy at build time, so an
	// on-disk rewrite isn't required for pokkum build to work — only for a
	// user who wants the change visible in the file itself (editor
	// tooling, or dropping the virtual-injection dependency entirely).
	WriteConfig bool
}

// AdoptResult describes the actions performed (or proposed in dry-run mode).
type AdoptResult struct {
	Directory          string   `json:"directory"`
	DryRun             bool     `json:"dry_run"`
	ConfigUpdated      bool     `json:"config_updated"`
	PackageJSONUpdated bool     `json:"package_json_updated"`
	IgnoreCreated      bool     `json:"ignore_created"`
	RemovedFiles       []string `json:"removed_files"`
	ChangesSummary     []string `json:"changes_summary"`
	Status             string   `json:"status"`
}

// hasSvelteKitDependency reports whether rawPkg (a parsed package.json)
// declares @sveltejs/kit as a dependency or devDependency. Adopt refuses to
// run without this — it previously had no gate at all and would happily
// "adopt" an arbitrary Node project, injecting @jesterkit/exe-sveltekit and
// a pokkum:build script into something that was never SvelteKit.
func hasSvelteKitDependency(rawPkg *orderedJSONObject) bool {
	for _, depKey := range []string{"dependencies", "devDependencies"} {
		if deps, ok := rawPkg.getObject(depKey); ok {
			if deps.has("@sveltejs/kit") {
				return true
			}
		}
	}
	return false
}

// Adopt migrates an existing SvelteKit project (Node/Vercel/Cloudflare/Dockerfile)
// to a native Pokkum container compilation setup.
func Adopt(opts AdoptOptions) (*AdoptResult, error) {
	if opts.Dir == "" {
		opts.Dir = "."
	}

	result := &AdoptResult{
		Directory:      opts.Dir,
		DryRun:         opts.DryRun,
		RemovedFiles:   make([]string, 0),
		ChangesSummary: make([]string, 0),
		Status:         "adopted",
	}
	if opts.DryRun {
		result.Status = "dry_run"
	}

	pkgPath := filepath.Join(opts.Dir, "package.json")
	pkgData, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("adopt: failed to read package.json in %s: %w", opts.Dir, err)
	}

	rawPkg, err := decodeOrderedJSONObject(pkgData)
	if err != nil {
		return nil, fmt.Errorf("adopt: failed to parse package.json: %w", err)
	}

	if !hasSvelteKitDependency(rawPkg) {
		return nil, fmt.Errorf("adopt: %s does not appear to be a SvelteKit project (no @sveltejs/kit dependency found in package.json): %w", opts.Dir, core.ErrInvalidRequest)
	}

	// 1. Clean up legacy adapters and add @jesterkit/exe-sveltekit + scripts in package.json
	pkgModified := false
	var removedAdapters []string

	for _, depKey := range []string{"devDependencies", "dependencies"} {
		if depsObj, ok := rawPkg.getObject(depKey); ok {
			for _, adapter := range LegacyAdapters {
				if depsObj.has(adapter) {
					depsObj.delete(adapter)
					removedAdapters = append(removedAdapters, adapter)
					pkgModified = true
				}
			}
		}
	}

	// Ensure devDependencies exists and contains @jesterkit/exe-sveltekit
	devDeps := rawPkg.ensureObject("devDependencies")
	if !devDeps.has("@jesterkit/exe-sveltekit") {
		devDeps.setString("@jesterkit/exe-sveltekit", "^0.2.0")
		pkgModified = true
	}

	// Ensure scripts has pokkum:build
	scripts := rawPkg.ensureObject("scripts")
	if !scripts.has("pokkum:build") {
		scripts.setString("pokkum:build", "pokkum build")
		pkgModified = true
	}

	if pkgModified {
		result.PackageJSONUpdated = true
		if len(removedAdapters) > 0 {
			result.ChangesSummary = append(result.ChangesSummary, fmt.Sprintf("Removed legacy adapters from package.json: %s", strings.Join(removedAdapters, ", ")))
		}
		result.ChangesSummary = append(result.ChangesSummary, "Configured @jesterkit/exe-sveltekit in devDependencies and added 'pokkum:build' script")

		if !opts.DryRun {
			formatted, err := rawPkg.marshalIndent()
			if err != nil {
				return nil, fmt.Errorf("adopt: failed to format package.json: %w", err)
			}
			formatted = append(formatted, '\n')
			if err := os.WriteFile(pkgPath, formatted, 0o644); err != nil {
				return nil, fmt.Errorf("adopt: failed to write package.json: %w", err)
			}
		}
	}

	// 2. Transform svelte.config.js — only if explicitly requested. pokkum
	// build already injects the adapter and SOURCE_DATE_EPOCH pin into a
	// virtual copy at build time (see AdoptOptions.WriteConfig's doc
	// comment), so this on-disk rewrite is opt-in, not required.
	if opts.WriteConfig {
		configPath := filepath.Join(opts.Dir, "svelte.config.js")
		if configData, err := os.ReadFile(configPath); err == nil {
			source := string(configData)
			transformed, err := TransformConfig(source, DefaultInjectorOptions())
			if err == nil && transformed != source {
				result.ConfigUpdated = true
				result.ChangesSummary = append(result.ChangesSummary, "Updated svelte.config.js with @jesterkit/exe-sveltekit adapter and reproducible kit.version.name pin")
				if !opts.DryRun {
					if err := os.WriteFile(configPath, []byte(transformed), 0o644); err != nil {
						return nil, fmt.Errorf("adopt: failed to write svelte.config.js: %w", err)
					}
				}
			}
		}
	}

	// 3. Create .pokkumignore if missing
	ignorePath := filepath.Join(opts.Dir, ".pokkumignore")
	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		result.IgnoreCreated = true
		result.ChangesSummary = append(result.ChangesSummary, "Created default .pokkumignore")
		if !opts.DryRun {
			defaultIgnore := `# Pokkum exclude patterns
.env
.env.local
.env.*
node_modules
.git
.pokkum
coverage
Dockerfile*
`
			if err := os.WriteFile(ignorePath, []byte(defaultIgnore), 0o644); err != nil {
				return nil, fmt.Errorf("adopt: failed to create .pokkumignore: %w", err)
			}
		}
	}

	// 4. Remove Dockerfile and legacy container files if requested
	if opts.RemoveDockerfile {
		for _, name := range DockerFilesToCheck {
			targetPath := filepath.Join(opts.Dir, name)
			if _, err := os.Stat(targetPath); err == nil {
				relPath := name
				result.RemovedFiles = append(result.RemovedFiles, relPath)
				result.ChangesSummary = append(result.ChangesSummary, fmt.Sprintf("Removed legacy container artifact: %s", relPath))
				if !opts.DryRun {
					if err := os.Remove(targetPath); err != nil {
						return nil, fmt.Errorf("adopt: failed to remove %s: %w", name, err)
					}
				}
			}
		}
	}

	return result, nil
}
