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

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// defaultTransport and insecureTransport are the two transports every registry
// operation in this package runs on: the former for ordinary targets, the
// latter for a *Request.Insecure target (local or self-signed test registries
// only).
//
// Both are clones of remote.DefaultTransport rather than bare &http.Transport{}
// literals. go-containerregistry tunes its default for exactly this workload —
// http.ProxyFromEnvironment, a 30s dial timeout, MaxIdleConnsPerHost: 50,
// ForceAttemptHTTP2: true — and a literal silently drops all of it, leaving no
// proxy support and net/http's default idle pool of 2 connections per host,
// which throttles a multi-layer push.
//
// Cloning earns its keep twice over on the insecure path. Clone() carries the
// tuning across, and it resets the internal HTTP/2 transport that
// http.Transport caches on first use, so h2 is renegotiated against the new
// TLSClientConfig instead of skipped. Assigning a custom TLSClientConfig is
// what suppresses net/http's automatic HTTP/2 upgrade in the first place, which
// is why the previous literal-based insecure transport quietly fell back to
// HTTP/1.1 while the secure path got h2.
//
// That fix covers the self-signed-TLS-over-https case only. A plain http://
// insecure target still speaks HTTP/1.1 no matter what is configured here:
// net/http has no h2c (cleartext HTTP/2) client support, so it is a limitation
// of the standard library rather than something this package can address.
//
// Package-level because http.Transport is meant to be reused across requests,
// not rebuilt per call — a per-call transport would defeat connection pooling
// entirely.
var (
	defaultTransport  = cloneDefaultTransport(nil)
	insecureTransport = cloneDefaultTransport(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // opt-in via the Insecure request fields
)

// cloneDefaultTransport returns a copy of remote.DefaultTransport, applying
// tlsConfig to the copy when it is non-nil.
//
// remote.DefaultTransport is declared as an http.RoundTripper, so its concrete
// type is asserted rather than guaranteed by the compiler. Should upstream ever
// change it to something other than *http.Transport, the assertion fails and
// the value is returned unmodified: that drops the TLS override, so an insecure
// target would start failing certificate verification instead of silently
// skipping it, and it avoids panicking during package initialisation.
func cloneDefaultTransport(tlsConfig *tls.Config) http.RoundTripper {
	base, ok := remote.DefaultTransport.(*http.Transport)
	if !ok {
		return remote.DefaultTransport
	}
	cloned := base.Clone()
	if tlsConfig != nil {
		cloned.TLSClientConfig = tlsConfig
	}
	return cloned
}

var (
	_ ports.Registry      = (*Adapter)(nil)
	_ ports.LocalLoader   = (*Adapter)(nil)
	_ ports.TarballWriter = (*Adapter)(nil)
)

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

// remoteConfig carries everything remoteOptions needs to build a registry
// operation's option set. It is a struct rather than a parameter list because
// the two optional knobs below are both zero-valued at most call sites, and a
// positional signature makes "false, "", "", 0, nil" indistinguishable at a
// glance from a call that meant something by those values.
type remoteConfig struct {
	// Insecure selects insecureTransport over defaultTransport, for local or
	// self-signed test registries only.
	Insecure bool

	// UserAgent is appended to go-containerregistry's own User-Agent when
	// non-empty.
	UserAgent string

	// RegistryConfigPath points at an alternative Docker config.json; empty
	// means the default keychain.
	RegistryConfigPath string

	// Jobs caps how many layers remote.Write uploads in parallel. Zero means
	// "leave the choice to go-containerregistry", whose own default is
	// currently 4 (defaultJobs, pkg/v1/remote/options.go).
	//
	// Zero must be omitted rather than passed through: remote.WithJobs
	// rejects any value <= 0, and it does so when the option is applied inside
	// remote.Write, not where the option is constructed — so passing 0 would
	// turn a caller who simply had no opinion into a push failure raised from
	// a confusingly distant place.
	Jobs int

	// Stats, when non-nil, wraps the chosen transport in a mountObserver that
	// records per-blob mount/stream/already-present outcomes into it. Nil
	// installs no wrapper at all, which is what callers that have no use for
	// the accounting should pass — there is no reason for them to carry the
	// extra indirection on every request.
	//
	// A non-nil value must be scoped to a single operation; see mountStats.
	Stats *mountStats
}

// remoteOptions builds the remote.Option set common to every registry
// operation: context threading (so a cancelled build aborts a 90MB upload
// rather than leaking it into the background) and keychain resolution (supporting custom config.json).
//
// The transport is always set explicitly, on the secure path as well as the
// insecure one. Leaving it unset would fall through to remote's own
// DefaultTransport, which is equivalent today but leaves the two paths running
// on transports this package does not own — see defaultTransport above.
func remoteOptions(ctx context.Context, cfg remoteConfig) ([]remote.Option, error) {
	kc, err := registryutils.ResolveKeychain(cfg.RegistryConfigPath)
	if err != nil {
		return nil, err
	}
	rt := defaultTransport
	if cfg.Insecure {
		rt = insecureTransport
	}
	if cfg.Stats != nil {
		// Wraps, never replaces: the observer delegates to the shared
		// package-level transport so the connection pool survives.
		rt = &mountObserver{base: rt, stats: cfg.Stats}
	}
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
		remote.WithTransport(rt),
	}
	if cfg.UserAgent != "" {
		opts = append(opts, remote.WithUserAgent(cfg.UserAgent))
	}
	if cfg.Jobs > 0 {
		opts = append(opts, remote.WithJobs(cfg.Jobs))
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
