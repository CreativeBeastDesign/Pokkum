package sigstore

import (
	"crypto/x509"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
)

// RefreshTrustedRootCommand is the exact command a maintainer runs to
// regenerate the embedded snapshot and its provenance sidecar from Sigstore's
// live TUF repository. It is a single string so that every staleness message —
// test failure, runtime warning, unknown-log error — quotes the same
// copy-pasteable text, and so a rename cannot leave a stale instruction behind
// in one of them.
//
// The updater lives in a test (trustedroot_network_test.go) rather than a
// standalone generator binary, following Go's golden-file `-update`
// convention: the same code path that *detects* divergence from the live
// repository is the one that fixes it, so the two can never disagree.
const RefreshTrustedRootCommand = "POKKUM_UPDATE_SIGSTORE_TRUSTED_ROOT=1 " +
	"go test ./internal/adapters/sigstore/ -run TestTrustedRootSnapshot_TracksLiveTUFRepository -count=1"

const (
	// TrustedRootMaxAge is how old the embedded snapshot may get before it is
	// reported stale. It is deliberately far shorter than any key's actual
	// lifetime: the failure mode this guards is not "an anchor expired" but
	// "Sigstore added an anchor we do not know about". A snapshot taken before
	// a new Rekor shard came online rejects every signature logged to that
	// shard, and does so with a signature-verification error that looks
	// exactly like a forgery. Six months bounds how long that window can be.
	//
	// This is not a hard cutoff at runtime — an air-gapped verifier with an
	// old snapshot still works for signatures its snapshot covers — it is the
	// threshold at which the guard test fails and the verifier warns.
	TrustedRootMaxAge = 180 * 24 * time.Hour

	// TrustedRootExpiryWindow is how close to expiry a currently-active trust
	// anchor may get before it is reported. Fulcio's public-good roots are
	// ten-year certificates, so this only ever fires with plenty of warning —
	// which is the point: the refresh must land before the anchor dies, not
	// after.
	TrustedRootExpiryWindow = 90 * 24 * time.Hour
)

// TrustedRootFreshness is the result of assessing one trust-root snapshot
// against a point in time. The zero value means "not assessed"; use
// CheckTrustedRootFreshness.
//
// It is a value, not an error: a stale snapshot must never by itself fail a
// verification (an air-gapped operator verifying an old signature against an
// old snapshot is doing nothing wrong). It exists so the staleness is *loud* —
// a test failure in CI, a warning on the verification path — instead of
// surfacing later as an inexplicable bad-signature error.
type TrustedRootFreshness struct {
	// CapturedAt and Now are the two times the assessment compared.
	CapturedAt time.Time
	Now        time.Time

	// Age is Now minus CapturedAt (never negative; a snapshot with a
	// capture date in the future clamps to zero and is reported in Problems).
	Age time.Duration

	// MaxAge is the threshold Age was compared against (TrustedRootMaxAge).
	MaxAge time.Duration

	// Overdue reports Age > MaxAge.
	Overdue bool

	// Problems lists every anchor-level finding, one human-readable line
	// each: an active anchor within TrustedRootExpiryWindow of expiry, an
	// active anchor already expired, or a whole anchor class (Fulcio CA,
	// Rekor log, CT log) with no member active at Now. Sorted, so the output
	// is deterministic across runs.
	//
	// Timestamping authorities are deliberately not assessed: this adapter
	// configures verification with WithIntegratedTimestamps only and never
	// consumes an RFC3161 timestamp, so a dead TSA in the snapshot is not a
	// defect in anything we rely on. Flagging it would train maintainers to
	// ignore this list.
	Problems []string
}

// Stale reports whether the snapshot should be refreshed — either it is older
// than MaxAge, or an anchor-level problem was found.
func (f TrustedRootFreshness) Stale() bool {
	return f.Overdue || len(f.Problems) > 0
}

// String renders an actionable one-paragraph summary naming the command that
// fixes it. Safe to call on a fresh result too, where it says so.
func (f TrustedRootFreshness) String() string {
	if !f.Stale() {
		return fmt.Sprintf(
			"embedded Sigstore trust root is current: captured %s, %d days old (limit %d), no anchor problems",
			f.CapturedAt.UTC().Format(time.RFC3339),
			int(f.Age.Hours()/24), int(f.MaxAge.Hours()/24))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "embedded Sigstore trust root is STALE: captured %s, %d days old (limit %d days)",
		f.CapturedAt.UTC().Format(time.RFC3339),
		int(f.Age.Hours()/24), int(f.MaxAge.Hours()/24))
	for _, p := range f.Problems {
		fmt.Fprintf(&b, "; %s", p)
	}
	fmt.Fprintf(&b, ". A snapshot that predates a new Sigstore anchor (notably a new Rekor log shard) "+
		"rejects valid signatures with a verification error indistinguishable from a forgery. "+
		"Refresh it with: %s — or pass a current snapshot with --sigstore-trusted-root=<file>.",
		RefreshTrustedRootCommand)
	return b.String()
}

// CheckDefaultTrustedRootFreshness assesses the snapshot embedded in this
// package as of now.
//
// now is a parameter rather than a time.Now() call so the assessment is a pure
// function: adapters in this codebase must not read the clock, and the guard
// tests need to pin synthetic "now" values to prove the thresholds fire.
// Production callers on the verification path pass nowFunc() (see
// verifier.go), which tests override.
func CheckDefaultTrustedRootFreshness(now time.Time) (TrustedRootFreshness, error) {
	meta, err := DefaultTrustedRootMetadata()
	if err != nil {
		return TrustedRootFreshness{}, err
	}
	return CheckTrustedRootFreshness(defaultTrustedRootJSON, meta, now)
}

// CheckTrustedRootFreshness assesses rootJSON, whose provenance is meta, as of
// now. It returns an error only when rootJSON cannot be parsed as a trusted
// root at all — a snapshot that parses but is useless is reported through
// TrustedRootFreshness.Problems, not as an error, because the caller decides
// whether that is fatal.
func CheckTrustedRootFreshness(rootJSON []byte, meta TrustedRootMetadata, now time.Time) (TrustedRootFreshness, error) {
	tr, err := root.NewTrustedRootFromJSON(rootJSON)
	if err != nil {
		return TrustedRootFreshness{}, fmt.Errorf(
			"sigstore: cannot assess freshness of a trust root that does not parse (%d bytes): %w",
			len(rootJSON), err)
	}

	f := TrustedRootFreshness{
		CapturedAt: meta.CapturedAt,
		Now:        now,
		MaxAge:     TrustedRootMaxAge,
	}

	switch {
	case now.Before(meta.CapturedAt):
		// A capture date in the future means the sidecar is wrong (a bad
		// refresh, a skewed clock). Report it rather than computing a
		// negative age that would silently read as "brand new".
		f.Problems = append(f.Problems, fmt.Sprintf(
			"provenance sidecar claims a capture date in the future (capturedAt=%s, now=%s), so the snapshot's age cannot be trusted",
			meta.CapturedAt.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)))
	default:
		f.Age = now.Sub(meta.CapturedAt)
		f.Overdue = f.Age > f.MaxAge
	}

	// Digest tripwire: the snapshot and its provenance record must describe
	// the same bytes. If they do not, the recorded capture date says nothing
	// about the bytes actually embedded, so the age check above is worthless
	// and must not be reported as reassurance.
	if meta.SHA256 != "" {
		if got := digestOf(rootJSON); got != meta.SHA256 {
			f.Problems = append(f.Problems, fmt.Sprintf(
				"snapshot does not match its provenance sidecar (trusted-root-metadata.json records sha256=%s, embedded bytes are sha256=%s), so its recorded capture date describes different bytes",
				meta.SHA256, got))
		}
	}

	f.Problems = append(f.Problems, assessAnchors(tr, now)...)
	sort.Strings(f.Problems)
	return f, nil
}

// assessAnchors reports problems with the three anchor classes this adapter
// actually verifies against: Fulcio CAs (certificate path building), Rekor
// logs (inclusion promise) and CT logs (embedded SCT).
//
// "Active at now" means now falls inside the anchor's declared validity
// period. Anchors whose period has already ended are *retired*, not broken —
// the public-good root permanently carries the 2021 Fulcio CA and the 2021 CT
// log so that old signatures still verify at their integrated time — so they
// are ignored entirely rather than reported as expired.
func assessAnchors(tr *root.TrustedRoot, now time.Time) []string {
	var problems []string

	// Fulcio certificate authorities.
	usableCAs := 0
	for _, ca := range tr.FulcioCertificateAuthorities() {
		fulcio, ok := ca.(*root.FulcioCertificateAuthority)
		if !ok {
			// A CA shape this adapter cannot introspect. Not a staleness
			// finding, and not something to guess about.
			continue
		}
		if !activeAt(fulcio.ValidityPeriodStart, fulcio.ValidityPeriodEnd, now) {
			continue
		}
		label := fulcio.URI
		if label == "" {
			label = "Fulcio CA"
		}
		if p, ok := expiryProblem("Fulcio CA "+label, fulcio.ValidityPeriodEnd, now); ok {
			problems = append(problems, p)
		}

		// The declared validity period is metadata; the certificates are what
		// x509 path building actually enforces, and they can outlive or
		// predecease it. The public-good root's active Fulcio CA declares no
		// end date at all, so its certificates' notAfter is the ONLY thing
		// that reveals it dying — which is also why an expired certificate
		// disqualifies the CA from counting as usable below rather than just
		// being noted.
		usable := true
		for _, cert := range append([]*x509.Certificate{fulcio.Root}, fulcio.Intermediates...) {
			if cert == nil {
				continue
			}
			name := fmt.Sprintf("Fulcio CA %s certificate %q", label, cert.Subject.CommonName)
			if p, ok := expiryProblem(name, cert.NotAfter, now); ok {
				problems = append(problems, p)
			}
			if now.After(cert.NotAfter) || now.Before(cert.NotBefore) {
				usable = false
			}
		}
		if usable {
			usableCAs++
		}
	}
	if usableCAs == 0 {
		problems = append(problems, "no Fulcio certificate authority in the snapshot is valid at the current time, so no keyless certificate issued today can chain to a trusted root")
	}

	problems = append(problems, assessLogs("Rekor transparency log", tr.RekorLogs(), now)...)
	problems = append(problems, assessLogs("CT log", tr.CTLogs(), now)...)

	return problems
}

// assessLogs reports expiring/expired active logs and an empty active set.
func assessLogs(kind string, logs map[string]*root.TransparencyLog, now time.Time) []string {
	var problems []string
	active := 0
	for id, tlog := range logs {
		if tlog == nil {
			continue
		}
		if !activeAt(tlog.ValidityPeriodStart, tlog.ValidityPeriodEnd, now) {
			continue
		}
		active++
		label := tlog.BaseURL
		if label == "" {
			label = id
		}
		if p, ok := expiryProblem(kind+" "+label, tlog.ValidityPeriodEnd, now); ok {
			problems = append(problems, p)
		}
	}
	if active == 0 {
		problems = append(problems, fmt.Sprintf(
			"no %s in the snapshot is valid at the current time", kind))
	}
	return problems
}

// activeAt reports whether now falls within [start, end], treating a zero
// start as "always was" and a zero end as "no scheduled retirement".
func activeAt(start, end, now time.Time) bool {
	if !start.IsZero() && now.Before(start) {
		return false
	}
	if !end.IsZero() && now.After(end) {
		return false
	}
	return true
}

// expiryProblem reports a line when notAfter is already past or lands within
// TrustedRootExpiryWindow of now. A zero notAfter means "no expiry declared".
func expiryProblem(what string, notAfter, now time.Time) (string, bool) {
	if notAfter.IsZero() {
		return "", false
	}
	if now.After(notAfter) {
		return fmt.Sprintf("%s expired on %s", what, notAfter.UTC().Format(time.RFC3339)), true
	}
	if remaining := notAfter.Sub(now); remaining <= TrustedRootExpiryWindow {
		return fmt.Sprintf("%s expires on %s (in %d days)",
			what, notAfter.UTC().Format(time.RFC3339), int(remaining.Hours()/24)), true
	}
	return "", false
}
