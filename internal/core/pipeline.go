package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"golang.org/x/sync/errgroup"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Deps is the set of driven ports Build calls out through, plus the two
// ambient sinks the pipeline needs. It is a plain struct rather than a
// constructor-built object because every field is an interface with no
// lifecycle: the composition root in cmd/pokkum fills it in once and hands it
// over.
//
// Which fields are required depends on the request and the options; see
// validate. A field that is not required for a given build may be nil, which
// is what lets a `--tarball` build run without ever constructing a registry
// client.
type Deps struct {
	// Compiler runs preflight, the SvelteKit build and the per-platform Bun
	// compile. Always required.
	Compiler ports.Compiler

	// BaseImages resolves and libc-checks the base image. Always required.
	BaseImages ports.BaseImageResolver

	// Supervisor yields the pokkum-init binary. Always required.
	Supervisor ports.SupervisorProvider

	// Packager assembles images and the index. Required unless the build is a
	// dry run.
	Packager ports.Packager

	// BunRuntime resolves pinned Bun runtime binaries. Required for StrategyLayered.
	BunRuntime ports.BunRuntimeResolver

	// Registry publishes to a remote registry and attaches SBOMs. Required for
	// OutputPush.
	Registry ports.Registry

	// Daemon loads into the local Docker daemon. Required for OutputLocal.
	Daemon ports.LocalLoader

	// Tarballs writes an OCI archive. Required for OutputTarball.
	Tarballs ports.TarballWriter

	// SBOM generates the bill of materials. Required when the request's SBOM
	// format is enabled and the build is not a dry run.
	SBOM ports.SBOMGenerator

	// NativeInspector checks for unsupported native modules or dynamic imports.
	// Always required.
	NativeInspector ports.NativeInspector

	// SLSAGenerator produces in-toto SLSA provenance statements.
	// Required when signing is enabled and the build is not a dry run.
	SLSAGenerator ports.SLSAGenerator

	// CosignSigner signs image digests and produces Simple Signing payloads.
	// Required when signing is enabled and the build is not a dry run.
	CosignSigner ports.CosignSigner

	// DSSESigner wraps and signs payloads in DSSE envelopes.
	// Required when signing is enabled and the build is not a dry run.
	DSSESigner ports.DSSESigner

	// Logger receives every progress and diagnostic line. Nil means
	// slog.Default(). Everything the pipeline logs is a log line, never
	// program output; see Stdout.
	Logger *slog.Logger

	// Stdout receives program output, and nothing else does. In a normal build
	// exactly one line is written here — the published "repo@sha256:…"
	// reference — because that string is what a CI pipeline captures and feeds
	// to the next step. The dry-run summary and the --print-manifest JSON also
	// go here, since in those modes they are the output.
	//
	// Nil means io.Discard: a caller that does not want the output still gets
	// it all in the returned BuildResult.
	Stdout io.Writer

	// Version is the pokkum CLI version, recorded in the toolchain summary and
	// stamped into the image as dev.pokkum.version. Empty is legal and means
	// "unknown", which is normal for a `go run` build.
	Version string

	// UserAgent is appended to the User-Agent sent to registries. Empty means
	// the registry adapter's own default.
	UserAgent string
}

// BuildOptions carries the two execution-mode switches that are not part of
// the request's description of the artefact.
//
// They live here rather than on BuildRequest deliberately: a BuildRequest says
// what image to produce, and both of these say how far to get before stopping.
// Two requests that differ only in these fields describe the same image.
type BuildOptions struct {
	// DryRun resolves everything that can be resolved without side effects —
	// preflight, the base image digest, the platform list, the computed repo
	// and tags — reports it, and stops. No compile, no write, no push.
	DryRun bool

	// PrintManifest performs the real compile and packaging, emits the computed
	// OCI manifest and config JSON, and stops before publishing.
	PrintManifest bool
}

func (o BuildOptions) validate() error {
	if o.DryRun && o.PrintManifest {
		return fmt.Errorf("core: --dry-run and --print-manifest are mutually exclusive: %w", ErrInvalidRequest)
	}
	return nil
}

func (d Deps) logger() *slog.Logger {
	if d.Logger == nil {
		return slog.Default()
	}
	return d.Logger
}

func (d Deps) stdout() io.Writer {
	if d.Stdout == nil {
		return io.Discard
	}
	return d.Stdout
}

// validate reports the first port that this particular build needs and does
// not have. It runs before any subprocess or network call, so a miswired
// composition root fails in microseconds rather than after a two-minute
// compile.
func (d Deps) validate(req BuildRequest, opts BuildOptions) error {
	missing := func(name string) error {
		return fmt.Errorf("core: %s port is required: %w", name, ErrInvalidRequest)
	}
	if d.Compiler == nil {
		return missing("compiler")
	}
	if d.BaseImages == nil {
		return missing("base image resolver")
	}
	if d.Supervisor == nil {
		return missing("supervisor provider")
	}
	if d.NativeInspector == nil {
		return missing("native inspector")
	}
	if opts.DryRun {
		return nil
	}
	if d.Packager == nil {
		return missing("packager")
	}
	if req.SBOM.Format.Enabled() && d.SBOM == nil {
		return missing("sbom generator")
	}
	if req.Sign {
		if d.SLSAGenerator == nil {
			return missing("slsa generator")
		}
		if d.CosignSigner == nil {
			return missing("cosign signer")
		}
		if d.DSSESigner == nil {
			return missing("dsse signer")
		}
	}
	if opts.PrintManifest {
		return nil
	}
	switch req.Output.Mode {
	case OutputPush:
		if d.Registry == nil {
			return missing("registry")
		}
	case OutputLocal:
		if d.Daemon == nil {
			return missing("local loader")
		}
	case OutputTarball:
		if d.Tarballs == nil {
			return missing("tarball writer")
		}
	}
	return nil
}

// Build runs the whole pipeline: validate, preflight, prepare, compile and
// package every platform, index them, generate the SBOM, publish, attach.
//
// The stage order is fixed and each stage is a barrier, because every stage
// consumes the previous one's output:
//
//  1. Normalize + Validate the request, and check the ports are wired. No
//     subprocess and no packet leaves the machine before this passes.
//  2. Compiler.Preflight — bun present and new enough, adapter installed,
//     project shaped like a SvelteKit project.
//  3. BaseImageResolver.Resolve — one call for every platform at once, and
//     the libc compatibility gate. Done before any compile so that an
//     incompatible base costs a round trip rather than two 90 MB builds.
//  4. --dry-run stops here and reports the plan.
//  5. Compiler.Prepare — the SvelteKit build. Exactly once, never in
//     parallel: it writes into ProjectDir/.svelte-kit and the port documents
//     it as unsafe to run concurrently for the same project.
//  6. Fan out: per platform Compile → Supervisor.Binary → Packager.Build,
//     plus the single SBOM scan. This is the only parallel section and it is
//     where all the wall-clock time is.
//  7. Packager.Index when more than one platform was built.
//  8. --print-manifest stops here and emits the manifest and config JSON.
//  9. Publish through whichever of Registry / LocalLoader / TarballWriter the
//     output mode selected.
//  10. Registry.AttachSBOM, push mode only, after the subject exists.
//  11. Write the published reference to Deps.Stdout, alone.
//
// ctx is threaded into every port call and is checked between stages, so a
// Ctrl-C aborts an in-flight compile or push instead of letting it finish.
func Build(ctx context.Context, deps Deps, req BuildRequest, opts BuildOptions) (BuildResult, error) {
	started := time.Now()

	// Stage 1: everything that can be decided from the request alone.
	req.Normalize()
	if err := req.Validate(); err != nil {
		return BuildResult{}, err
	}
	if err := opts.validate(); err != nil {
		return BuildResult{}, err
	}
	if err := deps.validate(req, opts); err != nil {
		return BuildResult{}, err
	}
	if err := checkCtx(ctx, "preflight"); err != nil {
		return BuildResult{}, err
	}

	log := deps.logger()
	log.Info("build starting",
		"projectDir", req.ProjectDir,
		"repo", req.Repo,
		"platforms", PlatformList(req.Platforms),
		"output", req.Output.Mode,
		"dryRun", opts.DryRun,
		"printManifest", opts.PrintManifest)

	// Stage 2: host toolchain and project layout.
	pf, err := deps.Compiler.Preflight(ctx, ports.PreflightRequest{
		ProjectDir:    req.ProjectDir,
		MinBunVersion: req.Compile.MinBunVersion,
		Env:           req.Compile.Env,
	})
	if err != nil {
		return BuildResult{}, err
	}
	log.Info("preflight ok", "bun", pf.BunVersion, "bunPath", pf.BunPath, "adapter", pf.AdapterVersion, "sveltekit", pf.SvelteKitVersion)

	// Preflight Native Inspection
	if _, err := deps.NativeInspector.Inspect(ctx, req.ProjectDir, req.Platforms[0]); err != nil {
		return BuildResult{}, err
	}
	log.Info("native inspector ok")

	toolchain := Toolchain{
		PokkumVersion:    deps.Version,
		BunVersion:       pf.BunVersion,
		AdapterVersion:   pf.AdapterVersion,
		SvelteKitVersion: pf.SvelteKitVersion,
	}
	// A supervisor with no version is explicitly legal ("unknown"), so a
	// failure to report one is a warning and never fails a build.
	if v, err := deps.Supervisor.Version(ctx); err != nil {
		log.Warn("supervisor version unavailable", "err", err)
	} else {
		toolchain.SupervisorVersion = v
	}

	if err := checkCtx(ctx, "base image resolution"); err != nil {
		return BuildResult{}, err
	}

	// Stage 3: the base image, resolved once for every platform. The port
	// takes the whole platform set and returns one image per platform, so this
	// is deliberately outside the per-platform fan-out — which is also what
	// makes it reachable from --dry-run.
	base, err := deps.BaseImages.Resolve(ctx, ports.BaseImageRequest{
		Preset:       req.BaseImage.Preset,
		Ref:          req.BaseImage.Ref,
		Platforms:    req.Platforms,
		Insecure:     req.Insecure,
		LockfilePath: filepath.Join(req.ProjectDir, ports.PokkumLockfileName),
		UpdateBase:   req.BaseImage.UpdateBase,
		Offline:      req.BaseImage.Offline,
	})
	if err != nil {
		return BuildResult{}, err
	}
	if base == nil {
		return BuildResult{}, fmt.Errorf("core: base image resolver returned no image for %q: %w", req.BaseImage.Ref, ErrInvalidBaseImage)
	}
	baseInfo := BaseImageInfo{
		Preset:    req.BaseImage.Preset,
		Ref:       base.Ref,
		PinnedRef: base.PinnedRef,
		Digest:    base.Digest,
	}
	log.Info("base image resolved", "ref", baseInfo.Ref, "pinned", baseInfo.PinnedRef, "isIndex", base.IsIndex)

	// Stage 4: --dry-run stops here, having touched nothing.
	if opts.DryRun {
		res := BuildResult{
			Image: ImageResult{
				Mode:        req.Output.Mode,
				Tags:        slices.Clone(req.Tags),
				TarballPath: req.Output.TarballPath,
				Platforms:   slices.Clone(req.Platforms),
				IsIndex:     len(req.Platforms) > 1,
			},
			BaseImage:       baseInfo,
			Toolchain:       toolchain,
			SourceDateEpoch: req.SourceDateEpoch,
			Duration:        time.Since(started),
		}
		if err := writePlan(deps.stdout(), req, res); err != nil {
			return res, err
		}
		log.Info("dry run complete; nothing was built, written or pushed")
		return res, nil
	}

	// The scratch directory for the compiled binaries. An explicit WorkDir is
	// kept; a temporary one is removed, because two 90 MB binaries per build
	// in the system temp directory is not a footprint to leave behind.
	workDir, cleanup, err := prepareWorkDir(req.WorkDir)
	if err != nil {
		return BuildResult{}, err
	}
	defer cleanup()

	if err := checkCtx(ctx, "sveltekit build"); err != nil {
		return BuildResult{}, err
	}

	// Stage 5: the SvelteKit build. Once, serially, before any fan-out.
	prep, err := deps.Compiler.Prepare(ctx, ports.PrepareRequest{
		Strategy:        req.Compile.Strategy,
		ProjectDir:      req.ProjectDir,
		SourceDateEpoch: req.SourceDateEpoch,
		Env:             req.Compile.Env,
		Platforms:       slices.Clone(req.Platforms),
		NoInject:        req.Compile.NoInject,
	})
	if err != nil {
		return BuildResult{}, err
	}
	log.Info("sveltekit build complete", "entrypoint", prep.EntrypointPath)

	if err := checkCtx(ctx, "compile"); err != nil {
		return BuildResult{}, err
	}

	// Stage 6: the parallel section.
	built, doc, err := fanOut(ctx, deps, req, base, prep, workDir, imageLabels(req, baseInfo, toolchain))
	if err != nil {
		return BuildResult{}, err
	}

	artifacts := make([]Artifact, len(built))
	images := make(map[Platform]v1.Image, len(built))
	for i, b := range built {
		artifacts[i] = b.artifact
		images[b.artifact.Platform] = b.image
	}

	result := BuildResult{
		Artifacts:       artifacts,
		BaseImage:       baseInfo,
		Toolchain:       toolchain,
		SourceDateEpoch: req.SourceDateEpoch,
	}
	if doc != nil {
		result.SBOM = &SBOMResult{
			Format:       doc.Format,
			AttachMode:   req.SBOM.AttachMode,
			SHA256:       doc.SHA256,
			PackageCount: doc.PackageCount,
			Size:         int64(len(doc.Content)),
		}
	}

	if err := checkCtx(ctx, "index assembly"); err != nil {
		return BuildResult{}, err
	}

	// Stage 7: an index only when there is more than one platform to tie
	// together. Pushing a one-entry index would be legal but it makes every
	// `docker pull` and every manifest inspection one indirection longer for
	// no benefit.
	multi := len(built) > 1
	payload := ports.Payload{Image: built[0].image}
	if multi {
		idx, err := deps.Packager.Index(ctx, ports.IndexRequest{
			Images:      images,
			Annotations: req.Annotations,
			CreatedAt:   req.SourceDateEpoch,
		})
		if err != nil {
			return BuildResult{}, err
		}
		payload = ports.Payload{Image: nil, Index: idx}
		log.Info("image index assembled", "platforms", PlatformList(req.Platforms))
	}

	// Stage 8: --print-manifest stops here, before anything is published.
	if opts.PrintManifest {
		if err := writeManifests(deps.stdout(), req, payload, built); err != nil {
			return result, err
		}
		result.Image = ImageResult{
			Mode:        req.Output.Mode,
			Tags:        slices.Clone(req.Tags),
			TarballPath: req.Output.TarballPath,
			Platforms:   slices.Clone(req.Platforms),
			IsIndex:     multi,
		}
		if d, err := payloadDigest(payload); err == nil {
			result.Image.Digest = d
		}
		result.Duration = time.Since(started)
		log.Info("manifest printed; nothing was published")
		return result, nil
	}

	if err := checkCtx(ctx, "publish"); err != nil {
		return BuildResult{}, err
	}

	// Stage 9: publish.
	pub, err := publish(ctx, deps, req, payload, multi)
	if err != nil {
		return BuildResult{}, err
	}
	result.Image = NewImageResult(req.Output.Mode, pub, publishedPlatforms(req, multi), multi && req.Output.Mode != OutputLocal)
	log.Info("published", "mode", req.Output.Mode, "ref", pub.Ref, "digest", pub.Digest.String(), "tags", strings.Join(pub.Tags, ","))

	// Stage 10: attach the SBOM to the digest that was just published — the
	// index digest for a multi-platform build, because the document describes
	// the project and not any one architecture.
	if doc != nil && req.Output.Mode == OutputPush && !req.SBOM.NoAttach {
		if err := checkCtx(ctx, "sbom attachment"); err != nil {
			return result, err
		}
		att, err := deps.Registry.AttachSBOM(ctx, ports.AttachSBOMRequest{
			Repo:       req.Repo,
			Subject:    pub.Digest,
			Document:   *doc,
			AttachMode: req.SBOM.AttachMode,
			Insecure:   req.Insecure,
		})
		if err != nil {
			return result, fmt.Errorf("core: attach sbom to %s (the image itself was pushed; set SBOM.NoAttach to skip this step): %w", pub.Ref, err)
		}
		result.SBOM.Ref = att.Ref
		log.Info("sbom attached", "ref", att.Ref, "format", doc.Format, "packages", doc.PackageCount)
	} else if doc != nil {
		log.Info("sbom generated but not attached", "mode", req.Output.Mode, "noAttach", req.SBOM.NoAttach)
	}

	if req.Sign && req.Output.Mode == OutputPush {
		if err := checkCtx(ctx, "signing"); err != nil {
			return result, err
		}
		log.Info("generating SLSA provenance attestation", "ref", pub.Ref)
		slsaStmt, serr := deps.SLSAGenerator.Generate(ctx, ports.SLSAGeneratorRequest{
			ProjectDir:      req.ProjectDir,
			Repo:            req.Repo,
			Tags:            req.Tags,
			Platforms:       req.Platforms,
			OutputMode:      req.Output.Mode.String(),
			BaseImage: ports.SLSABaseImage{
				Preset:    req.BaseImage.Preset,
				Ref:       base.Ref,
				PinnedRef: base.PinnedRef,
				Digest:    base.Digest,
			},
			OutputDigest: pub.Digest,
			Toolchain: ports.SLSAToolchain{
				PokkumVersion:     deps.Version,
				GoVersion:         runtime.Version(),
				BuilderOSArch:     runtime.GOOS + "/" + runtime.GOARCH,
				BunVersion:        toolchain.BunVersion,
				SupervisorVersion: toolchain.SupervisorVersion,
			},
			SourceDateEpoch: req.SourceDateEpoch,
		})
		if serr != nil {
			log.Warn("failed to generate SLSA provenance statement", "err", serr)
		} else {
			log.Info("generated SLSA provenance statement", "subject", slsaStmt.Subject[0].Name)
		}
	}

	result.Duration = time.Since(started)

	// Stage 11: the one line of program output. Callers pipe this straight
	// into `kubectl set image` or a manifest rewrite, so nothing else may ever
	// share the stream.
	if _, err := fmt.Fprintln(deps.stdout(), result.Image.Ref); err != nil {
		return result, fmt.Errorf("core: write result reference: %w", err)
	}
	log.Info("build complete", "duration", result.Duration.Round(time.Millisecond).String())
	return result, nil
}

// platformBuild is one platform's slice of the fan-out.
type platformBuild struct {
	artifact Artifact
	image    v1.Image
}

// fanOut runs the per-platform compile/supervisor/package chain concurrently,
// alongside the single project SBOM scan.
//
// Concurrency shape:
//
//   - Each platform is one task. req.Concurrency caps how many run at once,
//     enforced by an explicit slot channel rather than errgroup.SetLimit,
//     because the SBOM scan is per-build and must not consume a platform slot.
//   - The SBOM scan runs in the same group so that a scan failure cancels the
//     compiles instead of letting two 90 MB builds run to completion for
//     output that is already doomed. It is safe to run alongside them: it
//     reads ProjectDir, which Prepare has already finished writing to, and the
//     compilers write only into workDir.
//   - errgroup.WithContext gives the cancel-the-others behaviour: the first
//     error cancels gctx, every other in-flight port call sees a done context
//     and returns, and Wait reports the first error.
func fanOut(
	ctx context.Context,
	deps Deps,
	req BuildRequest,
	base *ports.BaseImage,
	prep ports.PrepareResult,
	workDir string,
	labels map[string]string,
) ([]platformBuild, *ports.SBOMDocument, error) {
	log := deps.logger()
	built := make([]platformBuild, len(req.Platforms))
	var doc *ports.SBOMDocument

	limit := req.Concurrency
	if limit <= 0 {
		limit = len(req.Platforms)
	}
	slots := make(chan struct{}, limit)

	g, gctx := errgroup.WithContext(ctx)

	for i, p := range req.Platforms {
		g.Go(func() error {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-gctx.Done():
				return gctx.Err()
			}

			baseImg, ok := base.Images[p]
			if !ok || baseImg == nil {
				return fmt.Errorf("core: base image %q has no image for %s: %w", base.Ref, p, ErrBaseImageIncompatible)
			}

			var art ports.Artifact
			var bunResult ports.BunResolverResult

			if req.Compile.Strategy == StrategyLayered {
				if deps.BunRuntime == nil {
					return fmt.Errorf("core: bun runtime resolver unavailable for layered strategy: %w", ErrPackageFailed)
				}
				res, err := deps.BunRuntime.Resolve(gctx, ports.BunResolverRequest{
					Platform:         p,
					Version:          req.BunRuntime.Version,
					Variant:          req.BunRuntime.Variant,
					CustomBinaryPath: req.BunRuntime.CustomBinaryPath,
					SourceDateEpoch:  req.SourceDateEpoch,
				})
				if err != nil {
					return fmt.Errorf("core: resolve bun runtime for %s: %w", p, err)
				}
				bunResult = res
				log.Info("resolved bun runtime", "platform", p.String(), "version", bunResult.Version, "sha256", bunResult.SHA256)
			} else {
				outPath := filepath.Join(workDir, "app-"+platformSlug(p))
				log.Info("compiling", "platform", p.String(), "output", outPath)
				compiledArt, err := deps.Compiler.Compile(gctx, ports.CompileRequest{
					ProjectDir:      req.ProjectDir,
					EntrypointPath:  prep.EntrypointPath,
					Platform:        p,
					OutputPath:      outPath,
					SourceDateEpoch: req.SourceDateEpoch,
					Env:             req.Compile.Env,
					Minify:          !req.Compile.NoMinify,
					Sourcemap:       req.Compile.Sourcemap,
				})
				if err != nil {
					return err
				}
				art = compiledArt
				log.Info("compiled", "platform", p.String(), "size", art.Size, "sha256", art.SHA256)
			}

			sup, err := deps.Supervisor.Binary(gctx, p)
			if err != nil {
				return err
			}

			pkgReq := ports.PackageRequest{
				Platform:    p,
				Base:        baseImg,
				Strategy:    req.Compile.Strategy,
				Compression: req.Compile.Compression,
				App:         art,
				BunRuntime:  bunResult,
				Supervisor:  sup,
				Runtime:     req.Runtime,
				CreatedAt:   req.SourceDateEpoch,
				Labels:      labels,
				Annotations: req.Annotations,
			}

			if req.Compile.Strategy == StrategyLayered {
				pkgReq.AppServerDir = filepath.Join(prep.OutputDir, "server")
				pkgReq.AppClientDir = filepath.Join(prep.OutputDir, "client")
				pkgReq.AppVendorDir = filepath.Join(prep.OutputDir, "vendor")
				pkgReq.AppNativeDir = filepath.Join(prep.OutputDir, "native")
			}

			img, err := deps.Packager.Build(gctx, pkgReq)
			if err != nil {
				return err
			}
			log.Info("packaged", "platform", p.String(), "strategy", req.Compile.Strategy)

			built[i] = platformBuild{artifact: art, image: img}
			return nil
		})
	}

	if req.SBOM.Format.Enabled() {
		g.Go(func() error {
			log.Info("scanning project for sbom", "format", req.SBOM.Format)
			d, err := deps.SBOM.Generate(gctx, ports.SBOMRequest{
				ProjectDir: req.ProjectDir,
				Format:     req.SBOM.Format,
				Name:       req.Repo,
				CreatedAt:  req.SourceDateEpoch,
			})
			if err != nil {
				return err
			}
			if d == nil {
				return fmt.Errorf("core: sbom generator returned no document: %w", ErrSBOMFailed)
			}
			if d.PackageCount == 0 {
				log.Warn("sbom catalogued no packages", "projectDir", req.ProjectDir)
			}
			doc = d
			return nil
		})
	} else {
		log.Info("sbom generation disabled")
	}

	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return built, doc, nil
}

// publish hands the payload to whichever destination the output mode selected.
func publish(ctx context.Context, deps Deps, req BuildRequest, payload ports.Payload, multi bool) (ports.PublishResult, error) {
	switch req.Output.Mode {
	case OutputPush:
		return deps.Registry.Push(ctx, ports.PushRequest{
			Repo:      req.Repo,
			Tags:      slices.Clone(req.Tags),
			Payload:   payload,
			Insecure:  req.Insecure,
			UserAgent: deps.UserAgent,
		})

	case OutputLocal:
		// A single-platform build names its platform so the daemon cannot pick
		// a different one; a multi-platform build leaves it zero, which the
		// port documents as "whatever the daemon itself runs" — the only
		// sensible answer when the classic image store can hold just one.
		var want Platform
		if !multi {
			want = req.Platforms[0]
		}
		return deps.Daemon.Load(ctx, ports.LoadRequest{
			Repo:     req.Repo,
			Tags:     slices.Clone(req.Tags),
			Payload:  payload,
			Platform: want,
		})

	case OutputTarball:
		return deps.Tarballs.Write(ctx, ports.TarballRequest{
			Path:    req.Output.TarballPath,
			Repo:    req.Repo,
			Tags:    slices.Clone(req.Tags),
			Payload: payload,
		})

	default:
		return ports.PublishResult{}, fmt.Errorf("output mode %q: %w", req.Output.Mode, ErrInvalidOutputMode)
	}
}

// publishedPlatforms reports which platforms the published artefact actually
// contains. Everything except a daemon load carries them all; the daemon holds
// one, and which one it chose is not something the port reports back, so a
// multi-platform load reports nothing rather than a guess.
func publishedPlatforms(req BuildRequest, multi bool) []Platform {
	if req.Output.Mode == OutputLocal && multi {
		return nil
	}
	return req.Platforms
}

// prepareWorkDir returns the scratch directory for compiled binaries and a
// cleanup function. The cleanup is a no-op for a caller-supplied directory,
// which is the whole point of BuildRequest.WorkDir.
func prepareWorkDir(dir string) (string, func(), error) {
	if strings.TrimSpace(dir) == "" {
		tmp, err := os.MkdirTemp("", "pokkum-build-")
		if err != nil {
			return "", nil, fmt.Errorf("core: create build scratch directory: %w: %w", err, ErrCompileFailed)
		}
		return tmp, func() { _ = os.RemoveAll(tmp) }, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, fmt.Errorf("core: resolve work directory %q: %w: %w", dir, err, ErrInvalidRequest)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", nil, fmt.Errorf("core: create work directory %q: %w: %w", abs, err, ErrInvalidRequest)
	}
	return abs, func() {}, nil
}

// imageLabels builds the labels core is responsible for. The packager supplies
// org.opencontainers.image.created and .base.digest itself — it reads the
// digest off the very image it layered onto, which is the only value that
// cannot be wrong — so core supplies the ones only it knows: the base
// reference as written, and the three tool versions.
//
// The user's own labels are applied last and win, per PackageRequest.Labels.
func imageLabels(req BuildRequest, base BaseImageInfo, tc Toolchain) map[string]string {
	out := make(map[string]string, len(req.Labels)+4)
	if base.Ref != "" {
		out[ports.LabelBaseName] = base.Ref
	}
	if tc.PokkumVersion != "" {
		out[ports.LabelPokkumVersion] = tc.PokkumVersion
	}
	if tc.BunVersion != "" {
		out[ports.LabelBunVersion] = tc.BunVersion
	}
	if tc.SupervisorVersion != "" {
		out[ports.LabelSupervisor] = tc.SupervisorVersion
	}
	for k, v := range req.Labels {
		out[k] = v
	}
	return out
}

// platformSlug renders a platform as a filename-safe token: "linux-amd64".
func platformSlug(p Platform) string { return strings.ReplaceAll(p.String(), "/", "-") }

// checkCtx turns a cancelled context into an error that names the stage that
// was about to start, so a Ctrl-C during a long build says what it interrupted.
func checkCtx(ctx context.Context, stage string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("core: build cancelled before %s: %w", stage, err)
	}
	return nil
}

// payloadDigest returns the digest of whichever of the payload's two members
// is set.
func payloadDigest(p ports.Payload) (v1.Hash, error) {
	switch {
	case p.Index != nil:
		return p.Index.Digest()
	case p.Image != nil:
		return p.Image.Digest()
	default:
		return v1.Hash{}, errors.New("core: empty payload")
	}
}

// writePlan renders the --dry-run report. It is written for a human reading a
// terminal, not for a machine: --print-manifest is the machine-readable mode.
// The first and last lines both say that nothing happened, because the middle
// of the report looks exactly like a successful build and that is precisely
// the confusion worth spending two lines to prevent.
func writePlan(w io.Writer, req BuildRequest, res BuildResult) error {
	var b strings.Builder

	b.WriteString("pokkum: DRY RUN - nothing was compiled, written or pushed\n\n")

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "  %s\t%s\n", k, v)
		}
	}
	row("project", req.ProjectDir)
	row("platforms", PlatformList(req.Platforms))
	row("base image", res.BaseImage.Ref)
	row("  pinned to", res.BaseImage.PinnedRef)
	row("bun", res.Toolchain.BunVersion)
	row("adapter", res.Toolchain.AdapterVersion)
	row("sveltekit", res.Toolchain.SvelteKitVersion)
	row("supervisor", res.Toolchain.SupervisorVersion)
	row("source date epoch", fmt.Sprintf("%s (%s)",
		req.SourceDateEpoch.UTC().Format(time.RFC3339),
		strconv.FormatInt(req.SourceDateEpoch.Unix(), 10)))
	row("sbom", planSBOM(req))
	row("artefact", planArtefact(req))

	switch req.Output.Mode {
	case OutputPush:
		row("would push", req.Repo)
	case OutputLocal:
		row("would load", "docker daemon, as "+req.Repo)
	case OutputTarball:
		row("would write", req.Output.TarballPath+", as "+req.Repo)
	}
	for _, t := range req.Tags {
		row("  tagged", req.Repo+":"+t)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("core: render dry-run plan: %w", err)
	}

	b.WriteString("\nRe-run without --dry-run to perform the build.\n")

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("core: write dry-run plan: %w", err)
	}
	return nil
}

func planSBOM(req BuildRequest) string {
	if !req.SBOM.Format.Enabled() {
		return "disabled"
	}
	s := req.SBOM.Format.String()
	switch {
	case req.SBOM.NoAttach:
		return s + ", not attached"
	case req.Output.Mode != OutputPush:
		return s + ", not attached (" + req.Output.Mode.String() + " mode has nowhere to put it)"
	default:
		return s + ", attached to the published digest"
	}
}

func planArtefact(req BuildRequest) string {
	if len(req.Platforms) > 1 {
		return fmt.Sprintf("multi-platform index over %d manifests", len(req.Platforms))
	}
	return "single-platform image manifest"
}

// manifestReport is the --print-manifest document: the computed OCI manifests
// and configs, nested as real JSON rather than as escaped strings so that the
// output is one `jq` away from anything a caller wants out of it.
type manifestReport struct {
	Repo   string          `json:"repo"`
	Tags   []string        `json:"tags"`
	Pushed bool            `json:"pushed"`
	Index  *manifestEntry  `json:"index,omitempty"`
	Images []manifestEntry `json:"images"`
}

type manifestEntry struct {
	Platform  string          `json:"platform,omitempty"`
	MediaType string          `json:"mediaType"`
	Digest    string          `json:"digest"`
	Size      int64           `json:"size,omitempty"`
	Manifest  json.RawMessage `json:"manifest"`
	Config    json.RawMessage `json:"config,omitempty"`
}

// writeManifests emits the computed manifest and config JSON for every image
// that would have been published, plus the index when there is one.
func writeManifests(w io.Writer, req BuildRequest, payload ports.Payload, built []platformBuild) error {
	rep := manifestReport{
		Repo:   req.Repo,
		Tags:   slices.Clone(req.Tags),
		Pushed: false,
		Images: make([]manifestEntry, 0, len(built)),
	}

	if payload.Index != nil {
		e, err := indexEntry(payload.Index)
		if err != nil {
			return err
		}
		rep.Index = &e
	}

	for _, b := range built {
		e, err := imageEntry(b.artifact.Platform, b.image)
		if err != nil {
			return err
		}
		rep.Images = append(rep.Images, e)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return fmt.Errorf("core: write manifest report: %w", err)
	}
	return nil
}

func indexEntry(idx v1.ImageIndex) (manifestEntry, error) {
	raw, err := idx.RawManifest()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read index manifest: %w: %w", err, ErrPackageFailed)
	}
	d, err := idx.Digest()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read index digest: %w: %w", err, ErrPackageFailed)
	}
	mt, err := idx.MediaType()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read index media type: %w: %w", err, ErrPackageFailed)
	}
	size, err := idx.Size()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read index size: %w: %w", err, ErrPackageFailed)
	}
	return manifestEntry{
		MediaType: string(mt),
		Digest:    d.String(),
		Size:      size,
		Manifest:  json.RawMessage(raw),
	}, nil
}

func imageEntry(p Platform, img v1.Image) (manifestEntry, error) {
	raw, err := img.RawManifest()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read manifest for %s: %w: %w", p, err, ErrPackageFailed)
	}
	cfg, err := img.RawConfigFile()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read config for %s: %w: %w", p, err, ErrPackageFailed)
	}
	d, err := img.Digest()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read digest for %s: %w: %w", p, err, ErrPackageFailed)
	}
	mt, err := img.MediaType()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read media type for %s: %w: %w", p, err, ErrPackageFailed)
	}
	size, err := img.Size()
	if err != nil {
		return manifestEntry{}, fmt.Errorf("core: read manifest size for %s: %w: %w", p, err, ErrPackageFailed)
	}
	return manifestEntry{
		Platform:  p.String(),
		MediaType: string(mt),
		Digest:    d.String(),
		Size:      size,
		Manifest:  json.RawMessage(raw),
		Config:    json.RawMessage(cfg),
	}, nil
}
