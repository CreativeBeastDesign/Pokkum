package registryutils

import (
	"context"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Session holds the one *remote.Puller and one *remote.Pusher that every
// registry operation sharing a single option set should run through.
//
// # Why this exists
//
// go-containerregistry's package-level convenience functions —
// remote.Get/Head/Image/Index/Write/WriteIndex/Tag/Put — each call
// newPuller(o)/newPusher(o) internally, and a fresh Puller/Pusher has an empty
// fetcher map. The first call on it therefore runs makeFetcher/makeWriter:
// authn.Resolve (a credential lookup, possibly a helper subprocess) followed by
// transport.NewWithContext, which is a `GET /v2/` ping, a 401 challenge, and a
// token request against the registry's auth host — usually a second TLS
// handshake to a different hostname. A signed two-platform push made roughly
// two dozen of those, one per call site, for what is the same authenticated
// session against the same registry.
//
// A Puller memoises *only* the fetcher (resolved authenticator + configured
// http.Client) per resource, in its internal readers map; a Pusher memoises the
// per-repository writer the same way. Neither caches manifests or blob content:
// Puller.Get always issues a fresh `GET /v2/<repo>/manifests/<ref>` through
// f.fetchManifest, and Puller.Head always issues a fresh HEAD. Sharing one is
// therefore an auth/connection optimisation with no read-your-own-writes or
// stale-manifest hazard — a re-pull-and-compare check against a floating tag
// still goes to the network and still observes a moved tag.
//
// # Why not remote.Reuse
//
// remote.Reuse(puller) plumbs a saved Puller into the package-level functions,
// but leaves every call site reading as though it were building its own
// session, and makes the effective option set (transport, keychain, platform,
// jobs) come from wherever the saved Puller was constructed rather than from
// the options visibly passed at the call. The Puller/Pusher methods take the
// context per call and make the sharing explicit, so that is what Session
// exposes.
//
// # Lifetime and concurrency
//
// A Session is safe for concurrent use: construction of the Puller/Pusher is
// guarded by sync.Once, and go-containerregistry's own readers/writers maps are
// sync.Maps whose entries self-initialise under a sync.Once. Scope one Session
// to one distinct option set (transport, keychain, user agent, jobs) — the
// options are captured at construction and are what every call on it uses.
type Session struct {
	opts []remote.Option

	pullerOnce sync.Once
	puller     *remote.Puller
	pullerErr  error

	pusherOnce sync.Once
	pusher     *remote.Pusher
	pusherErr  error
}

// NewSession returns a Session that runs every operation through one shared
// puller and one shared pusher built from opts.
//
// opts is copied, so a caller may keep appending to the slice it passed.
func NewSession(opts ...remote.Option) *Session {
	cloned := make([]remote.Option, len(opts))
	copy(cloned, opts)
	return &Session{opts: cloned}
}

// Puller returns the shared puller, building it on first use.
func (s *Session) Puller() (*remote.Puller, error) {
	s.pullerOnce.Do(func() {
		s.puller, s.pullerErr = remote.NewPuller(s.opts...)
	})
	return s.puller, s.pullerErr
}

// Pusher returns the shared pusher, building it on first use.
func (s *Session) Pusher() (*remote.Pusher, error) {
	s.pusherOnce.Do(func() {
		s.pusher, s.pusherErr = remote.NewPusher(s.opts...)
	})
	return s.pusher, s.pusherErr
}

// Get is remote.Get through the shared puller. It performs a real manifest GET
// every time; only the authenticated session is reused.
func (s *Session) Get(ctx context.Context, ref name.Reference) (*remote.Descriptor, error) {
	p, err := s.Puller()
	if err != nil {
		return nil, err
	}
	return p.Get(ctx, ref)
}

// Head is remote.Head through the shared puller. It performs a real manifest
// HEAD every time.
func (s *Session) Head(ctx context.Context, ref name.Reference) (*v1.Descriptor, error) {
	p, err := s.Puller()
	if err != nil {
		return nil, err
	}
	return p.Head(ctx, ref)
}

// Image is remote.Image through the shared puller: a fresh Get, then the
// descriptor resolved to an image (following an index to the option set's
// platform, exactly as remote.Image does).
func (s *Session) Image(ctx context.Context, ref name.Reference) (v1.Image, error) {
	desc, err := s.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	return desc.Image()
}

// Push is remote.Write/remote.WriteIndex through the shared pusher: it uploads
// t's dependencies (layers, child manifests) and then commits the manifest.
func (s *Session) Push(ctx context.Context, ref name.Reference, t remote.Taggable) error {
	p, err := s.Pusher()
	if err != nil {
		return err
	}
	return p.Push(ctx, ref, t)
}

// Put is remote.Tag/remote.Put through the shared pusher: a manifest PUT only,
// with no dependency upload. Callers must have already ensured everything t
// references exists in the target repository.
func (s *Session) Put(ctx context.Context, ref name.Reference, t remote.Taggable) error {
	p, err := s.Pusher()
	if err != nil {
		return err
	}
	return p.Put(ctx, ref, t)
}

// SessionCache memoises Sessions by an arbitrary comparable key, so that the
// several independent registry operations one build performs — resolve the
// base image, probe the remote cache, push the image, attach a signature,
// attach an SBOM — share a single authenticated session per distinct option
// set instead of re-authenticating once per operation.
//
// The key must capture every dimension of the option set that changes what a
// request does: transport (insecure), credentials (config path), user agent,
// jobs, and any per-operation option a call site appends. Two option sets that
// differ in any of those must not share a key, or one operation would silently
// run on another's transport or credentials.
//
// A SessionCache is deliberately NOT keyed on the context. That is safe
// because no Session method reads the context captured in its options: Get,
// Head, Image, Push and Put all take the caller's context per call and pass it
// straight through to the Puller/Pusher method, which is what
// go-containerregistry uses both for the request itself and for the one-time
// fetcher/writer construction. Callers should therefore build a cached
// Session's options *without* remote.WithContext.
//
// The zero value is ready to use and safe for concurrent use.
type SessionCache struct {
	m sync.Map // key -> *Session
}

// Session returns the cached Session for key, building one from opts on first
// use. opts is evaluated by the caller on every call; keep it cheap (it is,
// once ResolveKeychain is memoised) — the Puller and Pusher themselves are
// built lazily and only once per Session.
func (c *SessionCache) Session(key any, opts ...remote.Option) *Session {
	if v, ok := c.m.Load(key); ok {
		return v.(*Session)
	}
	v, _ := c.m.LoadOrStore(key, NewSession(opts...))
	return v.(*Session)
}
