package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
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

	// StaticServer yields the pokkum-static PID-1 static file server binary.
	// Required for StrategyStatic only.
	StaticServer ports.StaticServerProvider

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

	// Tarballs writes a legacy docker-save archive. Required for
	// OutputTarball.
	Tarballs ports.TarballWriter

	// OCILayouts writes a standards-conformant OCI image layout directory.
	// Required for OutputOCILayout.
	OCILayouts ports.OCILayoutWriter

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

	// Scanner scans container images and toolchain advisories for CVEs. Optional.
	Scanner ports.Scanner

	// SecretGuard performs build-time secret scanning on source files. Optional.
	SecretGuard ports.SecretGuard

	// EnvBakeDetector scans project source for $env/static/* imports ahead of
	// packaging. Optional — nil means no detection/warning/annotation.
	EnvBakeDetector ports.EnvBakeDetector

	// RouteFilter drops prerendered routes the operator excluded, after
	// Prepare has written the output tree and before the packager reads it.
	// Nil disables the feature.
	RouteFilter ports.RouteFilter

	// RemoteCache queries and reconciles remote OCI input caches. Optional.
	RemoteCache ports.RemoteCacher

	// AssetOverlay resolves rolling-deploy predecessor lineage and pulls
	// prior generations' immutable client asset content. Required only when
	// req.Compile.AssetOverlayGenerations > 0 (--asset-overlay is used);
	// nil is safe otherwise.
	AssetOverlay ports.AssetOverlayResolver

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

// validate reports the first dependency that this particular build needs and
// does not have. It runs before any subprocess or network call, so a
// miswired composition root fails in microseconds rather than after a
// two-minute compile.
func (d Deps) validate(req BuildRequest, opts BuildOptions) error {
	missing := func(name string) error {
		return fmt.Errorf("core: %s is required: %w", name, ErrInvalidRequest)
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
	if req.Compile.Strategy.ApplyStatic() && d.StaticServer == nil {
		return missing("static server provider")
	}
	if d.NativeInspector == nil {
		return missing("native inspector")
	}
	if req.Compile.AssetOverlayGenerations > 0 && d.AssetOverlay == nil {
		return missing("asset overlay resolver")
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
	case OutputOCILayout:
		if d.OCILayouts == nil {
			return missing("oci layout writer")
		}
	default:
		// An unrecognized output mode reached here would otherwise pass
		// wiring validation silently (none of the three cases' dependency
		// checks apply to it) and only fail much later, inside publish(),
		// with a far less obvious error. Fail fast, at the same point
		// every other missing-dependency case fails.
		return fmt.Errorf("core: unrecognized output mode %q: %w", req.Output.Mode, ErrInvalidOutputMode)
	}
	return nil
}

// Build runs the whole pipeline: validate, preflight, prepare, compile and
// package every platform, index them, generate the SBOM, publish, attach.
//
// The stage order is fixed and every stage but one is a barrier, because
// every stage consumes the previous one's output:
//
//  1. Normalize + Validate the request, and check the ports are wired. No
//     subprocess and no packet leaves the machine before this passes.
//  2. Compiler.Preflight — bun present and new enough, adapter installed,
//     project shaped like a SvelteKit project.
//  3. BaseImageResolver.Resolve — one call for every platform at once, and
//     the libc compatibility gate. Done before any compile so that an
//     incompatible base costs a round trip rather than two 90 MB builds.
//     Signature verification is deliberately *not* part of this call (see
//     stage 5) — Resolve only pins the digest that everything downstream,
//     including the remote-cache key, needs.
//  4. --dry-run stops here and reports the plan, after synchronously
//     verifying the base image signature so a dry run still fails fast on
//     an invalid one.
//  5. Compiler.Prepare — the SvelteKit build — runs concurrently (via
//     errgroup) with BaseImageResolver.VerifyBaseImage and
//     NativeInspector.Inspect, since a cache miss needs all three to
//     succeed before publishing but none of them needs to finish before
//     Prepare can start. Prepare itself still runs exactly once: it writes
//     into ProjectDir/.svelte-kit and the port documents it as unsafe to
//     run concurrently with another Prepare for the same project. A
//     failure in either concurrent check cancels the in-flight Prepare
//     (tearing down the bun subprocess tree, not just abandoning a
//     goroutine) and fails the build before fan-out is ever reached — first
//     error wins via errgroup, not errors.Join, so if two fail at genuinely
//     overlapping times only one error surfaces and which one is not
//     deterministic. A cache hit (stage 3.5, ahead of this stage) skips
//     VerifyBaseImage and Inspect entirely — neither check has anything
//     left to gate once nothing is going to be built from source or from
//     the base image.
//  6. Fan out: per platform Compile → Supervisor.Binary → Packager.Build,
//     plus the single SBOM scan. This is the only other parallel section
//     and it is where most of the wall-clock time is.
//  7. Packager.Index when more than one platform was built.
//  8. --print-manifest stops here and emits the manifest and config JSON.
//  9. Publish through whichever of Registry / LocalLoader / TarballWriter the
//     output mode selected.
//  10. Registry.AttachSBOM, push mode only, after the subject exists.
//     10.5 Signing, push mode only, when a signing key is available: the SLSA
//     provenance statement is DSSE-signed and attached as .att, and the
//     digest is Cosign-signed and attached as .sig — for the published
//     digest AND every per-platform manifest digest — then every attachment
//     is fetched back from the registry and cryptographically verified
//     (post-push self-verification) before the build may report success.
//     With signing enabled but no key, the image pushes unsigned with an
//     unmistakable warning and BuildResult.Signing records the fact;
//     Signing.Require turns that into a validation-time failure instead.
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

	// Static builds have no Bun runtime and no adapter-node server, so the
	// ORIGIN contract doesn't apply. For layered/exe, an unset ORIGIN is the
	// single highest-volume first-deploy failure this tool can proactively
	// warn about: adapter-node falls back to deriving its origin from the
	// raw socket, which is wrong behind a TLS-terminating ingress and
	// produces "403 Cross-site POST form submissions are forbidden" on the
	// app's first form action. Warn, don't fail — plenty of real deployments
	// (bare HTTP, no forms, dev/staging) never hit this.
	if req.Runtime.Origin == "" && req.Compile.Strategy != StrategyStatic {
		log.Warn("ORIGIN not set — if this app is served behind a reverse proxy or ingress, form actions will likely fail with \"403 Cross-site POST form submissions are forbidden\"; set --origin to the public URL this app is served at (e.g. --origin=https://example.com)")
	}

	// Signing honesty, up front — before any expensive work, mirroring the
	// --asset-overlay non-push warning below: --sign defaults to true, so
	// both of these states are ones a user can reach without asking for
	// them, and neither may pass silently.
	if req.Sign && req.Output.Mode != OutputPush {
		// Nothing to attach a .sig/.att to: signatures live in a registry,
		// keyed to the pushed digest. A local/tarball build therefore cannot
		// be signed by this pipeline, and saying nothing would let "--sign
		// was on" read as "the image is signed".
		log.Warn("--sign is enabled (the default) but this output mode writes no registry artifact to attach a signature to; this build will NOT be signed", "outputMode", req.Output.Mode)
	} else if req.Sign && len(req.Signing.KeyPEM) == 0 {
		log.Warn("--sign is enabled (the default) but no signing key is available — this build will push an UNSIGNED image; set POKKUM_SIGNING_KEY or --signing-key to sign, or pass --require-signed to make this an error")
	}

	// SvelteKit inlines $env/static/* as literal values at build time,
	// regardless of strategy — unlike $env/dynamic/*, which is read at
	// container startup, an image that imports from $env/static/* is
	// pinned to whatever environment built it. Promoting that exact image
	// to a different environment silently carries the old values along,
	// with nothing in the running container re-reading the environment to
	// notice — invalidating digest pinning/resolve/rollback/provenance as
	// *environment-independent* guarantees for that image. Detection is a
	// best-effort source scan (see sveltekitutils.DetectStaticEnvBindings's
	// own doc comment for its real gaps: re-exports and dynamic specifiers
	// aren't followed) — warn, don't fail, and record what was found as a
	// durable annotation rather than silently saying nothing. Optional,
	// like SecretGuard, so tests/callers that don't care can leave it nil.
	if deps.EnvBakeDetector != nil {
		envRes, err := deps.EnvBakeDetector.DetectStaticEnv(ctx, ports.EnvBakeRequest{ProjectDir: req.ProjectDir})
		if err == nil && len(envRes.Bindings) > 0 {
			log.Warn("this build imports from $env/static/* — this image will be pinned to the environment it was built in; promoting it to a different environment will NOT pick up new values for these", "bindings", strings.Join(envRes.Bindings, ","))
			req.Runtime.EnvBaked = envRes.Bindings
		}
	}

	// Stage 2: host toolchain and project layout.
	pf, err := deps.Compiler.Preflight(ctx, ports.PreflightRequest{
		ProjectDir:    req.ProjectDir,
		MinBunVersion: req.Compile.MinBunVersion,
		Env:           req.Compile.Env,
		Hermetic:      req.Hermetic,
		// req.Normalize() (called at the top of Build, above) has already
		// defaulted a zero-value Strategy to DefaultBuildStrategy by this
		// point, so Preflight's adapter-configured check is evaluated
		// against the strategy this build will actually use — see
		// ports.PreflightRequest.Strategy's doc comment and Lessons.md's
		// "Preflight is not strategy-aware" entry for why this previously
		// rejected every real --strategy=static project before Prepare's own
		// correct, strategy-aware check ever ran.
		Strategy: req.Compile.Strategy,
	})
	if err != nil {
		return BuildResult{}, err
	}
	log.Info("preflight ok", "bun", pf.BunVersion, "bunPath", pf.BunPath, "adapter", pf.AdapterVersion, "sveltekit", pf.SvelteKitVersion)

	toolchain := Toolchain{
		PokkumVersion:    deps.Version,
		BunVersion:       pf.BunVersion,
		AdapterVersion:   pf.AdapterVersion,
		SvelteKitVersion: pf.SvelteKitVersion,
	}
	// A supervisor/static-server with no version is explicitly legal
	// ("unknown"), so a failure to report one is a warning and never fails a
	// build. Only one of the two is ever embedded in the image, so only its
	// version is recorded — reporting Supervisor's version for a static build
	// (which ships pokkum-static, not pokkum-init) would misdescribe what's
	// actually running.
	if req.Compile.Strategy.ApplyStatic() {
		if v, err := deps.StaticServer.Version(ctx); err != nil {
			log.Warn("static server version unavailable", "err", err)
		} else {
			toolchain.StaticServerVersion = v
		}
	} else if v, err := deps.Supervisor.Version(ctx); err != nil {
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
	lockPath := filepath.Join(req.ProjectDir, ports.PokkumLockfileName)
	// baseReq is reused verbatim for the deferred VerifyBaseImage calls below
	// (dry-run's synchronous one and the concurrent one in the errgroup block
	// near Prepare), so Resolve and VerifyBaseImage always agree on
	// Ref/Preset/Platforms/Insecure/etc. VerifySignature is false here because
	// signature verification no longer happens inline inside Resolve — it now
	// happens later, gated on a confirmed cache miss (or synchronously for
	// dry-run), never on a cache hit.
	baseReq := ports.BaseImageRequest{
		Preset:       req.BaseImage.Preset,
		Ref:          req.BaseImage.Ref,
		Platforms:    req.Platforms,
		Insecure:     req.Insecure,
		LockfilePath: lockPath,
		// --dry-run reads the lockfile for pinning but must not write it back;
		// see BaseImageRequest.LockfileReadOnly.
		LockfileReadOnly: opts.DryRun,
		UpdateBase:       req.BaseImage.UpdateBase,
		Offline:          req.BaseImage.Offline,
		VerifySignature:  false,
		VerifyMode:       req.BaseImage.VerifyMode,
		KeylessIdentity: ports.KeylessIdentity{
			SAN:    req.BaseImage.KeylessSAN,
			Issuer: req.BaseImage.KeylessIssuer,
		},
		TrustedRootJSON: req.BaseImage.TrustedRootJSON,
		// Only a static-strategy payload is fully static. Layered and exe
		// payloads are dynamically linked against glibc, so the static-base gate
		// (which rejects distroless/static and scratch) must stay armed for them;
		// AllowStatic lifts it for --strategy=static so the libc-free default
		// base is permitted.
		AllowStatic: req.Compile.Strategy.ApplyStatic(),
	}
	base, err := deps.BaseImages.Resolve(ctx, baseReq)
	if err != nil {
		return BuildResult{}, err
	}
	if base == nil {
		return BuildResult{}, fmt.Errorf("core: base image resolver returned no image for %q: %w", req.BaseImage.Ref, ErrInvalidBaseImage)
	}
	baseInfo := BaseImageInfo{
		Preset:      req.BaseImage.Preset,
		Ref:         base.Ref,
		UpstreamRef: base.UpstreamRef,
		PinnedRef:   base.PinnedRef,
		Digest:      base.Digest,
	}
	log.Info("base image resolved", "ref", baseInfo.Ref, "pinned", baseInfo.PinnedRef, "isIndex", base.IsIndex)

	if deps.Scanner != nil {
		effectiveFailOn := req.FailOnCVE
		failGateActive := effectiveFailOn != ""
		if effectiveFailOn == "" {
			if envVal := os.Getenv("POKKUM_FAIL_ON_CVE"); envVal != "" {
				if envVal == "1" || strings.EqualFold(envVal, "true") || strings.EqualFold(envVal, "yes") {
					effectiveFailOn = ports.SeverityCritical
					failGateActive = true
				} else if parsedSev, err := ports.ParseSeverity(envVal); err == nil {
					effectiveFailOn = parsedSev
					failGateActive = true
				}
			}
		}
		if effectiveFailOn == "" {
			effectiveFailOn = ports.SeverityCritical
		}

		if req.BaseImage.Offline || req.Hermetic {
			// The scanner needs to query a remote vulnerability database, which an
			// offline/hermetic build cannot do by design. If pokkum.lock already
			// recorded a previous scan audit, we read it back.
			if base.LastScannedAt != "" {
				log.Info("using cached base image vulnerability audit from pokkum.lock",
					"last_scanned_at", base.LastScannedAt,
					"vulns", base.VulnerabilitiesCount,
					"max_severity", base.MaxSeverity)

				if failGateActive {
					if base.MaxSeverity != "" {
						recordedSev, err := ports.ParseSeverity(base.MaxSeverity)
						if err != nil {
							if req.AllowIncompleteScan {
								log.Warn("cached base image vulnerability audit contains invalid severity level; proceeding because --allow-incomplete was set", "max_severity", base.MaxSeverity, "err", err)
							} else {
								return BuildResult{}, fmt.Errorf("cached base image vulnerability audit from %s has invalid max severity %q: %w", base.LastScannedAt, base.MaxSeverity, ErrScanIncomplete)
							}
						} else if recordedSev.Rank() >= effectiveFailOn.Rank() {
							return BuildResult{}, fmt.Errorf("cached base image vulnerability audit from %s exceeds threshold %s (max severity: %s, %d vulnerabilities): %w",
								base.LastScannedAt, effectiveFailOn, base.MaxSeverity, base.VulnerabilitiesCount, ErrVulnerabilityThresholdExceeded)
						}
					} else if base.VulnerabilitiesCount > 0 {
						if req.AllowIncompleteScan {
							log.Warn("cached base image vulnerability audit recorded vulnerabilities but missing max severity; proceeding because --allow-incomplete was set", "vulns", base.VulnerabilitiesCount)
						} else {
							return BuildResult{}, fmt.Errorf("cached base image vulnerability audit from %s recorded %d vulnerabilities but missing max severity: %w", base.LastScannedAt, base.VulnerabilitiesCount, ErrScanIncomplete)
						}
					}
				}
			} else if failGateActive {
				if req.AllowIncompleteScan {
					log.Warn("base image vulnerability scan skipped: offline/hermetic build cannot reach the vulnerability database and no previous scan is recorded in pokkum.lock; proceeding because --allow-incomplete was set", "offline", req.BaseImage.Offline, "hermetic", req.Hermetic, "failOnCVE", effectiveFailOn)
				} else {
					return BuildResult{}, fmt.Errorf("base image vulnerability scan cannot run in offline/hermetic mode and no previous scan is recorded in pokkum.lock (fail-on-cve=%s): %w (pass --allow-incomplete to proceed without a scan, or drop --fail-on-cve for this build)", effectiveFailOn, ErrScanIncomplete)
				}
			} else {
				log.Debug("base image vulnerability scan skipped: offline/hermetic build cannot reach the vulnerability database", "offline", req.BaseImage.Offline, "hermetic", req.Hermetic)
			}
		} else {
			scanRes, scanErr := deps.Scanner.Scan(ctx, ports.ScanRequest{
				Target: base.PinnedRef,
				FailOn: effectiveFailOn,
				// Note: req.Normalize() ran at the top of Build, so
				// AppRuntime is never empty here — the scanner uses it to
				// key which embedded toolchain advisories can apply to the
				// image this build ships (a node image contains no Bun).
				AppRuntime:      string(req.AppRuntime),
				AllowIncomplete: req.AllowIncompleteScan,
				VEXExemptions:   req.VEXExemptions,
				// Real wall-clock time, not SOURCE_DATE_EPOCH — see
				// ports.VEXExemption.Expired's doc comment for why an
				// exemption's expiry is evaluated against actual current
				// time. This does not affect any image byte: it only
				// changes whether the build proceeds, exactly like the
				// live OSV.dev query this whole branch already depends on.
				Now: time.Now(),
			})

			// Record scan result in pokkum.lock if lockfile tracking is available.
			// Never on a dry run: this write is what created pokkum.lock in a
			// project that had none, from a command documented to perform no
			// writes.
			if deps.BaseImages != nil && !opts.DryRun {
				// req.BaseImage.Ref is passed alongside the preset because a
				// custom base's lockfile entry is keyed per reference, not per
				// preset — the same raw Ref handed to Resolve above.
				_ = deps.BaseImages.RecordScanResult(ctx, lockPath, req.BaseImage.Preset, req.BaseImage.Ref, scanRes)
			}

			if scanErr != nil {
				if errors.Is(scanErr, ErrVulnerabilityThresholdExceeded) {
					if failGateActive {
						return BuildResult{}, fmt.Errorf("base image vulnerability scan failed: %w", scanErr)
					}
					log.Warn("base image contains vulnerabilities exceeding threshold", "pinned", base.PinnedRef, "vulns", len(scanRes.Vulnerabilities), "maxSeverity", scanRes.MaxSeverityFound)
				} else if errors.Is(scanErr, ErrScanIncomplete) {
					if failGateActive && !req.AllowIncompleteScan {
						return BuildResult{}, fmt.Errorf("base image vulnerability scan incomplete: %w", scanErr)
					}
					log.Warn("base image vulnerability scan incomplete: vulnerability database lookup failed", "err", scanErr)
				} else {
					log.Debug("base image vulnerability scan warning", "err", scanErr)
				}
			} else if len(scanRes.Vulnerabilities) > 0 {
				log.Info("base image vulnerability scan passed", "pinned", base.PinnedRef, "vulns", len(scanRes.Vulnerabilities), "maxSeverity", scanRes.MaxSeverityFound)
			}

			if len(scanRes.ExemptedVulnerabilities) > 0 {
				seen := make(map[string]bool, len(scanRes.ExemptedVulnerabilities))
				var ids []string
				for _, v := range scanRes.ExemptedVulnerabilities {
					if !seen[v.ID] {
						seen[v.ID] = true
						ids = append(ids, v.ID)
					}
				}
				slices.Sort(ids)
				req.Runtime.VEXExemptions = ids
				log.Warn("VEX exemption(s) applied — these CVEs were excluded from the --fail-on-cve threshold", "cves", strings.Join(ids, ","))
			}
		}
	}

	// Pre-build source scan: earliest possible, best-located feedback for a
	// secret already sitting in the repo. This does NOT cover the shipped
	// artifact — see the post-build scan after Stage 5 (Prepare) below,
	// which is what actually gates what ships. Kept as an addition, not a
	// replacement: a source-level hit points a developer straight at the
	// offending source line, which the post-build scan (against
	// minified/bundled output) generally cannot do as precisely.
	if err := runSecretScan(ctx, deps, log, "pre-build source", req.ProjectDir, req.AllowSecretPatterns, false, req.ShowSecretValues); err != nil {
		return BuildResult{}, err
	}

	// Stage 4: --dry-run stops here, having touched nothing.
	if opts.DryRun {
		// Resolve above only resolved the digest/manifest (VerifySignature is
		// always false in baseReq now); verify the signature synchronously
		// here so a dry run still fails fast on a bad base image signature,
		// exactly as it did when Resolve verified inline.
		if !req.BaseImage.NoVerifyBase {
			if err := deps.BaseImages.VerifyBaseImage(ctx, base, baseReq); err != nil {
				return BuildResult{}, err
			}
		}
		// Same reasoning for native-module inspection: it used to run
		// unconditionally before this stage; keep that guarantee for dry
		// runs even though a real build now overlaps it with Prepare.
		if _, err := deps.NativeInspector.Inspect(ctx, req.ProjectDir, req.Platforms[0]); err != nil {
			return BuildResult{}, err
		}
		res := BuildResult{
			Image: ImageResult{
				Mode:          req.Output.Mode,
				Tags:          slices.Clone(req.Tags),
				TarballPath:   req.Output.TarballPath,
				OCILayoutPath: req.Output.OCILayoutPath,
				Platforms:     slices.Clone(req.Platforms),
				IsIndex:       len(req.Platforms) > 1,
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

	// Stage 4.4: --asset-overlay resolution.
	//
	// Runs unconditionally whenever the flag is set, regardless of whether
	// the remote build cache is in use (Stage 4.5, next) — the cache-key
	// hash below and the packaging stage after Stage 6 both need the SAME
	// resolved digest list, so resolving it exactly once here and threading
	// it through both is what keeps them from silently disagreeing about
	// what this build's overlay content actually is.
	//
	// Auto-discovery (walking the push target's own predecessor chain) only
	// makes sense for OutputPush: there is no "current tag at the target"
	// to inspect for --local/--tarball output. --asset-overlay-from's
	// explicit refs work regardless of output mode, since they name
	// arbitrary external images, not this build's own push target.
	var assetOverlayDigests []string
	var predecessorDigest string
	if req.Compile.AssetOverlayGenerations > 0 {
		switch {
		case len(req.Compile.AssetOverlayFrom) > 0:
			n := len(req.Compile.AssetOverlayFrom)
			if n > req.Compile.AssetOverlayGenerations {
				n = req.Compile.AssetOverlayGenerations
			}
			for _, ref := range req.Compile.AssetOverlayFrom[:n] {
				// digest is fully-qualified ("repo@sha256:...") — ref may
				// name a repository other than req.Repo. NOT eligible to
				// become predecessorDigest below: that annotation's own
				// contract is specifically "the digest of whatever THIS
				// push replaced at THIS build's own push target", which an
				// explicit --asset-overlay-from entry may not be at all.
				digest, err := deps.AssetOverlay.ResolveDigest(ctx, ref, req.RegistryConfigPath, req.Insecure)
				if err != nil {
					return BuildResult{}, err
				}
				assetOverlayDigests = append(assetOverlayDigests, digest)
			}
		case req.Output.Mode == OutputPush && req.Repo != "" && len(req.Tags) > 0:
			chain, err := deps.AssetOverlay.ResolvePredecessorChain(ctx, req.Repo, req.Tags[0], req.RegistryConfigPath, req.Insecure, req.Compile.AssetOverlayGenerations)
			if err != nil {
				return BuildResult{}, err
			}
			assetOverlayDigests = chain
			// The predecessor annotation is stamped ONLY on this
			// auto-discovery branch, never from an --asset-overlay-from
			// entry above: chain[0] (if present) is guaranteed to be
			// req.Repo's own current tag digest, matching what
			// ports.AnnotationPredecessor documents. An explicit
			// --asset-overlay-from ref makes no such guarantee — it may
			// name an unrelated image in a different repository entirely —
			// so stamping assetOverlayDigests[0] unconditionally (the prior
			// behavior) recorded false lineage whenever
			// --asset-overlay-from was used instead of auto-discovery.
			if len(chain) > 0 {
				predecessorDigest = chain[0]
			}
		default:
			log.Warn("--asset-overlay set but auto-discovery needs a registry push, which is the default; drop --local/--tarball, or pass --asset-overlay-from with explicit refs. No overlay will be added to this build", "outputMode", req.Output.Mode)
		}
		if len(assetOverlayDigests) > 0 {
			log.Info("asset overlay: resolved prior generations", "count", len(assetOverlayDigests), "digests", assetOverlayDigests)
		}
	}

	// Stage 4.5: Composite Remote OCI Input Caching check.
	//
	// Cache-poisoning mitigation and verification:
	// A candidate cache hit is validated against cryptographic signatures
	// (Cosign static-key or Sigstore keyless) before release tags are promoted.
	// For signed builds, cache hits are only permitted if signature verification
	// is active and succeeds, ensuring release tags are never promoted to
	// unverified digests. If verification fails or is missing, the cache hit
	// is rejected and the build falls through cleanly to compilation from source.
	var compositeInputHash string
	allowCache := deps.RemoteCache != nil && req.Output.Mode == OutputPush && !req.Compile.NoCache
	if req.Sign && (req.CacheVerify.VerifyMode == CacheVerifyNone || !req.CacheVerify.VerifySignature) {
		allowCache = false
	}
	// Cache-hit verification (default on) requires a <alg>-<hex>.sig on the
	// digest the cache tag resolves to. That signature is genuinely created
	// by this pipeline's own signing stage (after publish, below): the
	// cache-<hash> tag is appended to req.Tags and so points at the exact
	// digest the signing stage attaches .sig to — signing the pushed digest
	// IS signing what a future build's cache check resolves. So a builder
	// with a signing key gets the advertised sub-100ms verified cache-hit
	// path for real; a builder without one produces unsigned pushes whose
	// cache entries can never verify. Say so once, honestly, instead of
	// letting every future cache check silently miss.
	if allowCache && req.Sign && len(req.Signing.KeyPEM) == 0 {
		log.Info("remote cache: cache-hit verification is active but this build has no signing key, so it will not create a signed (promotable) cache entry itself; only cache entries pushed by a builder with a signing key can ever be verified and reused")
	}
	if allowCache {
		var pStrs []string
		for _, p := range req.Platforms {
			pStrs = append(pStrs, p.String())
		}
		inputHash, err := deps.RemoteCache.ComputeInputHash(ctx, ports.RemoteCacheInputRequest{
			ProjectDir:                req.ProjectDir,
			BaseImageDigest:           base.Digest.String(),
			AppRuntime:                string(req.AppRuntime),
			BunVersion:                toolchain.BunVersion,
			BunVariant:                string(req.BunRuntime.Variant),
			BunCustomBinaryPath:       req.BunRuntime.CustomBinaryPath,
			StubLauncher:              req.BunRuntime.StubLauncher,
			Platforms:                 pStrs,
			Strategy:                  string(req.Compile.Strategy),
			Compression:               string(req.Compile.Compression),
			NoPrune:                   req.Compile.NoPrune,
			KeepVendor:                slices.Clone(req.Compile.KeepVendor),
			NoPrecompress:             req.Compile.NoPrecompress,
			NoStrip:                   req.Compile.NoStrip,
			NoInject:                  req.Compile.NoInject,
			NoMinify:                  req.Compile.NoMinify,
			MinBunVersion:             req.Compile.MinBunVersion,
			CompileEnv:                slices.Clone(req.Compile.Env),
			Sourcemap:                 req.Compile.Sourcemap,
			Hermetic:                  req.Hermetic,
			SourceDateEpochUnix:       req.SourceDateEpoch.Unix(),
			Runtime:                   req.Runtime,
			Telemetry:                 req.Telemetry,
			Labels:                    req.Labels,
			Annotations:               req.Annotations,
			SBOMFormat:                string(req.SBOM.Format),
			SBOMAttachMode:            string(req.SBOM.AttachMode),
			SBOMNoAttach:              req.SBOM.NoAttach,
			AssetOverlaySourceDigests: slices.Clone(assetOverlayDigests),
		})
		if err == nil && inputHash != "" {
			compositeInputHash = inputHash
			// Offer this build's own signing public key as the LAST-RESORT
			// entry in the cache-verify key chain
			// (--cache-verify-key / POKKUM_CACHE_PUBKEY /
			// POKKUM_SIGNING_PUBKEY / POKKUM_BASE_IMAGE_PUBKEY, resolved in
			// that order inside the cacher). It is offered, not imposed:
			// every explicit source above wins, and the cacher only reaches
			// this field once all of them are empty — so configuring
			// --cache-verify-key with a deliberately different key is never
			// silently overridden by a locally derived one.
			//
			// Why deriving it implicitly is sound: the static-key arm accepts
			// a candidate only when its Simple Signing payload verifies
			// against this one key, and the mode itself is chosen from
			// KeylessIdentity, never from any key field. So falling back to
			// the operator's OWN signing public key means "trust only cache
			// entries I signed myself", which strictly NARROWS the accepted
			// set relative to any third-party key and strictly cannot widen
			// it: the status quo without this fallback is that no key exists,
			// every candidate is refused, and the build falls through to a
			// full rebuild. Verified against
			// internal/adapters/remotecacheutils' static-key arm, which has
			// no path accepting a signature from a key other than the one
			// handed to it.
			//
			// Empty whenever no signing key is configured, which leaves that
			// case behaving exactly as it did before.
			cacheVerify := req.CacheVerify
			cacheVerify.SigningPublicKeyPEM = req.Signing.PublicKeyPEM
			cacheRes, err := deps.RemoteCache.Check(ctx, ports.RemoteCacheRequest{
				Repo:               req.Repo,
				InputHash:          inputHash,
				Tags:               req.Tags,
				Insecure:           req.Insecure,
				UserAgent:          deps.UserAgent,
				RegistryConfigPath: req.RegistryConfigPath,
				Verify:             cacheVerify,
			})
			if err == nil && cacheRes.Hit {
				if cacheRes.Verified {
					log.Info("remote input cache hit; signature verified; build skipped", "repo", req.Repo, "digest", cacheRes.Digest.String(), "inputHash", inputHash, "signer", cacheRes.SignerIdentity)
				} else {
					log.Info("remote input cache hit; build skipped", "repo", req.Repo, "digest", cacheRes.Digest.String(), "inputHash", inputHash)
				}
				// Auditable disclosure of the accepted security tradeoff: a
				// confirmed cache hit short-circuits before Stage 5, so base-image
				// signature verification does NOT run on the hit path. Nothing is
				// built from the base image on a hit and the cache key already
				// binds the base image digest (base.Digest, pinned via pokkum.lock),
				// so the hit can only match the exact base the verifier would have
				// checked. Log it explicitly so CI/operators can see the skip and
				// the residual trust model instead of it being silently invisible.
				baseVerifySkipped := "cache hit; base image signature verification skipped (not built from base)"
				if req.BaseImage.NoVerifyBase {
					baseVerifySkipped = "base image signature verification already disabled via --no-verify-base"
				}
				log.Info(baseVerifySkipped, "repo", req.Repo, "baseRef", base.Ref, "baseDigest", base.Digest.String())
				res := BuildResult{
					Image: ImageResult{
						Mode:      req.Output.Mode,
						Ref:       cacheRes.Ref,
						Digest:    cacheRes.Digest,
						Tags:      slices.Clone(cacheRes.Tags),
						Platforms: slices.Clone(req.Platforms),
						IsIndex:   len(req.Platforms) > 1,
						Cached:    true,
					},
					Cached:          true,
					BaseImage:       baseInfo,
					Toolchain:       toolchain,
					SourceDateEpoch: req.SourceDateEpoch,
					Duration:        time.Since(started),
				}
				// A signing-enabled cache hit only happens with cache-hit
				// signature verification active (allowCache above), so the
				// promoted digest's .sig was just cryptographically verified
				// — record that instead of leaving Signing nil, which would
				// read as "signing never happened" for an image that is in
				// fact signed.
				if req.Sign {
					res.Signing = &SigningResult{Signed: cacheRes.Verified}
					if !cacheRes.Verified {
						res.Signing.Reason = "cache hit promoted without signature verification"
					}
				}
				if _, err := fmt.Fprintln(deps.stdout(), cacheRes.Ref); err != nil {
					return res, fmt.Errorf("writing output reference: %w", err)
				}
				return res, nil
			}
		}
	}

	if compositeInputHash != "" {
		if req.Annotations == nil {
			req.Annotations = make(map[string]string)
		}
		req.Annotations["pokkum.dev/build-input-hash"] = compositeInputHash
		req.Tags = append(req.Tags, "cache-"+compositeInputHash)
	}

	// Build the merged asset-overlay content now — never earlier: a cache
	// hit above returns before this point, and pulling N generations' full
	// client layer content is real network/CPU cost not worth spending on a
	// build that's about to be skipped entirely.
	var assetOverlayDir string
	if len(assetOverlayDigests) > 0 {
		dir, err := deps.AssetOverlay.BuildOverlayDir(ctx, req.Repo, assetOverlayDigests, req.RegistryConfigPath, req.Insecure)
		if err != nil {
			return BuildResult{}, err
		}
		assetOverlayDir = dir
		if assetOverlayDir != "" {
			defer os.RemoveAll(assetOverlayDir)
		}
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

	// Stage 5: the SvelteKit build, running concurrently with base-image
	// signature verification and native-module inspection — neither of
	// those checks has anything left to gate once the remote-cache check
	// above has already returned (a cache hit never reaches this point, and
	// a cache miss needs both checks to pass before the build can be
	// published, but not before Prepare itself can start). A failure in
	// either concurrent check cancels the in-flight Prepare via ctx and
	// fails the whole build before fanOut is ever reached. Like fanOut
	// below, this is first-error-wins via errgroup, not errors.Join — if
	// Prepare and VerifyBaseImage fail at genuinely overlapping times, only
	// one error surfaces and which one is not deterministic.
	g, gctx := errgroup.WithContext(ctx)

	var prep ports.PrepareResult
	g.Go(func() error {
		p, err := deps.Compiler.Prepare(gctx, ports.PrepareRequest{
			Strategy:               req.Compile.Strategy,
			ExcludeRoutes:          slices.Clone(req.ExcludeRoutes),
			ProjectDir:             req.ProjectDir,
			SourceDateEpoch:        req.SourceDateEpoch,
			Env:                    req.Compile.Env,
			Platforms:              slices.Clone(req.Platforms),
			NoInject:               req.Compile.NoInject,
			Hermetic:               req.Hermetic,
			HermeticMountIsolation: req.HermeticMountIsolation,
			NoPrune:                req.Compile.NoPrune,
			KeepVendor:             slices.Clone(req.Compile.KeepVendor),
			NoPrecompress:          req.Compile.NoPrecompress,
			NoStrip:                req.Compile.NoStrip,
			Telemetry:              req.Telemetry,
		})
		if err != nil {
			return err
		}
		prep = p
		return nil
	})

	if !req.BaseImage.NoVerifyBase {
		g.Go(func() error {
			return deps.BaseImages.VerifyBaseImage(gctx, base, baseReq)
		})
	}

	// Native inspection walks req.ProjectDir, which overlaps
	// ProjectDir/.svelte-kit and ProjectDir/build — the exact trees Prepare
	// is concurrently writing into. Today this is inert in production
	// (NewClosuredAdapter's Inspect never returns an error; the result is
	// discarded), but a StrictNativeAdapter wiring that fails on
	// HasUnsupportedDynamicImports could see a nondeterministic verdict
	// depending on how far Prepare has gotten. Not fixed here — flagging so
	// it isn't rediscovered as a mystery flake.
	g.Go(func() error {
		_, err := deps.NativeInspector.Inspect(gctx, req.ProjectDir, req.Platforms[0])
		return err
	})

	if err := g.Wait(); err != nil {
		return BuildResult{}, err
	}
	log.Info("sveltekit build complete", "entrypoint", prep.EntrypointPath)
	log.Info("native inspector ok")
	if !req.BaseImage.NoVerifyBase {
		log.Info("base image signature verified", "ref", baseInfo.Ref, "pinned", baseInfo.PinnedRef)
	}

	// Stage 5.5: post-build secret scan of the compiler's OUTPUT tree —
	// the bytes that actually get packaged into the image — not just the
	// pre-build source tree scanned above. $env/static/* baking, a Vite
	// `define` replacement, and anything a compromised build-time
	// dependency writes into build/server/chunks/*.js only produce their
	// final, shipped bytes here, after Prepare; none of it exists yet at
	// the pre-build scan point above.
	//
	// prep.OutputDir is the SAME shared build output every strategy starts
	// from (see the per-strategy pkgReq wiring inside fanOut, a few dozen
	// lines below, which postBuildScanDirs deliberately mirrors):
	//   - StrategyLayered ships prep.OutputDir in full (the server
	//     entrypoint and its sibling files, plus client/vendor/native/
	//     prerendered) — scanning the whole tree matches exactly what
	//     ships.
	//   - StrategyStatic ships only OutputDir/client and
	//     OutputDir/prerendered (no server component at all) — scanning
	//     just those two avoids false "coverage" of files this strategy
	//     never packages.
	//   - StrategyExe's ACTUAL shipped artifact is the single native
	//     executable Compile produces below, by bundling
	//     prep.EntrypointPath via `bun build --compile`. Its full
	//     pre-compile bundled-JS input is therefore prep.OutputDir AND
	//     the entrypoint's own directory, which is NOT always inside
	//     prep.OutputDir: with --telemetry it is
	//     <projectDir>/.pokkum/telemetry-entry.ts plus the generated
	//     .pokkum/otel-bootstrap.ts it imports, both written by Prepare
	//     (so absent at the pre-build source scan above) and both bundled
	//     into the shipped binary. postBuildScanDirs returns both trees;
	//     scanning only prep.OutputDir used to leave that bundled JS
	//     uncovered at both stages.
	//
	//     Scanning that input remains a deliberate best-effort PROXY, not
	//     parity with layered/static: it catches everything already baked
	//     into the JS by Prepare — every example in this feature's bug
	//     report (env baking, Vite define, a malicious dependency's
	//     chunk) — but NOT anything a bunfig.toml preload plugin or a
	//     `with { type: "macro" }` import could inject only during the
	//     Compile step itself. Scanning the compiled executable instead
	//     was considered and rejected: it is a single, non-line-oriented,
	//     size-unbounded binary blob whose embedded string constants may
	//     be transformed or split, so it risks both false negatives and
	//     the kind of noisy false positives that get a secret scanner
	//     switched off entirely. That residual gap for StrategyExe is
	//     accepted and reported honestly, not silently implied covered.
	//
	// --asset-overlay's merged prior-generation content (assetOverlayDir,
	// StrategyLayered only today) is pulled from a registry — either this
	// build's own predecessor chain, or, via --asset-overlay-from,
	// arbitrary caller-named images — and gets packaged into this build's
	// own image as its own layer. It is scanned here too: re-shipping
	// unscanned third-party registry content defeats the point of this
	// gate exactly as much as skipping the local build output would.
	if err := checkCtx(ctx, "post-build secret scan"); err != nil {
		return BuildResult{}, err
	}
	for _, dir := range postBuildScanDirs(req.Compile.Strategy, prep.OutputDir, prep.EntrypointPath) {
		if err := runSecretScan(ctx, deps, log, "post-build output", dir, req.AllowSecretPatterns, req.Compile.Sourcemap, req.ShowSecretValues); err != nil {
			return BuildResult{}, err
		}
	}
	if err := runSecretScan(ctx, deps, log, "post-build asset-overlay", assetOverlayDir, req.AllowSecretPatterns, req.Compile.Sourcemap, req.ShowSecretValues); err != nil {
		return BuildResult{}, err
	}

	if err := checkCtx(ctx, "compile"); err != nil {
		return BuildResult{}, err
	}

	// Resolve the embedded Bun runtime once, ahead of the per-platform fan-out
	// below, so its version/hash are available to attach to the SBOM and SLSA
	// provenance — both single documents describing the whole build, not
	// per-platform. Deliberately distinct from toolchain.BunVersion (the
	// HOST's compiler bun, from Preflight): this is the actual runtime
	// artifact that gets embedded in the image, which is what a dependency
	// descriptor in an SBOM/SLSA statement needs to name correctly, and the
	// two commonly differ (a developer's local bun and the pinned runtime
	// version are unrelated unless they happen to match).
	//
	// Resolving req.Platforms[0] here and then again inside fanOut's own
	// per-platform loop is intentional, not wasted work: the second call is a
	// cache hit (Resolver's own on-disk cache), and this keeps fanOut's
	// concurrent per-platform logic completely untouched rather than special
	// -casing one platform's goroutine to reuse a pre-fetched result.
	// Gated on RuntimeBun as well as the strategy: a --runtime=node image
	// embeds no Bun runtime at all — nothing to resolve, and an SBOM/SLSA
	// bun component naming an embedded runtime that isn't there would be a
	// false claim. The SLSA statement still records the HOST bun (the build
	// tool, from Preflight) via slsaGeneratorRequest's firstNonEmpty
	// fallback, which is the honest dependency for a node build.
	var bunToolchain ports.BunResolverResult
	if req.Compile.Strategy == StrategyLayered && req.AppRuntime == ports.RuntimeBun && deps.BunRuntime != nil && len(req.Platforms) > 0 {
		bunToolchain, err = deps.BunRuntime.Resolve(ctx, ports.BunResolverRequest{
			Platform:         req.Platforms[0],
			Version:          req.BunRuntime.Version,
			Variant:          req.BunRuntime.Variant,
			CustomBinaryPath: req.BunRuntime.CustomBinaryPath,
			StubLauncher:     req.BunRuntime.StubLauncher,
			SourceDateEpoch:  req.SourceDateEpoch,
			Offline:          req.Hermetic,
		})
		if err != nil {
			return BuildResult{}, fmt.Errorf("core: resolve bun runtime for sbom/provenance: %w", err)
		}
	}

	// Route exclusions run here: after Prepare's errgroup has been waited on
	// (so nothing fallible sits between a dispatch and its Wait) and before
	// the packager reads the tree. Deleting from .svelte-kit/output touches
	// generated build output only — never user-authored source — which is the
	// same tree Prepare already flattens.
	if len(req.ExcludeRoutes) > 0 && deps.RouteFilter != nil {
		if err := applyRouteExclusions(ctx, deps, req, prep); err != nil {
			return BuildResult{}, err
		}
	}

	// Stage 6: the parallel section.
	built, doc, err := fanOut(ctx, deps, req, base, prep, workDir, imageLabels(req, baseInfo, toolchain, bunToolchain), bunToolchain, predecessorDigest, assetOverlayDigests, assetOverlayDir)
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
		// The index has no config/labels of its own to mirror
		// pokkum.dev/predecessor and pokkum.dev/asset-overlay-sources from
		// the way each per-platform image's own manifest annotations
		// already get them (via mergeLabels/imageAnnotations) — Index()
		// only ever writes req.Annotations verbatim. Without this explicit
		// merge, a multi-platform push (Pokkum's default: linux/amd64 +
		// linux/arm64) would leave assetoverlay.ResolvePredecessorChain
		// unable to find this generation via <repo>:<tag>, which resolves
		// to the INDEX, not a per-platform child.
		indexAnnotations := req.Annotations
		if predecessorDigest != "" || len(assetOverlayDigests) > 0 {
			indexAnnotations = make(map[string]string, len(req.Annotations)+2)
			maps.Copy(indexAnnotations, req.Annotations)
			if predecessorDigest != "" {
				indexAnnotations[ports.AnnotationPredecessor] = predecessorDigest
			}
			if len(assetOverlayDigests) > 0 {
				sources := slices.Clone(assetOverlayDigests)
				slices.Sort(sources)
				indexAnnotations[ports.AnnotationAssetOverlaySources] = strings.Join(sources, ",")
			}
		}
		idx, err := deps.Packager.Index(ctx, ports.IndexRequest{
			Images:      images,
			Annotations: indexAnnotations,
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
			Mode:          req.Output.Mode,
			Tags:          slices.Clone(req.Tags),
			TarballPath:   req.Output.TarballPath,
			OCILayoutPath: req.Output.OCILayoutPath,
			Platforms:     slices.Clone(req.Platforms),
			IsIndex:       multi,
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
			Repo:               req.Repo,
			Subject:            pub.Digest,
			Document:           *doc,
			AttachMode:         req.SBOM.AttachMode,
			Insecure:           req.Insecure,
			RegistryConfigPath: req.RegistryConfigPath,
		})
		if err != nil {
			return result, fmt.Errorf("core: attach sbom to %s (the image itself was pushed; set SBOM.NoAttach to skip this step): %w", pub.Ref, err)
		}
		result.SBOM.Ref = att.Ref
		log.Info("sbom attached", "ref", att.Ref, "format", doc.Format, "packages", doc.PackageCount)
	} else if doc != nil {
		log.Info("sbom generated but not attached", "mode", req.Output.Mode, "noAttach", req.SBOM.NoAttach)
	}

	// Stage 10.5: signing. Runs only for push mode — a signature is a
	// registry artifact keyed to the pushed digest, and the non-push case was
	// already warned about loudly at build start. Signing happens strictly
	// after publish and never touches the image payload itself: signatures
	// are inherently non-deterministic (ECDSA randomness), so they must live
	// in separate .sig/.att artifacts, keeping the image bytes and digest
	// bit-for-bit reproducible.
	if req.Sign && req.Output.Mode == OutputPush {
		if err := checkCtx(ctx, "signing"); err != nil {
			return result, err
		}
		if len(req.Signing.KeyPEM) == 0 {
			// Absence must be impossible to mistake for success: warn in
			// capitals, record the state in the result, and remind the
			// caller of the CI gate. Signing.Require never reaches this
			// branch — Validate already rejected a Require request with no
			// key before anything was built.
			log.Warn("IMAGE NOT SIGNED: signing is enabled but no signing key is available — the pushed image carries no signature or provenance attestation; set POKKUM_SIGNING_KEY or --signing-key, or pass --require-signed to make this an error", "ref", pub.Ref)
			result.Signing = &SigningResult{Signed: false, Reason: "no signing key available"}
		} else {
			sigRes, err := signAndSelfVerify(ctx, deps, req, base, toolchain, bunToolchain, pub, built, multi, doc)
			if err != nil {
				// The image itself was pushed; the build still fails —
				// success would claim a signed image that isn't.
				return result, err
			}
			result.Signing = sigRes
			log.Info("image signed and self-verified",
				"ref", pub.Ref,
				"signatures", strings.Join(sigRes.SignatureRefs, ","),
				"attestations", strings.Join(sigRes.AttestationRefs, ","))
		}
	} else if req.Sign {
		result.Signing = &SigningResult{Signed: false, Reason: "output mode " + req.Output.Mode.String() + " has no registry to attach a signature to"}
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

// firstNonEmpty returns a, or b if a is empty.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// signingSubjects lists every digest the .sig/.att attachments cover: the
// published digest first (the index for a multi-platform build, the sole
// image manifest otherwise), then each per-platform manifest digest.
//
// Attaching to BOTH the index and the per-platform manifests is deliberate
// (Roadmap Tier 2 item 2d): verifiers disagree about which digest a policy
// check resolves — `cosign verify` and `pokkum verify` build the .sig/.att
// tag from whatever digest the given ref resolves to (the index for a tag
// ref, a manifest for a platform-pinned digest ref), while admission
// controllers commonly resolve a tag down to the per-platform child before
// checking. Signing both means either resolution path finds a valid
// signature whose payload names the exact digest it was asked about.
// `pokkum verify` therefore checks whichever digest its argument resolves
// to; both are now covered.
func signingSubjects(pub ports.PublishResult, built []platformBuild, multi bool) ([]v1.Hash, error) {
	subjects := []v1.Hash{pub.Digest}
	if multi {
		for _, b := range built {
			d, err := b.image.Digest()
			if err != nil {
				return nil, fmt.Errorf("core: read per-platform manifest digest for signing: %w: %w", err, ErrSigningFailed)
			}
			subjects = append(subjects, d)
		}
	}
	return subjects, nil
}

// slsaGeneratorRequest builds the provenance request for one subject digest.
// It is called once per signing subject so that each attached attestation's
// statement names exactly the digest it is attached to — a statement whose
// subject is only the index digest would fail subject cross-checks when
// fetched via a per-platform manifest's .att tag.
func slsaGeneratorRequest(deps Deps, req BuildRequest, base *ports.BaseImage, toolchain Toolchain, bunToolchain ports.BunResolverResult, subject v1.Hash) ports.SLSAGeneratorRequest {
	// GitRepo/GitCommit are deliberately left unset here rather than sourced
	// from req.Labels: BuildRequest has no dedicated field for them, and
	// req.Labels (org.opencontainers.image.source/.revision) is
	// user-overridable via --image-label, which would let a CLI flag inject
	// arbitrary "measured" source data into a cryptographically signed
	// statement. ProjectDir is passed through unchanged, and
	// slsa.Generator's own gitdiscovery.go resolves the real commit/repo by
	// shelling to git against it directly — a genuine measurement of the
	// actual working tree at build time, not an assertion accepted from a
	// flag. See ports.SLSAGeneratorRequest.GitRepo/.GitCommit's "optional/
	// discovered" doc comment.
	return ports.SLSAGeneratorRequest{
		ProjectDir: req.ProjectDir,
		Repo:       req.Repo,
		Tags:       req.Tags,
		Platforms:  req.Platforms,
		OutputMode: req.Output.Mode.String(),
		BaseImage: ports.SLSABaseImage{
			Preset:    req.BaseImage.Preset,
			Ref:       base.Ref,
			PinnedRef: base.PinnedRef,
			Digest:    base.Digest,
		},
		OutputDigest: subject,
		Toolchain: ports.SLSAToolchain{
			PokkumVersion: deps.Version,
			GoVersion:     runtime.Version(),
			BuilderOSArch: runtime.GOOS + "/" + runtime.GOARCH,
			// The image's application runtime is an external parameter a
			// verifier must replay (--runtime) to reproduce this build —
			// recorded unconditionally, bun included, since an explicit
			// value in a signed statement beats "absent means default".
			AppRuntime: string(req.AppRuntime),
			// bunToolchain is the resolved embedded-runtime artifact
			// (StrategyLayered only) — the correct thing for a dependency
			// descriptor to name. For strategies with no resolved runtime
			// (exe, static), fall back to toolchain.BunVersion — the host
			// compiler's bun.
			BunVersion:          firstNonEmpty(bunToolchain.Version, toolchain.BunVersion),
			BunBinaryHash:       bunToolchain.SHA256,
			SupervisorVersion:   toolchain.SupervisorVersion,
			StaticServerVersion: toolchain.StaticServerVersion,
		},
		SourceDateEpoch:     req.SourceDateEpoch,
		Hermetic:            req.Hermetic,
		HermeticEnforcement: hermeticEnforcementMode(req.Hermetic),
	}
}

// signAndSelfVerify is the real signing stage: for every subject digest it
// generates the SLSA statement, DSSE-signs it, attaches it as .att, builds
// and signs the Simple Signing payload, attaches it as .sig — and then
// SELF-VERIFIES: it fetches each attachment back from the registry and
// cryptographically verifies it against the signing key's public half before
// the build may report success. The read-back is what makes a broken attach
// path unshippable — a signature that was "attached" but cannot be fetched
// and verified from the registry fails the build here, not in a user's
// `cosign verify` weeks later.
//
// Any error fails the build even though the image itself is already pushed:
// the caller's error message says so, and an unsigned-but-pushed image with a
// failed build is a recoverable state, while a "successful" build with a
// silently missing signature is the exact defect this stage exists to
// prevent.
// signSBOMStatement wraps the generated SBOM document in an in-toto Statement
// whose subject is the image digest, and DSSE-signs it with the build's
// signing key.
//
// Binding the subject is the whole point: the SBOM used to be attached as a
// bare blob under the .sbom tag with nothing tying it to the image, so it
// could be replaced wholesale without any verification path noticing. A signed
// statement naming the digest cannot be swapped for another image's SBOM, and
// cannot be edited at all without breaking the signature.
func signSBOMStatement(
	ctx context.Context,
	deps Deps,
	req BuildRequest,
	subject v1.Hash,
	doc *ports.SBOMDocument,
) (ports.DSSEEnvelope, error) {
	if !json.Valid(doc.Content) {
		// Embedding non-JSON as a predicate would produce a statement no
		// verifier can parse, and it would be signed — an authentic-looking
		// artifact carrying garbage is worse than no artifact.
		return ports.DSSEEnvelope{}, fmt.Errorf("core: SBOM document for %s is not valid JSON, refusing to sign it as an attestation: %w", subject, ErrSigningFailed)
	}

	stmt := ports.SBOMStatement{
		Type: ports.InTotoStatementTypeV1,
		Subject: []ports.ResourceDescriptor{{
			Name:   req.Repo,
			Digest: map[string]string{"sha256": subject.Hex},
		}},
		PredicateType: ports.SBOMPredicateType(doc.Format),
		Predicate:     json.RawMessage(doc.Content),
	}
	stmtJSON, err := json.Marshal(stmt)
	if err != nil {
		return ports.DSSEEnvelope{}, fmt.Errorf("core: marshal SBOM statement for %s: %w: %w", subject, err, ErrSigningFailed)
	}
	env, err := deps.DSSESigner.Sign(ctx, ports.DSSESignRequest{
		PayloadBytes: stmtJSON,
		PayloadType:  ports.InTotoPayloadType,
		KeyPEM:       req.Signing.KeyPEM,
	})
	if err != nil {
		return ports.DSSEEnvelope{}, fmt.Errorf("core: DSSE-sign SBOM statement for %s: %w: %w", subject, err, ErrSigningFailed)
	}
	deps.logger().Info("sbom attestation signed",
		"subject", subject.String(), "predicateType", stmt.PredicateType, "packages", doc.PackageCount)
	return env, nil
}

func signAndSelfVerify(
	ctx context.Context,
	deps Deps,
	req BuildRequest,
	base *ports.BaseImage,
	toolchain Toolchain,
	bunToolchain ports.BunResolverResult,
	pub ports.PublishResult,
	built []platformBuild,
	multi bool,
	sbomDoc *ports.SBOMDocument,
) (*SigningResult, error) {
	log := deps.logger()

	subjects, err := signingSubjects(pub, built, multi)
	if err != nil {
		return nil, err
	}

	res := &SigningResult{}
	for _, subject := range subjects {
		stmt, err := deps.SLSAGenerator.Generate(ctx, slsaGeneratorRequest(deps, req, base, toolchain, bunToolchain, subject))
		if err != nil {
			return nil, fmt.Errorf("core: generate SLSA provenance for %s: %w: %w", subject, err, ErrSigningFailed)
		}
		if len(stmt.Subject) == 0 {
			// A statement with no subject names nothing — attaching it would
			// be attaching noise that verifiers reject later.
			return nil, fmt.Errorf("core: SLSA provenance statement for %s has no subject: %w", subject, ErrSigningFailed)
		}
		stmtJSON, err := json.Marshal(stmt)
		if err != nil {
			return nil, fmt.Errorf("core: marshal SLSA statement for %s: %w: %w", subject, err, ErrSigningFailed)
		}

		env, err := deps.DSSESigner.Sign(ctx, ports.DSSESignRequest{
			PayloadBytes: stmtJSON,
			PayloadType:  ports.InTotoPayloadType,
			KeyPEM:       req.Signing.KeyPEM,
		})
		if err != nil {
			return nil, fmt.Errorf("core: DSSE-sign SLSA statement for %s: %w: %w", subject, err, ErrSigningFailed)
		}
		// Sign the SBOM as a second in-toto attestation bound to this same
		// subject digest. It rides in the same .att attachment as an extra
		// layer, which is cosign's convention for multiple attestations, so
		// `cosign verify-attestation --type spdxjson` resolves it without
		// Pokkum in the loop. The provenance envelope stays layer 0.
		var extraEnvelopes []ports.DSSEEnvelope
		if sbomDoc != nil && len(sbomDoc.Content) > 0 {
			sbomEnv, serr := signSBOMStatement(ctx, deps, req, subject, sbomDoc)
			if serr != nil {
				return nil, serr
			}
			extraEnvelopes = append(extraEnvelopes, sbomEnv)
		}

		attRes, err := deps.Registry.AttachAttestation(ctx, ports.AttachAttestationRequest{
			Repo:                req.Repo,
			Subject:             subject,
			Envelope:            env,
			AdditionalEnvelopes: extraEnvelopes,
			Insecure:            req.Insecure,
			RegistryConfigPath:  req.RegistryConfigPath,
		})
		if err != nil {
			return nil, fmt.Errorf("core: attach attestation for %s (the image itself was pushed but is NOT fully signed): %w", subject, err)
		}

		bundle, err := deps.CosignSigner.Sign(ctx, ports.CosignSignRequest{
			Repo:            req.Repo,
			Digest:          subject,
			KeyPEM:          req.Signing.KeyPEM,
			Creator:         strings.TrimSpace("pokkum " + deps.Version),
			SourceDateEpoch: req.SourceDateEpoch,
		})
		if err != nil {
			return nil, fmt.Errorf("core: sign image digest %s: %w: %w", subject, err, ErrSigningFailed)
		}
		sigRes, err := deps.Registry.AttachSignature(ctx, ports.AttachSignatureRequest{
			Repo:               req.Repo,
			Subject:            subject,
			Bundle:             bundle,
			Insecure:           req.Insecure,
			RegistryConfigPath: req.RegistryConfigPath,
		})
		if err != nil {
			return nil, fmt.Errorf("core: attach signature for %s (the image itself was pushed but is NOT signed): %w", subject, err)
		}

		res.AttestationRefs = append(res.AttestationRefs, attRes.Ref)
		res.SignatureRefs = append(res.SignatureRefs, sigRes.Ref)
		log.Info("signed subject", "subject", subject.String(), "sig", sigRes.Ref, "att", attRes.Ref)
	}

	// Post-push self-verification: prove the registry serves back what a
	// verifier will actually fetch, and that it verifies against this
	// build's own public key. This is a real pipeline stage, not a test.
	for _, subject := range subjects {
		fetchReq := ports.FetchAttachmentRequest{
			Repo:               req.Repo,
			Subject:            subject,
			Insecure:           req.Insecure,
			RegistryConfigPath: req.RegistryConfigPath,
		}

		fetched, err := deps.Registry.FetchSignature(ctx, fetchReq)
		if err != nil {
			return nil, fmt.Errorf("core: self-verify: fetch signature for %s back from registry: %w: %w", subject, err, ErrSignatureSelfVerifyFailed)
		}
		if err := deps.CosignSigner.Verify(ctx, fetched, req.Signing.PublicKeyPEM, req.Repo, subject); err != nil {
			return nil, fmt.Errorf("core: self-verify: fetched signature for %s does not verify: %w: %w", subject, err, ErrSignatureSelfVerifyFailed)
		}

		envFetched, err := deps.Registry.FetchAttestation(ctx, fetchReq)
		if err != nil {
			return nil, fmt.Errorf("core: self-verify: fetch attestation for %s back from registry: %w: %w", subject, err, ErrSignatureSelfVerifyFailed)
		}
		payload, err := deps.DSSESigner.Verify(ctx, envFetched, req.Signing.PublicKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("core: self-verify: fetched attestation for %s does not verify: %w: %w", subject, err, ErrSignatureSelfVerifyFailed)
		}
		var fetchedStmt ports.SLSAStatement
		if err := json.Unmarshal(payload, &fetchedStmt); err != nil {
			return nil, fmt.Errorf("core: self-verify: fetched attestation payload for %s is not a SLSA statement: %w: %w", subject, err, ErrSignatureSelfVerifyFailed)
		}
		if !statementNamesDigest(fetchedStmt, subject) {
			return nil, fmt.Errorf("core: self-verify: fetched attestation for %s names a different subject digest: %w", subject, ErrSignatureSelfVerifyFailed)
		}
	}

	res.Signed = true
	return res, nil
}

// statementNamesDigest reports whether stmt's subject list names d — the
// cross-check the self-verification stage runs on a fetched attestation, so
// a statement attached under the wrong digest's tag cannot pass.
func statementNamesDigest(stmt ports.SLSAStatement, d v1.Hash) bool {
	for _, s := range stmt.Subject {
		if s.Digest != nil && s.Digest[d.Algorithm] == d.Hex {
			return true
		}
	}
	return false
}

// hermeticEnforcementMode reports how a hermetic build was actually enforced
// for the platform Build is running on, for honest SLSA provenance (see
// ports.SLSAGeneratorRequest.HermeticEnforcement's doc comment for why this
// must not just echo the hermetic bool back). This process spawns the bun
// subprocess directly — Pokkum does not do remote/distributed builds — so
// runtime.GOOS here is genuinely the OS the subprocess ran under, not a
// guess about some other machine. It is safe to derive this purely from
// GOOS and the hermetic flag, with no separate signal needed from the
// bunexec adapter: bunexec's own Prepare/Compile fail the whole build
// closed if kernel-level enforcement was expected but could not actually be
// applied (see hermetic_linux.go's verifyHermeticSandboxApplied and the
// Start-failure error paths) — so if Build reaches this point successfully
// with req.Hermetic true on Linux, enforcement genuinely happened.
func hermeticEnforcementMode(hermetic bool) string {
	if !hermetic {
		return ""
	}
	if runtime.GOOS == "linux" {
		return "kernel-enforced-netns"
	}
	return "advisory-env-only"
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
//
// bunToolchain is pre-resolved by the caller (Build), not by this function:
// the SBOM scan needs it immediately, with no dependency on any platform's
// own per-platform resolve completing, and threading a live resolve through
// this goroutine's own concurrency would couple two things (SBOM generation
// and Bun resolution) that this function's own doc comment above says are
// deliberately independent. Zero value (StrategyExe/static, or
// deps.BunRuntime == nil) is fine — Generate treats an empty BunVersion as
// "no Bun component" rather than an error.
func fanOut(
	ctx context.Context,
	deps Deps,
	req BuildRequest,
	base *ports.BaseImage,
	prep ports.PrepareResult,
	workDir string,
	labels map[string]string,
	bunToolchain ports.BunResolverResult,
	predecessorDigest string,
	assetOverlayDigests []string,
	assetOverlayDir string,
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

			switch req.Compile.Strategy {
			case StrategyLayered:
				// Only the Bun runtime is Pokkum-embedded; --runtime=node
				// images get their runtime from the base image itself
				// (ports.NodeBinaryPath), so there is nothing to resolve —
				// and resolving anyway would gate a node build on Bun
				// release infrastructure it doesn't depend on.
				if req.AppRuntime == ports.RuntimeBun {
					if deps.BunRuntime == nil {
						return fmt.Errorf("core: bun runtime resolver unavailable for layered strategy: %w", ErrPackageFailed)
					}
					res, err := deps.BunRuntime.Resolve(gctx, ports.BunResolverRequest{
						Platform:         p,
						Version:          req.BunRuntime.Version,
						Variant:          req.BunRuntime.Variant,
						CustomBinaryPath: req.BunRuntime.CustomBinaryPath,
						StubLauncher:     req.BunRuntime.StubLauncher,
						Offline:          req.Hermetic,
						SourceDateEpoch:  req.SourceDateEpoch,
					})
					if err != nil {
						return fmt.Errorf("core: resolve bun runtime for %s: %w", p, err)
					}
					bunResult = res
					log.Info("resolved bun runtime", "platform", p.String(), "version", bunResult.Version, "sha256", bunResult.SHA256)
				}
			case StrategyStatic:
				// Static builds have nothing to compile: the SvelteKit build
				// output (.svelte-kit/output) is the entire artifact, served by
				// pokkum-static with no Bun runtime and no bundled executable.
			case StrategyExe:
				outPath := filepath.Join(workDir, "app-"+platformSlug(p))
				log.Info("compiling", "platform", p.String(), "output", outPath)
				compiledArt, err := deps.Compiler.Compile(gctx, ports.CompileRequest{
					ProjectDir:             req.ProjectDir,
					EntrypointPath:         prep.EntrypointPath,
					Platform:               p,
					OutputPath:             outPath,
					SourceDateEpoch:        req.SourceDateEpoch,
					Env:                    req.Compile.Env,
					Minify:                 !req.Compile.NoMinify,
					Sourcemap:              req.Compile.Sourcemap,
					Hermetic:               req.Hermetic,
					HermeticMountIsolation: req.HermeticMountIsolation,
				})
				if err != nil {
					return err
				}
				art = compiledArt
				log.Info("compiled", "platform", p.String(), "size", art.Size, "sha256", art.SHA256)
			default:
				// Mirrors the packaging switch below: an unrecognized
				// strategy must fail loudly, not silently skip the
				// per-strategy work above.
				return fmt.Errorf("core: unhandled build strategy %q in compile fan-out: %w", req.Compile.Strategy, ErrInvalidStrategy)
			}
			// art.Platform must be set regardless of strategy: it is the map
			// key used below (images[b.artifact.Platform] = b.image) to
			// assemble the multi-platform index. Only StrategyExe's Compile
			// call above populates it as a side effect (compiledArt.Platform
			// is set from CompileRequest.Platform); StrategyLayered and
			// StrategyStatic never call Compile, so art was otherwise left at
			// its zero value here, and every platform's image would collide
			// under the same zero-value ports.Platform{} key — the last
			// platform processed silently wins, and Packager.Index then fails
			// with "unsupported platform" on the resulting empty-string
			// platform. See Lessons.md's 2026-08-16 entry.
			art.Platform = p

			var sup, staticSup []byte
			var runnerErr error
			if req.Compile.Strategy.ApplyStatic() {
				if deps.StaticServer == nil {
					return fmt.Errorf("core: static server provider unavailable for static strategy: %w", ErrPackageFailed)
				}
				staticSup, runnerErr = deps.StaticServer.Binary(gctx, p)
				if runnerErr != nil {
					return fmt.Errorf("core: resolve static server for %s: %w", p, runnerErr)
				}
				log.Info("resolved static server", "platform", p.String())
			} else {
				sup, runnerErr = deps.Supervisor.Binary(gctx, p)
				if runnerErr != nil {
					return runnerErr
				}
			}

			pkgReq := ports.PackageRequest{
				Platform:      p,
				Base:          baseImg,
				Strategy:      req.Compile.Strategy,
				AppRuntime:    req.AppRuntime,
				Compression:   req.Compile.Compression,
				App:           art,
				BunRuntime:    bunResult,
				Supervisor:    sup,
				StaticServer:  staticSup,
				Runtime:       req.Runtime,
				CreatedAt:     req.SourceDateEpoch,
				Labels:        labels,
				Annotations:   req.Annotations,
				NoPrune:       req.Compile.NoPrune,
				KeepVendor:    slices.Clone(req.Compile.KeepVendor),
				Sourcemap:     req.Compile.Sourcemap,
				NoPrecompress: req.Compile.NoPrecompress,
				NoStrip:       req.Compile.NoStrip,
				// Lineage annotations apply uniformly across strategies —
				// only the overlay LAYER itself (AssetOverlayDir, set below,
				// StrategyLayered only for now) needs per-strategy care.
				PredecessorDigest:         predecessorDigest,
				AssetOverlaySourceDigests: slices.Clone(assetOverlayDigests),
			}

			switch req.Compile.Strategy {
			case StrategyLayered:
				// AppServerDir is the WHOLE build output, not just its
				// server/ subdirectory: a real @sveltejs/adapter-node build
				// emits its actual entrypoint (index.js) and index.js's own
				// non-server dependencies (env.js, shims.js) as siblings of
				// server/, not inside it, and chunk files inside server/
				// reach back out to them via relative paths like
				// "../../shims.js" that assume the original nesting is
				// preserved. Flattening server/ alone into /app/server (the
				// prior behavior) silently dropped index.js from every
				// layered-strategy image and broke those relative escapes —
				// packaging the whole tree, with client/prerendered/
				// excluded below since they're packaged into their own
				// layers, keeps every original relative path correct by
				// construction instead of requiring bundler-output-specific
				// rewrites. See Lessons.md's corresponding entry.
				pkgReq.AppServerDir = prep.OutputDir
				pkgReq.AppClientDir = filepath.Join(prep.OutputDir, "client")
				pkgReq.AppVendorDir = filepath.Join(prep.OutputDir, "vendor")
				pkgReq.AppNativeDir = filepath.Join(prep.OutputDir, "native")
				// Production dependencies staged by Prepare, mounted where module
				// resolution looks (/app/node_modules) rather than /app/vendor,
				// which nothing consults.
				pkgReq.AppNodeModulesDir = prep.NodeModulesDir
				// Prerendered pages now live in their own /app/prerendered layer
				// instead of being dropped; POKKUM_PRERENDERED_DIR points the
				// patched handler at it.
				pkgReq.AppPrerenderedDir = filepath.Join(prep.OutputDir, "prerendered")
				// Non-empty only when Prepare generated a layered telemetry
				// bootstrap (req.Telemetry.Enabled and no user-owned
				// src/instrumentation.server.ts); the packager uses it to
				// insert `bun --preload <path>` into the layered Entrypoint
				// argv instead of the unconditional DefaultLayeredEntrypoint().
				pkgReq.TelemetryPreloadRelPath = prep.TelemetryPreloadRelPath
				// --asset-overlay: merged prior-generation immutable client
				// assets, empty unless the flag resolved at least one
				// generation (see Stage 4.4 above). StrategyLayered only for
				// now — StrategyStatic could benefit equally (pokkum-static
				// serves /app/client too) but has its own packaging branch
				// in internal/adapters/packager/packager.go that doesn't yet
				// call appendAssetOverlayLayer; tracked as a scoped follow-up
				// in docs/Roadmap.md rather than folded into this same change.
				pkgReq.AssetOverlayDir = assetOverlayDir
			case StrategyStatic:
				// No server JS, vendor or native trees for a static site; the
				// .svelte-kit/output staging holds the client and prerendered
				// trees that pokkum-static serves.
				pkgReq.AppClientDir = filepath.Join(prep.OutputDir, "client")
				pkgReq.AppPrerenderedDir = filepath.Join(prep.OutputDir, "prerendered")
				// Optional SPA fallback: the leaf filename adapter-static emitted
				// in the client staging. Only set for static builds that actually
				// configured one (Prepare returns it non-empty just for those).
				// The packager stamps POKKUM_STATIC_FALLBACK to the in-image path
				// AppClientDirPrefix/<rel> and verifies the file was staged.
				if rel := prep.StaticFallbackRelPath; rel != "" {
					pkgReq.StaticFallback = ports.AppClientDirPrefix + "/" + rel
				}
			case StrategyExe:
				// No extra packaging directories to set: the exe
				// strategy's shipped artifact is `art`, the single
				// compiled executable already assigned to pkgReq.App
				// above (from the Compile call earlier in this
				// goroutine) — prep.OutputDir was Compile's INPUT, not
				// something the packager mounts directly for this
				// strategy. An explicit no-op case, not a silent
				// fallthrough, so this is a deliberate choice rather
				// than an oversight — see the default case below.
			default:
				// A future BuildStrategy value that reaches this switch
				// without an explicit case would otherwise silently skip
				// every strategy-specific field above and package
				// whatever pkgReq already happened to hold — exactly the
				// "unrecognized enum value silently no-ops" failure mode
				// internal/architecture_test.go's exhaustiveness check
				// exists to catch. Fail the build for this platform
				// instead of guessing.
				return fmt.Errorf("core: unhandled build strategy %q in packaging fan-out: %w", req.Compile.Strategy, ErrInvalidStrategy)
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
				// The resolved base images, so the document catalogues the OS
				// packages the image actually ships rather than describing the
				// npm tree alone.
				BaseImages: baseImagesFor(base),
				Format:     req.SBOM.Format,
				Name:       req.Repo,
				CreatedAt:  req.SourceDateEpoch,
				BunVersion: bunToolchain.Version,
				BunSHA256:  bunToolchain.SHA256,
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
			Repo:               req.Repo,
			Tags:               slices.Clone(req.Tags),
			Payload:            payload,
			Insecure:           req.Insecure,
			UserAgent:          deps.UserAgent,
			RegistryConfigPath: req.RegistryConfigPath,
			Concurrency:        req.PushConcurrency,
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

	case OutputOCILayout:
		// No platform selection and no flattening, unlike OutputLocal and
		// OutputTarball respectively: an OCI image layout represents a
		// manifest list natively, so the whole index goes to disk exactly as
		// it would have gone to a registry.
		return deps.OCILayouts.WriteOCILayout(ctx, ports.OCILayoutRequest{
			Path:    req.Output.OCILayoutPath,
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
// baseNameForLabel returns the value for org.opencontainers.image.base.name.
//
// Only build-state-invariant references are eligible. UpstreamRef is
// preferred because it is both invariant and human-readable
// ("gcr.io/distroless/cc-debian12:nonroot"), which is what the annotation is
// for — the digest lives in org.opencontainers.image.base.digest. PinnedRef
// is the fallback for a resolver that supplies no UpstreamRef; it is
// redundant with base.digest but still invariant. Returning "" is correct
// when neither is available: omitting an annotation is better than baking in
// a value that moves the image digest.
func baseNameForLabel(base BaseImageInfo) string {
	if base.UpstreamRef != "" {
		return base.UpstreamRef
	}
	return base.PinnedRef
}

func imageLabels(req BuildRequest, base BaseImageInfo, tc Toolchain, bunToolchain ports.BunResolverResult) map[string]string {
	out := make(map[string]string, len(req.Labels)+4)
	// base.UpstreamRef, never base.Ref: this string is baked into the image
	// config, so a value that changes with local state changes the image
	// digest. base.Ref is rebound to the lockfile's pinned digest once
	// pokkum.lock exists and to the mirror tag when an escrow mirror is used,
	// so recording it made a project's first build differ from every
	// subsequent one, and a mirrored build differ from an unmirrored one, for
	// identical source. PinnedRef is the fallback because it is also
	// invariant; base.Ref is deliberately not a fallback at all, since
	// reintroducing it on any path reintroduces the bug.
	if ref := baseNameForLabel(base); ref != "" {
		out[ports.LabelBaseName] = ref
	}
	if tc.PokkumVersion != "" {
		out[ports.LabelPokkumVersion] = tc.PokkumVersion
	}
	// Stamped only when Bun is actually embedded in the image: RuntimeBun
	// (the default — an empty AppRuntime normalizes to it too; see the
	// LabelRuntime comment just below for the absence convention) AND a
	// strategy that ships a Bun runtime at all. ApplyStatic strategies
	// (static) are explicitly documented (BuildStrategy.ApplyStatic) as
	// shipping "no Bun runtime and no supervisor layer" — pokkum-static
	// serves prerendered files directly — so there is no embedded Bun for
	// the label to name, and it must be omitted there too, not just for
	// --runtime=node.
	//
	// The VALUE must be the runtime actually embedded, not the host's
	// build-tool bun: bunToolchain is the same resolved
	// ports.BunResolverResult that the SBOM and SLSA provenance requests
	// are built from (see slsaGeneratorRequest's identical firstNonEmpty
	// fallback) — reusing it here, rather than re-deriving a version, is
	// what keeps the label from drifting away from those other three
	// records again. For a strategy with no separate resolve (exe),
	// bunToolchain is zero and this falls back to tc.BunVersion, which is
	// correct there: `bun build --compile` bakes that exact host bun's
	// runtime directly into the artifact, so the host compiler bun IS the
	// embedded runtime.
	if (req.AppRuntime == "" || req.AppRuntime == ports.RuntimeBun) && !req.Compile.Strategy.ApplyStatic() {
		if v := firstNonEmpty(bunToolchain.Version, tc.BunVersion); v != "" {
			out[ports.LabelBunVersion] = v
		}
	}
	if tc.SupervisorVersion != "" {
		out[ports.LabelSupervisor] = tc.SupervisorVersion
	}
	// Stamped only for the non-default runtime, per LabelRuntime's doc
	// comment: absence already means bun (no pre-existing image carries the
	// label), and skipping the default keeps every existing bun image's
	// labels — and golden-pinned digests — byte-identical.
	if req.AppRuntime != "" && req.AppRuntime != ports.RuntimeBun {
		out[ports.LabelRuntime] = string(req.AppRuntime)
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

// runSecretScan invokes deps.SecretGuard.ScanDirectory against dir and turns
// a non-clean result into an error. A no-op (nil error) when deps.SecretGuard
// is nil (the port is optional) or dir is empty (nothing to scan — e.g. a
// strategy/stage combination with no applicable directory).
//
// Two failure sentinels are used deliberately, not one: ErrSecretInlined
// means the scanner found something and is confident about it;
// ErrSecretScanIncomplete means the scanner could not actually inspect one
// or more files (see ports.SecretSkip) and is refusing to guess. Collapsing
// both into one error would make "we found a secret" indistinguishable from
// "we don't know, ask a human" — the two call for different remediation.
//
// stage names the log/error context (e.g. "pre-build source", "post-build
// output") purely for operator-facing messages; it is not interpreted.
// generatedOutputDirs are directory names that conventionally hold build output
// for this ecosystem. Used only to phrase a suggestion — nothing is skipped on
// the strength of a name, because these are conventions and a project may keep
// real source in any of them.
var generatedOutputDirs = []string{"build", "dist", "out", ".output", ".vercel", ".netlify", ".next", "storybook-static"}

// generatedDirsAmong returns, sorted and deduplicated, the conventional
// output-directory prefixes present in the findings.
func generatedDirsAmong(matches []ports.SecretMatch) []string {
	seen := map[string]bool{}
	for _, m := range matches {
		// Compare the first path segment only: a "build" anywhere deeper is far
		// more likely to be a real source directory than generated output.
		first := m.FilePath
		if i := strings.IndexAny(first, "/\\"); i >= 0 {
			first = first[:i]
		}
		for _, d := range generatedOutputDirs {
			if first == d {
				seen[d] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// maxReportedSecretMatches caps how many locations a failure reports. A minified
// bundle is one logical line, and every rule reports every match on it, so a
// single inlined config object can produce hundreds — emitting all of them
// buries the first, which is usually the only interesting one.
const maxReportedSecretMatches = 10

// logSecretMatches reports where the secrets are, which is the whole point of the
// finding and used to be discarded: the failure carried only a count, so an
// operator was told their build contained four secrets and given nothing to act
// on. The skipped-files branch already listed paths, which made this an
// inconsistency as much as a gap.
//
// The matched text is deliberately NOT emitted. ports.SecretMatch carries a
// SecretSnippet and it is the matched substring — the secret itself. Echoing it
// would copy the value into terminal scrollback, CI logs and anything scraping
// build output, which is a poor trade for a tool whose purpose is stopping
// secrets from spreading. file:line plus the rule name is enough to act on, and
// the rule name describes the shape that matched without revealing the value.
//
// Order is file, then line, then rule, so the same project always reports the
// same sequence instead of one reflecting filesystem walk order.
func logSecretMatches(log *slog.Logger, stage string, matches []ports.SecretMatch, showValues bool) {
	if log == nil {
		return
	}
	sorted := make([]ports.SecretMatch, len(matches))
	copy(sorted, matches)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].FilePath != sorted[j].FilePath {
			return sorted[i].FilePath < sorted[j].FilePath
		}
		if sorted[i].LineNumber != sorted[j].LineNumber {
			return sorted[i].LineNumber < sorted[j].LineNumber
		}
		return sorted[i].RuleName < sorted[j].RuleName
	})

	shown := sorted
	if len(shown) > maxReportedSecretMatches {
		shown = shown[:maxReportedSecretMatches]
	}
	for _, m := range shown {
		attrs := []any{
			"stage", stage, "file", m.FilePath, "line", m.LineNumber,
			// The column is what makes a finding in generated code navigable: a
			// minified bundle is one logical line tens of kilobytes long, so the
			// line number alone points at the whole file.
			"col", m.Column, "rule", m.RuleName,
		}
		if showValues {
			attrs = append(attrs, "value", m.SecretSnippet)
		} else {
			// Named explicitly so an operator who wonders why the value is
			// absent can see it was a decision, and how to change it.
			attrs = append(attrs, "value", "redacted (--show-secret-values to reveal)")
		}
		log.Error("secret guard: hardcoded secret", attrs...)
	}
	if remaining := len(sorted) - len(shown); remaining > 0 {
		log.Error("secret guard: further hardcoded secrets not listed individually",
			"stage", stage, "remaining", remaining, "listed", len(shown))
	}

	// Both mechanisms are named, because neither covers every case. The marker is
	// precise and travels with the code, but cannot be added to generated output —
	// a minified bundle carrying a redaction string compiled from annotated source
	// will still be flagged, and only a pattern reaches that. Suggesting just one
	// would send half of a real project's findings down a path that cannot work.
	//
	// Deliberately no pre-filled regex: an allow pattern matches a whole line, so
	// generating one means printing the line — and if the finding is a genuine
	// secret, that is the secret, in a log and then in a committed config file.
	// The file:line above is enough to write a pattern from after looking, and
	// looking is the step that distinguishes a false positive from a real leak.
	// Pre-build findings inside a conventional output directory are almost always
	// a previous run's artifacts rather than source. pokkum init's default
	// .pokkumignore excludes build/, but init never rewrites an existing file, so
	// a project initialised before that default landed keeps scanning its own
	// output and has no way to guess why. Naming the directory is what turns that
	// into a one-line fix.
	if dirs := generatedDirsAmong(sorted); len(dirs) > 0 {
		log.Warn("secret guard: some findings are in directories that usually hold generated build output, "+
			"not source; if that is the case here, add them to .pokkumignore (pokkum init's default now excludes build/, "+
			"but it never rewrites an existing .pokkumignore)",
			"stage", stage, "directories", strings.Join(dirs, ", "))
	}

	log.Info("secret guard: to accept a finding, mark the line with a comment containing "+
		ports.AllowSecretMarker+" (on the line or the one above), or add a regex to "+
		"security.allow_secret_patterns in .pokkum.yaml / pass --allow-secret-pattern. "+
		"Generated or minified output cannot carry the comment, so those need the pattern",
		"stage", stage, "marker", ports.AllowSecretMarker)
}

func runSecretScan(ctx context.Context, deps Deps, log *slog.Logger, stage, dir string, allowPatterns []string, scanSourcemaps, showSecretValues bool) error {
	if deps.SecretGuard == nil || dir == "" {
		return nil
	}
	res, err := deps.SecretGuard.ScanDirectory(ctx, ports.SecretScanRequest{
		ProjectDir:     dir,
		AllowPatterns:  allowPatterns,
		ScanSourcemaps: scanSourcemaps,
	})
	if err != nil {
		return fmt.Errorf("secret guard (%s, %s): %w", stage, dir, err)
	}
	if len(res.Skipped) > 0 {
		names := make([]string, 0, len(res.Skipped))
		for _, s := range res.Skipped {
			names = append(names, s.FilePath)
		}
		return fmt.Errorf("secret guard (%s): %d file(s) in %s could not be inspected (%s): %w",
			stage, len(res.Skipped), dir, strings.Join(names, ", "), ErrSecretScanIncomplete)
	}
	if !res.Passed {
		// Logged as one structured line per finding rather than folded into the
		// error string: slog quotes an error value, so embedded newlines arrive
		// at the terminal as a literal \n and the list becomes less readable
		// than the single line it replaced. Structured fields also let a log
		// processor filter by file or rule.
		logSecretMatches(log, stage, res.Matches, showSecretValues)
		return fmt.Errorf("secret guard (%s): detected %d hardcoded secret(s) in %s (locations logged above; exempt a false positive with --allow-secret-pattern=<regex>, repeatable): %w",
			stage, len(res.Matches), dir, ErrSecretInlined)
	}
	log.Info("secret guard ok", "stage", stage, "checked", dir)
	return nil
}

// postBuildScanDirs returns the on-disk directories, rooted at Prepare's
// OutputDir, that mirror what actually gets packaged for strategy. This
// deliberately mirrors the per-strategy pkgReq.App*Dir wiring inside
// fanOut below — a change to one without the other would make the secret
// scan either miss shipped content or waste time scanning trees that never
// ship, so if fanOut's wiring changes, this should be revisited alongside
// it. Returns nil when outputDir is empty (nothing Prepare produced yet, or
// a test double that never sets it).
//
// entrypointPath is Prepare's EntrypointPath — needed only by StrategyExe,
// whose shipped artifact is compiled FROM that file rather than packaged
// from a directory (see the StrategyExe case).
func postBuildScanDirs(strategy BuildStrategy, outputDir, entrypointPath string) []string {
	if outputDir == "" {
		return nil
	}
	switch strategy {
	case StrategyStatic:
		// No server component ships for static — only the client and
		// prerendered trees pokkum-static serves.
		return []string{
			filepath.Join(outputDir, "client"),
			filepath.Join(outputDir, "prerendered"),
		}
	case StrategyLayered:
		// The whole tree ships (server entrypoint + its siblings, client,
		// vendor, native, prerendered) — see the pipeline.go comment at
		// pkgReq.AppServerDir's assignment for why it's the whole
		// directory rather than a subdirectory.
		return []string{outputDir}
	case StrategyExe:
		// Two trees, not one, because exe is the only strategy whose
		// shipped artifact is COMPILED from an entrypoint rather than
		// packaged from a directory: `bun build --compile` bundles
		// prep.EntrypointPath and everything it imports, and that
		// entrypoint is not always inside outputDir. With --telemetry,
		// sveltekitutils.PrepareVirtualTelemetryEntry rewrites it to
		// <projectDir>/.pokkum/telemetry-entry.ts, which imports a
		// generated .pokkum/otel-bootstrap.ts — both bundled into the
		// shipped binary, both created by Prepare (so they do not exist
		// yet at the pre-build source scan), and neither reachable from
		// outputDir. Scanning only outputDir left exactly that bundled JS
		// uncovered.
		//
		// Still a PROXY for the compiled binary, not parity with
		// layered/static — see the Stage 5.5 comment at the call site for
		// the honest residual limitation (a secret injected by the Compile
		// step itself, e.g. via a bunfig.toml preload plugin or a
		// `with { type: "macro" }` import, is present in neither tree).
		dirs := []string{outputDir}
		if entrypointDir := filepath.Dir(entrypointPath); entrypointPath != "" && !dirWithin(outputDir, entrypointDir) {
			dirs = append(dirs, entrypointDir)
		}
		return dirs
	default:
		// An unrecognized strategy: scan the whole tree rather than skip
		// silently, matching this file's other strategy switches' new
		// fail-into-safety default arms.
		return []string{outputDir}
	}
}

// dirWithin reports whether candidate is dir itself or a directory beneath
// it. Used to avoid handing the secret scanner the same tree twice (which
// would double every finding an operator has to read) while still catching
// the case where a compile entrypoint genuinely lives outside the build
// output tree.
//
// An unresolvable relative path (mismatched absolute/relative inputs, which
// Prepare never produces but a test double could) resolves to false — i.e.
// "scan it as well". The conservative direction for a security gate is more
// coverage, never less.
func dirWithin(dir, candidate string) bool {
	rel, err := filepath.Rel(dir, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
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
	case OutputOCILayout:
		row("would write", req.Output.OCILayoutPath+" (oci image layout), as "+req.Repo)
	default:
		// Should be unreachable by construction: Deps.validate (Stage 1,
		// run before dry-run ever reaches this point) now rejects an
		// unrecognized Output.Mode outright. Kept as an explicit, visible
		// row rather than silently rendering nothing for it, in case that
		// invariant is ever broken by a future change to validate — a
		// dry-run report is exactly the wrong place to hide a bug behind
		// silence.
		row("would output", fmt.Sprintf("(unrecognized output mode %q)", req.Output.Mode))
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

// baseImagesFor returns the resolved per-platform base images, or nil when
// none were resolved. nil is meaningful downstream: it records that no OS
// package scan was attempted, as distinct from one that found nothing.
func baseImagesFor(base *ports.BaseImage) map[ports.Platform]v1.Image {
	if base == nil || len(base.Images) == 0 {
		return nil
	}
	return base.Images
}

// applyRouteExclusions drops prerendered routes the operator asked to keep out
// of the image, and reports what that leaves dangling.
//
// Dead links warn rather than fail. An excluded route that something still
// links to is a real defect, but it is the operator's own trade-off to make —
// excluding an internal dashboard from a production image is worth a stale
// link in a footer, and failing the build would make the feature unusable for
// the case it exists to serve.
func applyRouteExclusions(ctx context.Context, deps Deps, req BuildRequest, prep ports.PrepareResult) error {
	log := deps.Logger
	res, err := deps.RouteFilter.FilterRoutes(ctx, ports.RouteFilterRequest{
		PrerenderedDir:  filepath.Join(prep.OutputDir, "prerendered"),
		Patterns:        req.ExcludeRoutes,
		AlreadyExcluded: prep.RoutesExcludedAtBuild,
	})
	if err != nil {
		return fmt.Errorf("core: excluding routes: %w", err)
	}

	for _, r := range prep.RoutesExcludedAtBuild {
		log.Info("route excluded from the build; its code is not in the image at all", "route", r)
	}
	for _, r := range res.ExcludedRoutes {
		log.Info("route's prerendered output removed from image", "route", r)
	}
	for _, f := range res.SkippedSymlinks {
		log.Warn("route exclusion skipped a symlink rather than deleting through it; it is still in the image", "path", f)
	}
	for _, p := range res.UnmatchedPatterns {
		// Not an error, but never silent: the operator asked for a route to be
		// absent and it is still there. On the layered strategy this is the
		// expected report for a server-rendered route, which is compiled into
		// the server bundle and cannot be removed by deleting a file.
		log.Warn("route exclusion pattern matched no prerendered route, so nothing was removed for it",
			"pattern", p, "strategy", string(req.Compile.Strategy))
	}
	for _, l := range res.DeadLinks {
		log.Warn("a page still links to an excluded route, so that link will 404",
			"page", l.FromPage, "href", l.Href, "route", l.Route)
	}
	if len(res.DeadLinks) > 0 {
		log.Warn("links to excluded routes found; the build continues because removing the route is the point, but these links are now broken",
			"count", len(res.DeadLinks))
	}
	return nil
}
