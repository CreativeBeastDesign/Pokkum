package bunexec

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// Production dependency vendoring for the layered strategy.
//
// Why this exists: Vite's SSR build externalises dependencies, so adapter-node
// output keeps bare imports (`valibot`, `bcryptjs`, …) and expects a
// node_modules tree beside it at runtime — its documented deployment model is
// "ship build/ AND production node_modules". Pokkum shipped only the first half.
//
// The consequences were not subtle. On Bun the gap was masked: Bun's runtime
// auto-install silently fetched the missing packages from the public npm
// registry into the running container, so the app appeared to work while
// executing code that is not in the image, not in the SBOM, not covered by the
// signature and not named in the provenance. Under `pokkum resolve`'s own
// readOnlyRootFilesystem the write failed and the pod crash-looped after
// reporting a successful rollout. On Node, which has no auto-install, images
// simply could not serve a route that touched an externalised dependency.
//
// Determinism: the install runs with --frozen-lockfile against the project's own
// lockfile, so versions are exactly what the lockfile pins; the packager then
// normalises every timestamp when it builds the layer. A missing lockfile is a
// hard error rather than a best-effort install, because an unpinned dependency
// tree cannot be reproduced and reproducibility is the product.

// lockfileNames are the lockfiles `bun install` can honour, in the order they
// are looked for.
var lockfileNames = []string{"bun.lock", "bun.lockb", "package-lock.json"}

// stageProductionDependencies installs the project's production dependencies
// into a staging tree under .pokkum/ and returns the path of the resulting
// node_modules directory.
//
// Nothing in the user's own tree is written to: the staging directory holds a
// copy of package.json and the lockfile, and bun installs beneath it.
func stageProductionDependencies(ctx context.Context, projectDir string, hermetic bool, log *slog.Logger) (string, error) {
	manifest := filepath.Join(projectDir, "package.json")
	if _, err := os.Stat(manifest); err != nil {
		return "", fmt.Errorf("bunexec: vendor: no package.json in %s: %w", projectDir, core.ErrInvalidRequest)
	}

	// A lockfile is strongly preferred but not required. Refusing to build
	// without one would turn a reproducibility preference into a hard gate on
	// building at all, which is the wrong trade for a tool whose first job is
	// producing a working image — and every project without a lockfile would be
	// refused outright. Warn instead, loudly, and say what it costs.
	lockName, lockPath := findLockfile(projectDir)
	if lockName == "" {
		log.Warn("bunexec: vendor: no lockfile found; installing production dependencies unpinned. "+
			"The image will work, but this build is not reproducible: a rebuild may resolve different versions. "+
			"Commit a lockfile to pin them",
			"projectDir", projectDir, "lookedFor", lockfileNames)
	}

	stage := filepath.Join(projectDir, ".pokkum", "vendor")
	if err := os.RemoveAll(stage); err != nil {
		return "", fmt.Errorf("bunexec: vendor: clear staging dir: %w", err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", fmt.Errorf("bunexec: vendor: create staging dir: %w", err)
	}
	staged := map[string]string{manifest: filepath.Join(stage, "package.json")}
	if lockName != "" {
		staged[lockPath] = filepath.Join(stage, lockName)
	}
	for src, dst := range staged {
		if err := copyFileContents(src, dst); err != nil {
			return "", fmt.Errorf("bunexec: vendor: stage %s: %w", filepath.Base(src), err)
		}
	}

	args := []string{"install", "--production", "--no-save"}
	if lockName != "" {
		// Exactly the versions the lockfile pins, or fail — never a silent
		// resolution drift in a build that claims to be reproducible.
		args = append(args, "--frozen-lockfile")
	}
	if hermetic {
		// A hermetic build has no egress, so the install must be served
		// entirely from Bun's local cache. If it is cold this fails, which is
		// the correct outcome: silently shipping an image without its
		// dependencies is what this whole change exists to stop.
		args = append(args, "--offline")
	}

	cmd := exec.CommandContext(ctx, "bun", args...)
	cmd.Dir = stage
	out, err := cmd.CombinedOutput()
	if err != nil {
		hint := ""
		if hermetic {
			hint = " Hermetic builds install from Bun's local cache only; run a normal `bun install` once to warm it, or build without --hermetic."
		}
		return "", fmt.Errorf("bunexec: vendor: bun install --production failed in %s: %w: %s.%s", stage, err, string(out), hint)
	}

	modules := filepath.Join(stage, "node_modules")
	info, err := os.Stat(modules)
	if err != nil || !info.IsDir() {
		// Legitimately possible: a project whose dependencies are all
		// devDependencies. Report it rather than treating it as failure.
		log.Info("bunexec: vendor: no production dependencies to vendor", "projectDir", projectDir, "lockfile", lockName)
		return "", nil
	}

	log.Info("bunexec: vendor: production dependencies staged", "dir", modules, "lockfile", lockName, "hermetic", hermetic)
	return modules, nil
}

// findLockfile returns the first recognised lockfile in projectDir as
// (name, absolute path), or ("", "") when none is present.
func findLockfile(projectDir string) (string, string) {
	for _, name := range lockfileNames {
		p := filepath.Join(projectDir, name)
		if _, err := os.Stat(p); err == nil {
			return name, p
		}
	}
	return "", ""
}

// copyFileContents copies src to dst, creating dst.
func copyFileContents(src, dst string) error {
	data, err := os.ReadFile(src) //nolint:gosec // both paths are constructed from the caller's own project dir
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
