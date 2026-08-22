// Package packager implements ports.Packager: it turns a resolved base image,
// a compiled application binary and the pokkum-init supervisor into an OCI
// image, and ties per-platform images together into a multi-platform index.
//
// Nothing here touches a registry, a daemon, a clock or the network. Build and
// Index are pure functions of their requests plus the two files on disk they
// are pointed at, and that is the whole point of the package: Pokkum's central
// promise is that two builds from identical inputs produce byte-identical
// images, and this is the only package in a position to break it.
//
// # How determinism is achieved
//
// Every value that could vary between two runs is pinned:
//
//   - Timestamps. PackageRequest.CreatedAt (derived by the CLI from
//     SOURCE_DATE_EPOCH or the git commit) is the mtime of every application-
//     content tar entry (server/client/vendor/native/prerendered, and the
//     exe strategy's compiled app binary), the image config's created field,
//     and the created field of every history entry this package adds — but
//     deliberately NOT the mtime of the three immutable embedded-binary
//     layers (the Bun runtime, pokkum-init, pokkum-static), which are pinned
//     to the fixed layer.go's pinnedImmutableBinaryEpoch instead; see that
//     constant's doc comment and docs/archive/Roadmap.md item 3f for why. time.Now is
//     never called; the package does not import time for anything but type
//     declarations, Truncate and that one fixed constant.
//
//   - Tar headers. See layer.go. Uid/Gid are pinned to the distroless nonroot
//     ids, Uname/Gname are empty, the format is set explicitly, and entries are
//     emitted in sorted order with explicit directory entries, so nothing is
//     inherited from the host filesystem or inferred by archive/tar.
//
//   - Map iteration. Go randomises it per process, so any map whose contents
//     reach serialized output is drained through a sorted key list before use.
//     Three maps matter: IndexRequest.Images (sorted by Platform.String()),
//     RuntimeConfig.Env (sorted by key, because it becomes an ordered []string
//     in the config) and the internal path set that builds the tar entry list
//     (sorted by path). Labels, image annotations and ExposedPorts are maps in
//     the output too, and encoding/json marshals map keys in sorted order, so
//     those need no help — but the *overlay order* between two label maps is
//     still fixed, so that a conflicting key resolves the same way every run.
//
//   - Compression. tarball.LayerFromOpener gzips with compress/gzip's zero
//     Header, which writes no name and a zero mtime, so the compressed layer
//     digest is stable and not merely the uncompressed diffID.
//
// # Media types
//
// Images are emitted with OCI media types (types.OCIManifestSchema1,
// types.OCIConfigJSON) and indexes with types.OCIImageIndex. This is not a
// stylistic preference: Docker's schema 2 manifest has no annotations field, so
// emitting Docker media types would silently discard every
// org.opencontainers.image.* annotation the caller asked for. The layer this
// package adds is types.OCILayer to match. Layers inherited from the base image
// keep whatever media type the base used, which for the gcr.io distroless
// images is the Docker one; a mixed manifest is what every tool that converts a
// Docker base to an OCI output produces, and registries accept it.
//
// Index descriptors carry an artifactType of the child's config media type.
// That is go-containerregistry's partial.Descriptor filling in an OCI 1.1
// field, not something this package sets, and IndexAddendum offers no way to
// suppress it. It is constant for a given child and therefore harmless to
// reproducibility; it is noted here only so that the extra field in an index
// manifest is not mistaken for a bug.
//
// # Two layers, ordered by volatility
//
// The supervisor and the application are two separate layers, per
// ports.PackageRequest's field docs on App and Supervisor. The supervisor
// layer is appended below the application layer — added first, so it sits
// lower in the image — because it changes only when pokkum itself is
// upgraded, while the application layer changes on every build; the stable
// layer belongs underneath the one that churns.
//
// An earlier version of this package combined them into one layer, reasoning
// that the split only saved a few percent of rebuild bandwidth. That measured
// the wrong thing: the real benefit is cross-image deduplication, not
// per-rebuild savings. Every image Pokkum builds at a given pokkum version
// shares the identical supervisor layer, so a registry hosting many
// Pokkum-built apps stores that layer once, and a node pulling several of them
// downloads it once — regardless of how often any single app rebuilds. Two
// layers is therefore the right shape even though the supervisor is small.
//
// # Concurrency
//
// A Packager holds no mutable state and is safe for concurrent use, which
// matters because core packages every platform in parallel.
package packager

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/attestutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/precompressutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/striputils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// historySupervisorCreatedBy and historyAppCreatedBy are the CreatedBy strings
// of the two history entries this package appends, one per layer, in the same
// order the layers are appended (supervisor, then application). Each is a
// fixed string rather than anything derived from the running process — a
// version, a command line, a hostname — because history is serialized into the
// image config and therefore into the config digest.
const (
	historySupervisorCreatedBy = "pokkum: add " + ports.SupervisorPath
	historyAppCreatedBy        = "pokkum: add " + ports.AppBinaryPath
)

// Packager implements ports.Packager. The zero value is usable but logs to
// slog.Default(); prefer NewPackager.
type Packager struct {
	log *slog.Logger
}

// Compile-time proof that the adapter satisfies the port.
var _ ports.Packager = (*Packager)(nil)

// NewPackager constructs a Packager. A nil logger defaults to slog.Default().
func NewPackager(log *slog.Logger) *Packager {
	if log == nil {
		log = slog.Default()
	}
	return &Packager{log: log}
}

// Build implements ports.Packager.
//
// It appends two layers to req.Base — the supervisor, then the application
// binary, in that order (see the package doc's "Two layers, ordered by
// volatility" section) — applies the runtime configuration, stamps the OCI
// annotations, and returns the resulting single-platform image. req.Base is
// never mutated: go-containerregistry's mutate package wraps rather than
// modifies, so the caller's base image is still usable for another platform or
// another build afterwards.
func (p *Packager) Build(ctx context.Context, req ports.PackageRequest) (v1.Image, error) {
	if err := validatePackageRequest(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", req.Platform, err)
	}

	if req.Strategy == ports.StrategyLayered {
		// Unconditional, not nil-guarded: core.Build's Normalize() already runs
		// RuntimeConfig.WithDefaults() once before the strategy is known (see
		// internal/core/model.go), which claims a nil Entrypoint with the
		// StrategyExe-shaped default before this code ever sees the request —
		// silently defeating a nil-check here. Mirror the static strategy's own
		// unconditional overwrite below instead of trusting a guard an earlier
		// generic pass can pre-empt. See Lessons.md's 2026-08-16 entry.
		//
		// The entrypoint is selected by a positive runtime switch (checklist
		// row 11): bun and node are the only two runtimes, everything else
		// is an error here rather than a silent fall-through to the Bun
		// shape — an image whose entrypoint names a runtime binary that
		// isn't in the image cannot start, which is the worst possible
		// place to discover an unhandled enum value.
		switch effectiveAppRuntime(req.AppRuntime) {
		case ports.RuntimeBun:
			if req.TelemetryPreloadRelPath != "" {
				// Telemetry SDK bootstrap for the layered (default) strategy: insert
				// `bun --preload <path>` ahead of the real entrypoint so the SDK
				// starts first (Bun's --preload runs a file's side effects before
				// the main entrypoint executes — confirmed to work with this exact
				// bare invocation form, no `run` subcommand, empirically before
				// this mechanism was built; see
				// sveltekitutils.PrepareLayeredTelemetryBootstrap's doc comment).
				// The path MUST be absolute (join against AppServerDirPrefix, not
				// left as a bare relative filename) — also confirmed empirically:
				// Bun's --preload treats an unprefixed relative specifier as an
				// npm package name to resolve, not a local file, and fails with
				// "preload not found" for exactly that reason.
				preloadPath := ports.AppServerDirPrefix + "/" + req.TelemetryPreloadRelPath
				req.Runtime.Entrypoint = []string{ports.SupervisorPath, "--", ports.BunBinaryPath, ports.BunNoInstallFlag, "--preload", preloadPath, ports.AppServerIndexPath}
			} else {
				req.Runtime.Entrypoint = ports.DefaultLayeredEntrypoint()
			}
		case ports.RuntimeNode:
			// The runtime swap: the base image's own Node executes
			// adapter-node's output directly. No telemetry-preload variant
			// exists for node (validatePackageRequest rejects the
			// combination), so this arm has exactly one shape.
			req.Runtime.Entrypoint = ports.DefaultLayeredNodeEntrypoint()
		default:
			return nil, fmt.Errorf("packager: build %s: unhandled app runtime %q: %w", req.Platform, req.AppRuntime, core.ErrPackageFailed)
		}
	}
	rc := req.Runtime.WithDefaults()
	if req.Strategy.ApplyStatic() {
		// A static image is its own init: pokkum-static is PID 1, so it is both
		// the entrypoint and the probe server, and its static roots are the
		// client and prerendered trees Pokkum mounts.
		rc.Entrypoint = ports.DefaultStaticEntrypoint()
		if rc.Env == nil {
			rc.Env = map[string]string{}
		}
		rc.Env[ports.EnvStaticRoots] = ports.AppClientDirPrefix + ":" + ports.AppPrerenderedDirPrefix
		if req.StaticFallback != "" {
			// Optional SPA fallback: verify the configured in-image path is a
			// regular file staged under the client root (never silently drop the
			// SPA shell), then stamp POKKUM_STATIC_FALLBACK so pokkum-static
			// serves it with 200 on unmatched GET/HEAD routes.
			rel := strings.TrimPrefix(req.StaticFallback, ports.AppClientDirPrefix+"/")
			if rel == req.StaticFallback || filepath.Base(rel) != rel {
				return nil, fmt.Errorf("packager: build %s: invalid static fallback path %q (must live under %s): %w", req.Platform, req.StaticFallback, ports.AppClientDirPrefix, core.ErrPackageFailed)
			}
			staged := filepath.Join(req.AppClientDir, rel)
			if fi, err := os.Stat(staged); err != nil || !fi.Mode().IsRegular() {
				return nil, fmt.Errorf("packager: build %s: static fallback %q configured but not staged at %s (SPA shell would be silently dropped): %w", req.Platform, req.StaticFallback, staged, core.ErrPackageFailed)
			}
			rc.Env[ports.EnvStaticFallback] = req.StaticFallback
		}
	} else if req.Strategy == ports.StrategyLayered {
		if rc.Env == nil {
			rc.Env = map[string]string{}
		}

		// Point the patched adapter-node handler at the client tree Pokkum
		// mounts at /app/client. Unconditional for layered builds: every one of
		// them has client assets, unlike prerendered pages below. Without this
		// the stock handler looks under /app/server/client, finds nothing, and
		// drops its asset middleware silently — the image boots, both probes
		// pass, and every stylesheet and script 404s.
		rc.Env[ports.EnvClientDir] = ports.AppClientDirPrefix

		if req.AppPrerenderedDir != "" {
			// Same redirection for the prerendered tree at /app/prerendered (see
			// Prepare and the fan-out in core). The stock handler would otherwise
			// look for it under /app/server/prerendered, where it no longer lives.
			rc.Env[ports.EnvPrerenderedDir] = ports.AppPrerenderedDirPrefix
		}
	}
	ts := pinnedTime(req.CreatedAt)

	// The base digest is read before anything else expensive: for a lazily
	// pulled base this is the last point at which a network error is cheap to
	// report, and the value goes into both the labels and the annotations.
	baseDigest, err := req.Base.Digest()
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: read base digest: %w: %w", req.Platform, err, core.ErrPackageFailed)
	}
	baseCfg, err := req.Base.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: read base config: %w: %w", req.Platform, err, core.ErrPackageFailed)
	}

	cfg := applyRuntime(baseCfg, rc, req, baseDigest, ts)

	img, err := mutate.ConfigFile(req.Base, cfg)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: apply config: %w: %w", req.Platform, err, core.ErrPackageFailed)
	}

	var addenda []mutate.Addendum

	// attestRecords accumulates the authoritative post-processing file records
	// for every layered-strategy /app tree as its layer is built. They are the
	// single input to the startup-attestation digest (hardening Option C):
	// stamped into the image config so pokkum-init can verify the extracted
	// /app tree matches at runtime. Only the layered branch fills it — the exe
	// strategy has no /app tree (the app is a sealed binary) and the static
	// strategy has no supervisor to run the check.
	var attestRecords []attestutils.Record

	layerMediaType := types.OCILayer
	if req.Compression.Normalize() == ports.CompressionZstd {
		layerMediaType = types.OCILayerZStd
	}

	if req.Strategy == ports.StrategyLayered {
		// The Bun runtime and the supervisor are immutable embedded binaries,
		// not this build's own source content: their tar ModTime (and cache
		// key) is deliberately pinnedImmutableBinaryEpoch, not ts
		// (SOURCE_DATE_EPOCH) — see that constant's doc comment in layer.go
		// and docs/archive/Roadmap.md item 3f. The layers below that DO derive from ts
		// (server, client, vendor, native, prerendered) are exactly the ones
		// that legitimately reflect this build's own source snapshot.
		// The Bun runtime layer exists only for RuntimeBun: a --runtime=node
		// image's runtime is the base image's own Node (NodeBinaryPath), so
		// there is no runtime binary for Pokkum to add — the layered layout
		// simply starts at the supervisor. effectiveAppRuntime already
		// rejected anything but bun/node in the entrypoint switch above.
		if effectiveAppRuntime(req.AppRuntime) == ports.RuntimeBun {
			bunLayer, err := BuildCustomFileLayer(ctx, req.Platform, ports.BunBinaryPath, req.BunRuntime.BinaryPath, pinnedImmutableBinaryEpoch, req.Compression)
			if err != nil {
				return nil, fmt.Errorf("packager: build %s: bun layer: %w", req.Platform, err)
			}
			addenda = append(addenda,
				mutate.Addendum{Layer: bunLayer, MediaType: layerMediaType, History: v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.BunBinaryPath}},
			)
		}
		supervisorLayer, err := buildSupervisorLayer(ctx, req, pinnedImmutableBinaryEpoch)
		if err != nil {
			return nil, err
		}
		// AppServerDir is the whole build output (see pipeline.go), so
		// client/vendor/native/prerendered are explicitly excluded here —
		// each is packaged into its own layer below/elsewhere, and without
		// this they'd be duplicated into /app/server too.
		serverLayer, _, serverRecs, err := BuildDirectoryTreeLayerWithPruning(ctx, req.Platform, req.AppServerDir, ports.AppServerDirPrefix, ts, req.Compression, pruneutils.PruneOptions{NoPrune: true, ExcludeDirs: []string{"client", "vendor", "native", "prerendered"}})
		if err != nil {
			return nil, fmt.Errorf("packager: build %s: server layer: %w", req.Platform, err)
		}
		attestRecords = append(attestRecords, serverRecs...)

		addenda = append(addenda,
			mutate.Addendum{Layer: supervisorLayer, MediaType: layerMediaType, History: v1.History{Created: v1.Time{Time: ts}, CreatedBy: historySupervisorCreatedBy}},
			mutate.Addendum{Layer: serverLayer, MediaType: layerMediaType, History: v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add /app/server"}},
		)

		// The asset overlay layer (if any) is appended BEFORE the current
		// build's own client layer, deliberately — OCI layers apply in
		// addenda order, and a later layer's file overwrites an earlier
		// layer's file at the same path when the image is extracted. Since
		// content-hashed filenames make a genuine same-path/different-bytes
		// collision between a prior generation and the current build
		// impossible in the ordinary case (that's the entire point of
		// content hashing), this ordering is belt-and-suspenders: it
		// guarantees the CURRENT build's bytes win, never a stale prior
		// generation's, if that guarantee is ever violated. attestRecords
		// below is deduped to match this same last-wins-by-Rel semantics.
		var overlayRecs []attestutils.Record
		if addenda, overlayRecs, err = appendAssetOverlayLayer(ctx, req, ts, layerMediaType, addenda); err != nil {
			return nil, err
		}
		attestRecords = append(attestRecords, overlayRecs...)

		if req.AppClientDir != "" {
			if info, err := os.Stat(req.AppClientDir); err == nil && info.IsDir() {
				if !req.NoPrecompress {
					// Layered strategy's runtime (adapter-node's bundled sirv
					// server) only ever negotiates gzip/brotli, never zstd —
					// see precompressutils.PrecompressOptions's doc comment.
					if err := precompressutils.PrecompressDirectory(req.AppClientDir, ts, precompressutils.PrecompressOptions{Gzip: true, Brotli: true}); err != nil {
						// Warned, not fatal: sidecars are a serving
						// optimisation, so a failure degrades response size
						// rather than correctness. Silence was the defect —
						// the image simply shipped slower with no signal.
						p.logger().Warn("packager: precompressing the client tree failed; the image will ship without some gzip/brotli sidecars", "dir", req.AppClientDir, "err", err)
					}
				}
				clientLayer, _, clientRecs, err := BuildDirectoryTreeLayerWithPruning(ctx, req.Platform, req.AppClientDir, ports.AppClientDirPrefix, ts, req.Compression, pruneutils.PruneOptions{NoPrune: true})
				if err != nil {
					return nil, fmt.Errorf("packager: build %s: client layer: %w", req.Platform, err)
				}
				attestRecords = append(attestRecords, clientRecs...)
				addenda = append(addenda, mutate.Addendum{
					Layer:     clientLayer,
					MediaType: layerMediaType,
					History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.AppClientDirPrefix},
				})
			}
		}

		if req.AppVendorDir != "" {
			if info, err := os.Stat(req.AppVendorDir); err == nil && info.IsDir() {
				if !req.NoStrip {
					stripped, skipped, stripErr := striputils.StripDirectory(ctx, req.AppVendorDir, ts)
					p.warnUnstripped(req.AppVendorDir, stripped, skipped, stripErr)
				}
				pruneOpts := pruneutils.PruneOptions{
					NoPrune:       req.NoPrune,
					KeepSourcemap: req.Sourcemap,
					KeepPatterns:  req.KeepVendor,
				}
				vendorLayer, pruned, vendorRecs, err := BuildDirectoryTreeLayerWithPruning(ctx, req.Platform, req.AppVendorDir, ports.AppVendorDirPrefix, ts, req.Compression, pruneOpts)
				if err != nil {
					return nil, fmt.Errorf("packager: build %s: vendor layer: %w", req.Platform, err)
				}
				attestRecords = append(attestRecords, vendorRecs...)
				if pruned.FilesPruned > 0 {
					p.logger().Info("pruned vendor layer junk files",
						"platform", req.Platform.String(),
						"files_pruned", pruned.FilesPruned,
						"bytes_saved", pruned.BytesSaved)
				}
				addenda = append(addenda, mutate.Addendum{
					Layer:     vendorLayer,
					MediaType: layerMediaType,
					History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.AppVendorDirPrefix},
				})
			}
		}

		if req.AppNodeModulesDir != "" {
			if info, err := os.Stat(req.AppNodeModulesDir); err == nil && info.IsDir() {
				// The project's production dependencies, mounted where module
				// resolution actually looks. Pruned like the vendor layer —
				// a node_modules tree carries a lot of files no runtime reads
				// (docs, tests, sourcemaps) — but never stripped, since these
				// are third-party artifacts whose bytes should stay as the
				// lockfile pinned them.
				pruneOpts := pruneutils.PruneOptions{
					NoPrune:       req.NoPrune,
					KeepSourcemap: req.Sourcemap,
					KeepPatterns:  req.KeepVendor,
				}
				nmLayer, pruned, nmRecs, err := BuildDirectoryTreeLayerWithPruning(ctx, req.Platform, req.AppNodeModulesDir, ports.AppNodeModulesDirPrefix, ts, req.Compression, pruneOpts)
				if err != nil {
					return nil, fmt.Errorf("packager: build %s: node_modules layer: %w", req.Platform, err)
				}
				attestRecords = append(attestRecords, nmRecs...)
				if pruned.FilesPruned > 0 {
					p.logger().Info("pruned node_modules layer junk files",
						"platform", req.Platform.String(),
						"files_pruned", pruned.FilesPruned,
						"bytes_saved", pruned.BytesSaved)
				}
				addenda = append(addenda, mutate.Addendum{
					Layer:     nmLayer,
					MediaType: layerMediaType,
					History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.AppNodeModulesDirPrefix},
				})
			}
		}

		if req.AppNativeDir != "" {
			if info, err := os.Stat(req.AppNativeDir); err == nil && info.IsDir() {
				if !req.NoStrip {
					stripped, skipped, stripErr := striputils.StripDirectory(ctx, req.AppNativeDir, ts)
					p.warnUnstripped(req.AppNativeDir, stripped, skipped, stripErr)
				}
				nativeLayer, _, nativeRecs, err := BuildDirectoryTreeLayerWithPruning(ctx, req.Platform, req.AppNativeDir, ports.AppNativeDirPrefix, ts, req.Compression, pruneutils.PruneOptions{NoPrune: true})
				if err != nil {
					return nil, fmt.Errorf("packager: build %s: native layer: %w", req.Platform, err)
				}
				attestRecords = append(attestRecords, nativeRecs...)
				addenda = append(addenda, mutate.Addendum{
					Layer:     nativeLayer,
					MediaType: layerMediaType,
					History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.AppNativeDirPrefix},
				})
			}
		}
		var prerenderedRecs []attestutils.Record
		if addenda, prerenderedRecs, err = appendPrerenderedLayer(ctx, p.logger(), req, ts, layerMediaType, addenda); err != nil {
			return nil, err
		}
		attestRecords = append(attestRecords, prerenderedRecs...)
	} else if req.Strategy.ApplyStatic() {
		// Static images carry no Bun runtime, no server JS and no supervisor:
		// pokkum-static is PID 1 and serves the client + prerendered trees.
		// pokkum-static itself is an immutable embedded binary, so it is
		// pinned like the Bun/supervisor layers above, not derived from ts.
		staticLayer, err := buildStaticServerLayer(ctx, req, pinnedImmutableBinaryEpoch)
		if err != nil {
			return nil, err
		}
		addenda = append(addenda, mutate.Addendum{
			Layer:     staticLayer,
			MediaType: layerMediaType,
			History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.StaticServerPath},
		})

		if req.AppClientDir != "" {
			if info, err := os.Stat(req.AppClientDir); err == nil && info.IsDir() {
				if !req.NoPrecompress {
					// pokkum-static genuinely negotiates zstd, unlike the
					// layered strategy's sirv server — keep all three formats.
					if err := precompressutils.PrecompressDirectory(req.AppClientDir, ts, precompressutils.PrecompressOptions{Gzip: true, Brotli: true, Zstd: true}); err != nil {
						p.logger().Warn("packager: precompressing the client tree failed; the image will ship without some gzip/brotli/zstd sidecars", "dir", req.AppClientDir, "err", err)
					}
				}
				clientLayer, err := BuildDirectoryTreeLayer(ctx, req.Platform, req.AppClientDir, ports.AppClientDirPrefix, ts, req.Compression)
				if err != nil {
					return nil, fmt.Errorf("packager: build %s: client layer: %w", req.Platform, err)
				}
				addenda = append(addenda, mutate.Addendum{
					Layer:     clientLayer,
					MediaType: layerMediaType,
					History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.AppClientDirPrefix},
				})
			}
		}

		if addenda, _, err = appendPrerenderedLayer(ctx, p.logger(), req, ts, layerMediaType, addenda); err != nil {
			return nil, err
		}
	} else {
		// StrategyExe: the supervisor is the same immutable embedded binary
		// as in the layered branch above and is pinned the same way. The
		// compiled application binary is this build's own source content —
		// unlike the layered strategy's Bun runtime, it is produced by
		// compiling THIS build's source with bun build --compile, so it
		// correctly keeps deriving from ts (SOURCE_DATE_EPOCH).
		supervisorLayer, err := buildSupervisorLayer(ctx, req, pinnedImmutableBinaryEpoch)
		if err != nil {
			return nil, err
		}
		appLayer, err := buildAppLayer(ctx, req, ts)
		if err != nil {
			return nil, err
		}
		addenda = append(addenda,
			mutate.Addendum{Layer: supervisorLayer, MediaType: layerMediaType, History: v1.History{Created: v1.Time{Time: ts}, CreatedBy: historySupervisorCreatedBy}},
			mutate.Addendum{Layer: appLayer, MediaType: layerMediaType, History: v1.History{Created: v1.Time{Time: ts}, CreatedBy: historyAppCreatedBy}},
		)
	}

	img, err = mutate.Append(img, addenda...)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: append layers: %w: %w", req.Platform, err, core.ErrPackageFailed)
	}

	// Startup attestation (hardening Option C): stamp the expected digest of the
	// layered /app tree into the image config so pokkum-init can verify it at
	// runtime before exec. The digest is computed from the authoritative
	// post-processing records accumulated while the layers were built (pruning,
	// precompression sidecars and stripping already applied), so it matches
	// exactly what the container will extract — by construction, not
	// re-derivation. attestRecords is empty for the exe and static strategies,
	// which have no /app tree for this check, so this block is a no-op there.
	// The supervisor layer itself is untouched, so its immutable cache is
	// preserved.
	//
	// dedupeAttestRecordsByRel is REQUIRED, not an optimization: the asset
	// overlay layer and the current build's own client layer both target
	// /app/client, and share the same Rel for every file whose content is
	// unchanged between the prior generation and the current build — the
	// ordinary case, since content-hashed filenames only change when their
	// content does. attestutils.RootDigest itself does not dedupe (it just
	// sorts and hashes), so without this step the digest would count such a
	// file's contribution TWICE — while pokkum-init's runtime walk of the
	// real, layer-squashed /app/client tree sees it exactly ONCE, since OCI
	// layers physically merge into one filesystem. That mismatch would make
	// every --asset-overlay image with any unchanged carried-forward asset
	// (i.e. nearly all of them) fail pokkum-init's startup check and refuse
	// to exec — found by this feature's own end-to-end test against a real
	// pushed image, not by any of the narrower unit tests, which happened to
	// use fixtures with no overlapping paths. dedupeAttestRecordsByRel keeps
	// the LAST occurrence of each Rel, matching attestRecords' append order,
	// which itself matches addenda's append order (server, overlay, client,
	// vendor, native, prerendered) — the same order OCI layers apply in, so
	// "last append wins" here reproduces exactly what "later layer overwrites
	// earlier layer at the same path" produces on disk at runtime.
	if len(attestRecords) > 0 {
		dedupedRecords := dedupeAttestRecordsByRel(attestRecords)
		digest := attestutils.RootDigest(dedupedRecords)
		imgCfg, cfgErr := img.ConfigFile()
		if cfgErr != nil {
			return nil, fmt.Errorf("packager: build %s: read config for attestation: %w: %w", req.Platform, cfgErr, core.ErrPackageFailed)
		}
		imgCfg.Config.Env = mergeEnv(imgCfg.Config.Env, []envVar{{ports.EnvAttestationDigest, digest}})
		img, cfgErr = mutate.ConfigFile(img, imgCfg)
		if cfgErr != nil {
			return nil, fmt.Errorf("packager: build %s: stamp attestation digest: %w: %w", req.Platform, cfgErr, core.ErrPackageFailed)
		}
		p.logger().Info("startup attestation digest stamped",
			"platform", req.Platform.String(),
			"files", len(dedupedRecords),
			"digest", digest)
	}

	// Redundant with cfg.Created, which applyRuntime already set, but it is the
	// documented way to pin the timestamp and it also covers the config that
	// mutate.Append recomputed.
	img, err = mutate.CreatedAt(img, v1.Time{Time: ts})
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: set created: %w: %w", req.Platform, err, core.ErrPackageFailed)
	}

	img = mutate.MediaType(img, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)

	if anns := imageAnnotations(cfg.Config.Labels, req.BaseRef, req.Annotations); len(anns) > 0 {
		annotated, ok := mutate.Annotations(img, anns).(v1.Image)
		if !ok {
			return nil, fmt.Errorf("packager: build %s: annotate: result is not an image: %w", req.Platform, core.ErrPackageFailed)
		}
		img = annotated
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", req.Platform, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: compute digest: %w: %w", req.Platform, err, core.ErrPackageFailed)
	}

	p.logger().Info("packaged image",
		"platform", req.Platform.String(),
		"digest", digest.String(),
		"strategy", string(req.Strategy),
		"base_digest", baseDigest.String(),
		"created", ts.Format(time.RFC3339))

	return img, nil
}

// appendPrerenderedLayer appends a standalone /app/prerendered layer to
// addenda when req.AppPrerenderedDir exists, shared by the layered- and
// static-strategy branches of Build, which both ship prerendered pages in
// their own dedicated layer rather than folded into the client/server tree.
// A no-op (returns addenda unchanged) when the directory is unset or absent.
func appendPrerenderedLayer(ctx context.Context, log *slog.Logger, req ports.PackageRequest, ts time.Time, layerMediaType types.MediaType, addenda []mutate.Addendum) ([]mutate.Addendum, []attestutils.Record, error) {
	if req.AppPrerenderedDir == "" {
		return addenda, nil, nil
	}
	info, err := os.Stat(req.AppPrerenderedDir)
	if err != nil || !info.IsDir() {
		return addenda, nil, nil
	}
	if !req.NoPrecompress {
		// Only pokkum-static (--strategy=static) negotiates zstd; the layered
		// strategy's sirv server never does, so skip generating dead bytes.
		opts := precompressutils.PrecompressOptions{Gzip: true, Brotli: true, Zstd: req.Strategy.ApplyStatic()}
		if err := precompressutils.PrecompressDirectory(req.AppPrerenderedDir, ts, opts); err != nil {
			log.Warn("packager: precompressing the prerendered tree failed; the image will ship without some sidecars", "dir", req.AppPrerenderedDir, "err", err)
		}
	}
	prerenderedLayer, _, prerenderedRecs, err := BuildDirectoryTreeLayerWithPruning(ctx, req.Platform, req.AppPrerenderedDir, ports.AppPrerenderedDirPrefix, ts, req.Compression, pruneutils.PruneOptions{NoPrune: true})
	if err != nil {
		return nil, nil, fmt.Errorf("packager: build %s: prerendered layer: %w", req.Platform, err)
	}
	return append(addenda, mutate.Addendum{
		Layer:     prerenderedLayer,
		MediaType: layerMediaType,
		History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.AppPrerenderedDirPrefix},
	}), prerenderedRecs, nil
}

// appendAssetOverlayLayer implements the --asset-overlay rolling-deploy
// feature's packaging half: when req.AssetOverlayDir is set (populated by
// core.Build via internal/adapters/assetoverlay.BuildOverlayDir, a merged
// tree of prior generations' /app/client/_app/immutable content), it becomes
// its own layer at ports.AppClientDirPrefix, alongside — not replacing —
// the current build's own client layer.
//
// No precompression step here, unlike the current build's own client layer:
// AssetOverlayDir's content was extracted verbatim from prior generations'
// already-built layers, .gz/.br sidecars included (precompressutils wrote
// them into the same directory tree at the time, so they're already present
// among the tar entries assetoverlay.ExtractClientImmutableAssets pulled) —
// re-running precompression here would be redundant work over bytes that
// already have it.
//
// The caller MUST fold this function's returned []attestutils.Record into
// attestRecords before RootDigest is computed — see the call site's own
// comment. Skipping that is not a cosmetic omission: pokkum-init
// independently re-derives the same digest from the live /app/client tree
// at startup and refuses to exec on a mismatch, so an overlay layer whose
// files were never counted in the expected digest fails every container
// that ships it, at startup, not at build time.
func appendAssetOverlayLayer(ctx context.Context, req ports.PackageRequest, ts time.Time, layerMediaType types.MediaType, addenda []mutate.Addendum) ([]mutate.Addendum, []attestutils.Record, error) {
	if req.AssetOverlayDir == "" {
		return addenda, nil, nil
	}
	info, err := os.Stat(req.AssetOverlayDir)
	if err != nil || !info.IsDir() {
		return addenda, nil, nil
	}
	overlayLayer, _, overlayRecs, err := BuildDirectoryTreeLayerWithPruning(ctx, req.Platform, req.AssetOverlayDir, ports.AppClientDirPrefix, ts, req.Compression, pruneutils.PruneOptions{NoPrune: true})
	if err != nil {
		return nil, nil, fmt.Errorf("packager: build %s: asset overlay layer: %w", req.Platform, err)
	}
	return append(addenda, mutate.Addendum{
		Layer:     overlayLayer,
		MediaType: layerMediaType,
		History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: ports.HistoryCreatedByAssetOverlay},
	}), overlayRecs, nil
}

// dedupeAttestRecordsByRel collapses records to at most one per Rel, keeping
// the LAST occurrence in records' order. Required whenever more than one
// layer can legitimately target the same in-image path (currently: the
// asset overlay layer and the current build's own client layer both target
// /app/client) — attestutils.RootDigest sorts and hashes every record it's
// given with no dedup of its own, so two records sharing a Rel would count
// that file's contribution to the digest twice, while pokkum-init's runtime
// walk of the real, layer-squashed filesystem sees the file exactly once.
// "Last occurrence wins" is deliberate, not arbitrary: it must match the
// real OCI layer-squash result, where a later-appended layer's file
// physically overwrites an earlier layer's file at the same path — so the
// caller MUST accumulate records in the same order it appends the
// corresponding addenda, or this function's dedup choice would silently
// disagree with what actually ends up on disk at container startup.
func dedupeAttestRecordsByRel(records []attestutils.Record) []attestutils.Record {
	byRel := make(map[string]attestutils.Record, len(records))
	for _, r := range records {
		byRel[r.Rel] = r
	}
	out := make([]attestutils.Record, 0, len(byRel))
	for _, r := range byRel {
		out = append(out, r)
	}
	return out
}

// descriptorPlatform returns the platform to stamp on the index descriptor that
// points at img, the child built for plat.
//
// It is read off the child image's own config rather than synthesized from the
// requested ports.Platform, because those two are not always the same. A base
// image may declare platform fields ports.Platform deliberately does not model
// — gcr.io/distroless' arm64 image declares "variant": "v8", and applyRuntime
// copies the base config's platform fields through verbatim — so a descriptor
// built from ports.Platform's variant-less spelling disagreed with the very
// config it points at. That disagreement is exactly what
// go-containerregistry's pkg/v1/validate compares (with v1.Platform.Equals),
// which is why `crane validate --remote` rejected every multi-architecture
// image this packager produced, on platform[1], with an empty message (gcr's
// validatePlatform has no Variant clause — it checks OSVersion twice — so it
// finds a mismatch it cannot describe).
//
// The requested platform is still enforced rather than trusted away: the
// child's OS and architecture must equal the fan-out key, so a genuinely
// mislabelled child image remains a hard error instead of a silently wrong
// index. Only the fields ports.Platform cannot carry — Variant (unless the
// request pinned one), OSVersion, OSFeatures — are adopted from the config,
// because the config is their only source of truth. Nothing here is
// architecture- or value-specific: "v8" is never named, it is whatever the
// resolved base image happened to declare.
func descriptorPlatform(img v1.Image, plat ports.Platform) (*v1.Platform, error) {
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("packager: index: platform %q: read child config: %w: %w", plat, err, core.ErrPackageFailed)
	}
	// DeepCopy, not the returned pointer: ConfigFile.Platform aliases the
	// config's own OSFeatures slice, and v1.Platform.Equals sorts that slice in
	// place, so handing it out would let a comparison mutate the child image's
	// config.
	declared := cfg.Platform().DeepCopy()
	if declared == nil {
		return nil, fmt.Errorf("packager: index: platform %q: child image config declares no platform: %w", plat, core.ErrPackageFailed)
	}
	if declared.OS != plat.OS || declared.Architecture != plat.Arch {
		return nil, fmt.Errorf("packager: index: platform %q: child image config declares %q: %w", plat, declared.String(), core.ErrUnsupportedPlatform)
	}
	if plat.Variant != "" && declared.Variant != plat.Variant {
		return nil, fmt.Errorf("packager: index: platform %q: child image config declares variant %q: %w", plat, declared.Variant, core.ErrUnsupportedPlatform)
	}
	return declared, nil
}

// Index implements ports.Packager.
//
// The descriptor order is the sorted order of Platform.String(), never Go's map
// iteration order, because that order is serialized into the index manifest and
// therefore into the index digest.
func (p *Packager) Index(ctx context.Context, req ports.IndexRequest) (v1.ImageIndex, error) {
	if len(req.Images) == 0 {
		return nil, fmt.Errorf("packager: index: no images: %w", core.ErrPackageFailed)
	}
	if req.CreatedAt.IsZero() {
		return nil, fmt.Errorf("packager: index: created timestamp is required: %w", core.ErrPackageFailed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: index: %w", err)
	}

	platforms := sortedPlatforms(req.Images)

	adds := make([]mutate.IndexAddendum, 0, len(platforms))
	for _, plat := range platforms {
		if !plat.Supported() {
			return nil, fmt.Errorf("packager: index: platform %q: %w", plat, core.ErrUnsupportedPlatform)
		}
		img := req.Images[plat]
		if img == nil {
			return nil, fmt.Errorf("packager: index: platform %q: nil image: %w", plat, core.ErrPackageFailed)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("packager: index: %w", err)
		}
		descPlat, err := descriptorPlatform(img, plat)
		if err != nil {
			return nil, err
		}
		adds = append(adds, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: descPlat},
		})
	}

	idx := mutate.AppendManifests(empty.Index, adds...)
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)

	if anns := indexAnnotations(req, pinnedTime(req.CreatedAt)); len(anns) > 0 {
		annotated, ok := mutate.Annotations(idx, anns).(v1.ImageIndex)
		if !ok {
			return nil, fmt.Errorf("packager: index: annotate: result is not an index: %w", core.ErrPackageFailed)
		}
		idx = annotated
	}

	digest, err := idx.Digest()
	if err != nil {
		return nil, fmt.Errorf("packager: index: compute digest: %w: %w", err, core.ErrPackageFailed)
	}

	p.logger().Info("packaged index",
		"digest", digest.String(),
		"platforms", platformList(platforms),
		"created", req.CreatedAt.UTC().Format(time.RFC3339))

	return idx, nil
}

// validatePackageRequest rejects a request that cannot produce a working image,
// before any file is opened or any layer is compressed.
func validatePackageRequest(req ports.PackageRequest) error {
	if !req.Platform.Supported() {
		return fmt.Errorf("packager: platform %q: %w (supported: %s)",
			req.Platform, core.ErrUnsupportedPlatform, platformList(ports.SupportedPlatforms))
	}
	if req.Base == nil {
		return fmt.Errorf("packager: build %s: base image is required: %w", req.Platform, core.ErrPackageFailed)
	}
	switch req.Strategy {
	case ports.StrategyLayered:
		switch effectiveAppRuntime(req.AppRuntime) {
		case ports.RuntimeBun:
			if req.BunRuntime.BinaryPath == "" {
				return fmt.Errorf("packager: build %s: bun runtime binary path is required for layered strategy: %w", req.Platform, core.ErrPackageFailed)
			}
		case ports.RuntimeNode:
			// No embedded runtime binary to check — the base image provides
			// Node at ports.NodeBinaryPath. But the telemetry preload
			// mechanism is `bun --preload` of a TypeScript file, which Node
			// can execute neither half of; core's validation already rejects
			// the combination, and this second, independent check keeps a
			// future caller from packaging an image whose entrypoint would
			// crash at startup (belt-and-suspenders, same discipline as
			// checklist row 22's dual containment checks).
			if req.TelemetryPreloadRelPath != "" {
				return fmt.Errorf("packager: build %s: telemetry preload is Bun-specific and cannot be packaged into a node-runtime image: %w", req.Platform, core.ErrPackageFailed)
			}
		default:
			return fmt.Errorf("packager: build %s: unhandled app runtime %q: %w", req.Platform, req.AppRuntime, core.ErrPackageFailed)
		}
		if req.AppServerDir == "" {
			return fmt.Errorf("packager: build %s: application server directory is required for layered strategy: %w", req.Platform, core.ErrPackageFailed)
		}
		if len(req.Supervisor) == 0 {
			return fmt.Errorf("packager: build %s: supervisor binary is empty: %w", req.Platform, core.ErrSupervisorUnavailable)
		}
	case ports.StrategyStatic:
		if len(req.StaticServer) == 0 {
			return fmt.Errorf("packager: build %s: static server binary is empty: %w", req.Platform, core.ErrStaticServerUnavailable)
		}
		// A static image needs the pre-rendered client tree. Client is optional
		// in principle (an API-only app could prerender everything), so only the
		// prerendered tree is required; without it there is nothing to serve.
		if req.AppPrerenderedDir == "" {
			return fmt.Errorf("packager: build %s: prerendered directory is required for static strategy: %w", req.Platform, core.ErrPackageFailed)
		}
	default: // StrategyExe
		if req.App.Path == "" {
			return fmt.Errorf("packager: build %s: application binary path is required: %w", req.Platform, core.ErrPackageFailed)
		}
		if !req.App.Platform.IsZero() && req.App.Platform != req.Platform {
			return fmt.Errorf("packager: build %s: application binary is for %s: %w",
				req.Platform, req.App.Platform, core.ErrPackageFailed)
		}
		if len(req.Supervisor) == 0 {
			return fmt.Errorf("packager: build %s: supervisor binary is empty: %w", req.Platform, core.ErrSupervisorUnavailable)
		}
	}
	if req.CreatedAt.IsZero() {
		return fmt.Errorf("packager: build %s: created timestamp is required: %w", req.Platform, core.ErrPackageFailed)
	}
	return nil
}

// effectiveAppRuntime maps the zero AppRuntime to ports.DefaultAppRuntime.
// The zero value must keep meaning "bun" inside this adapter — every
// pre-existing caller (and every test built before the runtime dimension
// existed) constructs PackageRequest without the field, and treating that as
// anything but the historical behavior would silently change what they
// package. core.Build always normalises the field before it gets here; this
// is for the direct-construction callers.
func effectiveAppRuntime(r ports.AppRuntime) ports.AppRuntime {
	if r == "" {
		return ports.DefaultAppRuntime
	}
	return r
}

// pinnedTime normalises a build timestamp to UTC whole seconds.
//
// The truncation is load-bearing twice over. A sub-second component in a tar
// header forces archive/tar to emit a PAX extended header carrying the full
// mtime, which is exactly the per-entry variable-length record this package is
// trying to avoid; and SOURCE_DATE_EPOCH is defined in whole seconds, so any
// nanoseconds present came from a clock read somewhere upstream and are noise.
func pinnedTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// sortedPlatforms returns the keys of an images map in a fixed order.
//
// This is the single most dangerous map in the package: ranging over it
// directly would produce index manifests whose descriptor order changed between
// runs on the same inputs, which changes the index digest, which breaks
// reproducibility in the least obvious way possible — the images would all be
// identical and only the thing tying them together would differ.
func sortedPlatforms(images map[ports.Platform]v1.Image) []ports.Platform {
	out := slices.Collect(maps.Keys(images))
	slices.SortFunc(out, func(a, b ports.Platform) int {
		return strings.Compare(a.String(), b.String())
	})
	return out
}

// platformList renders platforms for a single log field or error message.
func platformList(ps []ports.Platform) string {
	ss := make([]string, len(ps))
	for i, p := range ps {
		ss[i] = p.String()
	}
	return strings.Join(ss, ",")
}

// logger returns the effective logger, defensively covering a zero-value
// Packager built without NewPackager.
func (p *Packager) logger() *slog.Logger {
	if p.log == nil {
		return slog.Default()
	}
	return p.log
}

// warnUnstripped surfaces the result of a striputils.StripDirectory call.
// Stripping is a best-effort size optimization, not a correctness or
// security gate, so a host without a working ELF strip tool (e.g. plain
// macOS, where the built-in `strip` is Mach-O-only) must not fail the
// build — but it must not fail *silently* either, which is what happened
// before this warning existed: the caller discarded both the count and the
// error, so native addons were shipped completely unstripped with no
// signal that the feature had quietly done nothing.
func (p *Packager) warnUnstripped(dir string, stripped int, skipped []string, err error) {
	if len(skipped) > 0 {
		p.logger().Warn("native binaries left unstripped: no working ELF strip tool found",
			"dir", dir,
			"strippedCount", stripped,
			"skippedCount", len(skipped),
			"skipped", skipped,
			"reason", err,
		)
		return
	}
	if err != nil {
		p.logger().Warn("strip directory walk failed", "dir", dir, "err", err)
	}
}
