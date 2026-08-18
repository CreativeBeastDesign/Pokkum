package sigstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// nowFunc is the clock used for trust-root freshness reporting. Adapters in
// this codebase must not read the system clock for anything that reaches a
// build artifact — that is what SourceDateEpoch exists for — but "is this
// trust anchor still current?" is inherently a question about the present
// moment and has no deterministic answer.
//
// It is a package var rather than an inline time.Now() call so the staleness
// tests can pin it and stay deterministic. Nothing it feeds influences
// verification outcomes: it is used only to phrase warnings and to decide
// whether to append a staleness hint to an error that would be returned
// either way. Certificate path validation still uses the Rekor entry's
// integrated time, never this clock.
var nowFunc = time.Now

// Cosign's legacy signature-tag annotation keys, re-exported from the ports
// vocabulary where they are defined (ports.CosignSignatureAnnotation etc.).
// They live in ports because the adapters that extract this material off a
// manifest — baseimage, provenance, remotecacheutils — must not import this
// package to learn a wire-format constant; these aliases exist only so this
// package's own error messages and tests can keep using the short names.
const (
	// CosignSignatureAnnotation carries the base64 signature over the payload.
	CosignSignatureAnnotation = ports.CosignSignatureAnnotation
	// CosignCertificateAnnotation carries the Fulcio leaf certificate, as
	// plain PEM text.
	CosignCertificateAnnotation = ports.CosignCertificateAnnotation
	// CosignChainAnnotation carries the intermediate/root certificates as
	// plain PEM text. Pokkum never trusts it for path building; see
	// assembleV01Bundle.
	CosignChainAnnotation = ports.CosignChainAnnotation
	// CosignBundleAnnotation carries the Rekor inclusion material as plain
	// JSON text.
	CosignBundleAnnotation = ports.CosignBundleAnnotation
)

// Sentinel errors returned by Verifier.Verify. Every error it returns wraps
// exactly one of these (except the unconstrained-identity refusal, which is a
// caller programming error rather than a verification outcome), so callers can
// classify a failure with errors.Is without matching on message text.
var (
	// ErrNoBundle means the image carries no keyless signature material at
	// all — the certificate and/or Rekor bundle annotations are absent. This
	// is distinct from "material is present but did not verify": a caller may
	// legitimately fall back to another verification mode on ErrNoBundle, but
	// must never fall back on any of the errors below.
	//
	// It IS ports.ErrNoKeylessBundle, not a copy of it: the "not signed this
	// way, try another mode" signal belongs to the KeylessVerifier contract,
	// so callers holding only the interface (baseimage) can match it without
	// importing this adapter. errors.Is therefore matches under either name.
	ErrNoBundle = ports.ErrNoKeylessBundle

	// ErrMalformedMaterial means the material is present but structurally
	// unusable: unparseable PEM, malformed Rekor bundle JSON, bad base64/hex,
	// or a bundle sigstore-go rejects as invalid.
	ErrMalformedMaterial = errors.New("sigstore: malformed keyless signature material")

	// ErrChainInvalid means the signature did not verify against the trusted
	// root: the certificate did not chain to a trusted Fulcio CA, the SCT did
	// not verify, or the signature did not match the payload. This is the
	// catch-all for cryptographic verification failure.
	ErrChainInvalid = errors.New("sigstore: certificate chain validation failed")

	// ErrIdentityMismatch means the signature verified cryptographically but
	// the certificate was issued to a different identity than expected — the
	// signature is real, but it is not the signer we require.
	ErrIdentityMismatch = errors.New("sigstore: certificate identity mismatch")

	// ErrTlogInvalid means the Rekor transparency log entry could not be
	// verified: the inclusion promise did not check out against the trusted
	// log key, the entry did not match the signature/certificate/digest being
	// verified, or the entry was recorded outside the certificate's validity
	// window.
	ErrTlogInvalid = errors.New("sigstore: transparency log verification failed")
)

// Verifier verifies keyless Sigstore signatures (Fulcio short-lived
// certificate + Rekor transparency log) carried in Cosign's legacy
// signature-tag annotation format.
//
// Verification is fully offline: it needs the trusted root (embedded by
// default, see DefaultTrustedRootJSON) and the material in the request, and
// makes no network calls of its own. The caller is responsible for fetching
// the .sig manifest and its annotations. A caller that wants a live trust root
// fetches one itself via FetchTrustedRootJSON/ResolveTrustedRootJSON and passes
// it as KeylessVerifyRequest.TrustedRootJSON; nothing on this path ever does
// network I/O implicitly, so hermetic verification stays hermetic.
//
// When it falls back to the embedded trust-root snapshot, it reports staleness
// on the logger (see CheckDefaultTrustedRootFreshness) but never fails a
// verification for it. An expired snapshot is a maintenance problem, not
// evidence about the signature under inspection.
type Verifier struct {
	log *slog.Logger
}

// NewVerifier returns a Verifier that logs to log. A nil logger is allowed
// and means slog.Default().
func NewVerifier(log *slog.Logger) *Verifier {
	return &Verifier{log: log}
}

var _ ports.KeylessVerifier = (*Verifier)(nil)

func (v *Verifier) logger() *slog.Logger {
	if v.log != nil {
		return v.log
	}
	return slog.Default()
}

// verifierOptions is the VerifierConfig this adapter uses. The set is
// deliberately minimal and each option is load-bearing for the legacy Cosign
// keyless material we consume (Fulcio cert + Rekor SignedEntryTimestamp, no
// inclusion proof, no RFC3161 timestamp). Derived from reading
// pkg/verify/signed_entity.go in sigstore-go v1.3.0:
//
//   - WithTransparencyLog(1): Verifier.VerifyTransparencyLogInclusion is a
//     no-op unless requireTlogEntries is set. Without this option the tlog
//     entry we assembled would never be checked at all AND no timestamp would
//     be produced, so verification would fail later with "no valid observer
//     timestamps found". One entry is the threshold because Cosign writes
//     exactly one.
//
//   - WithIntegratedTimestamps(1): this is the answer to "when was this
//     certificate valid". Fulcio certificates live for ~10 minutes, so path
//     validation cannot use the current time; VerifyObserverTimestamps must
//     be given a trusted timestamp. Our only time source is the Rekor
//     SignedEntryTimestamp, and requireIntegratedTimestamps is exactly the
//     flag that (a) makes VerifyTlogEntry trust the entry's integratedTime
//     (it is passed as the trustIntegratedTime argument) and (b) requires at
//     least one such timestamp to exist before certificate path validation
//     runs. WithObserverTimestamps(1) would also work, but it additionally
//     attempts RFC3161 timestamp verification against the trusted root's
//     timestamping authorities — material we never have — so
//     WithIntegratedTimestamps states our actual requirement precisely.
//     WithCurrentTime() and WithNoObserverTimestamps() are both wrong here:
//     the former would reject every already-expired Fulcio cert (i.e. all of
//     them), and the latter is documented as key-only and is explicitly
//     rejected by the Verify loop when a certificate is present.
//
//   - WithSignedCertificateTimestamps(1): requires the embedded SCT in the
//     Fulcio certificate to verify against the trusted root's CT log key.
//     This is what proves the certificate itself was publicly logged, so a
//     Fulcio misissuance cannot go unnoticed. Cosign's own keyless verify
//     requires it, the public-good trust root we embed carries the CT log
//     keys needed for it, and it costs nothing extra at runtime.
//
// VerifierConfig.Validate() requires at least one of the timestamp options,
// which WithIntegratedTimestamps satisfies.
func verifierOptions() []verify.VerifierOption {
	return []verify.VerifierOption{
		verify.WithTransparencyLog(1),
		verify.WithIntegratedTimestamps(1),
		verify.WithSignedCertificateTimestamps(1),
	}
}

// Verify implements ports.KeylessVerifier.
func (v *Verifier) Verify(ctx context.Context, req ports.KeylessVerifyRequest) (ports.KeylessVerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.KeylessVerifyResult{}, fmt.Errorf("sigstore: keyless verification cancelled before it started: %w", err)
	}

	// 1. Refuse an unconstrained identity, before touching sigstore-go at
	// all. "Any certificate Fulcio ever issued" is trivially satisfiable by
	// any attacker with a GitHub account, so an empty identity must be
	// unreachable by construction here rather than something we hope the
	// library rejects downstream.
	if req.Identity.Empty() {
		return ports.KeylessVerifyResult{}, errors.New(
			"sigstore: no expected keyless identity configured — refusing to verify against an unconstrained identity, " +
				"which would accept any certificate issued by Fulcio to anyone")
	}

	// 2. Distinguish "no keyless material at all" from "material that fails
	// to verify". Callers use ErrNoBundle to detect an unsigned-by-this-means
	// image; they must not treat any other error that way.
	var missing []string
	if len(req.CertificatePEM) == 0 {
		missing = append(missing, CosignCertificateAnnotation)
	}
	if len(req.RekorBundleJSON) == 0 {
		missing = append(missing, CosignBundleAnnotation)
	}
	if len(missing) > 0 {
		return ports.KeylessVerifyResult{}, fmt.Errorf(
			"sigstore: %w: signature is missing required annotation(s) %v — this signature is not a keyless (Fulcio/Rekor) signature",
			ErrNoBundle, missing)
	}

	// 3. Load the trusted root: the caller's snapshot if supplied, otherwise
	// the embedded Sigstore public-good snapshot.
	trustedRootJSON := req.TrustedRootJSON
	trustedRootSource := "caller-supplied"
	usedEmbeddedRoot := false
	if len(trustedRootJSON) == 0 {
		trustedRootJSON = DefaultTrustedRootJSON()
		trustedRootSource = "embedded public-good snapshot"
		usedEmbeddedRoot = true
	}
	trustedRoot, err := root.NewTrustedRootFromJSON(trustedRootJSON)
	if err != nil {
		return ports.KeylessVerifyResult{}, fmt.Errorf(
			"%w: cannot parse the %s Sigstore trusted root (%d bytes): %w",
			ErrMalformedMaterial, trustedRootSource, len(trustedRootJSON), err)
	}

	// 3b. If we fell back to the embedded snapshot, say so out loud when it is
	// stale. A stale snapshot is NOT treated as a verification failure —
	// an air-gapped operator verifying a signature their snapshot does cover is
	// doing nothing wrong, and failing here would break hermetic verification
	// on a calendar date rather than on evidence. But it must never be silent:
	// the way this breaks is that a signature logged to a Rekor shard the
	// snapshot predates fails as if it were forged.
	if usedEmbeddedRoot {
		if freshness, ferr := CheckDefaultTrustedRootFreshness(nowFunc()); ferr != nil {
			// The embedded provenance sidecar is unreadable. That is a build
			// defect in this binary, not a property of the material under
			// verification, so it warns rather than failing the verification.
			v.logger().Warn("cannot assess the freshness of the embedded Sigstore trust root", "error", ferr)
		} else if freshness.Stale() {
			v.logger().Warn("keyless verification is using a stale embedded Sigstore trust root",
				"detail", freshness.String())
		}
	}

	// 4. Assemble a Sigstore v0.1 bundle out of the legacy annotation
	// material. Errors already wrap ErrMalformedMaterial.
	material, err := assembleV01Bundle(req)
	if err != nil {
		return ports.KeylessVerifyResult{}, err
	}

	// 5. Build the verifier.
	sev, err := verify.NewVerifier(trustedRoot, verifierOptions()...)
	if err != nil {
		return ports.KeylessVerifyResult{}, fmt.Errorf(
			"%w: cannot construct a Sigstore verifier from the %s trusted root: %w",
			ErrChainInvalid, trustedRootSource, err)
	}

	// 6. Build the expected identity matcher. NewShortCertificateIdentity
	// rejects an identity that constrains only the issuer or only the SAN, so
	// a half-configured identity fails here rather than silently widening
	// what we accept.
	certID, err := verify.NewShortCertificateIdentity(
		req.Identity.Issuer, req.Identity.IssuerRegex,
		req.Identity.SAN, req.Identity.SANRegex,
	)
	if err != nil {
		return ports.KeylessVerifyResult{}, fmt.Errorf(
			"sigstore: unusable expected keyless identity (issuer=%q issuer_regex=%q san=%q san_regex=%q): %w",
			req.Identity.Issuer, req.Identity.IssuerRegex, req.Identity.SAN, req.Identity.SANRegex, err)
	}

	// 7. Build the policy: the signature must cover exactly this payload, and
	// the certificate must match the expected identity.
	policy := verify.NewPolicy(
		verify.WithArtifact(bytes.NewReader(req.PayloadBytes)),
		verify.WithCertificateIdentity(certID),
	)

	// 7b. Before asking sigstore-go to verify the Rekor entry, check that the
	// trusted root even knows the log the entry came from — and if it does not,
	// say exactly that.
	//
	// This is the single most important consequence of the trust root being a
	// snapshot. Sigstore brings new Rekor shards online over time (the
	// public-good instance added log2025-1.rekor.sigstore.dev in September
	// 2025). A snapshot captured before a shard existed carries no key for it,
	// so every signature logged to that shard fails — and inside sigstore-go it
	// fails as an ordinary verification error, indistinguishable from a forged
	// signature or a tampered payload. An operator reading that error concludes
	// their base image's signature is bad, when in fact their verifier is out
	// of date.
	//
	// Checking log-ID membership ourselves is what turns that into an
	// actionable message. It is not a relaxation: an unknown log still fails
	// closed with ErrTlogInvalid, exactly as it did before. sigstore-go
	// independently rejects it too — this check only runs first, so the error
	// text is the true one.
	if err := checkRekorLogKnown(trustedRoot, material.RekorLogID, trustedRootSource, usedEmbeddedRoot, nowFunc()); err != nil {
		return ports.KeylessVerifyResult{}, err
	}

	// 8a. Run transparency-log verification on its own first, purely for
	// error attribution. Verifier.Verify runs the same step internally as its
	// first action and wraps the failure in a generic message; since
	// sigstore-go exposes no typed error for tlog failures and its message
	// text is not a stable API, calling the exported
	// VerifyTransparencyLogInclusion ourselves is the only way to attribute a
	// failure to ErrTlogInvalid rather than lumping it into ErrChainInvalid.
	// The cost is one extra ECDSA verification of the SignedEntryTimestamp on
	// the success path, which is negligible next to the network fetch the
	// caller already did; the benefit is that "Rekor said no" and "the
	// certificate did not chain" are distinguishable in an incident.
	if _, err := sev.VerifyTransparencyLogInclusion(material.Bundle); err != nil {
		return ports.KeylessVerifyResult{}, fmt.Errorf(
			"%w: Rekor entry from annotation %s (log %s, index %d) did not verify against the %s trusted root: %w",
			ErrTlogInvalid, CosignBundleAnnotation, material.RekorLogID, material.RekorLogIndex, trustedRootSource, err)
	}

	// 8b. Full verification.
	result, err := sev.Verify(material.Bundle, policy)
	if err != nil {
		return ports.KeylessVerifyResult{}, v.classify(req, material, trustedRootSource, err)
	}

	// 9. Extract what was proven. result.Signature.Certificate is the
	// summary of the leaf that sigstore-go actually verified, so read the
	// identity from there rather than re-parsing the certificate ourselves.
	if result == nil || result.Signature == nil || result.Signature.Certificate == nil {
		return ports.KeylessVerifyResult{}, fmt.Errorf(
			"%w: verification reported success but returned no verified certificate summary", ErrChainInvalid)
	}
	summary := result.Signature.Certificate

	out := ports.KeylessVerifyResult{
		Issuer:               summary.Issuer,
		SAN:                  summary.SubjectAlternativeName,
		CertificateNotBefore: material.LeafCertificate.NotBefore.UTC(),
		CertificateNotAfter:  material.LeafCertificate.NotAfter.UTC(),
		RekorLogID:           material.RekorLogID,
		RekorLogIndex:        material.RekorLogIndex,
		IntegratedTime:       material.IntegratedTime,
	}

	v.logger().Debug("keyless Sigstore signature verified",
		"issuer", out.Issuer,
		"san", out.SAN,
		"rekor_log_id", out.RekorLogID,
		"rekor_log_index", out.RekorLogIndex,
		"integrated_time", out.IntegratedTime,
		"cert_not_before", out.CertificateNotBefore,
		"cert_not_after", out.CertificateNotAfter,
		"trusted_root", trustedRootSource,
	)

	return out, nil
}

// classify maps a sigstore-go verification failure onto one of this package's
// sentinels.
//
// Classification is done exclusively with errors.As against sigstore-go's own
// exported error types. sigstore-go's error *messages* are not a stable API
// and must never be pattern-matched: a wording change upstream would silently
// reclassify a real failure, which for a security control is worse than a
// coarse-but-honest classification.
func (v *Verifier) classify(req ports.KeylessVerifyRequest, material *legacyMaterial, trustedRootSource string, err error) error {
	// Identity failures. ErrNoMatchingCertificateIdentity is what
	// CertificateIdentities.Verify returns; it aggregates the per-identity
	// ErrValueMismatch / ErrValueRegexMismatch causes, which are also matched
	// directly in case a future version surfaces one without the wrapper.
	var noMatch *verify.ErrNoMatchingCertificateIdentity
	var mismatch *verify.ErrValueMismatch
	var regexMismatch *verify.ErrValueRegexMismatch
	if errors.As(err, &noMatch) || errors.As(err, &mismatch) || errors.As(err, &regexMismatch) {
		return fmt.Errorf(
			"%w: certificate from annotation %s is valid but was not issued to the expected identity "+
				"(expected issuer=%q issuer_regex=%q san=%q san_regex=%q): %w",
			ErrIdentityMismatch, CosignCertificateAnnotation,
			req.Identity.Issuer, req.Identity.IssuerRegex, req.Identity.SAN, req.Identity.SANRegex, err)
	}

	// Everything else — certificate path validation, SCT verification and
	// signature verification — is a cryptographic verification failure. The
	// transparency-log case was already attributed to ErrTlogInvalid by the
	// separate VerifyTransparencyLogInclusion call before Verify ran.
	return fmt.Errorf(
		"%w: keyless signature for the payload covered by annotation %s did not verify against the %s trusted root "+
			"(Rekor log %s index %d, certificate valid %s..%s): %w",
		ErrChainInvalid, CosignSignatureAnnotation, trustedRootSource,
		material.RekorLogID, material.RekorLogIndex,
		material.LeafCertificate.NotBefore.UTC().Format("2006-01-02T15:04:05Z"),
		material.LeafCertificate.NotAfter.UTC().Format("2006-01-02T15:04:05Z"),
		err)
}

// checkRekorLogKnown fails closed when the trusted root carries no key for the
// transparency log the Rekor entry claims to come from.
//
// sigstore-go would reject the entry anyway — this does not add or relax a
// check. What it adds is attribution. Without it, "your trust root is older
// than the Rekor shard this signature was logged to" and "this signature is
// forged" produce the same error text, and the first is the far more likely
// cause in the field for anyone running a binary built months ago.
//
// now is passed in (see nowFunc) purely to phrase the staleness hint; the
// verdict itself does not depend on it.
func checkRekorLogKnown(trustedRoot *root.TrustedRoot, logID, trustedRootSource string, usedEmbeddedRoot bool, now time.Time) error {
	logs := trustedRoot.RekorLogs()

	// root.ParseTransparencyLogs keys this map by hex.EncodeToString of the
	// log's key ID, which is lower-case; the bundle's logID is hex text from a
	// registry annotation and its case is not guaranteed.
	if _, ok := logs[strings.ToLower(strings.TrimSpace(logID))]; ok {
		return nil
	}

	known := make([]string, 0, len(logs))
	for id, tlog := range logs {
		if tlog != nil && tlog.BaseURL != "" {
			known = append(known, fmt.Sprintf("%s=%s", tlog.BaseURL, id))
			continue
		}
		known = append(known, id)
	}
	sort.Strings(known)
	knownList := strings.Join(known, ", ")
	if knownList == "" {
		knownList = "(none — the trusted root declares no transparency logs at all)"
	}

	hint := "Supply a trusted root that covers this log via --sigstore-trusted-root=<file>, " +
		"or confirm the log is a legitimate Sigstore instance before trusting it."
	if usedEmbeddedRoot {
		hint = fmt.Sprintf("Refresh the embedded snapshot with: %s — or pass a current snapshot via "+
			"--sigstore-trusted-root=<file>.", RefreshTrustedRootCommand)
		if freshness, err := CheckDefaultTrustedRootFreshness(now); err == nil {
			hint = freshness.String() + " " + hint
		}
	}

	return fmt.Errorf(
		"%w: the Rekor entry in annotation %s was recorded in transparency log %s, which the %s trusted root "+
			"does not contain (it covers %d log(s): %s). This is most likely a trust-root coverage gap rather than a bad signature — "+
			"Sigstore brings new Rekor shards online over time and a snapshot captured before a shard existed carries "+
			"no key to verify entries from it. %s",
		ErrTlogInvalid, CosignBundleAnnotation, logID, trustedRootSource, len(logs), knownList, hint)
}
