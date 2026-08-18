package sigstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// activeFulcioCertNotAfter is the expiry of the certificates in the embedded
// snapshot's currently-active public-good Fulcio CA — the one whose declared
// validity period is still open ("O=sigstore.dev, CN=sigstore-intermediate"
// and its root, both notAfter 2031-10-05T13:56:58Z). Note this is NOT the
// snapshot's earliest CA expiry: the 2021 CA's root expires 2031-02-23 but its
// declared window ended in 2022, so it is retired and correctly ignored.
//
// It is hard-coded so the synthetic "now" values below stay readable;
// TestEmbeddedTrustedRoot_ActiveAnchorExpiryIsWhereExpected asserts the
// snapshot still agrees with it, so a refresh that rotates the CA makes that
// test fail loudly instead of leaving these cases silently testing nothing.
var activeFulcioCertNotAfter = time.Date(2031, 10, 5, 13, 56, 58, 0, time.UTC)

func mustMetadata(t *testing.T) TrustedRootMetadata {
	t.Helper()
	m, err := DefaultTrustedRootMetadata()
	if err != nil {
		t.Fatalf("DefaultTrustedRootMetadata(): %v", err)
	}
	return m
}

// TestDefaultTrustedRootMetadata_MatchesSnapshot is the tripwire against the
// snapshot and its provenance record drifting apart. If they disagree, the
// recorded capture date describes bytes that are not the ones embedded, and
// every age-based staleness guard below is measuring the wrong thing.
func TestDefaultTrustedRootMetadata_MatchesSnapshot(t *testing.T) {
	meta := mustMetadata(t)

	if got := digestOf(DefaultTrustedRootJSON()); got != meta.SHA256 {
		t.Errorf("embedded snapshot sha256 = %s, trusted-root-metadata.json records %s.\n"+
			"The snapshot and its provenance sidecar were not updated together. Regenerate both with:\n\t%s",
			got, meta.SHA256, RefreshTrustedRootCommand)
	}
	if meta.Source != "tuf-target" {
		t.Errorf("metadata source = %q, want %q — the snapshot must be the raw TUF-verified target, "+
			"not a hand-copied example file", meta.Source, "tuf-target")
	}
	if meta.TUFRepository != TrustedRootTUFRepository {
		t.Errorf("metadata tufRepository = %q, want %q", meta.TUFRepository, TrustedRootTUFRepository)
	}
	if meta.TUFTarget != TrustedRootTUFTarget {
		t.Errorf("metadata tufTarget = %q, want %q", meta.TUFTarget, TrustedRootTUFTarget)
	}
}

// TestEmbeddedTrustedRoot_IsFreshNow is THE staleness guard: it is the
// mechanism that stops the embedded trust root rotting silently.
//
// It runs against the real wall clock on purpose. Every other freshness test
// in this file pins a synthetic time to prove the *logic*; this one is the only
// thing in the repository that notices the *calendar* moving. Pin it and the
// guard stops guarding.
//
// It fails, rather than warns, because a warning in test output is exactly the
// kind of signal that gets scrolled past for six months.
func TestEmbeddedTrustedRoot_IsFreshNow(t *testing.T) {
	freshness, err := CheckDefaultTrustedRootFreshness(time.Now())
	if err != nil {
		t.Fatalf("CheckDefaultTrustedRootFreshness: %v", err)
	}
	if freshness.Stale() {
		t.Fatalf("The embedded Sigstore trust root needs refreshing.\n\n%s\n\n"+
			"Why this matters: keyless verification checks a signature's Rekor entry against the log keys in "+
			"this snapshot. Sigstore adds new Rekor log shards over time, and a snapshot that predates a shard "+
			"has no key for it — so every signature logged there fails with an error that looks like a forged "+
			"signature, not like an out-of-date verifier.\n\nRun:\n\t%s\n\n"+
			"then review `git diff internal/adapters/sigstore/` (new log keys / CAs should be additions, and a "+
			"*removed* anchor deserves scrutiny) and commit both trusted-root-public-good.json and "+
			"trusted-root-metadata.json together.",
			freshness.String(), RefreshTrustedRootCommand)
	}
	t.Logf("%s", freshness.String())
}

// TestEmbeddedTrustedRoot_ActiveAnchorExpiryIsWhereExpected keeps the synthetic
// "now" values in this file honest. If a refresh rotates the Fulcio root, the
// expiry-window cases below would keep passing while no longer exercising an
// expiry at all.
func TestEmbeddedTrustedRoot_ActiveAnchorExpiryIsWhereExpected(t *testing.T) {
	meta := mustMetadata(t)

	// A moment just inside the window before the known expiry must be reported;
	// a moment well before it must not.
	near, err := CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta,
		activeFulcioCertNotAfter.Add(-TrustedRootExpiryWindow/2))
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness (near expiry): %v", err)
	}
	if !containsSubstring(near.Problems, "2031-10-05") {
		t.Errorf("expected a problem naming the active Fulcio CA's 2031-10-05 expiry, got %v.\n"+
			"If the snapshot's Fulcio root was rotated, update activeFulcioCertNotAfter in this file — "+
			"otherwise the expiry-window cases here are testing nothing.", near.Problems)
	}
}

func containsSubstring(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// TestCheckTrustedRootFreshness_ReportsOverdueAge proves the age threshold
// actually fires, using a synthetic clock so the assertion is deterministic
// forever rather than only until the snapshot ages out.
func TestCheckTrustedRootFreshness_ReportsOverdueAge(t *testing.T) {
	meta := mustMetadata(t)

	justInside := meta.CapturedAt.Add(TrustedRootMaxAge - time.Hour)
	f, err := CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta, justInside)
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness: %v", err)
	}
	if f.Overdue {
		t.Errorf("snapshot reported overdue one hour before MaxAge elapsed (age %s, limit %s)", f.Age, f.MaxAge)
	}

	justOutside := meta.CapturedAt.Add(TrustedRootMaxAge + time.Hour)
	f, err = CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta, justOutside)
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness: %v", err)
	}
	if !f.Overdue {
		t.Errorf("snapshot not reported overdue one hour after MaxAge elapsed (age %s, limit %s)", f.Age, f.MaxAge)
	}
	if !f.Stale() {
		t.Error("Stale() = false for an overdue snapshot")
	}
	if !strings.Contains(f.String(), RefreshTrustedRootCommand) {
		t.Errorf("staleness message does not name the refresh command; a maintainer reading it would not "+
			"know what to run.\ngot: %s", f.String())
	}
	if !strings.Contains(f.String(), "--sigstore-trusted-root") {
		t.Errorf("staleness message does not mention the --sigstore-trusted-root escape hatch.\ngot: %s", f.String())
	}
}

// TestCheckTrustedRootFreshness_ReportsExpiringActiveAnchor proves the anchor
// expiry half of the guard: a snapshot can be young and still be a problem if
// the CA it depends on is about to die.
func TestCheckTrustedRootFreshness_ReportsExpiringActiveAnchor(t *testing.T) {
	// Pretend the snapshot was captured a week before the Fulcio root expires,
	// so age is nowhere near MaxAge and the only possible finding is the expiry.
	now := activeFulcioCertNotAfter.Add(-7 * 24 * time.Hour)
	meta := mustMetadata(t)
	meta.CapturedAt = now.Add(-24 * time.Hour)

	f, err := CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta, now)
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness: %v", err)
	}
	if f.Overdue {
		t.Fatalf("test setup wrong: snapshot should be one day old, got age %s", f.Age)
	}
	if !f.Stale() {
		t.Fatalf("a snapshot whose active Fulcio root expires in 7 days was reported fresh: %s", f.String())
	}
	if !containsSubstring(f.Problems, "Fulcio") {
		t.Errorf("problems do not mention the expiring Fulcio anchor: %v", f.Problems)
	}
}

// TestCheckTrustedRootFreshness_IgnoresRetiredAnchors is the false-positive
// guard. The public-good trust root permanently carries the retired 2021 Fulcio
// CA and 2021 CT log so that historical signatures still verify at their
// integrated time. Reporting those as expired would fill the output with noise
// that maintainers learn to ignore — which would defeat the whole guard.
func TestCheckTrustedRootFreshness_IgnoresRetiredAnchors(t *testing.T) {
	meta := mustMetadata(t)
	// A "now" long after the 2022-era anchors retired but long before the
	// active ones expire, and close to the capture date so age is not a factor.
	now := meta.CapturedAt
	f, err := CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta, now)
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness: %v", err)
	}
	if f.Stale() {
		t.Fatalf("the freshly captured snapshot was reported stale at its own capture time: %s", f.String())
	}
	// The retired anchors exist in the snapshot; confirm the test is meaningful.
	var doc struct {
		CTLogs []struct {
			BaseURL   string `json:"baseUrl"`
			PublicKey struct {
				ValidFor struct {
					End string `json:"end"`
				} `json:"validFor"`
			} `json:"publicKey"`
		} `json:"ctlogs"`
	}
	if err := json.Unmarshal(DefaultTrustedRootJSON(), &doc); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	retired := 0
	for _, l := range doc.CTLogs {
		if l.PublicKey.ValidFor.End != "" {
			retired++
		}
	}
	if retired == 0 {
		t.Skip("snapshot no longer carries a retired CT log; this test has nothing to guard against")
	}
}

// TestCheckTrustedRootFreshness_ReportsDigestMismatch proves the sidecar's
// digest is enforced, not decorative: without this, a hand-edited snapshot
// would inherit the old capture date and read as fresh.
func TestCheckTrustedRootFreshness_ReportsDigestMismatch(t *testing.T) {
	meta := mustMetadata(t)
	meta.SHA256 = strings.Repeat("00", 32)

	f, err := CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta, meta.CapturedAt)
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness: %v", err)
	}
	if !f.Stale() {
		t.Fatal("a snapshot whose digest disagrees with its provenance sidecar was reported fresh")
	}
	if !containsSubstring(f.Problems, "sha256") {
		t.Errorf("problems do not explain the digest mismatch: %v", f.Problems)
	}
}

// TestCheckTrustedRootFreshness_ReportsFutureCaptureDate covers the skewed
// clock / bad refresh case. A negative age must never be reported as "brand
// new".
func TestCheckTrustedRootFreshness_ReportsFutureCaptureDate(t *testing.T) {
	meta := mustMetadata(t)
	now := meta.CapturedAt.Add(-30 * 24 * time.Hour)

	f, err := CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta, now)
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness: %v", err)
	}
	if !f.Stale() {
		t.Fatal("a snapshot whose capture date is in the future was reported fresh")
	}
	if f.Age < 0 {
		t.Errorf("Age = %s, want a non-negative value", f.Age)
	}
	if !containsSubstring(f.Problems, "future") {
		t.Errorf("problems do not mention the future capture date: %v", f.Problems)
	}
}

// TestCheckTrustedRootFreshness_ReportsMissingActiveAnchors proves the
// "no usable anchor" arm. A snapshot with every anchor retired is worse than
// stale — nothing issued today can chain through it — and must be reported.
func TestCheckTrustedRootFreshness_ReportsMissingActiveAnchors(t *testing.T) {
	meta := mustMetadata(t)
	// Far past every anchor certificate's notAfter in any plausible snapshot.
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	meta.CapturedAt = now.Add(-24 * time.Hour)

	f, err := CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta, now)
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness: %v", err)
	}
	if !f.Stale() {
		t.Fatal("a snapshot with no anchor valid at the current time was reported fresh")
	}
	if !containsSubstring(f.Problems, "no Fulcio certificate authority") {
		t.Errorf("problems do not report the absent Fulcio CA: %v", f.Problems)
	}
}

// TestCheckTrustedRootFreshness_RejectsUnparseableRoot fails closed: a trust
// root that does not parse cannot be assessed, and must not be reported as
// fresh by omission.
func TestCheckTrustedRootFreshness_RejectsUnparseableRoot(t *testing.T) {
	meta := mustMetadata(t)
	if _, err := CheckTrustedRootFreshness([]byte("{not json"), meta, meta.CapturedAt); err == nil {
		t.Fatal("CheckTrustedRootFreshness accepted an unparseable trust root")
	}
}

// TestTrustedRootFreshness_StringOnFreshSnapshotSaysSo guards against the
// summary reading like a warning when nothing is wrong; an operator who sees
// scary text on every successful verification stops reading it.
func TestTrustedRootFreshness_StringOnFreshSnapshotSaysSo(t *testing.T) {
	meta := mustMetadata(t)
	f, err := CheckTrustedRootFreshness(DefaultTrustedRootJSON(), meta, meta.CapturedAt)
	if err != nil {
		t.Fatalf("CheckTrustedRootFreshness: %v", err)
	}
	if strings.Contains(f.String(), "STALE") {
		t.Errorf("fresh snapshot summary reads as stale: %s", f.String())
	}
}
