package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// AttachSignature implements ports.Registry.
//
// Dual-publication contract (see ports.AttachSignatureRequest): the .sig tag
// (ports.SigTag) is always written first — it is the read path of `cosign
// verify`, `pokkum verify`, and the remote cache's cache-hit verification, so
// its write failing fails the attach. The OCI 1.1 referrer is then attempted
// additively, with go-containerregistry's silent fallback-tag scheme disabled
// (the same referrersUnsupportedSubstring probe AttachSBOM's auto mode uses);
// a registry without referrers support keeps the tag alone. A referrer write
// that fails for any other reason is logged as a warning and does not fail
// the attach — the load-bearing tag artifact is already in place and is what
// the post-push self-verification stage reads back.
func (a *Adapter) AttachSignature(ctx context.Context, req ports.AttachSignatureRequest) (ports.PublishResult, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: attach signature: %w", core.ErrNoDockerRepo)
	}
	if req.Subject.Hex == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: attach signature %s: no subject digest: %w", req.Repo, core.ErrSigningFailed)
	}
	if len(req.Bundle.PayloadBytes) == 0 || req.Bundle.Base64Signature == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: attach signature %s: bundle has no payload or signature: %w", req.Repo, core.ErrSigningFailed)
	}

	img, err := signatureImage(req.Bundle)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach signature %s: build signature image: %w: %w", req.Repo, err, core.ErrSigningFailed)
	}

	return a.attachSupplyChainImage(ctx, supplyChainAttach{
		kind:               "signature",
		tag:                ports.SigTag(req.Subject),
		repo:               req.Repo,
		subject:            req.Subject,
		img:                img,
		insecure:           req.Insecure,
		registryConfigPath: req.RegistryConfigPath,
	})
}

// AttachAttestation implements ports.Registry, under the same
// dual-publication contract as AttachSignature but for the .att tag and a
// DSSE envelope layer.
func (a *Adapter) AttachAttestation(ctx context.Context, req ports.AttachAttestationRequest) (ports.PublishResult, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: attach attestation: %w", core.ErrNoDockerRepo)
	}
	if req.Subject.Hex == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: attach attestation %s: no subject digest: %w", req.Repo, core.ErrSigningFailed)
	}
	if req.Envelope.Payload == "" || len(req.Envelope.Signatures) == 0 {
		return ports.PublishResult{}, fmt.Errorf("registry: attach attestation %s: envelope has no payload or signatures: %w", req.Repo, core.ErrSigningFailed)
	}

	img, err := attestationImage(req.Envelope)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach attestation %s: build attestation image: %w: %w", req.Repo, err, core.ErrSigningFailed)
	}

	return a.attachSupplyChainImage(ctx, supplyChainAttach{
		kind:               "attestation",
		tag:                ports.AttTag(req.Subject),
		repo:               req.Repo,
		subject:            req.Subject,
		img:                img,
		insecure:           req.Insecure,
		registryConfigPath: req.RegistryConfigPath,
	})
}

// supplyChainAttach carries the shared parameters of one .sig/.att attach.
type supplyChainAttach struct {
	kind               string
	tag                string
	repo               string
	subject            v1.Hash
	img                v1.Image
	insecure           bool
	registryConfigPath string
}

// attachSupplyChainImage writes the tag-convention attachment (load-bearing,
// failure fails the attach) and then the additive OCI 1.1 referrer.
func (a *Adapter) attachSupplyChainImage(ctx context.Context, att supplyChainAttach) (ports.PublishResult, error) {
	repo, err := name.NewRepository(att.repo, nameOptions(att.insecure)...)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach %s: parse repository %q: %w: %w", att.kind, att.repo, err, core.ErrSigningFailed)
	}

	opts, err := remoteOptions(ctx, remoteConfig{
		Insecure:           att.insecure,
		RegistryConfigPath: att.registryConfigPath,
	})
	if err != nil {
		return ports.PublishResult{}, err
	}

	tagRef := repo.Tag(att.tag)
	if err := remote.Write(tagRef, att.img, opts...); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach %s %s: %w: %w", att.kind, att.repo, err, core.ErrSigningFailed)
	}

	digest, err := att.img.Digest()
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: attach %s %s: read pushed digest: %w: %w", att.kind, att.repo, err, core.ErrSigningFailed)
	}

	// Additive referrer: same image with the subject descriptor set, written
	// by digest, with go-containerregistry's own incompatible fallback-tag
	// scheme disabled exactly as AttachSBOM does. Only the version-pinned
	// "referrers unsupported" failure is silently tolerated (the tag above is
	// the compatibility contract); any other failure is surfaced as a warning
	// but does not undo an attach whose load-bearing artifact already landed.
	refImg := mutate.Subject(att.img, v1.Descriptor{Digest: att.subject}).(v1.Image)
	refDigest, err := refImg.Digest()
	if err == nil {
		referrerOpts := append(slices.Clone(opts), remote.WithReferrersTagFallback(false))
		if werr := remote.Write(repo.Digest(refDigest.String()), refImg, referrerOpts...); werr != nil {
			if strings.Contains(werr.Error(), referrersUnsupportedSubstring) {
				a.logger().Info("registry does not support OCI 1.1 referrers; "+att.kind+" attached by tag only", "repo", att.repo, "tag", att.tag)
			} else {
				a.logger().Warn("attaching "+att.kind+" as OCI 1.1 referrer failed; tag attachment is in place", "repo", att.repo, "tag", att.tag, "err", werr)
			}
		}
	}

	size, err := manifestSize(att.img)
	if err != nil {
		a.logger().Debug("attach "+att.kind+": could not compute transferred size", "repo", att.repo, "err", err)
	}

	a.logger().Info("attached "+att.kind, "repo", att.repo, "subject", att.subject.String(), "tag", att.tag, "digest", digest.String())

	return ports.PublishResult{
		Ref:    repo.Name() + ":" + att.tag,
		Digest: digest,
		Tags:   []string{att.tag},
		Size:   size,
	}, nil
}

// FetchSignature implements ports.Registry: it reads back the .sig tag
// attachment for req.Subject and reconstructs the bundle for verification.
func (a *Adapter) FetchSignature(ctx context.Context, req ports.FetchAttachmentRequest) (ports.CosignSignatureBundle, error) {
	img, manifest, err := a.fetchAttachmentImage(ctx, req, ports.SigTag(req.Subject), "signature")
	if err != nil {
		return ports.CosignSignatureBundle{}, err
	}

	layers, err := img.Layers()
	if err != nil || len(layers) == 0 {
		return ports.CosignSignatureBundle{}, fmt.Errorf("registry: fetch signature %s: attachment has no layers: %w", req.Repo, core.ErrSignatureMissing)
	}

	payload, err := layerBytes(layers[0])
	if err != nil {
		return ports.CosignSignatureBundle{}, fmt.Errorf("registry: fetch signature %s: read payload layer: %w: %w", req.Repo, err, core.ErrSignatureMissing)
	}

	sigStr := ""
	if len(manifest.Layers) > 0 && manifest.Layers[0].Annotations != nil {
		sigStr = manifest.Layers[0].Annotations[ports.CosignSignatureLayerAnnotation]
	}
	if sigStr == "" && manifest.Annotations != nil {
		sigStr = manifest.Annotations[ports.CosignSignatureLayerAnnotation]
	}
	if sigStr == "" {
		return ports.CosignSignatureBundle{}, fmt.Errorf("registry: fetch signature %s: no %s annotation on attachment: %w", req.Repo, ports.CosignSignatureLayerAnnotation, core.ErrSignatureMissing)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigStr))
	if err != nil {
		return ports.CosignSignatureBundle{}, fmt.Errorf("registry: fetch signature %s: decode base64 signature: %w: %w", req.Repo, err, core.ErrSignatureMissing)
	}

	return ports.CosignSignatureBundle{
		PayloadBytes:    payload,
		SignatureBytes:  sigBytes,
		Base64Signature: sigStr,
		Repo:            req.Repo,
		Digest:          req.Subject,
	}, nil
}

// FetchAttestation implements ports.Registry: it reads back the .att tag
// attachment for req.Subject and decodes the DSSE envelope it carries.
func (a *Adapter) FetchAttestation(ctx context.Context, req ports.FetchAttachmentRequest) (ports.DSSEEnvelope, error) {
	img, _, err := a.fetchAttachmentImage(ctx, req, ports.AttTag(req.Subject), "attestation")
	if err != nil {
		return ports.DSSEEnvelope{}, err
	}

	layers, err := img.Layers()
	if err != nil || len(layers) == 0 {
		return ports.DSSEEnvelope{}, fmt.Errorf("registry: fetch attestation %s: attachment has no layers: %w", req.Repo, core.ErrSignatureMissing)
	}

	data, err := layerBytes(layers[0])
	if err != nil {
		return ports.DSSEEnvelope{}, fmt.Errorf("registry: fetch attestation %s: read envelope layer: %w: %w", req.Repo, err, core.ErrSignatureMissing)
	}

	var env ports.DSSEEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return ports.DSSEEnvelope{}, fmt.Errorf("registry: fetch attestation %s: decode DSSE envelope: %w: %w", req.Repo, err, core.ErrSignatureMissing)
	}
	return env, nil
}

// fetchAttachmentImage resolves and fetches the tag-convention attachment
// image plus its manifest. A 404 is core.ErrSignatureMissing — the caller
// (the self-verification stage) treats an absent attachment as a hard
// failure, never as an empty success.
func (a *Adapter) fetchAttachmentImage(ctx context.Context, req ports.FetchAttachmentRequest, tag, kind string) (v1.Image, *v1.Manifest, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return nil, nil, fmt.Errorf("registry: fetch %s: %w", kind, core.ErrNoDockerRepo)
	}
	if req.Subject.Hex == "" {
		return nil, nil, fmt.Errorf("registry: fetch %s %s: no subject digest: %w", kind, req.Repo, core.ErrSignatureMissing)
	}

	repo, err := name.NewRepository(req.Repo, nameOptions(req.Insecure)...)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: fetch %s: parse repository %q: %w: %w", kind, req.Repo, err, core.ErrSignatureMissing)
	}

	opts, err := remoteOptions(ctx, remoteConfig{
		Insecure:           req.Insecure,
		RegistryConfigPath: req.RegistryConfigPath,
	})
	if err != nil {
		return nil, nil, err
	}

	img, err := remote.Image(repo.Tag(tag), opts...)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return nil, nil, fmt.Errorf("registry: fetch %s %s: no attachment at tag %s: %w", kind, req.Repo, tag, core.ErrSignatureMissing)
		}
		return nil, nil, fmt.Errorf("registry: fetch %s %s: %w: %w", kind, req.Repo, err, core.ErrSignatureMissing)
	}

	manifest, err := img.Manifest()
	if err != nil {
		return nil, nil, fmt.Errorf("registry: fetch %s %s: read attachment manifest: %w: %w", kind, req.Repo, err, core.ErrSignatureMissing)
	}
	return img, manifest, nil
}

// maxAttachmentBytes caps how much of a fetched .sig/.att layer is read. A
// real Simple Signing payload or DSSE-wrapped SLSA statement is a few KB; the
// cap guards the self-verification stage against a hostile registry streaming
// unbounded content (same discipline as assetoverlay's maxOverlayEntryBytes —
// registry-pulled content is not locally-trusted).
const maxAttachmentBytes = 4 << 20

// layerBytes reads one layer's content, preferring the uncompressed stream —
// static.NewLayer attachments are stored uncompressed, so this is normally a
// straight read.
func layerBytes(layer v1.Layer) ([]byte, error) {
	rc, err := layer.Uncompressed()
	if err != nil {
		rc, err = layer.Compressed()
		if err != nil {
			return nil, err
		}
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxAttachmentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAttachmentBytes {
		return nil, fmt.Errorf("attachment layer exceeds %d bytes", maxAttachmentBytes)
	}
	return data, nil
}

// signatureImage wraps a Cosign signature bundle as a single-layer image in
// cosign's own wire format: one uncompressed Simple Signing payload layer,
// with the base64 signature carried in the layer's
// dev.cosignproject.cosign/signature annotation — exactly the shape `cosign
// verify`, pokkum's provenance resolver, and the remote cache verifier parse.
func signatureImage(bundle ports.CosignSignatureBundle) (v1.Image, error) {
	layer := static.NewLayer(bundle.PayloadBytes, types.MediaType(ports.MediaTypeSimpleSigning))
	img, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer: layer,
		Annotations: map[string]string{
			ports.CosignSignatureLayerAnnotation: bundle.Base64Signature,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("append signature layer: %w", err)
	}
	return img, nil
}

// attestationImage wraps a DSSE envelope as a single-layer image: the
// serialized envelope as one uncompressed layer, media-typed per cosign's
// attestation convention. No layer annotation is needed — the signature
// lives inside the envelope itself.
func attestationImage(env ports.DSSEEnvelope) (v1.Image, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal DSSE envelope: %w", err)
	}
	layer := static.NewLayer(data, types.MediaType(ports.MediaTypeDSSEEnvelope))
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return nil, fmt.Errorf("append attestation layer: %w", err)
	}
	return img, nil
}
