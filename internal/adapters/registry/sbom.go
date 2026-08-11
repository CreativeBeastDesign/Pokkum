package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// AttachSBOM implements ports.Registry.
//
// It publishes the SBOM as a single-layer image, tagged per the cosign/ko
// convention (ports.SBOMTag: the subject digest's algorithm and hex joined by
// '-', suffixed ".sbom"). This is a tag, not an OCI 1.1 referrers-API entry —
// deliberately, per the package doc's v0.2 note — because a tag is readable
// by every registry, including ones that predate the referrers API.
func (a *Adapter) AttachSBOM(ctx context.Context, req ports.AttachSBOMRequest) (ports.PublishResult, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: attach sbom: %w", core.ErrNoDockerRepo)
	}
	if req.Subject.Hex == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: attach sbom %s: no subject digest: %w", req.Repo, core.ErrPushFailed)
	}
	if len(req.Document.Content) == 0 {
		return ports.PublishResult{}, fmt.Errorf("registry: attach sbom %s: empty document: %w", req.Repo, core.ErrPushFailed)
	}
	if req.Document.MediaType == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: attach sbom %s: document has no media type: %w", req.Repo, core.ErrPushFailed)
	}

	repo, err := name.NewRepository(req.Repo, nameOptions(req.Insecure)...)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach sbom: parse repository %q: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	img, err := sbomImage(req.Document)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach sbom %s: build sbom image: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	opts := remoteOptions(ctx, req.Insecure, "")

	attachMode := req.AttachMode
	if attachMode == "" {
		attachMode = ports.DefaultSBOMAttachMode
	}

	if attachMode == ports.SBOMAttachTag {
		tagStr := ports.SBOMTag(req.Subject)
		tagRef := repo.Tag(tagStr)

		if err := remote.Write(tagRef, img, opts...); err != nil {
			return ports.PublishResult{}, classifyPushErr(req.Repo, err)
		}

		digest, err := img.Digest()
		if err != nil {
			return ports.PublishResult{}, fmt.Errorf("registry: attach sbom %s: read pushed digest: %w: %w", req.Repo, err, core.ErrPushFailed)
		}
		size, err := manifestSize(img)
		if err != nil {
			a.logger().Debug("attach sbom: could not compute transferred size", "repo", req.Repo, "err", err)
		}

		a.logger().Info("attached sbom (tag mode)", "repo", req.Repo, "subject", req.Subject.String(), "tag", tagStr, "digest", digest.String())

		return ports.PublishResult{
			Ref:    repo.Name() + ":" + tagStr,
			Digest: digest,
			Tags:   []string{tagStr},
			Size:   size,
		}, nil
	}

	// Referrer mode (OCI 1.1)
	img = mutate.Subject(img, v1.Descriptor{Digest: req.Subject}).(v1.Image)

	digest, err := img.Digest()
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach sbom %s: read image digest: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	digestRef := repo.Digest(digest.String())
	if err := remote.Write(digestRef, img, opts...); err != nil {
		return ports.PublishResult{}, classifyPushErr(req.Repo, err)
	}

	size, err := manifestSize(img)
	if err != nil {
		a.logger().Debug("attach sbom: could not compute transferred size", "repo", req.Repo, "err", err)
	}

	a.logger().Info("attached sbom (referrer mode)", "repo", req.Repo, "subject", req.Subject.String(), "digest", digest.String())

	return ports.PublishResult{
		Ref:    digestRef.Name(),
		Digest: digest,
		Tags:   nil,
		Size:   size,
	}, nil
}

// sbomImage wraps an SBOM document as a single-layer, single-platform image:
// one uncompressed layer holding the document bytes verbatim, media-typed per
// doc.MediaType. static.NewLayer computes the layer's digest and diff-ID
// directly from the bytes (no compression step), matching what `cosign
// download sbom` expects to unwrap.
func sbomImage(doc ports.SBOMDocument) (v1.Image, error) {
	layer := static.NewLayer(doc.Content, types.MediaType(doc.MediaType))
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return nil, fmt.Errorf("append sbom layer: %w", err)
	}
	return img, nil
}
