package sigstore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// countingRepo stands in for a TUF repository and records whether it was
// contacted at all. Counting requests is the only way to prove "no network was
// attempted": asserting on the returned bytes cannot distinguish "we skipped
// the fetch" from "we tried, failed, and fell back", which are very different
// things under --hermetic.
func countingRepo(t *testing.T, handler http.HandlerFunc) (url string, hits *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if handler != nil {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

// TestFetchTrustedRootJSON_OfflineFailsClosed pins the hermetic contract the
// same way bunruntime's resolver does: an offline/hermetic caller asking for
// network material gets core.ErrHermeticViolation, not a best-effort attempt.
func TestFetchTrustedRootJSON_OfflineFailsClosed(t *testing.T) {
	url, hits := countingRepo(t, nil)

	opts := DefaultTUFOptions()
	opts.Offline = true
	opts.RepositoryBaseURL = url
	opts.CachePath = t.TempDir()

	_, err := FetchTrustedRootJSON(context.Background(), opts)
	if err == nil {
		t.Fatal("FetchTrustedRootJSON succeeded in offline mode")
	}
	if !errors.Is(err, core.ErrHermeticViolation) {
		t.Fatalf("error = %v, want one wrapping core.ErrHermeticViolation", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("offline fetch made %d HTTP request(s) to the TUF repository; hermetic mode must not "+
			"touch the network at all", got)
	}
	if !strings.Contains(err.Error(), "--sigstore-trusted-root") {
		t.Errorf("offline refusal does not tell the operator how to supply a snapshot instead.\ngot: %s", err)
	}
}

// TestFetchTrustedRootJSON_HonoursCancelledContext keeps the cancellation
// contract explicit. sigstore-go's TUF fetcher does not take a context, so this
// package checks ctx itself before and after; without the pre-check a cancelled
// build would still perform a full TUF round trip.
func TestFetchTrustedRootJSON_HonoursCancelledContext(t *testing.T) {
	url, hits := countingRepo(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	opts := DefaultTUFOptions()
	opts.RepositoryBaseURL = url
	opts.CachePath = t.TempDir()

	if _, err := FetchTrustedRootJSON(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want one wrapping context.Canceled", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("cancelled fetch made %d HTTP request(s)", got)
	}
}

// TestFetchTrustedRootJSON_UnreachableRepositoryFailsClosed proves this
// function never quietly substitutes the embedded snapshot. That behaviour
// belongs to ResolveTrustedRootJSON, which warns; a caller that asked for live
// material and silently got a six-month-old snapshot would be the exact
// deception this work exists to remove.
func TestFetchTrustedRootJSON_UnreachableRepositoryFailsClosed(t *testing.T) {
	url, _ := countingRepo(t, nil) // 404s everything

	opts := DefaultTUFOptions()
	opts.RepositoryBaseURL = url
	opts.CachePath = t.TempDir()

	data, err := FetchTrustedRootJSON(context.Background(), opts)
	if err == nil {
		t.Fatal("FetchTrustedRootJSON succeeded against a repository serving only 404s")
	}
	if data != nil {
		t.Errorf("FetchTrustedRootJSON returned %d bytes alongside an error", len(data))
	}
	if bytes.Equal(data, defaultTrustedRootJSON) {
		t.Error("FetchTrustedRootJSON fell back to the embedded snapshot; it must fail closed instead")
	}
}

// TestResolveTrustedRootJSON_OfflineUsesEmbeddedWithoutNetwork is the hermetic
// happy path: verification still works air-gapped, and does so without a single
// packet leaving the machine.
func TestResolveTrustedRootJSON_OfflineUsesEmbeddedWithoutNetwork(t *testing.T) {
	url, hits := countingRepo(t, nil)

	opts := DefaultTUFOptions()
	opts.Offline = true
	opts.RepositoryBaseURL = url
	opts.CachePath = t.TempDir()

	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))

	data, origin, err := ResolveTrustedRootJSON(context.Background(), logger, opts, mustMetadata(t).CapturedAt)
	if err != nil {
		t.Fatalf("ResolveTrustedRootJSON: %v", err)
	}
	if origin != TrustedRootOriginEmbedded {
		t.Errorf("origin = %q, want %q", origin, TrustedRootOriginEmbedded)
	}
	if !bytes.Equal(data, defaultTrustedRootJSON) {
		t.Error("offline resolve did not return the embedded snapshot")
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("offline resolve made %d HTTP request(s); it must not touch the network", got)
	}
	// A fresh snapshot at its own capture time must not produce a staleness
	// warning — otherwise every hermetic build cries wolf.
	if strings.Contains(log.String(), "stale embedded Sigstore trust root") {
		t.Errorf("offline resolve warned about staleness for a snapshot assessed at its capture time:\n%s", log.String())
	}
}

// TestResolveTrustedRootJSON_OfflineWarnsWhenEmbeddedIsStale is the other half:
// air-gapped operation is allowed to continue, but never silently. An operator
// running a year-old binary must be told why a signature might fail.
func TestResolveTrustedRootJSON_OfflineWarnsWhenEmbeddedIsStale(t *testing.T) {
	opts := DefaultTUFOptions()
	opts.Offline = true
	opts.CachePath = t.TempDir()

	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn}))

	stale := mustMetadata(t).CapturedAt.Add(TrustedRootMaxAge + 24*time.Hour)
	data, origin, err := ResolveTrustedRootJSON(context.Background(), logger, opts, stale)
	if err != nil {
		t.Fatalf("ResolveTrustedRootJSON: %v", err)
	}
	if origin != TrustedRootOriginEmbedded {
		t.Errorf("origin = %q, want %q", origin, TrustedRootOriginEmbedded)
	}
	if len(data) == 0 {
		t.Error("stale-but-offline resolve returned no trust root; hermetic verification must still work")
	}
	if !strings.Contains(log.String(), "stale embedded Sigstore trust root") {
		t.Fatalf("no staleness warning was logged for an overdue snapshot:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "go test ./internal/adapters/sigstore/") {
		t.Errorf("staleness warning does not name the refresh command:\n%s", log.String())
	}
}

// TestResolveTrustedRootJSON_WarnsOnTUFFailureAndFallsBack covers the
// degrade-gracefully path for a non-hermetic caller whose network is broken:
// verification proceeds on the embedded snapshot, and the reason it is not
// using live material is on the record.
func TestResolveTrustedRootJSON_WarnsOnTUFFailureAndFallsBack(t *testing.T) {
	url, hits := countingRepo(t, nil) // 404s everything

	opts := DefaultTUFOptions()
	opts.RepositoryBaseURL = url
	opts.CachePath = t.TempDir()

	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn}))

	data, origin, err := ResolveTrustedRootJSON(context.Background(), logger, opts, mustMetadata(t).CapturedAt)
	if err != nil {
		t.Fatalf("ResolveTrustedRootJSON returned an error instead of falling back: %v", err)
	}
	if origin != TrustedRootOriginEmbedded {
		t.Errorf("origin = %q, want %q", origin, TrustedRootOriginEmbedded)
	}
	if !bytes.Equal(data, defaultTrustedRootJSON) {
		t.Error("fallback did not return the embedded snapshot")
	}
	if got := hits.Load(); got == 0 {
		t.Error("non-hermetic resolve never contacted the TUF repository, so the fallback proves nothing")
	}
	if !strings.Contains(log.String(), "could not refresh the Sigstore trust root") {
		t.Fatalf("the TUF failure was not reported; falling back silently is the failure mode this "+
			"whole mechanism exists to prevent:\n%s", log.String())
	}
}

// TestDefaultTUFOptions_PublicGoodDefaults pins the defaults a caller gets
// without configuration, so a future edit cannot quietly point trust-root
// refreshes at a different repository.
func TestDefaultTUFOptions_PublicGoodDefaults(t *testing.T) {
	opts := DefaultTUFOptions()
	if opts.Offline {
		t.Error("DefaultTUFOptions is offline; the offline decision belongs to the caller's --hermetic flag")
	}
	if got := opts.repositoryBaseURL(); got != "https://tuf-repo-cdn.sigstore.dev" {
		t.Errorf("repositoryBaseURL() = %q, want the public-good Sigstore TUF CDN", got)
	}
	if got := opts.cachePath(); !strings.Contains(got, "pokkum") {
		t.Errorf("cachePath() = %q, want a pokkum-owned cache directory rather than a shared one", got)
	}
}
