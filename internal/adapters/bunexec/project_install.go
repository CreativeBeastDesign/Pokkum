package bunexec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// Automatic installation of the project's own dependencies.
//
// Why this exists: Prepare runs the SvelteKit build (`bun x vite build` or
// `bun run build`) in the project directory and relies entirely on
// <ProjectDir>/node_modules already being there. On a developer machine it
// always is — there is always a node_modules from the last `bun install`. On a
// fresh CI checkout it never is, and CI is exactly what `pokkum build` and the
// GitHub Action are pitched at. A real `sv create`-shaped project following
// docs/GITHUB_ACTION.md's own Quickstart (checkout, registry login, the
// action) failed on its first run with
//
//	.pokkum/vite.config.ts [UNRESOLVED_IMPORT] Could not resolve '@sveltejs/kit/vite'
//
// because nothing had installed anything (2026-09-12). `--dry-run` did not
// catch it — it returns before Prepare runs — and neither did the test suite,
// whose every fixture either symlinks a real node_modules back in or runs a
// fake bun that never needed one.
//
// This is a different install from vendor_install.go's, and the two must not
// be conflated. That one stages *production* dependencies under .pokkum/vendor
// for the image's runtime layer; this one populates the project's own
// node_modules, devDependencies included, so the build tooling (vite,
// @sveltejs/kit, the adapter) can resolve at all. The first ships in the
// image; this one never does.
//
// # When it runs
//
// Only when <ProjectDir>/node_modules is absent, and only for non-hermetic
// builds. An existing tree — whatever its state — is left exactly as found:
//
//   - It is the user's state, or their CI cache's. Rewriting it on every
//     build would make Pokkum the thing that clobbers a `bun link`, a
//     hand-patched package, or a tree a workflow restored from actions/cache,
//     and writes through a symlinked node_modules (which this repo's own test
//     harnesses use) would land in a directory Pokkum was never pointed at.
//   - A tree that is present but stale fails the build in exactly the way
//     `bun run build` would have failed for the user, with the same message —
//     nothing Pokkum-specific to explain.
//
// So the trigger is presence, not a content-hash stamp of the kind
// vendor_install.go keeps: that stamp exists to avoid re-materialising a tree
// Pokkum itself owns, and Pokkum does not own this one.
//
// --hermetic promises zero network egress, and Preflight already refuses a
// hermetic build without node_modules (core.ErrHermeticViolation) using the
// same presence check, so the install never runs there and never needs to.
// --no-install turns the install into a fail-fast check instead, for a
// pipeline that manages its own install step.

// projectNodeModulesPresent reports whether projectDir has an installed
// dependency tree: a node_modules directory, or a symlink to one (os.Stat
// follows it — a tree a monorepo or a test harness linked in is as installed
// as one that was copied).
//
// This is the single decision function for "does this project have its
// dependencies", shared by Preflight's hermetic check and Prepare's install
// gate. Two hand-written copies of the same stat would agree only by
// discipline (mem:self_review_checklist row 72).
func projectNodeModulesPresent(projectDir string) bool {
	info, err := os.Stat(filepath.Join(projectDir, "node_modules"))
	return err == nil && info.IsDir()
}

// ensureProjectDependencies makes sure the SvelteKit build about to run can
// resolve its imports, installing the project's dependencies when nothing has.
// env is the environment the build itself will run with, so a registry token
// or proxy setting passed for the build reaches the install too.
//
// It returns nil without spawning anything when node_modules is already
// present. Otherwise it runs `bun install --no-save` — with `--frozen-lockfile`
// when a lockfile is present — in projectDir, and returns an error wrapping
// core.ErrDependencyInstallFailed if that fails, or core.ErrDependenciesMissing
// if noInstall was requested.
func ensureProjectDependencies(ctx context.Context, projectDir string, noInstall bool, env []string, log *slog.Logger) error {
	if projectNodeModulesPresent(projectDir) {
		log.Debug("bunexec: prepare: node_modules present; not installing", "projectDir", projectDir)
		return nil
	}
	if noInstall {
		return fmt.Errorf(
			"bunexec: prepare %s: node_modules is missing and --no-install was set, so Pokkum did not install the project's dependencies and the SvelteKit build cannot resolve vite or @sveltejs/kit; "+
				"run `bun install --frozen-lockfile` in the project first, or drop --no-install to let Pokkum do it: %w",
			projectDir, core.ErrDependenciesMissing,
		)
	}

	// --no-save keeps package.json and the lockfile untouched: the only
	// thing this may write into the user's tree is node_modules itself.
	args := []string{"install", "--no-save"}
	lockName, _ := findLockfile(projectDir)
	if lockName == "" {
		// Same stance as the production vendor install, for the same
		// reason: refusing here would turn a reproducibility preference into
		// a gate on building at all. Warn, and say what it costs.
		log.Warn("bunexec: prepare: node_modules is missing and no lockfile was found; installing the project's dependencies unpinned. "+
			"The build will work, but it is not reproducible: a rebuild may resolve different versions. "+
			"Commit a lockfile to pin them",
			"projectDir", projectDir, "lookedFor", lockfileNames)
	} else {
		// Exactly the versions the lockfile pins, or fail — an unpinned
		// dev-dependency tree is as much a reproducibility hole as an
		// unpinned production one.
		args = append(args, "--frozen-lockfile")
	}
	log.Info("bunexec: prepare: node_modules is missing; installing the project's dependencies before the SvelteKit build (pass --no-install to fail here instead)",
		"projectDir", projectDir, "lockfile", lockName, "argv", append([]string{"bun"}, args...))

	cmd := exec.CommandContext(ctx, "bun", args...)
	cmd.Dir = projectDir
	cmd.Env = env
	// bun install forks helpers; on cancellation the whole group must go, or
	// Wait blocks on a grandchild still holding the stderr pipe.
	setNewProcessGroup(cmd)
	var stderrBuf bytes.Buffer
	// Streamed live so a long cold install shows progress, and captured so
	// the error can carry bun's own diagnosis. Diagnostic output belongs on
	// stderr for the same reason the build's does: stdout is reserved for the
	// image reference.
	cmd.Stdout = os.Stderr
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	log.Debug("bunexec: exec", "argv", cmd.Args, "dir", cmd.Dir)

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("bunexec: prepare %s: %w", projectDir, ctxErr)
		}
		return fmt.Errorf(
			"bunexec: prepare %s: node_modules was missing, so Pokkum ran `bun %s` to install the project's dependencies before the SvelteKit build, and it failed: %s: %w "+
				"(check that the lockfile matches package.json and that the registry is reachable; to manage the install yourself, run it before `pokkum build` and pass --no-install): %w",
			projectDir, strings.Join(args, " "), strings.TrimSpace(stderrBuf.String()), err, core.ErrDependencyInstallFailed,
		)
	}

	if !projectNodeModulesPresent(projectDir) {
		// bun exited 0 and materialised nothing. A real SvelteKit project
		// cannot get here — it declares vite and @sveltejs/kit at minimum —
		// so this is a project with no dependencies at all, and the build
		// will say so in its own terms. Not an error here: the install is a
		// convenience in front of the build, not a verification of it.
		log.Warn("bunexec: prepare: bun install succeeded but produced no node_modules; the project appears to declare no dependencies", "projectDir", projectDir)
		return nil
	}
	log.Info("bunexec: prepare: project dependencies installed", "projectDir", projectDir, "lockfile", lockName)
	return nil
}
