package scanner

import (
	"strconv"
	"strings"
	"testing"
)

// numericVersionOlderThan is a CORRECT, dot-segment-wise numeric version
// comparator, implemented here purely as a fuzzing oracle — it deliberately
// does NOT become the production implementation (that's out of scope for
// this workstream; adapter.go is owned elsewhere). It compares the numeric
// core of each version segment-by-segment as integers when both sides parse
// as one, padding a shorter core with implicit trailing zero segments (so
// "1.2" == "1.2.0"), and treats a trailing "-suffix" (pre-release tag or
// distro build revision, e.g. "1.2.0-beta" or "1.2.3-1ubuntu4") as making an
// otherwise-equal version OLDER than the bare core — correct per semver for
// pre-release tags, and the conservative choice for an unrecognized
// distro-style suffix (this is a CVE gate: treat "we don't know what this
// build revision means" as "still vulnerable", not "already patched").
//
// A previous version of this oracle used "" (rather than "0") as the
// default for a missing trailing segment, and fell back to a plain
// byte-wise string compare for any non-numeric segment (including a
// hyphenated pre-release/distro suffix once the comparison reached it).
// Both were bugs found while wiring up this differential fuzz test: the
// first made e.g. numericVersionOlderThan("1.2", "1.2.0") wrongly return
// true instead of the equal-version answer (false) this same doc comment
// already claimed as the intended behavior; the second made
// numericVersionOlderThan("1.2.0-beta", "1.2.0") wrongly return false
// (ASCII '0' < '-' put the bare release "before" the pre-release
// lexicographically) instead of true, contradicting this task's own
// confirmed-correct reproducer table (row: 1.2.0-beta / 1.2.0 -> true).
// Fixed here since this oracle — not adapter.go's production comparator —
// owns those two bugs.
//
// A blank (post-cleanVersion) v or fixed is deliberately excluded from all
// of the above and short-circuits to false, matching isVersionOlderThan's
// own pre-existing, unchanged "unresolved version" contract: a completely
// unknown installed/fixed version was already treated as "not older" before
// this bug fix, and revisiting that policy is out of scope for this fix —
// it's a separate design decision, not part of the lexicographic-vs-numeric
// defect this fuzz target exists to catch. Without this, the oracle and
// production would disagree on e.g. ("", "0") purely over that pre-existing
// policy rather than over anything the numeric-comparison fix changed.
func numericVersionOlderThan(v, fixed string) bool {
	if cleanVersion(v) == "" || cleanVersion(fixed) == "" {
		return false
	}
	vCore, vSuffix := numericCoreAndSuffix(cleanVersion(v))
	fCore, fSuffix := numericCoreAndSuffix(cleanVersion(fixed))

	if c := compareCoreSegments(vCore, fCore); c != 0 {
		return c < 0
	}

	if vSuffix == fSuffix {
		return false // equal core, equal (possibly absent) suffix
	}
	if vSuffix == "" {
		return false // bare release is never older than its own pre-release/suffix build
	}
	if fSuffix == "" {
		return true // a suffixed build is older than the bare release of the same core
	}
	// Two differently-suffixed builds of the same core: no universal
	// ordering exists, so — same fail-safe direction as adapter.go's
	// production comparator — treat v as not provably newer-or-equal.
	return true
}

// numericCoreAndSuffix splits off a trailing "-suffix" from a version's
// dotted numeric core, e.g. "1.2.0-beta" -> ("1.2.0", "beta").
func numericCoreAndSuffix(v string) (core, suffix string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// compareCoreSegments compares two dot-delimited numeric cores
// segment-by-segment, returning -1, 0, or 1. A missing trailing segment on
// the shorter side is an implicit "0". A segment that doesn't parse as an
// integer on both sides (and isn't byte-identical to its counterpart) has
// no reliable numeric ordering, so this also resolves fail-safe (-1, i.e.
// "not provably newer-or-equal") rather than via an incidental byte compare.
func compareCoreSegments(v, fixed string) int {
	vSegs := strings.Split(v, ".")
	fSegs := strings.Split(fixed, ".")
	n := len(vSegs)
	if len(fSegs) > n {
		n = len(fSegs)
	}
	for i := 0; i < n; i++ {
		vSeg, fSeg := "0", "0"
		if i < len(vSegs) {
			vSeg = vSegs[i]
		}
		if i < len(fSegs) {
			fSeg = fSegs[i]
		}
		if vSeg == fSeg {
			continue
		}
		vNum, vErr := strconv.Atoi(vSeg)
		fNum, fErr := strconv.Atoi(fSeg)
		if vErr != nil || fErr != nil {
			return -1
		}
		if vNum != fNum {
			if vNum < fNum {
				return -1
			}
			return 1
		}
	}
	return 0
}

// FuzzIsVersionOlderThan is a DIFFERENTIAL fuzz test against
// isVersionOlderThan (adapter.go), which gates whether an embedded advisory
// fires for the CVE tripwire — a security-relevant decision fed by
// version strings from package.json/lockfile content, i.e. untrusted
// project input.
//
// FIXED: isVersionOlderThan used to do a plain lexicographic string
// comparison (`v < fixed`), not a numeric, dot-segment-wise one, which
// silently misclassified version pairs whose segment widths differ (e.g.
// "1.2.0" vs "1.10.0"). It's now a proper dot-segment-wise numeric
// comparator (adapter.go's compareVersions/compareNumericCores) with
// pre-release/distro-suffix handling; this target exercises it against the
// independent oracle below.
//
// Run `go test -run='^$' -fuzz=FuzzIsVersionOlderThan -fuzztime=30s ./internal/adapters/scanner/...`
// for a real fuzzing run beyond the seed corpus.
func FuzzIsVersionOlderThan(f *testing.F) {
	f.Add("1.2.0", "1.10.0") // the headline case from the task description
	f.Add("1.9.0", "1.10.0") // same bug, one digit shorter
	f.Add("2.0.0", "10.0.0") // major-version-width mismatch
	f.Add("1.2.9", "1.2.10") // patch-version-width mismatch
	f.Add("0.9.0", "0.10.0")
	f.Add("1.0.0", "1.0.0") // equal versions: both should say false
	f.Add("1.0.1", "1.0.0") // v newer than fixed: both should say false
	f.Add("", "")
	f.Add("v1.2.0", "1.10.0")  // leading "v" (cleanVersion strips it)
	f.Add("^1.2.0", "~1.10.0") // range-prefix operators (cleanVersion strips these)
	f.Add("1.2.0-beta", "1.10.0")
	f.Add("not-a-version", "also-not-a-version")
	f.Add("1.2.3-1ubuntu4", "1.2.3") // distro build revision
	f.Add("1.2", "1.2.0")            // differing segment counts, otherwise equal

	f.Fuzz(func(t *testing.T, v, fixed string) {
		got := isVersionOlderThan(v, fixed)
		want := numericVersionOlderThan(v, fixed)
		if got != want {
			t.Errorf("isVersionOlderThan(%q, %q) = %v, numeric-correct answer = %v (known bug: lexicographic string comparison)", v, fixed, got, want)
		}
	})
}
