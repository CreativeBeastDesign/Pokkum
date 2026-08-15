// Package transportutils provides the shared HTTP transport builder used by
// every adapter that talks to a container registry over the network.
//
// It is a non-port utility package: it implements no Hexagonal port interface,
// so it carries the IsUtilityPackage sentinel to distinguish it from a port
// adapter implementation (see AGENTS.md §2).
package transportutils

import (
	"crypto/tls"
	"net/http"
	"sync"

	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// IsUtilityPackage marks this as a reusable utility package rather than a port
// adapter implementation (see AGENTS.md §2).
const IsUtilityPackage = true

// CloneDefaultTransport returns a copy of remote.DefaultTransport, applying
// tlsConfig to the copy when it is non-nil.
//
// Callers MUST clone rather than write bare &http.Transport{} literals.
// go-containerregistry tunes its default for exactly this workload —
// http.ProxyFromEnvironment, a 30s dial timeout, MaxIdleConnsPerHost: 50,
// ForceAttemptHTTP2: true — and a literal silently drops all of it, leaving no
// proxy support, net/http's default idle pool of 2 connections per host (which
// throttles a multi-layer push), and no HTTP/2.
//
// Cloning earns its keep twice over on the insecure path (tlsConfig with
// InsecureSkipVerify: true). Clone() carries the tuning across, and it resets
// the internal HTTP/2 transport that http.Transport caches on first use, so h2
// is renegotiated against the new TLSClientConfig instead of skipped. Assigning
// a custom TLSClientConfig is what suppresses net/http's automatic HTTP/2
// upgrade in the first place, which is why a literal-based insecure transport
// quietly fell back to HTTP/1.1 while the secure path got h2.
//
// That fix covers the self-signed-TLS-over-https case only. A plain http://
// insecure target still speaks HTTP/1.1 no matter what is configured here:
// net/http has no h2c (cleartext HTTP/2) client support, so it is a limitation
// of the standard library rather than something this package can address.
//
// remote.DefaultTransport is declared as an http.RoundTripper, so its concrete
// type is asserted rather than guaranteed by the compiler. Should upstream ever
// change it to something other than *http.Transport, the assertion fails and
// the value is returned unmodified: that drops the TLS override, so an insecure
// target would start failing certificate verification instead of silently
// skipping it, and it avoids panicking during package initialisation.
func CloneDefaultTransport(tlsConfig *tls.Config) http.RoundTripper {
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

// insecureTransport is the single, binary-wide insecure transport and the sole
// identity every adapter's `--insecure` / Insecure:true registry path runs on.
// It is built once (CloneDefaultTransport of remote.DefaultTransport with
// InsecureSkipVerify: true) and reused for the life of the process, so that
// rapid-fire one-shot registry operations — e.g. the per-cache-check calls in
// remotecacheutils — share one connection pool instead of each allocating a
// fresh transport (and fresh TLS state) per call.
//
// Sharing one transport across every adapter and every insecure host is safe
// and idiomatic: *http.Transport pools connections per host internally, and
// go-containerregistry itself shares a single remote.DefaultTransport across
// all hosts. Declaring it as a package-level var would force a clone at init
// even for binaries that never touch an insecure registry; sync.OnceValue
// defers that cost until the first insecure operation, and the tiny
// InsecureSkipVerify override is applied only to this clone, never to the
// shared remote.DefaultTransport (see Lessons.md).
//
// It is an http.RoundTripper cache, not a mutable holder: callers must treat
// the returned value as read-only and must not modify the underlying
// *http.Transport, or they would corrupt every other adapter's insecure path.
var insecureTransport = sync.OnceValue(func() http.RoundTripper {
	return CloneDefaultTransport(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // opt-in via the per-adapter Insecure request fields
})

// InsecureTransport returns the process-wide shared insecure transport (a
// clone of remote.DefaultTransport with InsecureSkipVerify: true). Use it -
// never a bare &http.Transport{} literal or a fresh CloneDefaultTransport per
// call - on every adapter's insecure registry path so the tuned defaults
// (proxy, ForceAttemptHTTP2, 50-conn per-host idle pool) are preserved and
// pooled across calls.
func InsecureTransport() http.RoundTripper {
	return insecureTransport()
}
