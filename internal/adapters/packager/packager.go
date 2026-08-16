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
//     SOURCE_DATE_EPOCH or the git commit) is the mtime of every tar entry, the
//     image config's created field, and the created field of the history entry
//     this package adds. time.Now is never called; the package does not import
//     time for anything but type declarations and Truncate.
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
	"slices"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"

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
		if req.Runtime.Entrypoint == nil {
			req.Runtime.Entrypoint = ports.DefaultLayeredEntrypoint()
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
	} else if req.Strategy == ports.StrategyLayered && req.AppPrerenderedDir != "" {
		// Let the patched adapter-node handler locate the prerendered tree Pokkum
		// mounts at /app/prerendered (see Prepare and the fan-out in core). The
		// stock handler would otherwise look for it under /app/server/prerendered,
		// where it no longer lives.
		if rc.Env == nil {
			rc.Env = map[string]string{}
		}
		rc.Env[ports.EnvPrerenderedDir] = ports.AppPrerenderedDirPrefix
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

	layerMediaType := types.OCILayer
	if req.Compression.Normalize() == ports.CompressionZstd {
		layerMediaType = types.OCILayerZStd
	}

	if req.Strategy == ports.StrategyLayered {
		bunLayer, err := BuildCustomFileLayer(ctx, req.Platform, ports.BunBinaryPath, req.BunRuntime.BinaryPath, ts, req.Compression)
		if err != nil {
			return nil, fmt.Errorf("packager: build %s: bun layer: %w", req.Platform, err)
		}
		supervisorLayer, err := buildSupervisorLayer(ctx, req, ts)
		if err != nil {
			return nil, err
		}
		serverLayer, err := BuildDirectoryTreeLayer(ctx, req.Platform, req.AppServerDir, "/app/server", ts, req.Compression)
		if err != nil {
			return nil, fmt.Errorf("packager: build %s: server layer: %w", req.Platform, err)
		}

		addenda = append(addenda,
			mutate.Addendum{Layer: bunLayer, MediaType: layerMediaType, History: v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.BunBinaryPath}},
			mutate.Addendum{Layer: supervisorLayer, MediaType: layerMediaType, History: v1.History{Created: v1.Time{Time: ts}, CreatedBy: historySupervisorCreatedBy}},
			mutate.Addendum{Layer: serverLayer, MediaType: layerMediaType, History: v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add /app/server"}},
		)

		if req.AppClientDir != "" {
			if info, err := os.Stat(req.AppClientDir); err == nil && info.IsDir() {
				if !req.NoPrecompress {
					_ = precompressutils.PrecompressDirectory(req.AppClientDir, ts)
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
				vendorLayer, pruned, err := BuildDirectoryTreeLayerWithPruning(ctx, req.Platform, req.AppVendorDir, ports.AppVendorDirPrefix, ts, req.Compression, pruneOpts)
				if err != nil {
					return nil, fmt.Errorf("packager: build %s: vendor layer: %w", req.Platform, err)
				}
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

		if req.AppNativeDir != "" {
			if info, err := os.Stat(req.AppNativeDir); err == nil && info.IsDir() {
				if !req.NoStrip {
					stripped, skipped, stripErr := striputils.StripDirectory(ctx, req.AppNativeDir, ts)
					p.warnUnstripped(req.AppNativeDir, stripped, skipped, stripErr)
				}
				nativeLayer, err := BuildDirectoryTreeLayer(ctx, req.Platform, req.AppNativeDir, ports.AppNativeDirPrefix, ts, req.Compression)
				if err != nil {
					return nil, fmt.Errorf("packager: build %s: native layer: %w", req.Platform, err)
				}
				addenda = append(addenda, mutate.Addendum{
					Layer:     nativeLayer,
					MediaType: layerMediaType,
					History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.AppNativeDirPrefix},
				})
			}
		}
		if addenda, err = appendPrerenderedLayer(ctx, req, ts, layerMediaType, addenda); err != nil {
			return nil, err
		}
	} else if req.Strategy.ApplyStatic() {
		// Static images carry no Bun runtime, no server JS and no supervisor:
		// pokkum-static is PID 1 and serves the client + prerendered trees.
		staticLayer, err := buildStaticServerLayer(ctx, req, ts)
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
					_ = precompressutils.PrecompressDirectory(req.AppClientDir, ts)
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

		if addenda, err = appendPrerenderedLayer(ctx, req, ts, layerMediaType, addenda); err != nil {
			return nil, err
		}
	} else {
		supervisorLayer, err := buildSupervisorLayer(ctx, req, ts)
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
func appendPrerenderedLayer(ctx context.Context, req ports.PackageRequest, ts time.Time, layerMediaType types.MediaType, addenda []mutate.Addendum) ([]mutate.Addendum, error) {
	if req.AppPrerenderedDir == "" {
		return addenda, nil
	}
	info, err := os.Stat(req.AppPrerenderedDir)
	if err != nil || !info.IsDir() {
		return addenda, nil
	}
	if !req.NoPrecompress {
		_ = precompressutils.PrecompressDirectory(req.AppPrerenderedDir, ts)
	}
	prerenderedLayer, err := BuildDirectoryTreeLayer(ctx, req.Platform, req.AppPrerenderedDir, ports.AppPrerenderedDirPrefix, ts, req.Compression)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: prerendered layer: %w", req.Platform, err)
	}
	return append(addenda, mutate.Addendum{
		Layer:     prerenderedLayer,
		MediaType: layerMediaType,
		History:   v1.History{Created: v1.Time{Time: ts}, CreatedBy: "pokkum: add " + ports.AppPrerenderedDirPrefix},
	}), nil
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
		adds = append(adds, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				// Set explicitly rather than left to be derived from each
				// image's config, so that a mislabelled child image is a
				// visible inconsistency rather than a silently wrong index.
				Platform: plat.ToV1(),
			},
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
		if req.BunRuntime.BinaryPath == "" {
			return fmt.Errorf("packager: build %s: bun runtime binary path is required for layered strategy: %w", req.Platform, core.ErrPackageFailed)
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
