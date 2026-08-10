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
// Preflight and Compile are safe for concurrent use. Prepare is NOT safe to
// call concurrently for the same ProjectDir: it runs `bun run build`, which
// writes into <ProjectDir>/.svelte-kit, and two concurrent SvelteKit builds
// against the same output directory race on every file the adapter writes.
// Callers (core) must call Prepare exactly once per build and only start
// concurrent Compile calls after it returns.
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
	"strings"
	"sync"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekit"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// adapterPackage and kitPackage are the npm package names bunexec looks for
// when checking that a project is wired for compilation.
const (
	adapterPackage = "@jesterkit/exe-sveltekit"
	kitPackage     = "@sveltejs/kit"
)

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

	// compileMu serializes Compile. Two `bun build --compile` processes run
	// concurrently against the same project do not reliably produce identical
	// output: measured over repeated two-platform builds, most runs agreed but
	// a minority produced a different binary for the same inputs, while three
	// consecutive single-platform builds were byte-identical every time. Bun
	// evidently shares state between concurrent compiles in the same project.
	//
	// Serializing costs almost nothing — the compile step itself is ~150ms
	// against a multi-second SvelteKit build — and reproducible digests are the
	// entire point of the tool, so correctness wins by a wide margin here.
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
// returns core.ErrProjectNotFound when package.json or svelte.config.js is
// missing, which is not one of the three sentinels ports.Compiler's doc
// names explicitly but is declared in core/errors.go for exactly this case.
func (c *Compiler) Preflight(ctx context.Context, req ports.PreflightRequest) (ports.PreflightResult, error) {
	log := c.logger
	log.Debug("bunexec: preflight", "projectDir", req.ProjectDir)

	if err := ctx.Err(); err != nil {
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: %w", req.ProjectDir, err)
	}

	pkg, err := sveltekit.ReadPackageJSON(req.ProjectDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: package.json not found: %w", req.ProjectDir, core.ErrProjectNotFound)
		}
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: read package.json: %w: %w", req.ProjectDir, err, core.ErrProjectNotFound)
	}

	cfgPath := filepath.Join(req.ProjectDir, "svelte.config.js")
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: svelte.config.js not found: %w", req.ProjectDir, core.ErrProjectNotFound)
		}
		return ports.PreflightResult{}, fmt.Errorf("bunexec: preflight %s: read svelte.config.js: %w: %w", req.ProjectDir, err, core.ErrProjectNotFound)
	}
	cfgSource := string(cfgData)

	if !sveltekit.AdapterConfigured(cfgSource, adapterPackage) && !pkg.HasDependency(adapterPackage) {
		return ports.PreflightResult{}, fmt.Errorf(
			"bunexec: preflight %s: %s is not configured in svelte.config.js or listed in package.json; install it with `bun add -D %s` and set it as the adapter: %w",
			req.ProjectDir, adapterPackage, adapterPackage, core.ErrAdapterMissing,
		)
	}

	if !sveltekit.TargetsLinuxX64(cfgSource) {
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
		AdapterVersion:   sveltekit.ResolveVersion(req.ProjectDir, adapterPackage, pkg),
		SvelteKitVersion: sveltekit.ResolveVersion(req.ProjectDir, kitPackage, pkg),
	}
	log.Info("bunexec: preflight ok", "bunPath", result.BunPath, "bunVersion", result.BunVersion, "adapterVersion", result.AdapterVersion)
	return result, nil
}

// Prepare runs `bun run build` once. It is NOT safe to call concurrently for
// the same req.ProjectDir — see the package doc.
func (c *Compiler) Prepare(ctx context.Context, req ports.PrepareRequest) (ports.PrepareResult, error) {
	log := c.logger
	log.Info("bunexec: prepare: running sveltekit build", "projectDir", req.ProjectDir)

	entrypoint := filepath.Join(req.ProjectDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")
	outputDir := filepath.Join(req.ProjectDir, ".svelte-kit", "jesterkit-sveltekit")

	cmd := exec.CommandContext(ctx, "bun", "run", "build")
	cmd.Dir = req.ProjectDir
	cmd.Env = buildEnvWithEpoch(req.Env, req.SourceDateEpoch)
	setNewProcessGroup(cmd)

	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	cmd.Stdout = os.Stdout

	log.Debug("bunexec: exec", "argv", cmd.Args, "dir", cmd.Dir)
	runErr := cmd.Run()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return ports.PrepareResult{}, fmt.Errorf("bunexec: prepare %s: %w", req.ProjectDir, ctxErr)
	}
	if runErr != nil {
		return ports.PrepareResult{}, fmt.Errorf(
			"bunexec: prepare %s: bun run build failed: %s: %w: %w",
			req.ProjectDir, strings.TrimSpace(stderrBuf.String()), runErr, core.ErrPrepareFailed,
		)
	}

	if _, err := os.Stat(entrypoint); err != nil {
		return ports.PrepareResult{}, fmt.Errorf(
			"bunexec: prepare %s: expected entrypoint %s not found after build (was @jesterkit/exe-sveltekit configured as the adapter?): %w: %w",
			req.ProjectDir, entrypoint, err, core.ErrPrepareFailed,
		)
	}

	log.Info("bunexec: prepare complete", "entrypoint", entrypoint)
	return ports.PrepareResult{EntrypointPath: entrypoint, OutputDir: outputDir}, nil
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
	cmd.Env = buildEnvWithEpoch(req.Env, req.SourceDateEpoch)
	setNewProcessGroup(cmd)

	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	cmd.Stdout = os.Stdout

	log.Debug("bunexec: exec", "argv", cmd.Args, "dir", cmd.Dir)
	log.Info("bunexec: compiling", "platform", req.Platform, "target", target, "output", req.OutputPath)
	runErr := cmd.Run()

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
