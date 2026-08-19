package baseimage

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/lockfileutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// --- helpers -------------------------------------------------------------

// lockfileKeys returns the lockfile's slot names sorted, so a failure message
// says which slots actually exist instead of only that the expected one does
// not.
func lockfileKeys(lf *ports.PokkumLockfile) []string {
	if lf == nil || lf.Bases == nil {
		return nil
	}
	keys := make([]string, 0, len(lf.Bases))
	for k := range lf.Bases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// digestOf returns the manifest digest a reference currently resolves to,
// straight from the registry, so a test can pin what it seeded a lockfile with
// independently of anything the resolver reports.
func digestOf(t *testing.T, ref string) v1.Hash {
	t.Helper()
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", ref, err)
	}
	desc, err := remote.Get(parsed)
	if err != nil {
		t.Fatalf("remote.Get(%q): %v", ref, err)
	}
	return desc.Digest
}

// seedLockfile writes a pokkum.lock containing exactly the given entries, so a
// test can construct the on-disk state a previous Pokkum version would have
// left behind.
func seedLockfile(t *testing.T, path string, entries map[string]ports.BaseLockEntry) {
	t.Helper()
	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases:     entries,
	}
	if err := lockfileutils.SaveLockfile(path, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}
}

func loadLockfile(t *testing.T, path string) *ports.PokkumLockfile {
	t.Helper()
	lf, err := lockfileutils.LoadLockfile(path)
	if err != nil {
		t.Fatalf("LoadLockfile: %v", err)
	}
	return lf
}

// --- lockKeyFor ----------------------------------------------------------

// TestLockKeyFor_FixedPresetsKeepTheirHistoricalKeys guards the half of the
// keying scheme that must NOT change: every existing pokkum.lock in the world
// stores its distroless/chainguard/distroless-node pin under the bare preset
// string, and re-keying those would orphan every one of those pins.
func TestLockKeyFor_FixedPresetsKeepTheirHistoricalKeys(t *testing.T) {
	for _, preset := range []ports.BaseImagePreset{
		ports.BaseImageDistroless,
		ports.BaseImageChainguard,
		ports.BaseImageDistrolessNode,
	} {
		// A Ref override must not move a fixed preset's slot either: the
		// preset, not the reference, is what identifies these entries.
		for _, ref := range []string{"", "gcr.io/distroless/cc-debian12:nonroot", "example.com/other:v1"} {
			if got := lockKeyFor(preset, ref); got != string(preset) {
				t.Errorf("lockKeyFor(%q, %q) = %q, want the bare preset string %q",
					preset, ref, got, string(preset))
			}
		}
	}
}

// TestLockKeyFor_CustomIsPerReference covers the three properties the custom
// scheme has to have: distinct references get distinct slots, the same image
// spelled two ways gets one slot, and the key is recognisably namespaced under
// the legacy name rather than colliding with it.
func TestLockKeyFor_CustomIsPerReference(t *testing.T) {
	shape := regexp.MustCompile(`^custom:[0-9a-f]{12}$`)

	a := lockKeyFor(ports.BaseImageCustom, "example.com/team/base-a:v1")
	b := lockKeyFor(ports.BaseImageCustom, "example.com/team/base-b:v1")

	for _, k := range []string{a, b} {
		if !shape.MatchString(k) {
			t.Errorf("custom lock key %q does not match %v", k, shape)
		}
		if k == legacyCustomLockKey {
			t.Errorf("custom lock key %q collides with the legacy shared slot", k)
		}
	}
	if a == b {
		t.Errorf("two different custom references share lock key %q; that is the bug this scheme exists to fix", a)
	}

	// Normalization: the same image spelled short and fully qualified must not
	// occupy two slots, or the entry.Ref guard and the slot would disagree
	// about what "the same reference" means.
	short := lockKeyFor(ports.BaseImageCustom, "alpine:3.19")
	long := lockKeyFor(ports.BaseImageCustom, "index.docker.io/library/alpine:3.19")
	if short != long {
		t.Errorf("lockKeyFor differs between spellings of one image: %q vs %q", short, long)
	}
	// Surrounding whitespace is not an identity either.
	if padded := lockKeyFor(ports.BaseImageCustom, "  alpine:3.19  "); padded != short {
		t.Errorf("lockKeyFor(%q) = %q, want %q", "  alpine:3.19  ", padded, short)
	}
	// An unparseable reference still yields a stable key rather than panicking
	// or collapsing every bad input into one slot.
	if bad1, bad2 := lockKeyFor(ports.BaseImageCustom, "NOT A REF"), lockKeyFor(ports.BaseImageCustom, "ALSO NOT A REF"); bad1 == bad2 {
		t.Errorf("two different unparseable references share lock key %q", bad1)
	}
}

// TestLockKeyPrefixMatchesTheCustomPreset pins the cross-package relationship
// between the slot names this file writes and the slot names
// lockfileutils.PresetNameForLockKey has to interpret. Without it, renaming the
// custom preset (or the prefix) would compile cleanly and quietly drop every
// custom base out of `pokkum base check`'s output.
func TestLockKeyPrefixMatchesTheCustomPreset(t *testing.T) {
	if lockfileutils.CustomLockKeyPrefix != legacyCustomLockKey+":" {
		t.Fatalf("lockfileutils.CustomLockKeyPrefix = %q, want %q",
			lockfileutils.CustomLockKeyPrefix, legacyCustomLockKey+":")
	}
	key := lockKeyFor(ports.BaseImageCustom, "example.com/team/base:v1")
	if got := lockfileutils.PresetNameForLockKey(key); got != legacyCustomLockKey {
		t.Errorf("PresetNameForLockKey(%q) = %q, want %q", key, got, legacyCustomLockKey)
	}
	for _, preset := range []ports.BaseImagePreset{
		ports.BaseImageDistroless, ports.BaseImageChainguard, ports.BaseImageDistrolessNode, ports.BaseImageCustom,
	} {
		k := string(preset)
		if got := lockfileutils.PresetNameForLockKey(k); got != k {
			t.Errorf("PresetNameForLockKey(%q) = %q, want it unchanged", k, got)
		}
	}
}

func TestSameLockedRef(t *testing.T) {
	cases := []struct {
		entryRef, ref string
		want          bool
	}{
		{"alpine:3.19", "alpine:3.19", true},
		{"alpine:3.19", "index.docker.io/library/alpine:3.19", true},
		{"alpine:3.19", "alpine:3.20", false},
		{"", "alpine:3.19", false},
		{"alpine:3.19", "", false},
		// An entry that says nothing must not match a request that says
		// nothing either: "no recorded reference" is not an identity.
		{"", "", true},
	}
	for _, c := range cases {
		if got := sameLockedRef(c.entryRef, c.ref); got != c.want {
			t.Errorf("sameLockedRef(%q, %q) = %v, want %v", c.entryRef, c.ref, got, c.want)
		}
	}
}

// --- the actual defect: two custom bases evicting each other --------------

// TestResolve_TwoCustomRefs_EachKeepsItsOwnLockSlot is the regression test for
// the item this change exists for. Two genuinely different images are resolved
// as custom bases into one pokkum.lock; both must end up with their own pinned
// slot, and — the part that actually matters to a user — both pins must still
// be *usable* afterwards. To prove the pins are real rather than incidental,
// both tags are retargeted to fresh content after locking and both are then
// resolved offline through a brand-new Resolver (so nothing can come from the
// in-process pull cache): each must come back with the digest it was locked to,
// not the digest its tag now points at.
//
// Under the old single-"custom"-slot scheme, resolving the second reference
// overwrote the first's entry, so the first reference had no pin left at all
// and the offline resolve below could not be satisfied.
func TestResolve_TwoCustomRefs_EachKeepsItsOwnLockSlot(t *testing.T) {
	s, _ := newTestRegistry(t)
	ctx := context.Background()
	lockPath := filepath.Join(t.TempDir(), "pokkum.lock")

	refA := pushImage(t, s, "app/base-a:v1", ports.LinuxAMD64)
	refB := pushImage(t, s, "app/base-b:v1", ports.LinuxAMD64)

	r := NewResolver(nil)
	gotA, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          refA,
		LockfilePath: lockPath,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("Resolve(A): %v", err)
	}
	gotB, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          refB,
		LockfilePath: lockPath,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("Resolve(B): %v", err)
	}
	if gotA.Digest == gotB.Digest {
		t.Fatalf("fixture is degenerate: both custom bases resolved to %s, so nothing about slot separation can be proven", gotA.Digest)
	}

	keyA := lockKeyFor(ports.BaseImageCustom, refA)
	keyB := lockKeyFor(ports.BaseImageCustom, refB)

	lf := loadLockfile(t, lockPath)
	if len(lf.Bases) != 2 {
		t.Fatalf("pokkum.lock holds %d entries (%v), want one per custom reference", len(lf.Bases), lockfileKeys(lf))
	}
	if _, ok := lf.Bases[legacyCustomLockKey]; ok {
		t.Errorf("a bare %q slot was written; the legacy key must only ever be read", legacyCustomLockKey)
	}
	for _, c := range []struct {
		name   string
		key    string
		ref    string
		digest v1.Hash
	}{
		{"A", keyA, refA, gotA.Digest},
		{"B", keyB, refB, gotB.Digest},
	} {
		entry, ok := lf.Bases[c.key]
		if !ok {
			t.Fatalf("no lockfile entry for custom base %s under %q; keys present: %v", c.name, c.key, lockfileKeys(lf))
		}
		if entry.Ref != c.ref {
			t.Errorf("entry %s: Ref = %q, want %q", c.name, entry.Ref, c.ref)
		}
		if entry.Digest != c.digest.String() {
			t.Errorf("entry %s: Digest = %q, want %q", c.name, entry.Digest, c.digest)
		}
	}

	// Retarget both tags to different content. A pin that is not honoured now
	// resolves to the new digest instead of the locked one.
	pushImage(t, s, "app/base-a:v1", ports.LinuxAMD64)
	pushImage(t, s, "app/base-b:v1", ports.LinuxAMD64)
	if moved := digestOf(t, refA); moved == gotA.Digest {
		t.Fatalf("retagging A did not change its digest (%s); the pin assertions below would be vacuous", moved)
	}

	fresh := NewResolver(nil)
	for _, c := range []struct {
		name   string
		ref    string
		locked v1.Hash
	}{
		{"A", refA, gotA.Digest},
		{"B", refB, gotB.Digest},
	} {
		got, err := fresh.Resolve(ctx, ports.BaseImageRequest{
			Preset:       ports.BaseImageCustom,
			Ref:          c.ref,
			LockfilePath: lockPath,
			Offline:      true,
			Platforms:    []ports.Platform{ports.LinuxAMD64},
		})
		if err != nil {
			t.Fatalf("offline Resolve(%s) after both bases were locked: %v (its pin was evicted by the other custom base)", c.name, err)
		}
		if got.Digest != c.locked {
			t.Errorf("offline Resolve(%s) = %s, want the locked digest %s", c.name, got.Digest, c.locked)
		}
	}
}

// --- migration ------------------------------------------------------------

// TestResolve_LegacyCustomSlot_HonouredAndMigratedWhenRefMatches is the
// forward half of the migration story: a pokkum.lock written before
// per-reference slots existed keeps its custom pin under the bare "custom"
// key, and that pin must survive the upgrade.
//
// The tag is retargeted to fresh content before resolving, so an
// implementation that ignored the legacy slot would silently re-pull and
// re-pin the moved tag — the pin would be gone and the test would see the new
// digest. The scan metadata is checked too, because discarding it would
// re-attribute an already-completed scan to nothing.
func TestResolve_LegacyCustomSlot_HonouredAndMigratedWhenRefMatches(t *testing.T) {
	s, _ := newTestRegistry(t)
	ctx := context.Background()
	lockPath := filepath.Join(t.TempDir(), "pokkum.lock")

	ref := pushImage(t, s, "app/legacy-base:v1", ports.LinuxAMD64)
	lockedDigest := digestOf(t, ref)
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	legacyEntry := ports.BaseLockEntry{
		Ref:                  ref,
		Digest:               lockedDigest.String(),
		PinnedRef:            pinnedRef(parsed, lockedDigest),
		LastScannedAt:        "2026-01-02T03:04:05Z",
		VulnerabilitiesCount: 7,
		MaxSeverity:          string(ports.SeverityHigh),
		UpdatedAt:            "2026-01-02T03:04:05Z",
	}
	seedLockfile(t, lockPath, map[string]ports.BaseLockEntry{legacyCustomLockKey: legacyEntry})

	// Move the tag. Only an honoured pin can still produce lockedDigest now.
	pushImage(t, s, "app/legacy-base:v1", ports.LinuxAMD64)
	if moved := digestOf(t, ref); moved == lockedDigest {
		t.Fatalf("retagging did not change the digest (%s); this test would prove nothing", moved)
	}

	got, err := NewResolver(nil).Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          ref,
		LockfilePath: lockPath,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Digest != lockedDigest {
		t.Fatalf("Resolve = %s, want the legacy-locked digest %s; the legacy \"custom\" pin was discarded rather than migrated", got.Digest, lockedDigest)
	}
	if got.LastScannedAt != legacyEntry.LastScannedAt || got.VulnerabilitiesCount != legacyEntry.VulnerabilitiesCount {
		t.Errorf("scan metadata not carried over from the legacy entry: LastScannedAt=%q vulns=%d, want %q / %d",
			got.LastScannedAt, got.VulnerabilitiesCount, legacyEntry.LastScannedAt, legacyEntry.VulnerabilitiesCount)
	}

	lf := loadLockfile(t, lockPath)
	newKey := lockKeyFor(ports.BaseImageCustom, ref)
	migrated, ok := lf.Bases[newKey]
	if !ok {
		t.Fatalf("legacy entry was honoured but never rewritten under %q; the migration never completes and every later build keeps reading the shared slot (keys: %v)",
			newKey, lockfileKeys(lf))
	}
	if migrated.Digest != legacyEntry.Digest || migrated.PinnedRef != legacyEntry.PinnedRef || migrated.Ref != legacyEntry.Ref {
		t.Errorf("migrated entry is not a faithful copy:\n got %+v\nwant %+v", migrated, legacyEntry)
	}
	if migrated.VulnerabilitiesCount != legacyEntry.VulnerabilitiesCount || migrated.MaxSeverity != legacyEntry.MaxSeverity {
		t.Errorf("migrated entry lost its scan metadata: %+v", migrated)
	}
	if _, still := lf.Bases[legacyCustomLockKey]; still {
		t.Errorf("legacy %q slot survived its own migration; the duplicate would diverge from %q on the next --update-base",
			legacyCustomLockKey, newKey)
	}

	// The migrated slot must be usable on its own terms: offline, with a fresh
	// resolver, and with no legacy entry left to fall back on.
	after, err := NewResolver(nil).Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          ref,
		LockfilePath: lockPath,
		Offline:      true,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("offline Resolve after migration: %v", err)
	}
	if after.Digest != lockedDigest {
		t.Errorf("offline Resolve after migration = %s, want %s", after.Digest, lockedDigest)
	}
}

// TestResolve_LegacyCustomSlot_NotTrustedWhenRefDiffers is the reverse half.
// A bare "custom" entry that belongs to some *other* custom reference must
// never be honoured for the reference being resolved — that is the
// wrong-image-served bug — and it must not be deleted either, because it is
// still that other reference's only pin and its own next build's migration
// source.
func TestResolve_LegacyCustomSlot_NotTrustedWhenRefDiffers(t *testing.T) {
	s, _ := newTestRegistry(t)
	ctx := context.Background()
	lockPath := filepath.Join(t.TempDir(), "pokkum.lock")

	otherRef := pushImage(t, s, "app/other-base:v1", ports.LinuxAMD64)
	wantedRef := pushImage(t, s, "app/wanted-base:v1", ports.LinuxAMD64)
	otherDigest := digestOf(t, otherRef)
	wantedDigest := digestOf(t, wantedRef)
	if otherDigest == wantedDigest {
		t.Fatalf("fixture is degenerate: both images have digest %s", otherDigest)
	}
	otherParsed, err := name.ParseReference(otherRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	legacyEntry := ports.BaseLockEntry{
		Ref:                  otherRef,
		Digest:               otherDigest.String(),
		PinnedRef:            pinnedRef(otherParsed, otherDigest),
		LastScannedAt:        "2026-01-02T03:04:05Z",
		VulnerabilitiesCount: 9,
		MaxSeverity:          string(ports.SeverityCritical),
		UpdatedAt:            "2026-01-02T03:04:05Z",
	}
	seedLockfile(t, lockPath, map[string]ports.BaseLockEntry{legacyCustomLockKey: legacyEntry})

	got, err := NewResolver(nil).Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          wantedRef,
		LockfilePath: lockPath,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Digest == otherDigest {
		t.Fatalf("Resolve(%s) returned the *other* custom base's content (%s): a mismatched legacy entry was trusted", wantedRef, got.Digest)
	}
	if got.Digest != wantedDigest {
		t.Fatalf("Resolve = %s, want %s", got.Digest, wantedDigest)
	}
	if got.LastScannedAt != "" || got.VulnerabilitiesCount != 0 {
		t.Errorf("scan metadata bled across references: LastScannedAt=%q vulns=%d", got.LastScannedAt, got.VulnerabilitiesCount)
	}

	lf := loadLockfile(t, lockPath)
	wantedKey := lockKeyFor(ports.BaseImageCustom, wantedRef)
	entry, ok := lf.Bases[wantedKey]
	if !ok {
		t.Fatalf("no entry written for %s under %q; keys: %v", wantedRef, wantedKey, lockfileKeys(lf))
	}
	if entry.Digest != wantedDigest.String() {
		t.Errorf("entry for %s has Digest %q, want %q", wantedRef, entry.Digest, wantedDigest)
	}
	if entry.VulnerabilitiesCount != 0 || entry.LastScannedAt != "" {
		t.Errorf("new entry inherited the mismatched legacy entry's scan metadata: %+v", entry)
	}
	survivor, still := lf.Bases[legacyCustomLockKey]
	if !still {
		t.Fatalf("the legacy %q slot was deleted although it describes a different reference (%s); that discards a pin this resolve was never entitled to touch",
			legacyCustomLockKey, otherRef)
	}
	if survivor != legacyEntry {
		t.Errorf("the other reference's legacy entry was modified:\n got %+v\nwant %+v", survivor, legacyEntry)
	}
}

// TestResolve_FixedPresetSlotUnchanged proves the migration did not disturb the
// presets it was not about: an existing lockfile keyed by the bare preset name
// is still read, still honoured against a moved tag, and still written back
// under that same key.
func TestResolve_FixedPresetSlotUnchanged(t *testing.T) {
	s, _ := newTestRegistry(t)
	ctx := context.Background()
	lockPath := filepath.Join(t.TempDir(), "pokkum.lock")

	ref := pushImage(t, s, "app/preset-base:v1", ports.LinuxAMD64)
	lockedDigest := digestOf(t, ref)
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	seedLockfile(t, lockPath, map[string]ports.BaseLockEntry{
		string(ports.BaseImageDistroless): {
			Ref:       ref,
			Digest:    lockedDigest.String(),
			PinnedRef: pinnedRef(parsed, lockedDigest),
			UpdatedAt: "2026-01-02T03:04:05Z",
		},
	})

	pushImage(t, s, "app/preset-base:v1", ports.LinuxAMD64)
	if moved := digestOf(t, ref); moved == lockedDigest {
		t.Fatalf("retagging did not change the digest (%s)", moved)
	}

	got, err := NewResolver(nil).Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageDistroless,
		Ref:          ref,
		LockfilePath: lockPath,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Digest != lockedDigest {
		t.Errorf("Resolve = %s, want the locked digest %s; a fixed preset's existing slot stopped being honoured", got.Digest, lockedDigest)
	}

	lf := loadLockfile(t, lockPath)
	if _, ok := lf.Bases[string(ports.BaseImageDistroless)]; !ok {
		t.Errorf("the %q slot disappeared; keys: %v", ports.BaseImageDistroless, lockfileKeys(lf))
	}
	for _, k := range lockfileKeys(lf) {
		if k != string(ports.BaseImageDistroless) {
			t.Errorf("unexpected extra slot %q written for a fixed preset", k)
		}
	}
}

// TestRecordScanResult_LandsInTheReferencesOwnSlot proves the scan recorder
// follows the same keying. Two custom references are locked in one lockfile;
// recording a scan for one must update that one and leave the other untouched.
// Keyed by preset alone — as it was before this change — both references named
// the same slot, so one reference's findings were attributed to whichever
// custom base happened to own the shared entry.
func TestRecordScanResult_LandsInTheReferencesOwnSlot(t *testing.T) {
	s, _ := newTestRegistry(t)
	ctx := context.Background()
	lockPath := filepath.Join(t.TempDir(), "pokkum.lock")

	refA := pushImage(t, s, "app/scan-a:v1", ports.LinuxAMD64)
	refB := pushImage(t, s, "app/scan-b:v1", ports.LinuxAMD64)

	r := NewResolver(nil)
	for _, ref := range []string{refA, refB} {
		if _, err := r.Resolve(ctx, ports.BaseImageRequest{
			Preset:       ports.BaseImageCustom,
			Ref:          ref,
			LockfilePath: lockPath,
			Platforms:    []ports.Platform{ports.LinuxAMD64},
		}); err != nil {
			t.Fatalf("Resolve(%s): %v", ref, err)
		}
	}

	scan := ports.ScanResult{
		Target:           refB,
		MaxSeverityFound: ports.SeverityHigh,
		Vulnerabilities:  []ports.Vulnerability{{ID: "CVE-2026-1234", Severity: ports.SeverityHigh}},
	}
	if err := r.RecordScanResult(ctx, lockPath, ports.BaseImageCustom, refB, scan); err != nil {
		t.Fatalf("RecordScanResult: %v", err)
	}

	lf := loadLockfile(t, lockPath)
	entryB, ok := lf.Bases[lockKeyFor(ports.BaseImageCustom, refB)]
	if !ok {
		t.Fatalf("no entry for B; keys: %v", lockfileKeys(lf))
	}
	if entryB.VulnerabilitiesCount != 1 || entryB.MaxSeverity != string(ports.SeverityHigh) {
		t.Errorf("B's entry did not record the scan: %+v", entryB)
	}
	entryA, ok := lf.Bases[lockKeyFor(ports.BaseImageCustom, refA)]
	if !ok {
		t.Fatalf("no entry for A; keys: %v", lockfileKeys(lf))
	}
	if entryA.VulnerabilitiesCount != 0 || entryA.LastScannedAt != "" || entryA.MaxSeverity != "" {
		t.Errorf("A's entry was credited with B's scan findings: %+v", entryA)
	}
}

// --- Task 1: TrustedRootJSON ---------------------------------------------

// recordingKeylessVerifier captures the KeylessVerifyRequest it is handed and
// then refuses, so a test can assert on what the resolver passed down without
// needing verification to succeed.
type recordingKeylessVerifier struct {
	got ports.KeylessVerifyRequest
}

func (v *recordingKeylessVerifier) Verify(_ context.Context, req ports.KeylessVerifyRequest) (ports.KeylessVerifyResult, error) {
	v.got = req
	return ports.KeylessVerifyResult{}, errors.New("recording verifier: refusing by design")
}

// TestVerifyBaseImage_TrustedRootJSONReachesVerifierWithoutAFileRead proves the
// bytes-not-path change end to end at the adapter boundary: the resolver hands
// the KeylessVerifier exactly the bytes it was given in the request, and does
// no filesystem access of its own to get them. The sentinel bytes below are not
// a valid trusted root and never need to be — what is under test is that they
// arrive unmodified, which a path-based field could not express without a temp
// file somewhere.
func TestVerifyBaseImage_TrustedRootJSONReachesVerifierWithoutAFileRead(t *testing.T) {
	s, _ := newTestRegistry(t)
	ctx := context.Background()

	ref := pushImage(t, s, "app/trusted-root-bytes:v1", ports.LinuxAMD64)
	sentinel := []byte(`{"sentinel":"trusted-root-bytes-not-a-path"}`)

	rec := &recordingKeylessVerifier{}
	r := NewResolver(nil, WithKeylessVerifier(rec))

	pre, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
		Insecure:  true,
	})
	if err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	pushKeylessSignatureFixture(t, s, parsed.Context().Name(), pre.Digest, "../sigstore/testdata/distroless-nonroot")

	req := ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: true,
		VerifyMode:      ports.BaseImageVerifyKeyless,
		KeylessIdentity: ports.KeylessIdentity{
			SAN:    "https://github.com/example/repo/.github/workflows/release.yml@refs/heads/main",
			Issuer: "https://token.actions.githubusercontent.com",
		},
		TrustedRootJSON: sentinel,
	}
	err = r.VerifyBaseImage(ctx, pre, req)
	if err == nil {
		t.Fatal("VerifyBaseImage succeeded although the injected verifier always refuses")
	}
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Errorf("error does not wrap core.ErrBaseSignatureInvalid: %v", err)
	}
	if string(rec.got.TrustedRootJSON) != string(sentinel) {
		t.Errorf("verifier received TrustedRootJSON %q, want the request's own bytes %q", rec.got.TrustedRootJSON, sentinel)
	}
}

// TestVerifyBaseImage_EmptyTrustedRootJSONLeavesTheDefaultInPlace pins the
// "empty means embedded default" contract the field's doc comment promises: an
// unset field must reach the verifier as unset, not as an empty document that
// would shadow the embedded snapshot.
func TestVerifyBaseImage_EmptyTrustedRootJSONLeavesTheDefaultInPlace(t *testing.T) {
	s, _ := newTestRegistry(t)
	ctx := context.Background()

	ref := pushImage(t, s, "app/trusted-root-default:v1", ports.LinuxAMD64)
	rec := &recordingKeylessVerifier{}
	r := NewResolver(nil, WithKeylessVerifier(rec))

	pre, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
		Insecure:  true,
	})
	if err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	pushKeylessSignatureFixture(t, s, parsed.Context().Name(), pre.Digest, "../sigstore/testdata/distroless-nonroot")

	if err := r.VerifyBaseImage(ctx, pre, ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: true,
		VerifyMode:      ports.BaseImageVerifyKeyless,
		KeylessIdentity: ports.KeylessIdentity{SAN: "san", Issuer: "issuer"},
	}); err == nil {
		t.Fatal("VerifyBaseImage succeeded although the injected verifier always refuses")
	}
	if len(rec.got.TrustedRootJSON) != 0 {
		t.Errorf("verifier received %d bytes of trusted root for an unset field; empty must mean "+
			"\"use the embedded default\", which only the verifier can decide", len(rec.got.TrustedRootJSON))
	}
	// Sanity: the embedded default really is non-empty, so "empty" is a
	// meaningful sentinel and not just what every path produces.
	if len(sigstore.DefaultTrustedRootJSON()) == 0 {
		t.Error("sigstore.DefaultTrustedRootJSON() is empty, so the assertion above distinguishes nothing")
	}
}
