// Package bunexec implements ports.Compiler by shelling out to Bun.
//
// # The two-stage flow
//
// A Pokkum build compiles a SvelteKit project in two stages, matching the
// ports.Compiler contract exactly:
//
//   - Stage one (Prepare) runs `bun run build` in the project directory. That
//     invokes the project's configured SvelteKit adapter,
//     @jesterkit/exe-sveltekit, whose adapt() hook emits a single generated
//     entrypoint at
//     <ProjectDir>/.svelte-kit/jesterkit-sveltekit/temp-server/index.ts,
//     alongside assets.generated.ts, manifest.js, server/, client/ and
//     prerendered/ in the same directory. As a side effect of running,
//     adapt() also shells out to its own `bun build --compile` — this is
//     unavoidable, the adapter has no flag to skip it — and produces a binary
//     Pokkum never uses. See sveltekit.TargetsLinuxX64 for why the project's
//     svelte.config.js should still set that internal pass's target to
//     "linux-x64" even though Pokkum discards its output.
//
//   - Stage two (Compile) runs `bun build --compile` directly, once per
//     requested platform, against the temp-server/index.ts stage one
//     produced. This is the actual point of this package: the adapter's own
//     internal TARGETS_MAP has no plain "linux-arm64" entry (only
//     linux-x64, linux-x64-baseline, linux-x64-musl and linux-arm64-musl —
//     see its source), but Bun's own `--target` flag supports
//     "bun-linux-arm64" directly. Compile bypasses the adapter's target list
//     entirely and calls bun itself, so Pokkum can produce a glibc
//     linux/arm64 binary that the adapter alone cannot.
//
// Platform.BunTarget in internal/ports carries the OS/arch -> bun --target
// mapping this package relies on:
//
//	linux/amd64 -> bun-linux-x64
//	linux/arm64 -> bun-linux-arm64
//
// Both are glibc targets, which is required: Pokkum's base images are glibc
// distroless, and a musl-linked binary would fail to start under them
// (core.ErrBaseImageIncompatible covers that failure mode downstream, in the
// packager, not here).
//
// # Concurrency
//
// Preflight is safe for concurrent use. Compile is safe to call concurrently
// but does not run concurrently: it serializes internally on the receiver (see
// compileMu). Prepare is NOT safe to call concurrently for the same
// ProjectDir: it runs `bun run build`, which writes into
// <ProjectDir>/.svelte-kit, and two concurrent SvelteKit builds against the
// same output directory race on every file the adapter writes. Callers (core)
// must call Prepare exactly once per build and only start Compile calls after
// it returns.
//
// # A gap between this package's brief and the ports.CompileRequest contract
//
// This package was briefed to also support a `--bytecode` compile flag.
// ports.CompileRequest (the request type this package must accept, already
// authored and out of this package's scope to change) carries only Minify
// and Sourcemap — there is no Bytecode field for Compile to read, and
// core.CompileOptions carries no such field either. bytecode compilation is
// therefore NOT implemented: there is no way for a caller to request it
// through the current contract. If bytecode support is wanted, ports.
// CompileRequest needs a Bytecode bool field added upstream first.
package bunexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// adapterPackage and kitPackage are the npm package names bunexec looks for
// when checking that a project is wired for compilation.
const (
	adapterPackage = "@jesterkit/exe-sveltekit"
	kitPackage     = "@sveltejs/kit"
)

var _ ports.Compiler = (*Compiler)(nil)

// Compiler implements ports.Compiler by shelling out to a `bun` executable
// found on PATH. The zero value is not usable; construct one with
// NewCompiler.
//
// Compiler holds no mutable per-build state on its receiver — every method
// derives everything it needs from its request argument and the ambient
// environment (PATH, os.Environ()) — which is what makes Preflight/Compile
// safe to call concurrently per the ports.Compiler contract. bun's location
// is re-resolved via exec.LookPath on every call rather than cached, trading
// a negligible amount of repeated filesystem work for the simplicity of
// having no cache to invalidate or race on.
type Compiler struct {
	logger *slog.Logger

	// compileMu serializes Compile.
	//
	// This was added on a hypothesis that has since been disproved. Repeated
	// two-platform builds were intermittently non-reproducible while three
	// consecutive single-platform builds were byte-identical, which looked like
	// concurrent bun processes sharing state. The actual cause was upstream and
	// per-build, not per-platform: the adapter emitted assets.generated.ts in
	// filesystem order, so each `bun run build` produced a different bundle
	// while both platforms within a single run saw the same one. Sorting that
	// file (see assets.go) fixed it; the single-platform runs had simply been
	// lucky.
	//
	// The lock is kept anyway. bun's behaviour under concurrent compiles in one
	// project is undocumented, serializing costs ~150ms of a multi-second build,
	// and reproducible digests are the whole point of the tool. It is cheap
	// insurance, not a fix — do not cite it as one.
	compileMu sync.Mutex
}

// NewCompiler builds a Compiler. A nil logger falls back to slog.Default().
func NewCompiler(logger *slog.Logger) *Compiler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Compiler{logger: logger}
}

// Preflight verifies the host toolchain and the project layout without
// touching the project directory or starting any compile work. See the
// ports.Compiler doc for the sentinel-error mapping; this method additionally
// returns core.ErrProjectNotFound when package.json is missing, which is not
// one of the three sentinels ports.Compiler's doc names explicitly but is
// declared in core/errors.go for exactly this case.
func (c *Compiler) Preflight(ctx context.Context, req ports.PreflightRequest) (ports.PreflightResult, error) {
	log := c.logger
	log.Debug("bunexec: preflight", "projectDir", req.ProjectDir)

	if err := ctx.Err(); err != nil {
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: %w", req.ProjectDir, err)
	}

	if req.Hermetic {
		nodeModulesPath := filepath.Join(req.ProjectDir, "node_modules")
		if info, err := os.Stat(nodeModulesPath); err != nil || !info.IsDir() {
			return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: hermetic mode requires pre-populated node_modules: %w", req.ProjectDir, core.ErrHermeticViolation)
		}
		log.Info("bunexec: preflight hermetic mode active; node_modules verified", "projectDir", req.ProjectDir)
	}

	pkg, err := sveltekitutils.ReadPackageJSON(req.ProjectDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: package.json not found: %w", req.ProjectDir, core.ErrProjectNotFound)
		}
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: read package.json: %w: %w", req.ProjectDir, err, core.ErrProjectNotFound)
	}

	// svelte.config.js is optional, not required: current `sv create` scaffolds
	// (sv@0.17.0 / @sveltejs/kit@2.63.0+) generate none at all and configure
	// the adapter entirely via vite.config.ts's sveltekit() plugin options
	// instead — see checkEffectiveAdapter in Prepare, and
	// sveltekitutils.EffectiveAdapterConfigured, for the full detection this
	// mirrors a strategy-unaware subset of. A real project's absence of this
	// file is therefore not evidence it isn't a SvelteKit project; only a
	// genuine read failure other than "does not exist" is treated as one.
	cfgPath := filepath.Join(req.ProjectDir, "svelte.config.js")
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: read svelte.config.js: %w: %w", req.ProjectDir, err, core.ErrProjectNotFound)
	}
	cfgSource := string(cfgData)

	if !sveltekitutils.AdapterConfigured(cfgSource, adapterPackage) && !pkg.HasDependency(adapterPackage) && !pkg.HasDependency("@sveltejs/adapter-node") {
		return ports.PreflightResult{}, fmt.Errorf(
			"bunexec: preflight %s: %s or @sveltejs/adapter-node is not configured in svelte.config.js or listed in package.json; install it with `bun add -D %s`: %w",
			req.ProjectDir, adapterPackage, adapterPackage, core.ErrAdapterMissing,
		)
	}

	if !sveltekitutils.TargetsLinuxX64(cfgSource) {
		log.Warn(
			"bunexec: svelte.config.js does not appear to set the adapter's target to \"linux-x64\"; the adapter's own mandatory internal `bun build --compile` pass (there is no opt-out) will emit a host-architecture binary that pokkum discards, wasting build time and disk space — set target: \"linux-x64\" in the adapter options",
			"projectDir", req.ProjectDir,
		)
	}

	bunPath, err := exec.LookPath("bun")
	if err != nil {
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight: no bun executable on PATH: %w: %w", err, core.ErrBunNotFound)
	}

	verOut, err := runCapture(ctx, log, bunPath, req.ProjectDir, req.Env, "--version")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight: %w", ctxErr)
		}
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight: bun --version: %w: %w", err, core.ErrBunNotFound)
	}
	bunVer, err := parseBunVersion(verOut)
	if err != nil {
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight: %w: %w", err, core.ErrBunNotFound)
	}

	minVersion := strings.TrimSpace(req.MinBunVersion)
	if minVersion == "" {
		minVersion = defaultMinBunVersion
	}
	minVer, err := parseBunVersion(minVersion)
	if err != nil {
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight: invalid minimum bun version %q: %w: %w", minVersion, err, core.ErrInvalidRequest)
	}
	if bunVer.less(minVer) {
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight: bun %s found, %s or newer required: %w", bunVer, minVer, core.ErrBunTooOld)
	}

	result := ports.PreflightResult{
		BunPath:          bunPath,
		BunVersion:       bunVer.String(),
		AdapterVersion:   sveltekitutils.ResolveVersion(req.ProjectDir, adapterPackage, pkg),
		SvelteKitVersion: sveltekitutils.ResolveVersion(req.ProjectDir, kitPackage, pkg),
	}
	log.Info("bunexec: preflight ok", "bunPath", result.BunPath, "bunVersion", result.BunVersion, "adapterVersion", result.AdapterVersion)
	return result, nil
}

// Prepare runs `bun run build` once. It is NOT safe to call concurrently for
// the same req.ProjectDir — see the package doc.
func (c *Compiler) Prepare(ctx context.Context, req ports.PrepareRequest) (ports.PrepareResult, error) {
	log := c.logger
	// staticFallbackRel is the leaf filename of the opt-in SPA fallback page
	// adapter-static emitted for a static build, empty otherwise. It is
	// populated in the StrategyStatic branch below and threaded into the
	// result so the packager can stage it and stamp POKKUM_STATIC_FALLBACK.
	var staticFallbackRel string
	log.Info("bunexec: prepare: running sveltekit build", "projectDir", req.ProjectDir, "strategy", req.Strategy)

	targetAdapter := "@sveltejs/adapter-node"
	if req.Strategy == ports.StrategyExe {
		targetAdapter = "@jesterkit/exe-sveltekit"
	} else if req.Strategy.ApplyStatic() {
		targetAdapter = "@sveltejs/adapter-static"
	}

	// If target adapter is not configured, either apply Option B (zero-config
	// virtual Vite config injection) or fail fast if --no-inject is set or
	// injection fails.
	var runViteWrapper bool
	var viteWrapperConfigPath string

	if checkErr := checkEffectiveAdapter(req.ProjectDir, req.Strategy, targetAdapter); checkErr != nil {
		if req.NoInject {
			return ports.PrepareResult{}, checkErr
		}

		// Option B is only safe to engage when it's equivalent to what the
		// project's own build script would have done: swapping in
		// `bun x vite build` instead of `bun run build` silently skips
		// anything else that script does (env setup, a monorepo task
		// runner, pre-build codegen). Require the script to be exactly
		// `vite build` before taking over the build invocation; anything
		// else falls back to Option C's clear, actionable error.
		pkg, pkgErr := sveltekitutils.ReadPackageJSON(req.ProjectDir)
		if pkgErr != nil || strings.TrimSpace(pkg.Scripts["build"]) != "vite build" {
			return ports.PrepareResult{}, checkErr
		}

		viteSource, viteName := readViteConfigSource(req.ProjectDir)
		opts := sveltekitutils.DefaultInjectorOptions()
		opts.TargetAdapter = targetAdapter
		opts.SourceEpoch = req.SourceDateEpoch.Format("20060102150405")
		if req.SourceDateEpoch.IsZero() {
			opts.SourceEpoch = "pokkum-reproducible-build"
		}

		vcVite, err := sveltekitutils.PrepareVirtualViteConfig(req.ProjectDir, viteName, viteSource, opts)
		if err != nil {
			log.Warn("bunexec: failed to prepare virtual vite config; falling back to error", "err", err)
			return ports.PrepareResult{}, checkErr
		}
		runViteWrapper = true
		viteWrapperConfigPath = vcVite.VirtualConfigPath
		log.Info("bunexec: virtual vite config injected", "path", vcVite.VirtualConfigPath)
	}

	entrypoint := filepath.Join(req.ProjectDir, "build", "index.js")
	outputDir := filepath.Join(req.ProjectDir, "build")
	if req.Strategy == ports.StrategyExe {
		entrypoint = filepath.Join(req.ProjectDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")
		outputDir = filepath.Join(req.ProjectDir, ".svelte-kit", "jesterkit-sveltekit")
	} else if req.Strategy.ApplyStatic() {
		// A static build has no server entrypoint: the entire artifact is
		// SvelteKit's .svelte-kit/output staging (client + prerendered), which
		// every adapter populates before it runs. pokkum-static serves those two
		// trees directly, so outputDir points at the staging dir (see the fan-out
		// in core).
		entrypoint = ""
		outputDir = filepath.Join(req.ProjectDir, ".svelte-kit", "output")
	}

	// SOURCE_DATE_EPOCH is exported for user code to read directly (e.g. a
	// svelte.config.js pinning kit.version.name from it, matching
	// testdata/fixtures/sveltekit-basic's convention) — this is not the same
	// as the .pokkum/svelte.config.js virtual-config write PrepareVirtualConfig
	// used to also perform here: that output is never read by either build
	// path (bun run build reads the real svelte.config.js; the Option B
	// wrapper points Vite at .pokkum/vite.config.ts instead), so it's dropped.
	baseEnv := buildEnvWithEpoch(req.Env, req.SourceDateEpoch)

	if !req.NoInject {
		sourceEpoch := req.SourceDateEpoch.Format("20060102150405")
		if req.SourceDateEpoch.IsZero() {
			sourceEpoch = "pokkum-reproducible-build"
		}
		baseEnv = sveltekitutils.BuildEnv(baseEnv, sourceEpoch)
	}

	if req.Hermetic {
		baseEnv = stripHermeticSensitiveEnv(baseEnv)
		baseEnv = append(baseEnv, "BUN_OFFLINE=1", "NODE_ENV=production", "NO_UPDATE_NOTIFIER=1")
		log.Info("bunexec: hermetic environment active", "offline", true)
	}

	var cmd *exec.Cmd
	if runViteWrapper {
		relWrapper, err := filepath.Rel(req.ProjectDir, viteWrapperConfigPath)
		if err != nil {
			relWrapper = viteWrapperConfigPath
		}
		cmd = exec.CommandContext(ctx, "bun", "x", "vite", "build", "--config", relWrapper)
	} else {
		cmd = exec.CommandContext(ctx, "bun", "run", "build")
	}
	cmd.Dir = req.ProjectDir
	cmd.Env = baseEnv
	setNewProcessGroup(cmd)

	hermeticSandboxApplied := false
	hermeticMountIsolationApplied := false
	if req.Hermetic {
		if hermeticSandboxSupported {
			applyHermeticSandbox(cmd)
			hermeticSandboxApplied = true
			if req.HermeticMountIsolation {
				// Reads cmd.Path/Args/Dir/Env (the real `bun run build` /
				// `bun x vite build` invocation) and retargets cmd to the
				// hidden reexec subcommand instead — see
				// hermetic_reexec_linux.go's applyHermeticMountIsolation doc
				// comment for the full mechanism. Must run after cmd.Env is
				// finalized (baseEnv above) and before cmd.Start().
				log.Debug("bunexec: real (pre-reexec) argv", "argv", cmd.Args, "dir", cmd.Dir)
				if err := applyHermeticMountIsolation(cmd); err != nil {
					return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: %w: %w", req.ProjectDir, err, core.ErrPrepareFailed)
				}
				hermeticMountIsolationApplied = true
			}
		} else {
			log.Warn(
				"bunexec: --hermetic requested, but kernel-enforced network isolation is only implemented on Linux; this platform falls back to advisory-only isolation (BUN_OFFLINE=1/NODE_ENV=production/NO_UPDATE_NOTIFIER=1), which a compromised or malicious build-time dependency can simply ignore",
				"projectDir", req.ProjectDir, "goos", runtime.GOOS,
			)
			if req.HermeticMountIsolation {
				log.Warn(
					"bunexec: --hermetic-mount-isolation requested, but is only implemented on Linux; ignored on this platform",
					"projectDir", req.ProjectDir, "goos", runtime.GOOS,
				)
			}
		}
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	// Bun and vite progress chatter is diagnostic output, not data, so it goes
	// to stderr. Sending it to os.Stdout would interleave it with the one thing
	// stdout is reserved for — the resulting image reference — and would corrupt
	// `pokkum resolve -f k8s/ | kubectl apply -f -`, where stdout carries the
	// rewritten manifests.
	cmd.Stdout = os.Stderr

	log.Debug("bunexec: exec", "argv", cmd.Args, "dir", cmd.Dir)

	if hermeticSandboxApplied {
		if err := verifyHermeticSandboxApplied(cmd); err != nil {
			return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: %w: %w", req.ProjectDir, err, core.ErrPrepareFailed)
		}
	}
	if hermeticMountIsolationApplied {
		if err := verifyHermeticMountIsolationApplied(cmd); err != nil {
			return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: %w: %w", req.ProjectDir, err, core.ErrPrepareFailed)
		}
	}

	// Start and Wait are split (rather than cmd.Run()) so a namespace-setup
	// failure (Start) can be distinguished precisely from a real build
	// failure (Wait returning a non-zero exit) instead of guessing from
	// whether any stderr was captured — and so the "sandbox active" log line
	// only fires once the sandboxed process has actually started, not
	// pre-emptively (see mem:self_review_checklist row 16/17 family: an
	// enforcement claim must be logged only once it's verified true, not
	// asserted ahead of the fact).
	if startErr := cmd.Start(); startErr != nil {
		if hermeticSandboxApplied {
			return ports.PrepareResult{}, fmt.Errorf(
				"bunexec: prepare %s: bun run build failed to start inside the hermetic network sandbox: %w (this usually means either the kernel does not allow unprivileged user namespaces here — check /proc/sys/kernel/unprivileged_userns_clone — or pokkum itself is running inside a container whose own seccomp/capability policy blocks creating one, which some Docker-based CI executors restrict by default even though a plain VM runner would not; run without --hermetic to fall back to advisory-only isolation): %w",
				req.ProjectDir, startErr, core.ErrPrepareFailed,
			)
		}
		return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: bun run build failed to start: %w: %w", req.ProjectDir, startErr, core.ErrPrepareFailed)
	}
	if hermeticSandboxApplied {
		log.Info("bunexec: hermetic build sandbox active — kernel-enforced zero network egress via an unshared Linux network namespace, not merely advisory env vars", "projectDir", req.ProjectDir)
	}

	runErr := cmd.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: %w", req.ProjectDir, ctxErr)
	}
	if runErr != nil {
		return ports.PrepareResult{}, fmt.Errorf(
			"bunexec: prepare %s: bun run build failed: %s: %w: %w",
			req.ProjectDir, strings.TrimSpace(stderrBuf.String()), runErr, core.ErrPrepareFailed,
		)
	}

	if req.Strategy.ApplyStatic() {
		// A static build has no server entrypoint; the artifact we contract on is
		// SvelteKit's prerendered output staging, which the fan-out packages.
		if _, err := os.Stat(filepath.Join(outputDir, "prerendered")); err != nil {
			return ports.PrepareResult{}, fmt.Errorf(
				"bunexec: prepare %s: expected prerendered output %s after build (was @sveltejs/adapter-static configured as the adapter, with all routes prerenderable?): %w: %w",
				req.ProjectDir, filepath.Join(outputDir, "prerendered"), err, core.ErrPrepareFailed,
			)
		}

		// Opt-in SPA fallback: if the project's svelte.config.js configures an
		// adapter-static `fallback` page, adapter-static emits it at the client
		// output root (outputDir/client/<rel>). The source of truth for WHAT was
		// configured is the user's config (read non-mutatingly here); the source
		// of truth for WHAT WAS EMITTED is the staged output, verified below.
		// A fallback that was configured but not emitted is a hard failure — the
		// packager would otherwise silently drop the SPA shell from the image.
		if rel, configured := sveltekitutils.StaticFallbackFilename(readConfigSource(req.ProjectDir)); configured {
			emitted := filepath.Join(outputDir, "client", rel)
			// Guard against a config-escape: a fallback name containing a path
			// separator or ".." (Base != itself) would let the config read
			// outside the client output root.
			if filepath.Base(rel) != rel {
				return ports.PrepareResult{}, fmt.Errorf(
					"bunexec: prepare %s: adapter-static fallback %q is not a plain filename (cannot be staged safely): %w",
					req.ProjectDir, rel, core.ErrPrepareFailed,
				)
			}
			if fi, err := os.Stat(emitted); err != nil || fi.IsDir() {
				return ports.PrepareResult{}, fmt.Errorf(
					"bunexec: prepare %s: adapter-static fallback %q was configured but not emitted at %s (is the site actually in SPA mode?): %w: %w",
					req.ProjectDir, rel, emitted, err, core.ErrPrepareFailed,
				)
			}
			staticFallbackRel = rel
			log.Info("bunexec: static SPA fallback detected", "rel", rel, "emitted", emitted)
		}
	} else {
		if _, err := os.Stat(entrypoint); err != nil {
			expectedAdapter := "@sveltejs/adapter-node"
			if req.Strategy == ports.StrategyExe {
				expectedAdapter = "@jesterkit/exe-sveltekit"
			}
			return ports.PrepareResult{}, fmt.Errorf(
				"bunexec: prepare %s: expected entrypoint %s not found after build (was %s configured as the adapter?): %w: %w",
				req.ProjectDir, entrypoint, expectedAdapter, err, core.ErrPrepareFailed,
			)
		}

		if req.Strategy == ports.StrategyExe {
			// @jesterkit/exe-sveltekit's discoverClientAssets walks the filesystem
			// without sorting, so assets.generated.ts (which temp-server/index.ts
			// imports and Compile bundles) can list the same set of assets in a
			// different order between two otherwise-identical builds. That reordering
			// alone changes the compiled binary's bytes. Normalize it here, once, right
			// after the SvelteKit build that generated it and before any Compile call
			// reads it, so every platform compiles against an identically-ordered
			// entrypoint.
			//
			// assets.generated.ts is exclusively a @jesterkit/exe-sveltekit artifact
			// (its adapt() step generates it) — @sveltejs/adapter-node (StrategyLayered)
			// never produces one, so this must not run for any other strategy. It used
			// to run unconditionally for every non-static strategy, which meant a real
			// StrategyLayered build against the correctly-documented adapter-node adapter
			// always failed here with "no such file or directory". See Lessons.md.
			assetsPath := filepath.Join(filepath.Dir(entrypoint), assetsGeneratedFilename)
			if err := normalizeGeneratedAssetsFile(assetsPath); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: %w", req.ProjectDir, err)
			}
		}
	}

	// The adapter-node handler resolves prerendered pages from its own
	// directory by default; Pokkum mounts them in their own /app/prerendered
	// layer and points the handler at it via POKKUM_PRERENDERED_DIR (set by the
	// packager). Patch the generated handler in the build sandbox so prerendered
	// pages actually serve (layered strategy only; static has its own server).
	if req.Strategy == ports.StrategyLayered {
		if err := c.patchPrerenderedHandler(outputDir, req.ProjectDir); err != nil {
			return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: %w: %w", req.ProjectDir, err, core.ErrPrepareFailed)
		}
	}

	// Telemetry SDK bootstrap. Two different mechanisms depending on
	// strategy, deliberately a positive switch rather than a negative "not
	// static" check (mem:self_review_checklist row 11's own prescribed
	// pattern — a negative check silently includes every strategy nobody
	// thought about; this exact function's earlier StrategyLayered-is-a-
	// silent-no-op bug is row 11's own origin-note incident, see
	// Lessons.md's 2026-08-18 entry, bug 4):
	//
	//   - StrategyExe wraps the *compile* entrypoint (a source-level import
	//     bun build --compile bundles in) — see
	//     sveltekitutils.PrepareVirtualTelemetryEntry's doc comment for why
	//     SvelteKit's own on-disk-only config loading rules out touching
	//     svelte.config.js/src/instrumentation.server.ts directly.
	//   - StrategyLayered has no compile step to wrap an import into — it
	//     packages outputDir directly and execs a fixed argv. Instead,
	//     sveltekitutils.PrepareLayeredTelemetryBootstrap writes the
	//     bootstrap file straight into outputDir (so the packager includes
	//     it automatically, no packaging-layer change needed) and returns
	//     its path relative to outputDir; the packager inserts
	//     `bun --preload <path>` ahead of the real entrypoint in the
	//     image's Entrypoint argv instead of the unconditional
	//     ports.DefaultLayeredEntrypoint() (see packager.go).
	var telemetryPreloadRelPath string
	if req.Telemetry.Enabled {
		switch req.Strategy {
		case ports.StrategyExe:
			telemetryRes, err := sveltekitutils.PrepareVirtualTelemetryEntry(req.ProjectDir, entrypoint, req.Telemetry)
			if err != nil {
				return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: telemetry entry wrapper: %w: %w", req.ProjectDir, err, core.ErrPrepareFailed)
			}
			if telemetryRes.Skipped {
				log.Info("bunexec: telemetry enabled but skipped — project already has its own src/instrumentation.server.{ts,js,mjs}", "projectDir", req.ProjectDir)
			} else {
				log.Info("bunexec: telemetry SDK bootstrap wired into compile entrypoint", "wrapper", telemetryRes.EntrypointPath)
				entrypoint = telemetryRes.EntrypointPath
			}
		case ports.StrategyLayered:
			layeredRes, err := sveltekitutils.PrepareLayeredTelemetryBootstrap(req.ProjectDir, outputDir, req.Telemetry)
			if err != nil {
				return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: telemetry layered bootstrap: %w: %w", req.ProjectDir, err, core.ErrPrepareFailed)
			}
			if layeredRes.Skipped {
				log.Info("bunexec: telemetry enabled but skipped — project already has its own src/instrumentation.server.{ts,js,mjs}", "projectDir", req.ProjectDir)
			} else {
				log.Info("bunexec: telemetry SDK bootstrap packaged for layered runtime preload", "bootstrap", layeredRes.PreloadRelPath)
				telemetryPreloadRelPath = layeredRes.PreloadRelPath
			}
		case ports.StrategyStatic:
			// Deliberate no-op, not a fallthrough to default: a static site
			// (StrategyStatic) has no server-side runtime for a telemetry SDK
			// to hook into — there is nothing here to wrap or preload. Written
			// as its own case (mem:self_review_checklist row 11) so this is
			// legible as "verified nothing to do for this strategy" rather
			// than "nobody has looked at this strategy yet", which is exactly
			// the ambiguity that let StrategyLayered silently fall through the
			// negative-check predecessor of this switch (Lessons.md,
			// 2026-08-18).
		default:
			// req.Strategy is validated by BuildRequest.Validate
			// (internal/core/model.go, BuildStrategy.Valid()) before
			// core.Build ever reaches bunexec.Compiler.Prepare, so every value
			// that can arrive here today is one of the three cases above —
			// this branch should be unreachable in the current codebase.
			// Erroring explicitly (rather than silently skipping telemetry
			// wiring) means a future BuildStrategy value added without
			// updating this switch fails loudly at the exact call site that
			// forgot it, instead of quietly shipping images whose telemetry
			// was never wired — the same failure mode row 11 exists to catch,
			// just guarded against for a strategy that doesn't exist yet
			// instead of one that already shipped broken.
			return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: telemetry enabled for unrecognized build strategy %q: %w", req.ProjectDir, req.Strategy, core.ErrPrepareFailed)
		}
	}

	log.Info("bunexec: prepare complete", "entrypoint", entrypoint, "outputDir", outputDir, "staticFallback", staticFallbackRel)
	return ports.PrepareResult{
		EntrypointPath:          entrypoint,
		OutputDir:               outputDir,
		StaticFallbackRelPath:   staticFallbackRel,
		TelemetryPreloadRelPath: telemetryPreloadRelPath,
	}, nil
}

// readConfigSource returns the raw source text of <projectDir>/svelte.config.js,
// or "" if it cannot be read. It is a non-mutating read used to extract the
// adapter-static `fallback` name; an unreadable config simply yields no SPA
// fallback (the build already validated the project layout by this point).
func readConfigSource(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "svelte.config.js"))
	if err != nil {
		return ""
	}
	return string(data)
}

// viteConfigNames are the Vite config filenames Prepare consults, in Vite's own
// resolution order — the first one that exists is the one Vite loads, so the
// scan must stop there rather than merge every candidate it finds.
var viteConfigNames = []string{
	"vite.config.js",
	"vite.config.mjs",
	"vite.config.ts",
	"vite.config.cjs",
	"vite.config.mts",
	"vite.config.cts",
}

// readViteConfigSource returns the raw source text and filename of the
// project's Vite config, or ("", "") when it has none. It mirrors
// readConfigSource: a non-mutating read whose failure simply means "no Vite
// config governs here".
func readViteConfigSource(projectDir string) (source, name string) {
	for _, candidate := range viteConfigNames {
		data, err := os.ReadFile(filepath.Join(projectDir, candidate))
		if err == nil {
			return string(data), candidate
		}
	}
	return "", ""
}

// checkEffectiveAdapter fails the build when targetAdapter — the adapter this
// strategy's post-build contract depends on — is not configured in the file
// SvelteKit will actually read for this project.
//
// The two failure shapes are reported differently on purpose, because they have
// different fixes and only one of them is obvious:
//
//   - svelte.config.js governs and does not name the package: the ordinary
//     "wrong or missing adapter" case.
//   - vite.config.* governs (it passes options to the sveltekit() plugin, which
//     makes SvelteKit ignore svelte.config.js entirely) and does not name the
//     package: the fix belongs in the Vite config, and editing svelte.config.js
//     — including a svelte.config.js that already names the package correctly —
//     accomplishes nothing at all. This is the shape current `sv create`
//     scaffolds produce.
func checkEffectiveAdapter(projectDir string, strategy ports.BuildStrategy, targetAdapter string) error {
	svelteSource := readConfigSource(projectDir)
	viteSource, viteName := readViteConfigSource(projectDir)

	configured, readFrom, overridden := sveltekitutils.EffectiveAdapterConfigured(svelteSource, viteSource, viteName, targetAdapter)
	if configured {
		return nil
	}

	// A zero-value Strategy is normalised to the default by core before it gets
	// here; render it that way rather than printing an empty flag value.
	shownStrategy := strategy
	if shownStrategy == "" {
		shownStrategy = ports.DefaultBuildStrategy
	}

	if overridden {
		alsoInSvelteConfig := ""
		if sveltekitutils.AdapterConfigured(svelteSource, targetAdapter) {
			alsoInSvelteConfig = " (svelte.config.js does reference it, but SvelteKit never reads that file here)"
		}
		return fmt.Errorf(
			"bunexec: prepare %s: --strategy=%s requires %s, but %s passes options to its sveltekit() plugin call, so SvelteKit ignores svelte.config.js entirely (\"svelte.config.js is ignored when options are passed via your Vite config\") and takes the adapter from %s — which does not reference %s%s; fix it in %s: run `bun add -D %s`, then `import adapter from '%s'` and pass `adapter: adapter()` inside sveltekit({ ... }). If the adapter is re-exported from a local module, import it directly so pokkum can see it: %w",
			projectDir, shownStrategy, targetAdapter, readFrom, readFrom, targetAdapter, alsoInSvelteConfig, readFrom, targetAdapter, targetAdapter, core.ErrAdapterMisconfigured,
		)
	}

	return fmt.Errorf(
		"bunexec: prepare %s: --strategy=%s requires %s, but svelte.config.js does not reference it (a fresh `sv create` project ships @sveltejs/adapter-auto, which does not produce the build output pokkum packages); fix it in svelte.config.js: run `bun add -D %s`, then `import adapter from '%s'` and set `kit.adapter: adapter()`. If the adapter is re-exported from a local module, import it directly so pokkum can see it: %w",
		projectDir, shownStrategy, targetAdapter, targetAdapter, targetAdapter, core.ErrAdapterMisconfigured,
	)
}

// Compile runs `bun build --compile` once, for req.Platform, producing
// req.OutputPath.
//
// Concurrency: calls are serialized on the receiver. Distinct platforms and
// output paths are logically independent, but bun does not produce byte-stable
// output when two compiles run at once against the same project — see the
// compileMu comment on Compiler. Callers may still call this from several
// goroutines; they will simply queue.
//
// Note also that bun embeds the output file's *basename* in the executable
// (two compiles differing only in --outfile basename produce binaries that
// differ in exactly that one byte range, while the same basename in different
// directories produces identical bytes). Callers that care about reproducible
// digests must therefore keep req.OutputPath's basename stable across builds;
// the containing directory may vary freely.
func (c *Compiler) Compile(ctx context.Context, req ports.CompileRequest) (ports.Artifact, error) {
	c.compileMu.Lock()
	defer c.compileMu.Unlock()

	log := c.logger

	target, ok := req.Platform.BunTarget()
	if !ok {
		return ports.Artifact{}, fmt.Errorf("bunexec: compile: platform %s: %w", req.Platform, core.ErrUnsupportedPlatform)
	}

	if err := os.MkdirAll(filepath.Dir(req.OutputPath), 0o755); err != nil {
		return ports.Artifact{}, fmt.Errorf("bunexec: compile %s: create output directory: %w: %w", req.Platform, err, core.ErrCompileFailed)
	}

	args := buildCompileArgs(req.EntrypointPath, req.OutputPath, target, req.Minify, req.Sourcemap)
	cmd := exec.CommandContext(ctx, "bun", args...)
	cmd.Dir = req.ProjectDir
	compileEnv := buildEnvWithEpoch(req.Env, req.SourceDateEpoch)
	if req.Hermetic {
		compileEnv = stripHermeticSensitiveEnv(compileEnv)
	}
	cmd.Env = compileEnv
	setNewProcessGroup(cmd)

	// `bun build --compile` bundles req.EntrypointPath — a file the
	// third-party SvelteKit adapter generated during Prepare — and Bun's
	// bundler runs bunfig.toml preload plugins and `with { type: "macro" }`
	// imports at bundle time, so this stage must be sandboxed identically to
	// Prepare's `bun run build`/`bun x vite build`: a Compiler that only
	// sandboxes Prepare is not actually hermetic, since a malicious
	// build-time dependency can simply wait for this stage to run.
	hermeticSandboxApplied := false
	hermeticMountIsolationApplied := false
	if req.Hermetic {
		if hermeticSandboxSupported {
			applyHermeticSandbox(cmd)
			hermeticSandboxApplied = true
			if req.HermeticMountIsolation {
				log.Debug("bunexec: real (pre-reexec) argv", "argv", cmd.Args, "dir", cmd.Dir)
				if err := applyHermeticMountIsolation(cmd); err != nil {
					return ports.Artifact{}, fmt.Errorf("bunexec: compile %s: %w: %w", req.Platform, err, core.ErrCompileFailed)
				}
				hermeticMountIsolationApplied = true
			}
		} else {
			log.Debug(
				"bunexec: --hermetic requested, but kernel-enforced network isolation is only implemented on Linux; this platform's compile stage also falls back to advisory-only isolation (see Prepare's Warn log for the full explanation, logged once per build rather than once per platform here)",
				"platform", req.Platform, "goos", runtime.GOOS,
			)
			if req.HermeticMountIsolation {
				log.Debug(
					"bunexec: --hermetic-mount-isolation requested, but is only implemented on Linux; ignored on this platform's compile stage",
					"platform", req.Platform, "goos", runtime.GOOS,
				)
			}
		}
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	// Bun and vite progress chatter is diagnostic output, not data, so it goes
	// to stderr. Sending it to os.Stdout would interleave it with the one thing
	// stdout is reserved for — the resulting image reference — and would corrupt
	// `pokkum resolve -f k8s/ | kubectl apply -f -`, where stdout carries the
	// rewritten manifests.
	cmd.Stdout = os.Stderr

	log.Debug("bunexec: exec", "argv", cmd.Args, "dir", cmd.Dir)
	log.Info("bunexec: compiling", "platform", req.Platform, "target", target, "output", req.OutputPath)

	if hermeticSandboxApplied {
		if err := verifyHermeticSandboxApplied(cmd); err != nil {
			return ports.Artifact{}, fmt.Errorf("bunexec: compile %s: %w: %w", req.Platform, err, core.ErrCompileFailed)
		}
	}
	if hermeticMountIsolationApplied {
		if err := verifyHermeticMountIsolationApplied(cmd); err != nil {
			return ports.Artifact{}, fmt.Errorf("bunexec: compile %s: %w: %w", req.Platform, err, core.ErrCompileFailed)
		}
	}

	// Start/Wait split for the same reason as Prepare: precisely distinguish
	// a namespace-setup failure from a real compile failure, and only log
	// sandbox-active once the sandboxed process has actually started.
	if startErr := cmd.Start(); startErr != nil {
		if hermeticSandboxApplied {
			return ports.Artifact{}, fmt.Errorf(
				"bunexec: compile %s: bun build --compile failed to start inside the hermetic network sandbox: %w (this usually means either the kernel does not allow unprivileged user namespaces here — check /proc/sys/kernel/unprivileged_userns_clone — or pokkum itself is running inside a container whose own seccomp/capability policy blocks creating one; run without --hermetic to fall back to advisory-only isolation): %w",
				req.Platform, startErr, core.ErrCompileFailed,
			)
		}
		return ports.Artifact{}, fmt.Errorf("bunexec: compile %s: bun build --compile failed to start: %w: %w", req.Platform, startErr, core.ErrCompileFailed)
	}
	if hermeticSandboxApplied {
		log.Info("bunexec: hermetic build sandbox active for compile — kernel-enforced zero network egress", "platform", req.Platform)
	}

	runErr := cmd.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return ports.Artifact{}, fmt.Errorf("bunexec: compile %s: %w", req.Platform, ctxErr)
	}
	if runErr != nil {
		return ports.Artifact{}, fmt.Errorf(
			"bunexec: compile %s: bun build --compile failed: %s: %w: %w",
			req.Platform, strings.TrimSpace(stderrBuf.String()), runErr, core.ErrCompileFailed,
		)
	}

	artifact, err := hashArtifact(req.Platform, req.OutputPath)
	if err != nil {
		return ports.Artifact{}, fmt.Errorf("bunexec: compile %s: %w: %w", req.Platform, err, core.ErrCompileFailed)
	}

	log.Info("bunexec: compiled", "platform", req.Platform, "size", artifact.Size, "sha256", artifact.SHA256)
	return artifact, nil
}

// hashArtifact opens the file at path and computes its size and SHA256 digest
// in a single pass.
func hashArtifact(platform ports.Platform, path string) (ports.Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return ports.Artifact{}, fmt.Errorf("open compiled artifact %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return ports.Artifact{}, fmt.Errorf("hash compiled artifact %s: %w", path, err)
	}

	return ports.Artifact{
		Platform: platform,
		Path:     path,
		Size:     size,
		SHA256:   hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// runCapture runs bunPath with args in dir, using extraEnv on top of the
// inherited environment, and returns trimmed combined stdout. It is used only
// for the short, cheap `bun --version` probe during Preflight; stage-one and
// stage-two subprocesses use Cmd directly in Prepare/Compile because they
// need to stream, not just capture.
func runCapture(ctx context.Context, log *slog.Logger, bunPath, dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bunPath, args...)
	cmd.Dir = dir
	cmd.Env = buildEnv(extraEnv)
	setNewProcessGroup(cmd)
	log.Debug("bunexec: exec", "argv", cmd.Args, "dir", cmd.Dir)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
