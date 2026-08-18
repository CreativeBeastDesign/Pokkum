package sigstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
)

// fixtureRekorLogID is the transparency log the captured distroless fixture was
// recorded in: the original public-good Rekor instance.
const fixtureRekorLogID = "c0d23d6ad406973f9559f3ba2d1ca01f84147d8ffc5b8445c224f98b9591801d"

// trustedRootWithoutLog returns the embedded snapshot with the tlog whose
// hex-encoded key ID is logID removed, and nothing else changed.
//
// This is how a trust root captured *before* a Rekor shard existed looks: not
// corrupt, not malformed, just missing one log's key. Recreating it from the
// current snapshot is the only honest way to test the condition — the
// alternative would be pinning a copy of the historical pre-2025 snapshot as a
// fixture, which would then itself rot.
func trustedRootWithoutLog(t *testing.T, logID string) []byte {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(DefaultTrustedRootJSON(), &doc); err != nil {
		t.Fatalf("unmarshal embedded trust root: %v", err)
	}
	tlogs, ok := doc["tlogs"].([]any)
	if !ok {
		t.Fatal("embedded trust root has no tlogs array")
	}

	kept := make([]any, 0, len(tlogs))
	removed := 0
	for _, entry := range tlogs {
		m, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("tlog entry is not an object: %T", entry)
		}
		id, _ := m["logId"].(map[string]any)
		keyID, _ := id["keyId"].(string)
		raw, err := base64.StdEncoding.DecodeString(keyID)
		if err != nil {
			t.Fatalf("tlog logId.keyId %q is not base64: %v", keyID, err)
		}
		if hex.EncodeToString(raw) == logID {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	if removed != 1 {
		t.Fatalf("expected to remove exactly one tlog with key ID %s, removed %d — "+
			"has the fixture or the snapshot changed?", logID, removed)
	}
	if len(kept) == 0 {
		t.Fatal("removing the fixture's log left no tlogs at all; the resulting root would exercise " +
			"the empty-root branch instead of the missing-shard branch this test is about")
	}
	doc["tlogs"] = kept

	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal modified trust root: %v", err)
	}
	// Sanity: it must still be a structurally valid trust root, otherwise the
	// failure under test would be ErrMalformedMaterial, not a coverage gap.
	if _, err := root.NewTrustedRootFromJSON(out); err != nil {
		t.Fatalf("modified trust root no longer parses: %v", err)
	}
	return out
}

// TestVerify_TrustRootMissingTheSignaturesRekorLog is the regression test for
// the failure mode this whole trust-root-staleness work exists to prevent.
//
// Sigstore added the log2025-1.rekor.sigstore.dev shard in September 2025. A
// Pokkum binary carrying a trust-root snapshot captured before that date has no
// key for the shard, so every signature logged there fails — and before this
// change it failed with sigstore-go's generic "not enough verified log entries"
// text, which is the same error a forged signature or a tampered payload
// produces. An operator would reasonably conclude their base image's signature
// was bad.
//
// The verdict must stay fail-closed (it does: ErrTlogInvalid, same as before).
// What must change is that the error says which log was unknown and what to do.
func TestVerify_TrustRootMissingTheSignaturesRekorLog(t *testing.T) {
	req := loadFixture(t)
	req.TrustedRootJSON = trustedRootWithoutLog(t, fixtureRekorLogID)

	_, err := NewVerifier(nil).Verify(context.Background(), req)
	if err == nil {
		t.Fatal("Verify() accepted a signature whose Rekor log is absent from the trust root")
	}
	if !errors.Is(err, ErrTlogInvalid) {
		t.Fatalf("Verify() error = %v, want one wrapping ErrTlogInvalid", err)
	}

	msg := err.Error()
	for _, want := range []string{
		fixtureRekorLogID,              // which log was unknown
		"does not contain",             // what is wrong with the trust root
		"trust-root coverage gap",      // the diagnosis, in words
		"--sigstore-trusted-root",      // what to do about it
		"log2025-1.rekor.sigstore.dev", // the logs it *does* know, for comparison
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message does not mention %q — an operator could not tell a stale trust root "+
				"from a bad signature.\ngot: %s", want, msg)
		}
	}
	t.Logf("rejected as expected: %v", err)
}

// TestVerify_TrustRootWithTheLogStillSucceeds is the other half of the proof
// above, in the sense checklist row 30 demands: without it, the previous test
// could be passing because the fixture is broken in some unrelated way rather
// than because the log-coverage check works.
func TestVerify_TrustRootWithTheLogStillSucceeds(t *testing.T) {
	req := loadFixture(t)
	req.TrustedRootJSON = DefaultTrustedRootJSON()

	if _, err := NewVerifier(nil).Verify(context.Background(), req); err != nil {
		t.Fatalf("the same fixture failed against the full embedded trust root, so "+
			"TestVerify_TrustRootMissingTheSignaturesRekorLog proves nothing: %v", err)
	}
}

// TestCheckRekorLogKnown_IsCaseInsensitive guards a real trap: the trusted
// root's log map is keyed by lower-case hex (hex.EncodeToString), while the log
// ID arrives as text from a registry annotation whose case nobody controls. A
// case-sensitive lookup would reject a perfectly good signature with the
// "unknown log" error — turning a diagnostic aid into a new false positive.
func TestCheckRekorLogKnown_IsCaseInsensitive(t *testing.T) {
	tr, err := root.NewTrustedRootFromJSON(DefaultTrustedRootJSON())
	if err != nil {
		t.Fatalf("parse embedded trust root: %v", err)
	}
	now := mustMetadata(t).CapturedAt

	for _, id := range []string{
		fixtureRekorLogID,
		strings.ToUpper(fixtureRekorLogID),
		"  " + fixtureRekorLogID + "  ",
	} {
		if err := checkRekorLogKnown(tr, id, "embedded public-good snapshot", true, now); err != nil {
			t.Errorf("checkRekorLogKnown(%q) = %v, want nil", id, err)
		}
	}
}

// TestCheckRekorLogKnown_EmbeddedHintNamesTheRefreshCommand covers the arm the
// end-to-end test above cannot reach: when the *embedded* snapshot is the one
// missing the log, the fix is to refresh it, and the error has to say so
// verbatim. (Verify() only takes the embedded path when the request supplies no
// trust root, which no test can combine with a modified root.)
func TestCheckRekorLogKnown_EmbeddedHintNamesTheRefreshCommand(t *testing.T) {
	tr, err := root.NewTrustedRootFromJSON(trustedRootWithoutLog(t, fixtureRekorLogID))
	if err != nil {
		t.Fatalf("parse modified trust root: %v", err)
	}

	err = checkRekorLogKnown(tr, fixtureRekorLogID, "embedded public-good snapshot", true, time.Now())
	if err == nil {
		t.Fatal("checkRekorLogKnown accepted a log absent from the trust root")
	}
	if !errors.Is(err, ErrTlogInvalid) {
		t.Fatalf("error = %v, want one wrapping ErrTlogInvalid", err)
	}
	if !strings.Contains(err.Error(), RefreshTrustedRootCommand) {
		t.Errorf("error does not name the refresh command, so a maintainer is not told what to run.\ngot: %s", err)
	}
}

// TestCheckRekorLogKnown_EmptyTrustRootFailsClosed covers the degenerate case:
// a trust root declaring no transparency logs at all must be rejected with an
// explanation, not silently treated as "nothing to check".
func TestCheckRekorLogKnown_EmptyTrustRootFailsClosed(t *testing.T) {
	empty, err := root.NewTrustedRootFromJSON([]byte(
		`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1"}`))
	if err != nil {
		t.Fatalf("parse minimal trust root: %v", err)
	}

	err = checkRekorLogKnown(empty, fixtureRekorLogID, "caller-supplied", false, time.Now())
	if err == nil {
		t.Fatal("checkRekorLogKnown accepted a trust root with no transparency logs at all")
	}
	if !errors.Is(err, ErrTlogInvalid) {
		t.Fatalf("error = %v, want one wrapping ErrTlogInvalid", err)
	}
	if !strings.Contains(err.Error(), "none") {
		t.Errorf("error does not say the trust root declares no logs.\ngot: %s", err)
	}
}

// TestVerify_StaleEmbeddedRootWarnsButStillVerifies pins the two halves of the
// runtime staleness contract that only Verify() can express, by overriding the
// package clock (nowFunc) so the embedded snapshot reads as long overdue:
//
//  1. Verification still succeeds. Air-gapped operation is a supported mode;
//     failing a signature because the calendar moved — rather than because of
//     anything about the signature — would break hermetic verification on a
//     date, which is worse than the staleness itself.
//  2. It is not silent. A warning naming the refresh command is emitted, so the
//     operator has the context to interpret a later failure.
//
// This is also what makes nowFunc's existence honest: without a test that pins
// it, a package-level clock var is untested indirection.
func TestVerify_StaleEmbeddedRootWarnsButStillVerifies(t *testing.T) {
	original := nowFunc
	t.Cleanup(func() { nowFunc = original })
	stale := mustMetadata(t).CapturedAt.Add(TrustedRootMaxAge + 30*24*time.Hour)
	nowFunc = func() time.Time { return stale }

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// No TrustedRootJSON in the request, so the embedded snapshot is used and
	// the freshness path runs.
	if _, err := NewVerifier(logger).Verify(context.Background(), loadFixture(t)); err != nil {
		t.Fatalf("Verify() failed against a stale-but-covering embedded trust root: %v.\n"+
			"Staleness must warn, never fail: an air-gapped operator verifying a signature the "+
			"snapshot does cover is doing nothing wrong.", err)
	}

	out := logBuf.String()
	if !strings.Contains(out, "stale embedded Sigstore trust root") {
		t.Fatalf("no staleness warning was emitted during verification:\n%s", out)
	}
	if !strings.Contains(out, RefreshTrustedRootCommand) {
		t.Errorf("staleness warning does not name the refresh command:\n%s", out)
	}
}

// TestVerify_FreshEmbeddedRootDoesNotWarn is the false-positive half: a warning
// on every successful verification is a warning nobody reads.
func TestVerify_FreshEmbeddedRootDoesNotWarn(t *testing.T) {
	original := nowFunc
	t.Cleanup(func() { nowFunc = original })
	nowFunc = func() time.Time { return mustMetadata(t).CapturedAt }

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if _, err := NewVerifier(logger).Verify(context.Background(), loadFixture(t)); err != nil {
		t.Fatalf("Verify() failed against the fresh embedded trust root: %v", err)
	}
	if out := logBuf.String(); strings.TrimSpace(out) != "" {
		t.Errorf("a successful verification against a current trust root logged warnings:\n%s", out)
	}
}
