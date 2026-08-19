package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Write implements ports.TarballWriter.
//
// The archive is built in a temp file in the same directory as req.Path and
// renamed into place only once tarball.MultiWriteToFile has fully succeeded,
// so a run interrupted mid-write (ctrl-C, OOM-kill, disk full) leaves either
// the previous archive untouched or nothing at all — never a truncated file
// at req.Path that a later `docker load` would accept and then fail on.
func (a *Adapter) Write(ctx context.Context, req ports.TarballRequest) (ports.PublishResult, error) {
	if strings.TrimSpace(req.Path) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball: path is required: %w", core.ErrTarballFailed)
	}
	if strings.TrimSpace(req.Repo) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: repository is required: %w", req.Path, core.ErrTarballFailed)
	}
	if err := validatePayload(req.Payload); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: %w: %w", req.Path, err, core.ErrTarballFailed)
	}
	if err := ctx.Err(); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: %w", req.Path, err)
	}

	repo, err := name.NewRepository(req.Repo, nameOptions(false)...)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball: parse repository %q: %w: %w", req.Repo, err, core.ErrTarballFailed)
	}

	tags := tagsOrDefault(req.Tags)

	dir := filepath.Dir(req.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: create directory: %w: %w", req.Path, err, core.ErrTarballFailed)
	}

	var (
		tagToImage map[name.Tag]v1.Image
		written    []string
		digest     v1.Hash
	)
	if req.Payload.Index != nil {
		tagToImage, written, err = flattenIndexTags(repo, req.Payload.Index, tags)
		if err != nil {
			return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: %w: %w", req.Path, err, core.ErrTarballFailed)
		}
		digest, err = req.Payload.Index.Digest()
	} else {
		tagToImage = make(map[name.Tag]v1.Image, len(tags))
		for _, t := range tags {
			tagToImage[repo.Tag(t)] = req.Payload.Image
		}
		written = append([]string(nil), tags...)
		digest, err = req.Payload.Image.Digest()
	}
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: read digest: %w: %w", req.Path, err, core.ErrTarballFailed)
	}

	tmpPath, err := reserveTempFile(dir, filepath.Base(req.Path))
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: create temp file: %w: %w", req.Path, err, core.ErrTarballFailed)
	}
	// Only fires on the error paths below: a successful run renames tmpPath
	// onto req.Path first, after which this is a harmless no-op (the file no
	// longer exists under the temp name).
	defer os.Remove(tmpPath)

	if err := tarball.MultiWriteToFile(tmpPath, tagToImage); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: %w: %w", req.Path, err, core.ErrTarballFailed)
	}

	if err := os.Rename(tmpPath, req.Path); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: tarball %s: finalize archive: %w: %w", req.Path, err, core.ErrTarballFailed)
	}

	size, err := payloadSize(req.Payload)
	if err != nil {
		a.logger().Debug("tarball: could not compute size", "path", req.Path, "err", err)
	}

	// tagToImage's values are exactly what tarball.MultiWriteToFile just
	// wrote, one manifest.json entry per image — and, per the docker-save
	// format, with no annotations field at all. Warn here, once per distinct
	// image actually written, naming precisely which annotation keys were
	// just dropped.
	warnDroppedAnnotations(a.logger(), "tarball", imagesOf(tagToImage)...)

	a.logger().Info("wrote tarball", "path", req.Path, "repo", req.Repo, "digest", digest.String(), "tags", written)

	return ports.PublishResult{
		Ref:    repo.Name() + "@" + digest.String(),
		Digest: digest,
		Tags:   written,
		Path:   req.Path,
		Size:   size,
	}, nil
}

// reserveTempFile creates an empty, uniquely-named file in dir derived from
// base and returns its path, closing the handle immediately. tarball.
// MultiWriteToFile opens the path itself (os.Create, which truncates), so
// this only needs to reserve a collision-free name; the actual archive bytes
// land there in the caller's very next step.
func reserveTempFile(dir, base string) (string, error) {
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if cerr := f.Close(); cerr != nil {
		os.Remove(path)
		return "", cerr
	}
	return path, nil
}

// imagesOf returns the v1.Image values of m. A single-image write maps every
// tag to the same v1.Image; a flattened index maps distinct tags to distinct
// per-platform images. Either way this is exactly the set of manifests
// tarball.MultiWriteToFile writes, so it is the correct input for
// warnDroppedAnnotations: nothing more (a tag actually written), nothing
// less (every platform, not just one).
func imagesOf(m map[name.Tag]v1.Image) []v1.Image {
	out := make([]v1.Image, 0, len(m))
	for _, img := range m {
		out = append(out, img)
	}
	return out
}

// flattenIndexTags expands a multi-platform index into one tar entry per
// (platform, tag) pair.
//
// go-containerregistry's tarball package is the docker-save-compatible
// legacy format, not a true OCI Image Layout, and it has no representation
// for a manifest list — a map[name.Tag]v1.Image simply cannot express "this
// tag names several platforms at once". The chosen trade-off keeps every
// platform in the one archive rather than dropping any: tag "latest" becomes
// "latest-linux-amd64", "latest-linux-arm64", and so on, each its own
// manifest.json entry. This differs from Load's platform selection —
// pick-one-and-log-the-rest — because a tarball has no "current daemon
// platform" to prefer and nothing is saved by discarding data that costs
// nothing to keep on disk.
func flattenIndexTags(repo name.Repository, idx v1.ImageIndex, tags []string) (map[name.Tag]v1.Image, []string, error) {
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, nil, fmt.Errorf("read index manifest: %w", err)
	}

	tagToImage := make(map[name.Tag]v1.Image)
	var written []string
	for _, d := range im.Manifests {
		if d.Platform == nil || d.Platform.OS == "unknown" || d.Platform.Architecture == "unknown" {
			continue // attestation/provenance manifest, not a runnable platform
		}
		p, ok := ports.PlatformFromV1(d.Platform)
		if !ok {
			continue
		}
		img, err := idx.Image(d.Digest)
		if err != nil {
			return nil, nil, fmt.Errorf("fetch %s child %s: %w", p, d.Digest, err)
		}
		suffix := strings.ReplaceAll(p.String(), "/", "-")
		for _, t := range tags {
			tagStr := t + "-" + suffix
			tagToImage[repo.Tag(tagStr)] = img
			written = append(written, tagStr)
		}
	}
	if len(tagToImage) == 0 {
		return nil, nil, fmt.Errorf("index has no platform manifests to write")
	}
	return tagToImage, written, nil
}
