package registry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	dockerclient "github.com/moby/moby/client"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// pingTimeout bounds how long Load waits to discover whether a Docker daemon
// is reachable, independent of the caller's ctx. ctx may carry no deadline at
// all — a build with no timeout flag — and the entire point of this check is
// to fail fast with a clear error rather than hang, which is exactly the
// failure mode documented in the package doc: authn.DefaultKeychain's
// credential-helper hang cost W6 a full debugging cycle, and an unreachable
// daemon must not repeat it here. Once the daemon has answered a ping, the
// actual load uses the caller's ctx and may run as long as a 90MB transfer
// needs.
const pingTimeout = 5 * time.Second

// Load implements ports.LocalLoader.
//
// The Docker daemon's classic image store cannot hold a multi-platform index
// (see ports.LoadRequest.Payload). When the payload is an index, Load selects
// the single child matching req.Platform — or the daemon's own platform when
// req.Platform is the zero value — and loads that alone. It never fails
// merely because the payload was an index: `--local` means "give me
// something I can run here," and every platform that was NOT loaded is named
// in the log line, not silently dropped.
func (a *Adapter) Load(ctx context.Context, req ports.LoadRequest) (ports.PublishResult, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return ports.PublishResult{}, fmt.Errorf("registry: load: repository is required: %w", core.ErrPushFailed)
	}
	if err := validatePayload(req.Payload); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: load %s: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	repo, err := name.NewRepository(req.Repo, nameOptions(false)...)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: load: parse repository %q: %w: %w", req.Repo, err, core.ErrPushFailed)
	}

	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: load %s: build docker client: %w: %w", req.Repo, err, core.ErrDaemonUnavailable)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if _, err := cli.Ping(pingCtx, dockerclient.PingOptions{}); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: load %s: docker daemon not reachable (is Docker running?): %w: %w", req.Repo, err, core.ErrDaemonUnavailable)
	}

	img, err := a.selectForDaemon(ctx, cli, req)
	if err != nil {
		return ports.PublishResult{}, err
	}

	tags := tagsOrDefault(req.Tags)
	primary := repo.Tag(tags[0])
	opts := []daemon.Option{daemon.WithContext(ctx), daemon.WithClient(cli)}

	if _, err := daemon.Write(primary, img, opts...); err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: load %s: write to daemon: %w: %w", req.Repo, err, core.ErrPushFailed)
	}
	for _, t := range tags[1:] {
		if err := daemon.Tag(primary, repo.Tag(t), opts...); err != nil {
			return ports.PublishResult{}, fmt.Errorf("registry: load %s: tag %s: %w: %w", req.Repo, t, err, core.ErrPushFailed)
		}
	}

	digest, err := img.Digest()
	if err != nil {
		return ports.PublishResult{}, fmt.Errorf("registry: load %s: read digest: %w: %w", req.Repo, err, core.ErrPushFailed)
	}
	size, err := manifestSize(img)
	if err != nil {
		a.logger().Debug("load: could not compute size", "repo", req.Repo, "err", err)
	}

	// daemon.Write above just streamed img through the same docker-save
	// tarball format as Write in tarball.go, which has no annotations field
	// at all — see warnDroppedAnnotations' doc comment.
	warnDroppedAnnotations(a.logger(), "docker daemon load", img)

	a.logger().Info("loaded image into docker daemon", "repo", req.Repo, "tag", primary.String(), "digest", digest.String())

	return ports.PublishResult{
		Ref:    primary.String(),
		Digest: digest,
		Tags:   append([]string(nil), tags...),
		Size:   size,
	}, nil
}

// selectForDaemon returns the single image to load: the payload's image
// directly, or — for an index — the child matching req.Platform, discovering
// the daemon's own platform first when req.Platform is the zero value.
func (a *Adapter) selectForDaemon(ctx context.Context, cli *dockerclient.Client, req ports.LoadRequest) (v1.Image, error) {
	if req.Payload.Image != nil {
		return req.Payload.Image, nil
	}

	want := req.Platform
	if want.IsZero() {
		discovered, err := hostPlatform(ctx, cli)
		if err != nil {
			return nil, fmt.Errorf("registry: load %s: discover daemon platform: %w: %w", req.Repo, err, core.ErrDaemonUnavailable)
		}
		want = discovered
	}

	img, skipped, err := selectIndexChild(req.Payload.Index, want)
	if err != nil {
		return nil, fmt.Errorf("registry: load %s: %w: %w", req.Repo, err, core.ErrPushFailed)
	}
	if len(skipped) > 0 {
		a.logger().Info("daemon load: selected one platform from a multi-platform index; the daemon's image store cannot hold the rest",
			"repo", req.Repo, "selected", want.String(), "skipped", strings.Join(skipped, ", "))
	} else {
		a.logger().Info("daemon load: selected platform from index", "repo", req.Repo, "selected", want.String())
	}
	return img, nil
}

// hostPlatform asks the daemon what platform it runs containers for, so Load
// can pick the right index child when the caller did not name one.
func hostPlatform(ctx context.Context, cli *dockerclient.Client) (ports.Platform, error) {
	sv, err := cli.ServerVersion(ctx, dockerclient.ServerVersionOptions{})
	if err != nil {
		return ports.Platform{}, err
	}
	if sv.Os == "" || sv.Arch == "" {
		return ports.Platform{}, fmt.Errorf("daemon did not report an OS/architecture")
	}
	return ports.Platform{OS: sv.Os, Arch: sv.Arch}, nil
}

// selectIndexChild picks the child of idx whose platform matches want. It
// returns the selected image and the human-readable platform of every child
// that was considered but not selected (excluding "unknown/unknown"
// attestation/provenance manifests, which are never candidates and never
// worth reporting as "skipped"), so the caller can log precisely what got
// left out of the daemon load.
//
// This function touches no network and no daemon — idx is already resolved
// in memory — which is what makes it unit-testable without either.
func selectIndexChild(idx v1.ImageIndex, want ports.Platform) (img v1.Image, skipped []string, err error) {
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, nil, fmt.Errorf("read index manifest: %w", err)
	}

	var selected *v1.Descriptor
	for _, d := range im.Manifests {
		d := d
		if d.Platform == nil || d.Platform.OS == "unknown" || d.Platform.Architecture == "unknown" {
			continue
		}
		if selected == nil && platformMatches(d.Platform, want) {
			selected = &d
			continue
		}
		skipped = append(skipped, platformString(d.Platform))
	}

	if selected == nil {
		return nil, skipped, fmt.Errorf("index has no manifest for platform %s (present: %s)", want, strings.Join(skipped, ", "))
	}

	img, err = idx.Image(selected.Digest)
	if err != nil {
		return nil, skipped, fmt.Errorf("fetch %s child %s: %w", want, selected.Digest, err)
	}
	return img, skipped, nil
}

// platformString renders an index child's platform descriptor the same way
// ports.Platform does, for consistent log output.
func platformString(p *v1.Platform) string {
	if p == nil {
		return ""
	}
	pl, ok := ports.PlatformFromV1(p)
	if !ok {
		return p.OS + "/" + p.Architecture
	}
	return pl.String()
}
