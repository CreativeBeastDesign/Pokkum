package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// referrersUnsupportedSubstring is the (unwrapped, un-sentineled) error text
// go-containerregistry's remote.Write returns when a subject-bearing push is
// attempted against a registry that doesn't support the OCI 1.1 Referrers
// API and remote.WithReferrersTagFallback(false) was set — see that
// function's doc comment in go-containerregistry@v0.21.9's
// pkg/v1/remote/options.go. There is no typed/sentinel error for this in the
// pinned version, so detection is a substring match against this exact,
// version-pinned message.
const referrersUnsupportedSubstring = "does not support the Referrers API"

// AttachSBOM implements ports.Registry.
//
// Three attach modes (ports.SBOMAttachMode):
//   - "tag": always the legacy .sbom tag convention (ports.SBOMTag) — readable
//     by every registry, including ones that predate OCI 1.1.
//   - "referrer": always the OCI 1.1 Referrers API, explicitly, with no
//     silent fallback — remote.WithReferrersTagFallback(false) is set, so an
//     unsupported registry fails loudly instead of go-containerregistry's own
//     default fallback tag scheme ("sha256-<hex>", no ".sbom" suffix — a
//     genuinely different tag than ports.SBOMTag, invisible to `cosign
//     download sbom` and to this adapter's own tag-mode read path). A caller
//     that explicitly asked for referrer mode gets the mode it asked for, or
//     a clear error, never a silent third outcome.
//   - "auto" (default): tries referrer mode the same way, and on exactly the
//     "unsupported" failure, falls back to this adapter's own tag mode (not
//     go-containerregistry's incompatible one) — so ECR, older Harbor, and
//     older Artifactory (which still lack OCI 1.1 referrers) get a real SBOM
//     attachment without the caller needing to know to pass --sbom-attach=tag.
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

	// Jobs and Stats are left at their zero values deliberately: an SBOM is a
	// single small layer, so there is nothing to parallelise and no mount
	// accounting worth paying an extra transport hop for.
	cfg := remoteConfig{
		Insecure:           req.Insecure,
		RegistryConfigPath: req.RegistryConfigPath,
	}
	sess, err := a.remoteSession(cfg)
	if err != nil {
		return ports.PublishResult{}, err
	}

	attachMode := req.AttachMode
	if attachMode == "" {
		attachMode = ports.DefaultSBOMAttachMode
	}

	if attachMode == ports.SBOMAttachTag {
		return a.attachSBOMTag(ctx, sess, req, repo, img)
	}

	// referrer and auto both start by actually attempting the real
	// Referrers API, with go-containerregistry's own silent-fallback-to-a-
	// different-tag-scheme disabled. That is a different option set, so it
	// gets its own session rather than borrowing the one above.
	referrerCfg := cfg
	referrerCfg.NoReferrersTagFallback = true
	referrerSess, err := a.remoteSession(referrerCfg)
	if err != nil {
		return ports.PublishResult{}, err
	}
	res, refErr := a.attachSBOMReferrer(ctx, referrerSess, req, repo, img)
	if refErr == nil {
		return res, nil
	}
	if attachMode == ports.SBOMAttachReferrer || !strings.Contains(refErr.Error(), referrersUnsupportedSubstring) {
		return ports.PublishResult{}, refErr
	}

	a.logger().Info("registry does not support OCI 1.1 referrers, falling back to tag mode", "repo", req.Repo, "subject", req.Subject.String())
	return a.attachSBOMTag(ctx, sess, req, repo, img)
}

// attachSBOMTag publishes img tagged per the cosign/ko convention
// (ports.SBOMTag: the subject digest's algorithm and hex joined by '-',
// suffixed ".sbom") — readable by every registry, including ones that
// predate the referrers API.
func (a *Adapter) attachSBOMTag(ctx context.Context, sess *registryutils.Session, req ports.AttachSBOMRequest, repo name.Repository, img v1.Image) (ports.PublishResult, error) {
	tagStr := ports.SBOMTag(req.Subject)
	tagRef := repo.Tag(tagStr)

	if err := sess.Push(ctx, tagRef, img); err != nil {
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

// attachSBOMReferrer publishes img as an OCI 1.1 referrer of req.Subject.
// sess must already have been built with remote.WithReferrersTagFallback(false)
// — see AttachSBOM's doc comment for why the caller, not this function, owns
// that.
func (a *Adapter) attachSBOMReferrer(ctx context.Context, sess *registryutils.Session, req ports.AttachSBOMRequest, repo name.Repository, img v1.Image) (ports.PublishResult, error) {
	img = mutate.Subject(img, v1.Descriptor{Digest: req.Subject}).(v1.Image)

	digest, err := img.Digest()
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach sbom %s: read image digest: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	digestRef := repo.Digest(digest.String())
	if err := sess.Push(ctx, digestRef, img); err != nil {
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
