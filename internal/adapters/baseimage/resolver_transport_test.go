package baseimage

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// TestInsecureTransport_PreservesRemoteDefaultTransportTuning guards the fix
// that replaced the package's insecure transport from a bare &http.Transport{}
// literal to a clone of remote.DefaultTransport. A literal silently dropped
// go-containerregistry's workload tuning — http.ProxyFromEnvironment, 30s dial
// timeout, MaxIdleConnsPerHost: 50, and ForceAttemptHTTP2: true — leaving no
// proxy support, a 2-conn idle pool, and no HTTP/2 on the insecure path (the
// very case that most needs h2 for local/self-signed test registries over TLS).
// See internal/adapters/transportutils for the shared helper and its rationale.
func TestInsecureTransport_PreservesRemoteDefaultTransportTuning(t *testing.T) {
	it, ok := insecureTransport.(*http.Transport)
	if !ok {
		t.Fatalf("insecureTransport is %T, want *http.Transport", insecureTransport)
	}

	// remote.DefaultTransport's own configured values — read directly rather
	// than hardcoded, so this fails loudly (not silently drifts) if upstream
	// ever changes its tuning and the clone picks the change up automatically.
	want, ok := remote.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("remote.DefaultTransport is %T, want *http.Transport", remote.DefaultTransport)
	}

	if it.ForceAttemptHTTP2 != want.ForceAttemptHTTP2 {
		t.Errorf("insecureTransport.ForceAttemptHTTP2 = %v, want %v (matching remote.DefaultTransport)", it.ForceAttemptHTTP2, want.ForceAttemptHTTP2)
	}
	if it.MaxIdleConnsPerHost != want.MaxIdleConnsPerHost {
		t.Errorf("insecureTransport.MaxIdleConnsPerHost = %d, want %d (matching remote.DefaultTransport)", it.MaxIdleConnsPerHost, want.MaxIdleConnsPerHost)
	}
	if it.Proxy == nil {
		t.Errorf("insecureTransport.Proxy = nil, want a non-nil proxy func (proxy support silently dropped)")
	} else if reflect.ValueOf(it.Proxy).Pointer() != reflect.ValueOf(want.Proxy).Pointer() {
		t.Errorf("insecureTransport.Proxy is not the same function as remote.DefaultTransport.Proxy")
	}
	if it.DialContext == nil {
		t.Errorf("insecureTransport.DialContext = nil, want remote.DefaultTransport's 30s-timeout dialer")
	}

	// The clone must not share remote.DefaultTransport's own instance — cloning
	// exists so the insecure path's TLSClientConfig override doesn't collide
	// with the shared upstream value.
	if it == want {
		t.Fatal("insecureTransport points at the same *http.Transport instance as remote.DefaultTransport; Clone() did not happen")
	}

	// The whole point of the insecure transport: verification is deliberately
	// skipped. This must remain true even though Clone() lazily allocates a
	// TLSClientConfig on the source transport (see Lessons.md); our own
	// override takes precedence.
	if it.TLSClientConfig == nil || !it.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("insecureTransport.TLSClientConfig.InsecureSkipVerify = %v, want true (insecure path must skip verification)", it.TLSClientConfig != nil && it.TLSClientConfig.InsecureSkipVerify)
	}
}
