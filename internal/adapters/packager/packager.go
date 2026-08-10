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
// # One layer, not two
//
// The supervisor and the application share a single layer. ports.PackageRequest
// documents them as two layers, on the reasoning that the supervisor changes
// only when pokkum itself is upgraded and so an app rebuild would re-push one
// layer instead of two. In practice the split saves 2.2 MB out of ~92 MB on a
// rebuild — under 3% — while costing a second layer descriptor, a second
// history entry and a second gzip pass on every build. W10 specifies the
// combined layer; this comment exists so the divergence from the port's prose
// is a recorded decision rather than an oversight. Splitting it again is a
// contained change: add a second layerFile set and a second mutate.Addendum.
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
	"slices"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// historyCreatedBy is the CreatedBy string of the single history entry this
// package appends. It is a fixed string rather than anything derived from the
// running process — a version, a command line, a hostname — because the history
// is serialized into the image config and therefore into the config digest.
const historyCreatedBy = "pokkum: add " + ports.SupervisorPath + " and " + ports.AppBinaryPath

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
// It appends exactly one layer to req.Base — the application binary and the
// supervisor together — applies the runtime configuration, stamps the OCI
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

	rc := req.Runtime.WithDefaults()
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

	// Building the layer computes both the diffID and the compressed digest,
	// which means gzipping ~90 MB. It is the expensive step, so the context is
	// checked either side of it.
	layer, err := buildAppLayer(ctx, req, ts)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", req.Platform, err)
	}

	cfg := applyRuntime(baseCfg, rc, req, baseDigest, ts)

	img, err := mutate.ConfigFile(req.Base, cfg)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: apply config: %w: %w", req.Platform, err, core.ErrPackageFailed)
	}

	img, err = mutate.Append(img, mutate.Addendum{
		Layer:     layer,
		MediaType: types.OCILayer,
		History: v1.History{
			Created:   v1.Time{Time: ts},
			CreatedBy: historyCreatedBy,
			Comment:   "",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: append layer: %w: %w", req.Platform, err, core.ErrPackageFailed)
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

	if anns := imageAnnotations(cfg.Config.Labels, req.Annotations); len(anns) > 0 {
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
	layerDigest, err := layer.Digest()
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: compute layer digest: %w: %w", req.Platform, err, core.ErrPackageFailed)
	}

	p.logger().Info("packaged image",
		"platform", req.Platform.String(),
		"digest", digest.String(),
		"layer", layerDigest.String(),
		"base_digest", baseDigest.String(),
		"created", ts.Format(time.RFC3339))

	return img, nil
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
