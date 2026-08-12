// Package registry implements ports.Registry, ports.LocalLoader and
// ports.TarballWriter against github.com/google/go-containerregistry: pushing
// images and SBOMs to a remote registry, loading into the local Docker
// daemon, and writing OCI archives to disk.
//
// A single Adapter implements all three interfaces. That mirrors how the
// three interfaces are actually used: core selects one destination per build
// via OutputMode, so there is never a reason to construct three separate
// adapters wired to three separate configurations. Splitting them into three
// interfaces at the port level (W1) still pays for itself even though one
// type satisfies all three, because it lets a caller that only ever runs
// `--tarball` depend on ports.TarballWriter and never pull in a Docker daemon
// client transitively.
//
// # Idempotent push
//
// Push is the expensive operation in this package — the application layer
// alone runs roughly 90MB per architecture — so Push always checks whether
// the target digest already exists in the registry (remote.Head) before
// uploading anything. When it does, the upload is skipped entirely and the
// existing digest is returned. This is what makes re-running a pipeline that
// didn't change the image cheap.
//
// The skip is keyed on the manifest digest alone, not on the requested tags.
// That is the correct trade-off for the case this exists to serve — rerunning
// the same build, which requests the same tags for the same content — and it
// is deliberately not a full reconciliation of "does every requested tag
// already point at this digest": doing that would cost one HEAD per tag,
// eroding exactly the round-trip savings this feature exists to capture, for
// a scenario (identical content, newly-requested tag) that a rebuild does not
// produce. A caller that retags identical content under a brand new tag for
// the first time should expect to pay for a real push.
//
// # The credential-helper hang
//
// authn.DefaultKeychain reads $DOCKER_CONFIG/config.json. On a machine
// configured with "credsStore": "desktop" (Docker Desktop's default), that
// means every authenticated request shells out to docker-credential-desktop,
// which blocks forever if Docker Desktop is not running — it does not error,
// it hangs. See resolver_test.go in internal/adapters/baseimage for the
// original diagnosis; TestMain in this package applies the same fix by
// pointing DOCKER_CONFIG at an empty directory for the whole test binary, so
// DefaultKeychain falls back to anonymous auth against the in-memory test
// registry instead of touching the host's real credential store.
package registry

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// insecureTransport is used for requests against a *Request.Insecure target:
// local or self-signed test registries only. Package-level because
// http.Transport is meant to be reused, not built per call.
var insecureTransport http.RoundTripper = &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in via Insecure fields
}

// Adapter implements ports.Registry, ports.LocalLoader and
// ports.TarballWriter. The zero value is not usable; construct with
// NewAdapter.
//
// Adapter is safe for concurrent use: it holds no mutable state of its own,
// every method builds what it needs from its arguments.
type Adapter struct {
	log *slog.Logger
}

// NewAdapter constructs an Adapter. A nil logger defaults to slog.Default().
func NewAdapter(log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{log: log}
}

// logger returns the effective logger, defensively covering a zero-value
// Adapter (e.g. in a test that skips NewAdapter).
func (a *Adapter) logger() *slog.Logger {
	if a == nil || a.log == nil {
		return slog.Default()
	}
	return a.log
}

// nameOptions builds the github.com/google/go-containerregistry/pkg/name
// options common to every reference this package parses: weak validation
// (matching baseimage's convention, since the strict form rejects registries
// this tool has no reason to reject) plus name.Insecure when the caller opted
// into plain HTTP or an unverified TLS certificate.
func nameOptions(insecure bool) []name.Option {
	opts := []name.Option{name.WeakValidation}
	if insecure {
		opts = append(opts, name.Insecure)
	}
	return opts
}

type customConfigFileKeychain struct {
	cf *configfile.ConfigFile
}

func (k *customConfigFileKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	if k.cf == nil {
		return authn.Anonymous, nil
	}
	reg := target.RegistryStr()
	cfg, err := k.cf.GetAuthConfig(reg)
	if err != nil {
		return authn.Anonymous, nil
	}
	if cfg.Username != "" || cfg.Password != "" || cfg.Auth != "" || cfg.IdentityToken != "" || cfg.RegistryToken != "" {
		return authn.FromConfig(authn.AuthConfig{
			Username:      cfg.Username,
			Password:      cfg.Password,
			Auth:          cfg.Auth,
			IdentityToken: cfg.IdentityToken,
			RegistryToken: cfg.RegistryToken,
		}), nil
	}
	return authn.Anonymous, nil
}

func resolveKeychain(configPath string) (authn.Keychain, error) {
	if configPath == "" {
		return authn.DefaultKeychain, nil
	}

	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("registry: open auth config %s: %w: %w", configPath, err, core.ErrRegistryAuth)
	}
	defer f.Close()

	cf, err := config.LoadFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("registry: load auth config %s: %w: %w", configPath, err, core.ErrRegistryAuth)
	}

	return authn.NewMultiKeychain(&customConfigFileKeychain{cf: cf}, authn.DefaultKeychain), nil
}

// remoteOptions builds the remote.Option set common to every registry
// operation: context threading (so a cancelled build aborts a 90MB upload
// rather than leaking it into the background) and keychain resolution (supporting custom config.json).
func remoteOptions(ctx context.Context, insecure bool, userAgent string, registryConfigPath string) ([]remote.Option, error) {
	kc, err := resolveKeychain(registryConfigPath)
	if err != nil {
		return nil, err
	}
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
	}
	if userAgent != "" {
		opts = append(opts, remote.WithUserAgent(userAgent))
	}
	if insecure {
		opts = append(opts, remote.WithTransport(insecureTransport))
	}
	return opts, nil
}

// payloadDigest returns the digest of whichever of Image or Index is set. It
// does not validate that exactly one is set; call validatePayload first.
func payloadDigest(p ports.Payload) (v1.Hash, error) {
	if p.Index != nil {
		return p.Index.Digest()
	}
	return p.Image.Digest()
}

// payloadSize returns the total manifest+config+layers size in bytes of
// whichever of Image or Index is set: the config descriptor plus every layer
// descriptor's Size, summed across every child for an index. This is the
// number of bytes a push actually transfers, unlike v1.Image.Size() /
// v1.ImageIndex.Size(), which report only the manifest's own byte length.
//
// A failure here is informational-data-only: callers should log it and
// proceed with Size left at zero rather than fail the publish over it.
func payloadSize(p ports.Payload) (int64, error) {
	if p.Index != nil {
		im, err := p.Index.IndexManifest()
		if err != nil {
			return 0, fmt.Errorf("read index manifest: %w", err)
		}
		var total int64
		for _, d := range im.Manifests {
			if d.MediaType.IsIndex() {
				continue
			}
			child, err := p.Index.Image(d.Digest)
			if err != nil {
				return 0, fmt.Errorf("fetch child %s: %w", d.Digest, err)
			}
			s, err := manifestSize(child)
			if err != nil {
				return 0, err
			}
			total += s
		}
		return total, nil
	}
	return manifestSize(p.Image)
}

// manifestSize sums one image's config size and every layer's size, i.e. the
// bytes a push of this image alone transfers.
func manifestSize(img v1.Image) (int64, error) {
	m, err := img.Manifest()
	if err != nil {
		return 0, fmt.Errorf("read manifest: %w", err)
	}
	total := m.Config.Size
	for _, l := range m.Layers {
		total += l.Size
	}
	return total, nil
}

// validatePayload reports an error if p does not carry exactly one of Image
// or Index, per the ports.Payload contract.
func validatePayload(p ports.Payload) error {
	switch {
	case p.Image == nil && p.Index == nil:
		return fmt.Errorf("payload carries neither an image nor an index")
	case p.Image != nil && p.Index != nil:
		return fmt.Errorf("payload carries both an image and an index; exactly one is required")
	default:
		return nil
	}
}

// tagsOrDefault returns tags, or a single-element slice holding
// ports.DefaultTag when tags is empty.
func tagsOrDefault(tags []string) []string {
	if len(tags) == 0 {
		return []string{ports.DefaultTag}
	}
	return tags
}

// platformMatches reports whether an index child's platform descriptor
// satisfies the requested platform. It compares OS and architecture only —
// see the equivalent, independently-maintained helper in
// internal/adapters/baseimage for why Variant is ignored — and skips the
// "unknown/unknown" placeholder platform that registries use for attestation
// and provenance manifests co-located in the same index.
func platformMatches(cp *v1.Platform, want ports.Platform) bool {
	if cp == nil {
		return false
	}
	if cp.OS == "unknown" || cp.Architecture == "unknown" {
		return false
	}
	return cp.OS == want.OS && cp.Architecture == want.Arch
}
