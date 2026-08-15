package transportutils

import (
	"crypto/tls"
	"net/http"
	"reflect"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// TestCloneDefaultTransport_PreservesTuning guards the shared helper against
// regression back to a bare &http.Transport{}. CloneDefaultTransport is the
// single home for the transport that every registry adapter relies on, so this
// test certifies that cloning from remote.DefaultTransport carries over the
// workload tuning — ForceAttemptHTTP2, the enlarged per-host idle pool, proxy
// support, and the 30s dialer — that a literal would silently drop.
func TestCloneDefaultTransport_PreservesTuning(t *testing.T) {
	tr, ok := CloneDefaultTransport(nil).(*http.Transport)
	if !ok {
		t.Fatalf("CloneDefaultTransport(nil) is %T, want *http.Transport", CloneDefaultTransport(nil))
	}

	want, ok := remote.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("remote.DefaultTransport is %T, want *http.Transport", remote.DefaultTransport)
	}

	if tr.ForceAttemptHTTP2 != want.ForceAttemptHTTP2 {
		t.Errorf("ForceAttemptHTTP2 = %v, want %v (matching remote.DefaultTransport)", tr.ForceAttemptHTTP2, want.ForceAttemptHTTP2)
	}
	if tr.MaxIdleConnsPerHost != want.MaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d (matching remote.DefaultTransport)", tr.MaxIdleConnsPerHost, want.MaxIdleConnsPerHost)
	}
	if tr.Proxy == nil {
		t.Errorf("Proxy = nil, want a non-nil proxy func (proxy support silently dropped)")
	} else if reflect.ValueOf(tr.Proxy).Pointer() != reflect.ValueOf(want.Proxy).Pointer() {
		t.Errorf("Proxy is not the same function as remote.DefaultTransport.Proxy")
	}
	if tr.DialContext == nil {
		t.Errorf("DialContext = nil, want remote.DefaultTransport's 30s-timeout dialer")
	}
	if tr == want {
		t.Fatal("clone points at the same *http.Transport instance as remote.DefaultTransport; Clone() did not happen")
	}
}

// TestCloneDefaultTransport_AppliesTLSOverride verifies the tlsConfig override
// is applied to the clone (the insecure-registry case) and that it does not
// leak onto the shared upstream transport.
func TestCloneDefaultTransport_AppliesTLSOverride(t *testing.T) {
	insecure := CloneDefaultTransport(&tls.Config{InsecureSkipVerify: true}).(*http.Transport)
	if insecure.TLSClientConfig == nil || !insecure.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("clone with TLS override has InsecureSkipVerify = %v, want true", insecure.TLSClientConfig != nil && insecure.TLSClientConfig.InsecureSkipVerify)
	}

	// The shared upstream transport must not be mutated by cloning off it.
	if want, ok := remote.DefaultTransport.(*http.Transport); ok {
		if want.TLSClientConfig != nil && want.TLSClientConfig.InsecureSkipVerify {
			t.Errorf("remote.DefaultTransport.TLSClientConfig.InsecureSkipVerify = true; cloning leaked the insecure override onto the shared upstream transport")
		}
	}
}

// TestInsecureTransport_IsSharedSingleton guards the shared, process-wide
// insecure transport that every adapter's Insecure path now runs on. It must be
// a single cached instance (so rapid-fire one-shot registry calls share one
// connection pool instead of each allocating a fresh transport) that still
// carries the tuned defaults and the InsecureSkipVerify override, and that
// never leaks the override onto the shared upstream transport.
func TestInsecureTransport_IsSharedSingleton(t *testing.T) {
	a := InsecureTransport()
	b := InsecureTransport()
	if a != b {
		t.Fatal("InsecureTransport() returned different instances across calls; expected a single cached transport (connection pooling would be defeated)")
	}

	tr, ok := a.(*http.Transport)
	if !ok {
		t.Fatalf("InsecureTransport() is %T, want *http.Transport", a)
	}

	want, ok := remote.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("remote.DefaultTransport is %T, want *http.Transport", remote.DefaultTransport)
	}

	if tr.ForceAttemptHTTP2 != want.ForceAttemptHTTP2 {
		t.Errorf("InsecureTransport().ForceAttemptHTTP2 = %v, want %v (matching remote.DefaultTransport)", tr.ForceAttemptHTTP2, want.ForceAttemptHTTP2)
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("InsecureTransport().TLSClientConfig.InsecureSkipVerify = %v, want true", tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify)
	}
	if tr == want {
		t.Fatal("InsecureTransport() points at the same *http.Transport instance as remote.DefaultTransport; Clone() did not happen")
	}

	if want.TLSClientConfig != nil && want.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("remote.DefaultTransport.TLSClientConfig.InsecureSkipVerify = true; the shared insecure transport leaked its override onto the shared upstream transport")
	}
}
