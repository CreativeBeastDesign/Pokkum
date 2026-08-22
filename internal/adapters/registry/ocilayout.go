package registry

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// The two annotation keys written onto each index.json descriptor, naming
// the image the descriptor stands for. Two keys rather than one because the
// consumers this output mode exists to serve do not agree on which to read,
// and writing only one of them makes the layout unusable for half of them:
//
//   - annotationRefName is the OCI image-spec key. skopeo's `oci:` transport
//     and podman resolve `oci:/path:latest` by matching it, and they match
//     against the *bare tag*, not a full repository reference.
//   - annotationContainerdImageName is containerd's own key. containerd's
//     archive importer (`ctr images import`, and therefore `k3d image
//     import` / `minikube image load` / any kubelet-adjacent path) reads it
//     first and falls back to annotationRefName only if it is absent — and
//     it wants a *fully qualified* reference, since it becomes the image
//     name in the containerd namespace.
//
// Both are spec/implementation-pinned strings; neither is Pokkum vocabulary.
const (
	annotationRefName             = "org.opencontainers.image.ref.name"
	annotationContainerdImageName = "io.containerd.image.name"
)

// WriteOCILayout implements ports.OCILayoutWriter.
//
// # Why this mode exists beyond "another way to write a file"
//
// Write (tarball.go) emits go-containerregistry's legacy docker-save format,
// whose manifest entry carries only Config, RepoTags, Layers and
// LayerSources — there is no annotations field in the format at all, and no
// representation of a manifest list either. So `--tarball` silently discards
// every annotation Pokkum stamps (pokkum.dev/predecessor,
// pokkum.dev/asset-overlay-sources, pokkum.dev/vex-exemptions,
// pokkum.dev/env-baked, the whole org.opencontainers.image.* set) and
// flattens a multi-platform build into one platform-suffixed tag per child.
// An OCI image layout has first-class support for both, so this mode is the
// lossless local output: it is the only way to get a fully-annotated,
// genuinely multi-platform artefact onto disk without a registry.
//
// # Layout shape
//
// index.json holds one descriptor per requested tag, each pointing at the
// payload's own top-level blob — the real multi-platform index for an index
// payload, the manifest for a single image — annotated with the image's name
// (see the annotation constants above). This is the shape `crane pull
// --format=oci` produces and the one every consumer of an `oci-layout`
// directory expects; writing the payload index *as* index.json instead would
// leave nowhere to record the tag.
//
// # Atomic replacement
//
// The layout is assembled in a staging directory alongside req.Path and
// swapped into place only once it is complete, for the same reason Write
// builds its archive in a temp file: a run interrupted mid-write must leave
// either the previous layout untouched or nothing at all, never a directory
// with an index.json referencing blobs that were never written — which a
// cluster import would accept as a directory and then fail on. The swap also
// makes replacement wholesale rather than additive, so re-running a build
// into the same directory cannot accumulate orphaned blobs from every
// earlier run.
func (a *Adapter) WriteOCILayout(ctx context.Context, req ports.OCILayoutRequest) (ports.PublishResult, error) {
	if strings.TrimSpace(req.Path) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout: path is required: %w", core.ErrTarballFailed)
	}
	if strings.TrimSpace(req.Repo) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: repository is required: %w", req.Path, core.ErrTarballFailed)
	}
	if err := validatePayload(req.Payload); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: %w: %w", req.Path, err, core.ErrTarballFailed)
	}
	if err := ctx.Err(); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: %w", req.Path, err)
	}

	repo, err := name.NewRepository(req.Repo, nameOptions(false)...)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout: parse repository %q: %w: %w", req.Repo, err, core.ErrTarballFailed)
	}

	tags := tagsOrDefault(req.Tags)

	dest, err := filepath.Abs(req.Path)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: resolve path: %w: %w", req.Path, err, core.ErrTarballFailed)
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: create directory: %w: %w", dest, err, core.ErrTarballFailed)
	}

	staging, err := os.MkdirTemp(parent, "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: create staging directory: %w: %w", dest, err, core.ErrTarballFailed)
	}
	// Only fires on the error paths below and on the successful path's
	// leftovers: swapLayoutDir renames staging onto dest, after which this is
	// a harmless no-op (the directory no longer exists under the temp name).
	defer func() { _ = os.RemoveAll(staging) }()

	lp, err := layout.Write(staging, empty.Index)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: initialise layout: %w: %w", dest, err, core.ErrTarballFailed)
	}

	for _, t := range tags {
		if err := appendTagged(lp, repo.Tag(t), t, req.Payload); err != nil {
			return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: write tag %q: %w: %w", dest, t, err, core.ErrTarballFailed)
		}
	}

	var digest v1.Hash
	if req.Payload.Index != nil {
		digest, err = req.Payload.Index.Digest()
	} else {
		digest, err = req.Payload.Image.Digest()
	}
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: read digest: %w: %w", dest, err, core.ErrTarballFailed)
	}

	if err := swapLayoutDir(staging, dest); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: oci layout %s: finalize layout: %w: %w", dest, err, core.ErrTarballFailed)
	}

	size, err := payloadSize(req.Payload)
	if err != nil {
		a.logger().Debug("oci layout: could not compute size", "path", dest, "err", err)
	}

	// Deliberately no warnDroppedAnnotations call here, unlike Write and
	// Load: an OCI image layout preserves every annotation byte-for-byte, so
	// there is nothing to warn about. A warning fired here would be actively
	// wrong, and TestOCILayout_AnnotationsSurvive_TarballLosesThem asserts
	// its absence rather than leaving that to a reader of this comment.

	a.logger().Info("wrote oci image layout",
		"path", dest, "repo", req.Repo, "digest", digest.String(), "tags", tags,
		"multi_platform", req.Payload.Index != nil)

	return ports.PublishResult{
		Ref:    repo.Name() + "@" + digest.String(),
		Digest: digest,
		Tags:   append([]string(nil), tags...),
		Path:   dest,
		Size:   size,
	}, nil
}

// appendTagged writes the payload into lp and adds one index.json descriptor
// for it, annotated so a consumer can resolve the layout back to a named
// image.
//
// The blob write is content-addressed and idempotent, so appending the same
// payload under several tags costs one copy of the bytes and N descriptors —
// which is exactly what a registry holding the same digest under several
// tags looks like.
func appendTagged(lp layout.Path, ref name.Tag, tag string, payload ports.Payload) error {
	opts := []layout.Option{layout.WithAnnotations(map[string]string{
		annotationRefName:             tag,
		annotationContainerdImageName: ref.Name(),
	})}

	if payload.Index != nil {
		// No layout.WithPlatform for an index: the platform belongs on each
		// of the index's own children, which the index blob already carries,
		// and putting one on the index descriptor would claim the whole
		// multi-platform artefact is a single platform.
		return lp.AppendIndex(payload.Index, opts...)
	}

	// A single-image descriptor, by contrast, has no child to carry the
	// platform, so read it off the image's own config rather than leaving
	// the descriptor platform-less — that is what lets a consumer pick this
	// layout up for the right architecture. partial.Descriptor (which
	// AppendImage uses) never populates Platform on its own.
	if p, ok := imagePlatform(payload.Image); ok {
		opts = append(opts, layout.WithPlatform(p))
	}
	return lp.AppendImage(payload.Image, opts...)
}

// imagePlatform reads an image's real OS/architecture off its own config
// file. It reports false — rather than an error — when the config cannot be
// read or names no platform, because a missing platform on a descriptor is a
// loss of discoverability, not a corrupt layout, and failing the whole write
// over it would be disproportionate.
func imagePlatform(img v1.Image) (v1.Platform, bool) {
	cfg, err := img.ConfigFile()
	if err != nil || cfg == nil {
		return v1.Platform{}, false
	}
	if cfg.OS == "" || cfg.Architecture == "" {
		return v1.Platform{}, false
	}
	return v1.Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant}, true
}

// swapLayoutDir moves the fully-written staging directory onto dest,
// replacing whatever was there.
//
// An existing dest is moved aside first rather than deleted first: os.Rename
// cannot replace a non-empty directory, and removing dest up front would
// mean a failure of the subsequent rename leaves the caller with neither the
// old layout nor the new one. Moving aside keeps a rollback available right
// up to the last operation that can fail.
func swapLayoutDir(staging, dest string) error {
	backup := ""
	switch _, err := os.Lstat(dest); {
	case err == nil:
		b, err := os.MkdirTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".old-*")
		if err != nil {
			return fmt.Errorf("reserve backup name: %w", err)
		}
		// MkdirTemp reserves a collision-free name by creating the directory;
		// os.Rename needs that name free (a directory-onto-directory rename
		// is not portable), so drop it immediately and use the name alone.
		if err := os.Remove(b); err != nil {
			return fmt.Errorf("reserve backup name: %w", err)
		}
		if err := os.Rename(dest, b); err != nil {
			return fmt.Errorf("move existing layout aside: %w", err)
		}
		backup = b
	case errors.Is(err, fs.ErrNotExist):
		// Nothing to replace.
	default:
		return fmt.Errorf("inspect destination: %w", err)
	}

	if err := os.Rename(staging, dest); err != nil {
		if backup != "" {
			// Best effort: put the previous layout back so a failed write
			// leaves the caller exactly as it found them.
			_ = os.Rename(backup, dest)
		}
		return err
	}

	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}
