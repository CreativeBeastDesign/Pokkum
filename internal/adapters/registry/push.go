package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Push implements ports.Registry.
func (a *Adapter) Push(ctx context.Context, req ports.PushRequest) (ports.PublishResult, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: push: %w", core.ErrNoDockerRepo)
	}
	if err := validatePayload(req.Payload); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: push %s: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	repo, err := name.NewRepository(req.Repo, nameOptions(req.Insecure)...)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: push: parse repository %q: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	digest, err := payloadDigest(req.Payload)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: push %s: compute digest: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	tags := tagsOrDefault(req.Tags)
	opts, err := remoteOptions(ctx, req.Insecure, req.UserAgent, req.RegistryConfigPath)
	if err != nil {
		return ports.PublishResult{}, err
	}

	digestRef := repo.Digest(digest.String())

	exists, err := a.digestExists(digestRef, opts)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: push %s: check existing digest %s: %w", req.Repo, digest, err)
	}

	var size int64
	if exists {
		// The content is already in the registry, so the blob and manifest
		// uploads can be skipped — but the tags this push asked for still have
		// to point at it. Identical content under a new tag yields the same
		// digest, so skipping outright would leave the new tag uncreated while
		// still reporting success, and anything downstream that resolved
		// repo:<tag> would 404.
		reconciled, err := a.reconcileTags(repo, digest, tags, req.Payload, opts)
		if err != nil {
			return ports.PublishResult{}, fmt.Errorf("registry: push %s: reconcile tags: %w", req.Repo, err)
		}
		a.logger().Info("push skipped: digest already present in registry",
			"repo", req.Repo, "digest", digest.String(), "tags", tags,
			"tags_created", reconciled)
	} else {
		if err := a.writePayload(repo, req.Payload, tags, opts); err != nil {
			return ports.PublishResult{}, classifyPushErr(req.Repo, err)
		}
		size, err = payloadSize(req.Payload)
		if err != nil {
			// Informational only: the push already succeeded. Log and carry on
			// with Size left at zero rather than fail a completed publish.
			a.logger().Debug("push: could not compute transferred size", "repo", req.Repo, "err", err)
		}
		a.logger().Info("pushed image", "repo", req.Repo, "digest", digest.String(), "tags", tags, "size", size)
	}

	return ports.PublishResult{
		Ref:    repo.Name() + "@" + digest.String(),
		Digest: digest,
		Tags:   append([]string(nil), tags...),
		Size:   size,
	}, nil
}

// digestExists checks whether digestRef is already present in the registry
// via a HEAD request, so Push can skip re-uploading unchanged content. It
// distinguishes three outcomes:
//
//   - 404 Not Found: the content genuinely is not there. Not an error —
//     digestExists returns (false, nil) and Push proceeds to upload.
//   - 401/403: the registry rejected the credentials used for the check. This
//     must surface as an error, not be treated as "not present"; silently
//     falling through to an upload attempt would trade one confusing failure
//     (an auth error on HEAD) for a more confusing one (an auth error on PUT,
//     with no indication the HEAD ever ran).
//   - anything else (DNS failure, connection refused, timeout, 5xx): also an
//     error, for the same reason — a transient network problem must not be
//     silently reinterpreted as "content missing, please upload 90MB".
//
// transport.Error is how go-containerregistry surfaces a registry's HTTP
// response as a Go error; CheckError (used internally by remote.Head/Get) is
// what populates it from the raw *http.Response. errors.As is therefore the
// correct way to recover the status code here — a bare string match against
// err.Error() would be fragile across registry implementations.
func (a *Adapter) digestExists(digestRef name.Digest, opts []remote.Option) (bool, error) {
	_, err := remote.Head(digestRef, opts...)
	if err == nil {
		return true, nil
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, err
	}

	var terr *transport.Error
	if errors.As(err, &terr) {
		switch terr.StatusCode {
		case http.StatusNotFound:
			return false, nil
		case http.StatusUnauthorized, http.StatusForbidden:
			return false, fmt.Errorf("registry rejected credentials: %w: %w", err, core.ErrRegistryAuth)
		default:
			return false, fmt.Errorf("unexpected registry response: %w: %w", err, core.ErrPushFailed)
		}
	}

	// No structured transport.Error to classify: DNS failure, connection
	// refused, TLS handshake failure, or similar. This is a network problem,
	// not proof of absence.
	return false, fmt.Errorf("network error: %w: %w", err, core.ErrPushFailed)
}

// writePayload uploads req's image or index to repo under every tag: the
// first tag via remote.Write/WriteIndex (which uploads the manifest and any
// blobs the registry does not already have — go-containerregistry itself
// HEADs each blob before uploading it, so a partially-cached push is cheap
// even without this package's own digest check), and every remaining tag via
// remote.Tag, which PUTs only the manifest against blobs already confirmed
// present by the first write.
func (a *Adapter) writePayload(repo name.Repository, p ports.Payload, tags []string, opts []remote.Option) error {
	primary := repo.Tag(tags[0])

	var taggable remote.Taggable
	if p.Index != nil {
		if err := remote.WriteIndex(primary, p.Index, opts...); err != nil {
			return err
		}
		taggable = p.Index
	} else {
		if err := remote.Write(primary, p.Image, opts...); err != nil {
			return err
		}
		taggable = p.Image
	}

	for _, t := range tags[1:] {
		if err := remote.Tag(repo.Tag(t), taggable, opts...); err != nil {
			return err
		}
	}
	return nil
}

// reconcileTags points every requested tag at digest when the content is
// already present in the registry, and reports how many tags it had to create.
//
// The blob and manifest uploads are genuinely skippable in that case, but the
// tags are not: two builds of identical content under different tags share a
// digest, so a skip keyed on the digest alone would leave the second tag
// uncreated while Push still reported it as published. Creating a tag over
// already-present blobs is a single manifest PUT, so this stays cheap and the
// idempotent path remains free when the tags already resolve correctly.
func (a *Adapter) reconcileTags(repo name.Repository, digest v1.Hash, tags []string, p ports.Payload, opts []remote.Option) (int, error) {
	var taggable remote.Taggable = p.Image
	if p.Index != nil {
		taggable = p.Index
	}

	created := 0
	for _, t := range tags {
		tagRef := repo.Tag(t)

		desc, err := remote.Head(tagRef, opts...)
		if err == nil && desc.Digest == digest {
			continue // already points where we want it
		}
		if err != nil && !isNotFound(err) {
			return created, fmt.Errorf("check tag %q: %w", t, err)
		}

		// Either the tag is absent, or it resolves to different content and
		// must be moved onto this digest.
		if err := remote.Tag(tagRef, taggable, opts...); err != nil {
			return created, fmt.Errorf("tag %q: %w: %w", t, err, core.ErrPushFailed)
		}
		created++
	}
	return created, nil
}

// isNotFound reports whether err is a registry 404, as opposed to an auth
// failure or a transport problem — only absence justifies creating the tag.
func isNotFound(err error) bool {
	var terr *transport.Error
	return errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound
}

// classifyPushErr maps a go-containerregistry write/tag error onto a core
// sentinel, following the same 401/403-vs-everything-else split as
// digestExists.
func classifyPushErr(repoName string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("registry: push %s: %w", repoName, err)
	}

	var terr *transport.Error
	if errors.As(err, &terr) {
		switch terr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("registry: push %s: registry rejected credentials: %w: %w", repoName, err, core.ErrRegistryAuth)
		}
	}
	return fmt.Errorf("registry: push %s: %w: %w", repoName, err, core.ErrPushFailed)
}
