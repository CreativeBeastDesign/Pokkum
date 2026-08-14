package sveltekitutils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	var rawPkg map[string]any
	if err := json.Unmarshal(pkgData, &rawPkg); err != nil {
		return nil, fmt.Errorf("adopt: failed to parse package.json: %w", err)
	}

	// 1. Clean up legacy adapters and add @jesterkit/exe-sveltekit + scripts in package.json
	pkgModified := false
	var removedAdapters []string

	for _, depKey := range []string{"devDependencies", "dependencies"} {
		if depsMap, ok := rawPkg[depKey].(map[string]any); ok {
			for _, adapter := range LegacyAdapters {
				if _, exists := depsMap[adapter]; exists {
					delete(depsMap, adapter)
					removedAdapters = append(removedAdapters, adapter)
					pkgModified = true
				}
			}
		}
	}

	// Ensure devDependencies exists and contains @jesterkit/exe-sveltekit
	devDeps, ok := rawPkg["devDependencies"].(map[string]any)
	if !ok {
		devDeps = make(map[string]any)
		rawPkg["devDependencies"] = devDeps
	}
	if _, exists := devDeps["@jesterkit/exe-sveltekit"]; !exists {
		devDeps["@jesterkit/exe-sveltekit"] = "^0.2.0"
		pkgModified = true
	}

	// Ensure scripts has pokkum:build
	scripts, ok := rawPkg["scripts"].(map[string]any)
	if !ok {
		scripts = make(map[string]any)
		rawPkg["scripts"] = scripts
	}
	if _, exists := scripts["pokkum:build"]; !exists {
		scripts["pokkum:build"] = "pokkum build"
		pkgModified = true
	}

	if pkgModified {
		result.PackageJSONUpdated = true
		if len(removedAdapters) > 0 {
			result.ChangesSummary = append(result.ChangesSummary, fmt.Sprintf("Removed legacy adapters from package.json: %s", strings.Join(removedAdapters, ", ")))
		}
		result.ChangesSummary = append(result.ChangesSummary, "Configured @jesterkit/exe-sveltekit in devDependencies and added 'pokkum:build' script")

		if !opts.DryRun {
			formatted, err := json.MarshalIndent(rawPkg, "", "  ")
			if err != nil {
				return nil, fmt.Errorf("adopt: failed to format package.json: %w", err)
			}
			formatted = append(formatted, '\n')
			if err := os.WriteFile(pkgPath, formatted, 0o644); err != nil {
				return nil, fmt.Errorf("adopt: failed to write package.json: %w", err)
			}
		}
	}

	// 2. Transform svelte.config.js
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
