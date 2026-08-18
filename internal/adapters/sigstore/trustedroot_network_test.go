package sigstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// updateEnvVar, when set to "1", turns TestTrustedRootSnapshot_TracksLiveTUFRepository
// from a check into a regeneration step. This is Go's golden-file `-update`
// convention: the code that detects divergence from the live repository is the
// same code that fixes it, so the two cannot drift apart the way a separate
// refresh script would.
const updateEnvVar = "POKKUM_UPDATE_SIGSTORE_TRUSTED_ROOT"

const (
	snapshotFile = "trusted-root-public-good.json"
	metadataFile = "trusted-root-metadata.json"
)

// anchorSet is the security-relevant content of a trust root: which keys and
// CAs it will accept material from. Two trust roots with the same anchorSet
// accept and reject exactly the same signatures, whatever else differs
// (formatting, field ordering, base URLs).
//
// Timestamping authorities are excluded because this adapter verifies with
// WithIntegratedTimestamps only and never consumes an RFC3161 timestamp — a
// difference there cannot change a verification outcome here, and including it
// would make the guard fail for a reason nobody needs to act on.
type anchorSet struct {
	tlogs  []string
	ctlogs []string
	cas    []string
}

func extractAnchorSet(t *testing.T, what string, rootJSON []byte) anchorSet {
	t.Helper()

	var doc struct {
		Tlogs  []anchorLog `json:"tlogs"`
		Ctlogs []anchorLog `json:"ctlogs"`
		CAs    []struct {
			URI       string `json:"uri"`
			CertChain struct {
				Certificates []struct {
					RawBytes string `json:"rawBytes"`
				} `json:"certificates"`
			} `json:"certChain"`
		} `json:"certificateAuthorities"`
	}
	if err := json.Unmarshal(rootJSON, &doc); err != nil {
		t.Fatalf("unmarshal %s trust root: %v", what, err)
	}

	set := anchorSet{}
	for _, l := range doc.Tlogs {
		set.tlogs = append(set.tlogs, l.label())
	}
	for _, l := range doc.Ctlogs {
		set.ctlogs = append(set.ctlogs, l.label())
	}
	for _, ca := range doc.CAs {
		// Identify a CA by its root certificate (the last entry in the chain):
		// that is the anchor x509 path building actually terminates at.
		certs := ca.CertChain.Certificates
		if len(certs) == 0 {
			t.Fatalf("%s trust root has a certificateAuthority with an empty certChain", what)
		}
		set.cas = append(set.cas, fmt.Sprintf("%s/%s", ca.URI, certs[len(certs)-1].RawBytes))
	}

	sort.Strings(set.tlogs)
	sort.Strings(set.ctlogs)
	sort.Strings(set.cas)
	return set
}

type anchorLog struct {
	BaseURL string `json:"baseUrl"`
	LogID   struct {
		KeyID string `json:"keyId"`
	} `json:"logId"`
	PublicKey struct {
		RawBytes string `json:"rawBytes"`
	} `json:"publicKey"`
}

// label identifies a log by its key ID *and* its key material. Key ID alone
// would miss the worst case — a log whose ID is unchanged but whose key was
// rotated — which is exactly what testdata/trusted-root-wrong-keys.json
// simulates.
func (l anchorLog) label() string {
	return fmt.Sprintf("%s(%s)/%s", l.BaseURL, l.LogID.KeyID, l.PublicKey.RawBytes)
}

func diffStrings(want, got []string) (missing, extra []string) {
	have := make(map[string]bool, len(got))
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	wanted := make(map[string]bool, len(want))
	for _, w := range want {
		wanted[w] = true
	}
	for _, g := range got {
		if !wanted[g] {
			extra = append(extra, g)
		}
	}
	return missing, extra
}

// TestTrustedRootSnapshot_TracksLiveTUFRepository is the second staleness
// guard, and the one with teeth: it compares the embedded snapshot's trust
// anchors against Sigstore's live TUF repository.
//
// The age guard (TestEmbeddedTrustedRoot_IsFreshNow) notices the calendar
// moving; this one notices Sigstore actually changing. A new Rekor shard is the
// case that matters: verification against a snapshot that predates it fails
// with an error indistinguishable from a forged signature.
//
// It is skipped under -short and skipped (not failed) when the repository is
// unreachable — a guard that breaks the build on a flaky network gets disabled,
// and then guards nothing. The always-on age guard is what covers the offline
// case.
//
// Setting POKKUM_UPDATE_SIGSTORE_TRUSTED_ROOT=1 makes this test regenerate the
// snapshot and its provenance sidecar instead of checking them. See
// RefreshTrustedRootCommand.
func TestTrustedRootSnapshot_TracksLiveTUFRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live Sigstore TUF fetch in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	opts := DefaultTUFOptions()
	// A throwaway cache forces a genuine fetch: reusing the developer's cache
	// could satisfy this test from a stale local copy, which is the very thing
	// it is supposed to detect.
	opts.CachePath = t.TempDir()

	live, err := FetchTrustedRootJSON(ctx, opts)
	if err != nil {
		t.Skipf("cannot reach the Sigstore TUF repository (%s), so snapshot divergence cannot be checked: %v\n"+
			"This is a skip rather than a failure on purpose; the always-on age guard "+
			"(TestEmbeddedTrustedRoot_IsFreshNow) still bounds how stale the snapshot may get.",
			opts.repositoryBaseURL(), err)
	}

	if os.Getenv(updateEnvVar) == "1" {
		updateEmbeddedSnapshot(t, live)
		return
	}

	wantSet := extractAnchorSet(t, "live TUF", live)
	gotSet := extractAnchorSet(t, "embedded", DefaultTrustedRootJSON())

	report := func(kind string, want, got []string) {
		missing, extra := diffStrings(want, got)
		for _, m := range missing {
			t.Errorf("the live Sigstore trust root has a %s the embedded snapshot does not: %s\n"+
				"Any signature recorded there will fail verification with an error that looks like a bad "+
				"signature. Refresh the snapshot with:\n\t%s", kind, m, RefreshTrustedRootCommand)
		}
		for _, e := range extra {
			t.Errorf("the embedded snapshot carries a %s the live Sigstore trust root no longer does: %s\n"+
				"An anchor dropped upstream may have been retired or revoked; continuing to trust it is a "+
				"real risk. Review it, then refresh with:\n\t%s", kind, e, RefreshTrustedRootCommand)
		}
	}
	report("Rekor transparency log", wantSet.tlogs, gotSet.tlogs)
	report("CT log", wantSet.ctlogs, gotSet.ctlogs)
	report("Fulcio certificate authority", wantSet.cas, gotSet.cas)

	if !t.Failed() {
		t.Logf("embedded snapshot's trust anchors match the live TUF repository (%d Rekor log(s), %d CT log(s), %d Fulcio CA(s))",
			len(gotSet.tlogs), len(gotSet.ctlogs), len(gotSet.cas))
	}
}

// updateEmbeddedSnapshot writes the fetched trust root and a matching
// provenance sidecar into the package directory.
//
// Both files are written together, always: a refresh that updated one and not
// the other would leave the sidecar's capture date describing different bytes,
// which is precisely what TestDefaultTrustedRootMetadata_MatchesSnapshot exists
// to catch. Writing them here as a pair means that state is not reachable
// through the sanctioned refresh path.
func updateEmbeddedSnapshot(t *testing.T, live []byte) {
	t.Helper()

	before := DefaultTrustedRootJSON()
	if string(before) == string(live) {
		t.Logf("%s is already byte-identical to the live TUF target; nothing to update", snapshotFile)
		return
	}

	beforeSet := extractAnchorSet(t, "embedded", before)
	afterSet := extractAnchorSet(t, "live TUF", live)

	// The snapshot is checked in read-only so it is not edited by hand; the
	// refresh path is allowed to replace it.
	if err := os.Remove(snapshotFile); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s before rewriting it: %v", snapshotFile, err)
	}
	if err := os.WriteFile(snapshotFile, live, 0o444); err != nil {
		t.Fatalf("write %s: %v", snapshotFile, err)
	}

	meta := TrustedRootMetadata{
		CapturedAt:    time.Now().UTC().Truncate(24 * time.Hour),
		SHA256:        digestOf(live),
		Source:        "tuf-target",
		TUFRepository: TrustedRootTUFRepository,
		TUFTarget:     TrustedRootTUFTarget,
	}
	encoded, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", metadataFile, err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(metadataFile, encoded, 0o644); err != nil {
		t.Fatalf("write %s: %v", metadataFile, err)
	}

	t.Logf("updated %s (%d -> %d bytes, sha256 %s) and %s (capturedAt %s)",
		snapshotFile, len(before), len(live), meta.SHA256, metadataFile,
		meta.CapturedAt.Format(time.RFC3339))

	for _, change := range describeAnchorChanges(beforeSet, afterSet) {
		t.Logf("  %s", change)
	}
	t.Logf("Review `git diff internal/adapters/sigstore/` before committing. Added anchors are the "+
		"expected case; a REMOVED anchor means Sigstore retired or revoked one, and the fixture in "+
		"testdata/ may need regenerating too. Re-run without %s=1 to confirm the guard now passes.",
		updateEnvVar)
}

func describeAnchorChanges(before, after anchorSet) []string {
	var out []string
	for _, kind := range []struct {
		name          string
		before, after []string
	}{
		{"Rekor transparency log", before.tlogs, after.tlogs},
		{"CT log", before.ctlogs, after.ctlogs},
		{"Fulcio certificate authority", before.cas, after.cas},
	} {
		added, removed := diffStrings(kind.after, kind.before)
		for _, a := range added {
			out = append(out, fmt.Sprintf("ADDED %s: %s", kind.name, truncateLabel(a)))
		}
		for _, r := range removed {
			out = append(out, fmt.Sprintf("REMOVED %s: %s", kind.name, truncateLabel(r)))
		}
	}
	if len(out) == 0 {
		out = append(out, "no trust anchor changes; the difference is formatting or non-anchor metadata only")
	}
	return out
}

// truncateLabel keeps anchor labels (which embed base64 key material) readable
// in test output.
func truncateLabel(s string) string {
	const max = 96
	if len(s) <= max {
		return s
	}
	return s[:max] + "..." + fmt.Sprintf("(%d bytes total)", len(s))
}

// TestUpdateEnvVarMatchesRefreshCommand keeps the advertised command and the
// mechanism that implements it in sync. RefreshTrustedRootCommand is quoted in
// every staleness message an operator or maintainer will ever see; if it named
// a variable or test that no longer existed, the guard would tell people to run
// something that does nothing.
func TestUpdateEnvVarMatchesRefreshCommand(t *testing.T) {
	for _, want := range []string{
		updateEnvVar + "=1",
		"TestTrustedRootSnapshot_TracksLiveTUFRepository",
		"./internal/adapters/sigstore/",
	} {
		if !strings.Contains(RefreshTrustedRootCommand, want) {
			t.Errorf("RefreshTrustedRootCommand (%q) does not contain %q, so following it would not "+
				"actually refresh the snapshot", RefreshTrustedRootCommand, want)
		}
	}
}
