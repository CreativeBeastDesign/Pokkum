package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
)

// countingServer stands in for the Sigstore TUF repository and records
// whether it was contacted at all. Counting requests is the only way to
// prove "no network was attempted" -- asserting on the returned bytes alone
// cannot distinguish "the fetch was skipped" from "the fetch was attempted,
// failed, and fell back", which are very different things under --hermetic.
// Mirrors internal/adapters/sigstore/tufrefresh_test.go's countingRepo, one
// layer up at the CLI wiring this task actually owns.
func countingServer(t *testing.T) (url string, hits *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

// installCountingTUFFactory points the package-level sigstoreTUFOptionsFactory
// seam (declared in build.go, shared by verify.go) at a fresh request-counting
// server for the duration of the test, and restores the real
// sigstore.DefaultTUFOptions factory afterwards. Returns the hit counter.
func installCountingTUFFactory(t *testing.T) *atomic.Int64 {
	t.Helper()
	url, hits := countingServer(t)
	original := sigstoreTUFOptionsFactory
	cache := t.TempDir()
	sigstoreTUFOptionsFactory = func() sigstore.TUFOptions {
		opts := sigstore.DefaultTUFOptions()
		opts.RepositoryBaseURL = url
		opts.CachePath = cache
		return opts
	}
	t.Cleanup(func() { sigstoreTUFOptionsFactory = original })
	return hits
}

// TestSigstoreTUFRefreshFlag_RegisteredOnBuildAndVerify guards the CLI
// surface itself: a struct field existing on TUFOptions is not evidence a
// flag reaches it (checklist row 16) -- the flag has to actually be
// registered, default off, on both commands that feed a trusted root.
func TestSigstoreTUFRefreshFlag_RegisteredOnBuildAndVerify(t *testing.T) {
	buildCmd := newBuildCommand(context.Background(), slog.Default())
	flag := buildCmd.Flags().Lookup("sigstore-tuf-refresh")
	if flag == nil {
		t.Fatal("expected --sigstore-tuf-refresh to be registered on `pokkum build`")
	}
	if flag.DefValue != "false" {
		t.Errorf("`pokkum build --sigstore-tuf-refresh` default = %q, want %q (opt-in only)", flag.DefValue, "false")
	}

	verifyCmd := newVerifyCommand(context.Background(), slog.Default())
	flag = verifyCmd.Flags().Lookup("sigstore-tuf-refresh")
	if flag == nil {
		t.Fatal("expected --sigstore-tuf-refresh to be registered on `pokkum verify`")
	}
	if flag.DefValue != "false" {
		t.Errorf("`pokkum verify --sigstore-tuf-refresh` default = %q, want %q (opt-in only)", flag.DefValue, "false")
	}

	// base.go deliberately does NOT get this flag: `base update`/`base check`
	// never set ports.BaseImageRequest.VerifySignature, so neither ever
	// consumes a trusted root at all -- there is nothing for the flag to feed.
	baseCmd := newBaseCommand(context.Background(), slog.Default())
	for _, sub := range baseCmd.Commands() {
		if f := sub.Flags().Lookup("sigstore-tuf-refresh"); f != nil {
			t.Errorf("`pokkum base %s` unexpectedly carries --sigstore-tuf-refresh; base never verifies signatures", sub.Name())
		}
	}
}

// TestBuildRequest_SigstoreTUFRefreshAbsent_NoNetworkAttempt is the default-off
// half of the contract: without the flag, req.CacheVerify.TrustedRootJSON
// stays empty (the sigstore.Verifier's own existing default then falls back
// to the embedded snapshot downstream) and the TUF factory is never even
// invoked, so zero network requests are structurally possible.
func TestBuildRequest_SigstoreTUFRefreshAbsent_NoNetworkAttempt(t *testing.T) {
	hits := installCountingTUFFactory(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	flags := baseRuntimeTestFlags()
	flags.sigstoreTUFRefresh = false

	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, t.TempDir())
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
	}
	if len(req.CacheVerify.TrustedRootJSON) != 0 {
		t.Errorf("CacheVerify.TrustedRootJSON = %d bytes, want empty (embedded default applies downstream)", len(req.CacheVerify.TrustedRootJSON))
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("TUF repository contacted %d time(s) with --sigstore-tuf-refresh absent; want 0", got)
	}
}

// TestBuildRequest_SigstoreTUFRefreshWithHermetic_UsesEmbeddedNoNetwork is the
// single most important test in this file: --hermetic must make a hermetic
// build's trust-root refresh make ZERO network attempts, even with
// --sigstore-tuf-refresh set. It asserts the counting server directly rather
// than merely asserting the call succeeded, because "attempted and silently
// fell back" would look identical to "correctly never attempted" on every
// assertion except this one.
func TestBuildRequest_SigstoreTUFRefreshWithHermetic_UsesEmbeddedNoNetwork(t *testing.T) {
	hits := installCountingTUFFactory(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	flags := baseRuntimeTestFlags()
	flags.sigstoreTUFRefresh = true
	flags.hermetic = true

	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, t.TempDir())
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
	}
	if !bytes.Equal(req.CacheVerify.TrustedRootJSON, sigstore.DefaultTrustedRootJSON()) {
		t.Error("hermetic + --sigstore-tuf-refresh did not resolve to the embedded snapshot")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("hermetic build contacted the Sigstore TUF repository %d time(s); --hermetic must make this "+
			"the same as an offline air-gapped build, zero network attempts, not merely a graceful fallback", got)
	}
}

// TestBuildRequest_SigstoreTUFRefreshWithoutHermetic_AttemptsNetworkAndFallsBack
// is the contrasting case: outside --hermetic, the flag really does reach the
// network (proving the previous test's zero hits is a --hermetic effect, not
// an accident of the test server never being reachable at all), and a TUF
// failure degrades to the embedded snapshot with a logged warning rather than
// failing the build.
func TestBuildRequest_SigstoreTUFRefreshWithoutHermetic_AttemptsNetworkAndFallsBack(t *testing.T) {
	hits := installCountingTUFFactory(t) // 404s everything
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	flags := baseRuntimeTestFlags()
	flags.sigstoreTUFRefresh = true
	flags.hermetic = false

	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, t.TempDir())
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags returned an error instead of falling back: %v", err)
	}
	if !bytes.Equal(req.CacheVerify.TrustedRootJSON, sigstore.DefaultTrustedRootJSON()) {
		t.Error("fallback did not resolve to the embedded snapshot")
	}
	if got := hits.Load(); got == 0 {
		t.Error("non-hermetic build never contacted the TUF repository, so the fallback proves nothing")
	}
	if !strings.Contains(logBuf.String(), "could not refresh the Sigstore trust root") {
		t.Errorf("TUF failure was not logged; a silent fallback is exactly the deception this mechanism must "+
			"prevent:\n%s", logBuf.String())
	}
}

// TestBuildRequest_ExplicitTrustedRootBeatsRefreshFlag pins the documented
// precedence: --sigstore-trusted-root always wins over --sigstore-tuf-refresh,
// and the refresh path (including any network attempt) is never even reached
// when both are supplied.
func TestBuildRequest_ExplicitTrustedRootBeatsRefreshFlag(t *testing.T) {
	hits := installCountingTUFFactory(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	custom := []byte(`{"custom":"trusted-root-for-precedence-test"}`)
	path := filepath.Join(t.TempDir(), "custom-trusted-root.json")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatalf("write custom trusted root: %v", err)
	}

	flags := baseRuntimeTestFlags()
	flags.sigstoreTUFRefresh = true
	flags.hermetic = true
	flags.sigstoreTrustedRoot = path

	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, t.TempDir())
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
	}
	if !bytes.Equal(req.CacheVerify.TrustedRootJSON, custom) {
		t.Errorf("CacheVerify.TrustedRootJSON = %q, want the explicit --sigstore-trusted-root file's bytes", req.CacheVerify.TrustedRootJSON)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("TUF repository contacted %d time(s) although --sigstore-trusted-root was explicitly set; the "+
			"refresh path must not even run", got)
	}
}

// TestRunVerify_SigstoreTUFRefreshAbsent_NoNetworkAttempt is verify's
// default-off case. runVerify is exercised against an address nothing listens
// on (loopback, reserved port) so ResolveProvenance fails fast and
// deterministically once it gets there; what matters for this test is that
// the TUF factory is never touched on the way to that failure.
func TestRunVerify_SigstoreTUFRefreshAbsent_NoNetworkAttempt(t *testing.T) {
	hits := installCountingTUFFactory(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	oldExit := exitFunc
	exitFunc = func(int) {}
	defer func() { exitFunc = oldExit }()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() {
		w.Close()
		os.Stdout = oldStdout
		_, _ = io.Copy(io.Discard, r)
	}()

	_ = runVerify(context.Background(), logger, &verifyOptions{noRebuild: true, output: "json"}, "127.0.0.1:1/nonexistent:latest")

	if got := hits.Load(); got != 0 {
		t.Errorf("TUF repository contacted %d time(s) with --sigstore-tuf-refresh absent; want 0", got)
	}
}

// TestRunVerify_SigstoreTUFRefresh_AttemptsNetworkAndFallsBack is verify's
// version of the build test above: setting the flag really does reach the
// network (Offline is hardcoded false for verify -- it has no --hermetic of
// its own), and a TUF failure degrades to the embedded snapshot with a
// logged warning, without ever failing the command on that account.
func TestRunVerify_SigstoreTUFRefresh_AttemptsNetworkAndFallsBack(t *testing.T) {
	hits := installCountingTUFFactory(t) // 404s everything
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	oldExit := exitFunc
	exitFunc = func(int) {}
	defer func() { exitFunc = oldExit }()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() {
		w.Close()
		os.Stdout = oldStdout
		_, _ = io.Copy(io.Discard, r)
	}()

	_ = runVerify(context.Background(), logger, &verifyOptions{noRebuild: true, output: "json", sigstoreTUFRefresh: true}, "127.0.0.1:1/nonexistent:latest")

	if got := hits.Load(); got == 0 {
		t.Error("`pokkum verify --sigstore-tuf-refresh` never contacted the TUF repository, so the fallback proves nothing")
	}
	if !strings.Contains(logBuf.String(), "could not refresh the Sigstore trust root") {
		t.Errorf("TUF failure was not logged:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "resolved Sigstore trust root for verification") {
		t.Errorf("origin of the trust root used for verification was not logged:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), string(sigstore.TrustedRootOriginEmbedded)) {
		t.Errorf("origin log does not name the embedded fallback:\n%s", logBuf.String())
	}
}

// TestRunVerify_ExplicitTrustedRootBeatsRefreshFlag mirrors the build-side
// precedence test: --sigstore-trusted-root wins, and the refresh path (and
// its network attempt) is never reached even when --sigstore-tuf-refresh is
// also set.
func TestRunVerify_ExplicitTrustedRootBeatsRefreshFlag(t *testing.T) {
	hits := installCountingTUFFactory(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	custom := []byte(`{"custom":"trusted-root-for-verify-precedence-test"}`)
	path := filepath.Join(t.TempDir(), "custom-trusted-root.json")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatalf("write custom trusted root: %v", err)
	}

	oldExit := exitFunc
	exitFunc = func(int) {}
	defer func() { exitFunc = oldExit }()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() {
		w.Close()
		os.Stdout = oldStdout
		_, _ = io.Copy(io.Discard, r)
	}()

	_ = runVerify(context.Background(), logger, &verifyOptions{
		noRebuild:          true,
		output:             "json",
		trustedRoot:        path,
		sigstoreTUFRefresh: true,
	}, "127.0.0.1:1/nonexistent:latest")

	if got := hits.Load(); got != 0 {
		t.Errorf("TUF repository contacted %d time(s) although --sigstore-trusted-root was explicitly set; the "+
			"refresh path must not even run", got)
	}
}
