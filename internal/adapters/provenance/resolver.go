package provenance

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/keymaterialutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var _ ports.ProvenanceResolver = (*Resolver)(nil)

// Sentinel errors returned by ResolveProvenance for conditions the caller is
// expected to act on rather than merely report.
var (
	// ErrKeylessIdentityRequired means the image carries keyless Sigstore
	// signature material but no expected signer identity was configured, so
	// there is nothing to check the certificate against. This is deliberately
	// a hard failure and not a "SignatureValid: false" result: an
	// unconstrained keyless check is satisfiable by any certificate Fulcio has
	// ever issued, so silently reporting either verdict would be misleading.
	ErrKeylessIdentityRequired = errors.New("provenance resolver: image carries a keyless Sigstore signature but no expected signer identity was configured")

	// ErrKeylessIdentityIncomplete means an identity was supplied but
	// constrains only the issuer or only the SAN. Sigstore requires both, and
	// half a constraint is a much weaker check than the caller almost
	// certainly intended, so it is refused up front with an actionable message
	// rather than surfacing later as a generic library error.
	ErrKeylessIdentityIncomplete = errors.New("provenance resolver: incomplete expected keyless identity")

	// ErrStaticKeyRequired means the image carries a Cosign static-key
	// signature (a base64 signature annotation, no Fulcio certificate/Rekor
	// bundle) but no static verification key is configured anywhere
	// (ProvenanceResolverRequest.PublicKeyPEM, POKKUM_SIGNING_PUBKEY, or
	// POKKUM_BASE_IMAGE_PUBKEY). Deliberately a hard failure rather than a
	// silent SignatureValid: false, for the same reason as
	// ErrKeylessIdentityRequired: "checked and failed" and "nothing to check
	// against" are different operator problems. A shared, unattributed
	// placeholder public key (cosign.DefaultPublicKeyPEM) used to paper over
	// this distinction — no real signer ever held its private half, so it
	// could never actually verify anything — and has been deleted
	// (docs/archive/Roadmap.md item 2h).
	ErrStaticKeyRequired = errors.New("provenance resolver: image carries a Cosign static-key signature but no verification key was configured")

	// ErrUnverifiedSourceProvenance means req.ExpectSource was set but the
	// only source repo/commit information ResolveProvenance could find is
	// unverified: either nothing at all, or values read off the image's own
	// unsigned OCI annotations (org.opencontainers.image.source/.revision)
	// rather than a cryptographically verified SLSA "source-code" resolved
	// dependency. Comparing --expect-source against those annotations reads
	// as a real security check while verifying nothing — the image's
	// publisher (anyone with push access to the repository) controls their
	// content, so a hostile image can simply set them to whatever
	// --expect-source will be asked for. Deliberately a hard failure rather
	// than a silent comparison, for the same reason as
	// ErrKeylessIdentityRequired/ErrStaticKeyRequired: "verified and matched"
	// and "nothing verified, but the strings happened to be equal" are
	// different operator-facing claims and must never share one result
	// shape. req.AllowUnverifiedSource (--allow-unverified-source) is the
	// explicit, visibly-marked escape hatch.
	ErrUnverifiedSourceProvenance = errors.New("provenance resolver: --expect-source requires a cryptographically verified SLSA source-code attestation")

	// ErrVerifierNotInjected means signature or attestation material was
	// present and the Resolver had no injected verifier able to check it —
	// no ports.CosignSigner for a static-key signature, no
	// ports.KeylessVerifier for Fulcio/Rekor material, or no ports.DSSESigner
	// for a DSSE-enveloped SLSA statement.
	//
	// This is a composition-root wiring defect, not an operator mistake, but
	// it is reported with the same fail-closed discipline as
	// ErrStaticKeyRequired and ErrKeylessIdentityRequired, and for the same
	// reason: each of those verifier fields used to be defaulted inside
	// NewResolver by constructing a concrete peer adapter, so "not injected"
	// was unreachable. With the defaults gone it is reachable, and the branches
	// that used to be written `case r.signer != nil:` / `&& r.keyless != nil`
	// would have silently skipped verification — producing SignatureValid:
	// false with a nil error for a genuinely signed image, which is exactly
	// the fail-open a149b28 removed from this file. Skipping is therefore
	// tracked as its own outcome and refused here.
	ErrVerifierNotInjected = errors.New("provenance resolver: signature material is present but no verifier was injected to check it")
)

// maxSignatureBlobBytes caps how much of any single registry-supplied blob or
// tar entry this package will hold in memory while looking for signature and
// attestation material.
//
// Every read this bounds happens on content pulled from a registry BEFORE any
// signature has been verified, so an unbounded io.ReadAll here is a
// decompression-bomb / OOM vector reachable by anyone who can serve a
// `<repo>:<alg>-<hex>.sig` or `.att` tag. Real Cosign simple-signing payloads
// and SLSA DSSE envelopes are kilobytes; 64MiB is several orders of magnitude
// of headroom while still bounding the damage. Same discipline as
// assetoverlay.maxOverlayEntryBytes, layerdiffutils' io.LimitReader(tr,
// 500<<20), and bunruntime's capped archive read.
const maxSignatureBlobBytes = 64 << 20

// readCapped reads at most maxSignatureBlobBytes from r, returning an error if
// the content is larger. The LimitReader is given one extra byte so content
// that exactly fills the cap is not misreported as oversized.
func readCapped(r io.Reader, what string) ([]byte, error) {
	return readCappedTo(r, what, maxSignatureBlobBytes)
}

// readCappedTo is readCapped with an explicit cap, so tests can exercise the
// oversize path without allocating maxSignatureBlobBytes.
func readCappedTo(r io.Reader, what string, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s exceeds the %d-byte limit for registry-supplied signature material", what, limit)
	}
	return b, nil
}

// Resolver implements ports.ProvenanceResolver by pulling remote OCI manifests,
// verifying Cosign signatures, and inspecting in-toto / SLSA provenance statements.
type Resolver struct {
	log     *slog.Logger
	signer  ports.CosignSigner
	keyless ports.KeylessVerifier
	dsse    ports.DSSESigner
}

// Option injects a Resolver dependency.
//
// None of the three verifier dependencies has a default. They were previously
// defaulted inside NewResolver by constructing cosign.NewSigner(log),
// sigstore.NewVerifier(log) and dsse.NewSigner(log) directly, which made this
// adapter import three of its peers and wire them behind the composition
// root's back. Injection is now the only source; cmd/pokkum supplies all
// three. An un-injected verifier never means "skip that check" — every path
// that needs one and does not have one fails closed with
// ErrVerifierNotInjected.
type Option func(*Resolver)

// WithCosignSigner injects the static-key Cosign signer. A nil signer is
// ignored, which leaves the static-key path unverifiable (and refused), never
// silently self-wired.
func WithCosignSigner(signer ports.CosignSigner) Option {
	return func(r *Resolver) {
		if signer != nil {
			r.signer = signer
		}
	}
}

// WithKeylessVerifier injects the keyless Sigstore verifier. A nil verifier is
// ignored, with the same consequence described on WithCosignSigner.
func WithKeylessVerifier(verifier ports.KeylessVerifier) Option {
	return func(r *Resolver) {
		if verifier != nil {
			r.keyless = verifier
		}
	}
}

// WithDSSESigner injects the DSSE signer/verifier used to check DSSE-enveloped
// SLSA statements. A nil signer is ignored, with the same consequence described
// on WithCosignSigner.
func WithDSSESigner(signer ports.DSSESigner) Option {
	return func(r *Resolver) {
		if signer != nil {
			r.dsse = signer
		}
	}
}

// NewResolver constructs a ProvenanceResolver instance. A nil logger defaults
// to slog.Default(). The verifier dependencies have no defaults and must be
// injected with WithCosignSigner / WithKeylessVerifier / WithDSSESigner by the
// composition root; see Option.
func NewResolver(log *slog.Logger, opts ...Option) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	r := &Resolver{
		log: log,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// ResolveProvenance parses and verifies attestations and provenance for an image ref.
func (r *Resolver) ResolveProvenance(ctx context.Context, req ports.ProvenanceResolverRequest) (ports.ProvenanceSummary, error) {
	if strings.TrimSpace(req.ImageRef) == "" {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: image ref is required: %w", core.ErrInvalidRequest)
	}

	r.log.DebugContext(ctx, "resolving provenance for image", "ref", req.ImageRef)

	// The expected keyless identity is a caller input, never something read
	// back off the certificate being verified. Reject a half-specified one
	// before any network I/O, so the operator gets a message naming the flag
	// they missed instead of a generic Sigstore error several fetches later.
	if err := validateExpectedKeylessIdentity(req.KeylessIdentity); err != nil {
		return ports.ProvenanceSummary{}, err
	}

	nameOpts := []name.Option{name.WeakValidation}
	parsedRef, err := name.ParseReference(req.ImageRef, nameOpts...)
	if err != nil {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: parse image reference %q: %w: %w", req.ImageRef, err, core.ErrInvalidRequest)
	}

	opts, err := r.remoteOptions(ctx, req.RegistryConfigPath)
	if err != nil {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: resolve auth for %s: %w", req.ImageRef, err)
	}

	desc, err := remote.Get(parsedRef, opts...)
	if err != nil {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: fetch image manifest for %s: %w: %w", req.ImageRef, err, core.ErrInvalidRequest)
	}

	imageDigest := desc.Digest.String()
	annotations := make(map[string]string)

	if desc.MediaType.IsIndex() {
		if idx, ierr := desc.ImageIndex(); ierr == nil {
			if im, mierr := idx.IndexManifest(); mierr == nil && im.Annotations != nil {
				annotations = im.Annotations
			}
		}
	} else {
		if img, ierr := desc.Image(); ierr == nil {
			if m, mierr := img.Manifest(); mierr == nil && m.Annotations != nil {
				annotations = m.Annotations
			}
		}
	}

	summary := ports.ProvenanceSummary{
		ImageRef:    req.ImageRef,
		ImageDigest: imageDigest,
		PinnedInputs: ports.PinnedBuildInputs{
			Repo:   annotations["org.opencontainers.image.source"],
			Commit: annotations["org.opencontainers.image.revision"],
		},
	}
	// These are seeded from the image's own unsigned annotations — anyone
	// with push access to the repository controls them. Marked unverified
	// now; overwritten below to SourceProvenanceVerified only if a
	// cryptographically verified SLSA statement actually carries a
	// "source-code" resolved dependency (populateInputsFromSLSA overwrites
	// Repo/Commit in that case too, so the two stay in lockstep).
	if summary.PinnedInputs.Repo != "" || summary.PinnedInputs.Commit != "" {
		summary.PinnedInputs.SourceProvenance = ports.SourceProvenanceUnverified
	}

	repo := parsedRef.Context().Name()

	pubKeyPEM := req.PublicKeyPEM
	if len(pubKeyPEM) == 0 {
		// Either variable may hold PEM text or a path to a PEM file, resolved
		// through the shared helper so the same value means the same thing here
		// as at the composition root. A set-but-unresolvable value is an error,
		// not a reason to fall through to the next variable.
		resolved, _, rerr := keymaterialutils.ResolveFirst(
			keymaterialutils.Candidate{Source: "POKKUM_SIGNING_PUBKEY", Setting: os.Getenv("POKKUM_SIGNING_PUBKEY")},
			keymaterialutils.Candidate{Source: "POKKUM_BASE_IMAGE_PUBKEY", Setting: os.Getenv("POKKUM_BASE_IMAGE_PUBKEY")},
		)
		if rerr != nil {
			return summary, rerr
		}
		pubKeyPEM = resolved
	}
	// No fallback key beyond this. A shared, unattributed placeholder public
	// key used to live here (cosign.DefaultPublicKeyPEM) — no real signer
	// ever held its private half, so it could never actually verify
	// anything; it has been deleted (docs/archive/Roadmap.md item 2h). verifyCosignSignature
	// below tracks whether a static signature was present with nothing
	// configured to check it against (sigVerifyOutcome.staticSigSeenNoKey),
	// so that case fails closed with ErrStaticKeyRequired instead of quietly
	// resolving to SignatureValid: false — indistinguishable, to a caller,
	// from a signature that was checked and found invalid.

	// 1. Check for Cosign signature tag: <repo>:<alg>-<hex>.sig
	sigTagStr := fmt.Sprintf("%s:%s-%s.sig", repo, desc.Digest.Algorithm, desc.Digest.Hex)
	if sigRef, sErr := name.ParseReference(sigTagStr, nameOpts...); sErr == nil {
		if sigImg, iErr := remote.Image(sigRef, opts...); iErr == nil {
			outcome := r.verifyCosignSignature(ctx, repo, desc.Digest, sigImg, pubKeyPEM, req)
			summary.SignatureValid = outcome.valid
			summary.SignerIdentity = outcome.identity

			// Fail closed first on a wiring gap: this image carries signature
			// material and the Resolver was handed nothing able to judge it.
			// Checked before the operator-facing refusals below because it is
			// the more fundamental problem — with no verifier injected, the
			// static-key and keyless outcome flags cannot be trusted to have
			// been evaluated at all — and because reporting SignatureValid:
			// false here would be indistinguishable from a forged signature.
			if outcome.verifierMissing && !outcome.valid {
				return summary, fmt.Errorf(
					"%w: %s carries signature material on %s but no %s was injected into the provenance resolver; "+
						"refusing to report the image as unverified when nothing attempted to verify it: %w",
					ErrVerifierNotInjected, req.ImageRef, sigTagStr, outcome.verifierMissingWhat, core.ErrInvalidRequest)
			}

			// Fail closed: keyless material is present, nothing else
			// verified the image, and we were given no identity to check the
			// certificate against. Reporting SignatureValid=false here would
			// be indistinguishable from a forged signature, and "verify
			// against whatever the certificate says" would accept any Fulcio
			// signer on earth — so refuse and name the missing flags.
			if outcome.keylessMaterialSeen && !outcome.valid && req.KeylessIdentity.Empty() {
				return summary, fmt.Errorf(
					"%w: %s has a Fulcio certificate and Rekor bundle on %s, but --keyless-identity/--keyless-issuer "+
						"were not supplied. Refusing to verify against an unconstrained identity, which any "+
						"certificate Fulcio ever issued would satisfy. Re-run with "+
						"--keyless-identity <expected certificate SAN> --keyless-issuer <expected OIDC issuer URL>: %w",
					ErrKeylessIdentityRequired, req.ImageRef, sigTagStr, core.ErrInvalidRequest)
			}
			if outcome.keylessMaterialSeen && !outcome.valid && outcome.keylessErr != nil {
				r.log.WarnContext(ctx, "keyless signature present but did not verify against the expected identity",
					"ref", req.ImageRef,
					"expected_issuer", req.KeylessIdentity.Issuer,
					"expected_san", req.KeylessIdentity.SAN,
					"err", outcome.keylessErr)
			}

			// Same fail-closed discipline for the static-key path: a Cosign
			// static-signature annotation is present, but no key was
			// configured anywhere (req.PublicKeyPEM, POKKUM_SIGNING_PUBKEY,
			// POKKUM_BASE_IMAGE_PUBKEY all empty) to check it against. This
			// used to silently resolve to SignatureValid: false via a shared,
			// unattributed placeholder key that could never actually verify
			// anything — indistinguishable from a signature that was checked
			// and found invalid. Skipped when keyless material was also
			// present: that path already reports its own, more specific
			// fail-closed error above when it applies.
			if !outcome.valid && !outcome.keylessMaterialSeen && outcome.staticSigSeenNoKey {
				return summary, fmt.Errorf(
					"%w: %s carries a signature on %s; pass --public-key, or set POKKUM_SIGNING_PUBKEY or POKKUM_BASE_IMAGE_PUBKEY: %w",
					ErrStaticKeyRequired, req.ImageRef, sigTagStr, core.ErrInvalidRequest)
			}
		}
	}

	// 2. Check for SLSA provenance attestation tag: <repo>:<alg>-<hex>.att
	attTagStr := fmt.Sprintf("%s:%s-%s.att", repo, desc.Digest.Algorithm, desc.Digest.Hex)
	if attRef, aErr := name.ParseReference(attTagStr, nameOpts...); aErr == nil {
		if attImg, iErr := remote.Image(attRef, opts...); iErr == nil {
			stmt, serr := r.extractSLSAStatement(ctx, attImg, desc.Digest, pubKeyPEM)
			// A missing DSSE verifier must not read as "this image has no
			// provenance". Downstream that answer is load-bearing: step 3
			// below decides --expect-source purely from
			// PinnedInputs.SourceProvenance, so an unwired verifier that
			// silently produced HasProvenance == false would let
			// --allow-unverified-source compare against unsigned annotations
			// while the operator believes a verifier merely found nothing.
			// Every other error here genuinely means "no verified statement in
			// these layers" and is still ignored, as before.
			if serr != nil && errors.Is(serr, ErrVerifierNotInjected) {
				return summary, fmt.Errorf("provenance resolver: %s: %w", req.ImageRef, serr)
			}
			if serr == nil {
				summary.HasProvenance = true
				if r.populateInputsFromSLSA(&summary.PinnedInputs, stmt) {
					// A verified statement actually carried a
					// "source-code" resolved dependency: Repo/Commit above
					// were just overwritten with its values, so the
					// provenance marker must move with them rather than
					// staying at whatever the unsigned-annotation seeding
					// seeded above.
					summary.PinnedInputs.SourceProvenance = ports.SourceProvenanceVerified
				}
			}
		}
	}

	// 3. Validate ExpectSource assertion if provided.
	//
	// Fails closed unless the source values being compared came from a
	// cryptographically verified SLSA statement: comparing --expect-source
	// against the image's own unsigned annotations (or against nothing at
	// all) would look like a real check to an operator reading CI logs
	// while actually verifying nothing, because whoever can push a tag to
	// the repository controls those annotations outright. See
	// ErrUnverifiedSourceProvenance and req.AllowUnverifiedSource.
	if req.ExpectSource != "" {
		verified := summary.PinnedInputs.SourceProvenance == ports.SourceProvenanceVerified
		if !verified && !req.AllowUnverifiedSource {
			reason := fmt.Sprintf("only unsigned OCI annotations were found (repo=%q, commit=%q)", summary.PinnedInputs.Repo, summary.PinnedInputs.Commit)
			if summary.PinnedInputs.Repo == "" && summary.PinnedInputs.Commit == "" {
				reason = "no source repository or commit information was found at all"
			}
			return summary, fmt.Errorf(
				"%w: %s — %s, which any party able to push a tag to %s controls. Sign builds with SLSA "+
					"provenance (`pokkum build --sign`) so --expect-source can verify cryptographically, "+
					"or re-run with --allow-unverified-source to compare anyway (the result will be "+
					"explicitly marked unverified): %w",
				ErrUnverifiedSourceProvenance, req.ImageRef, reason, repo, core.ErrInvalidRequest)
		}
		if !verified {
			r.log.WarnContext(ctx, "--expect-source is comparing against unverified source information (--allow-unverified-source was set); this does not prove supply-chain integrity, it only checks the image's own annotations, which the image's publisher controls",
				"ref", req.ImageRef,
				"expect_source", req.ExpectSource,
				"resolved_repo", summary.PinnedInputs.Repo,
				"resolved_commit", summary.PinnedInputs.Commit)
		}
		if err := validateSourceMatch(summary.PinnedInputs.Repo, summary.PinnedInputs.Commit, req.ExpectSource); err != nil {
			return summary, fmt.Errorf("provenance resolver: %w", err)
		}
	}

	// 4. Toolchain match analysis and L1/L2 prediction
	localGo := runtime.Version()
	localOSArch := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)

	if summary.PinnedInputs.GoVersion != "" {
		provGo := summary.PinnedInputs.GoVersion
		if provGo == localGo {
			summary.ToolchainMatch = true
			summary.ExpectedL1Match = true
			summary.ToolchainNotes = fmt.Sprintf("Local builder (%s) matches provenance builder (%s)", localGo, provGo)
		} else {
			summary.ToolchainMatch = false
			summary.ExpectedL1Match = false
			summary.ToolchainNotes = fmt.Sprintf("Toolchain skew detected: local Go %s vs provenance Go %s (predict L2 semantic match due to gzip framing)", localGo, provGo)
		}
		if summary.PinnedInputs.BuilderOSArch != "" && summary.PinnedInputs.BuilderOSArch != localOSArch {
			summary.ToolchainNotes += fmt.Sprintf("; OS/arch skew: local %s vs builder %s", localOSArch, summary.PinnedInputs.BuilderOSArch)
		}
	} else {
		summary.ToolchainMatch = true
		summary.ExpectedL1Match = true
		summary.ToolchainNotes = fmt.Sprintf("Local builder toolchain: %s (%s)", localGo, localOSArch)
	}

	return summary, nil
}

func (r *Resolver) remoteOptions(ctx context.Context, registryConfigPath string) ([]remote.Option, error) {
	kc, err := registryutils.ResolveKeychain(registryConfigPath)
	if err != nil {
		return nil, err
	}
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
	}, nil
}

// validateExpectedKeylessIdentity rejects a partially-specified expected
// identity. An entirely empty identity is allowed through here — it means "no
// keyless expectation was configured at all", which callers handle separately
// (and fail closed on) once they know whether the image actually carries
// keyless material.
func validateExpectedKeylessIdentity(id ports.KeylessIdentity) error {
	if id.Empty() {
		return nil
	}
	hasIssuer := id.Issuer != "" || id.IssuerRegex != ""
	hasSAN := id.SAN != "" || id.SANRegex != ""
	switch {
	case !hasIssuer:
		return fmt.Errorf("%w: an expected certificate SAN was given but no expected OIDC issuer — "+
			"Sigstore requires both, so supply --keyless-issuer as well: %w",
			ErrKeylessIdentityIncomplete, core.ErrInvalidRequest)
	case !hasSAN:
		return fmt.Errorf("%w: an expected OIDC issuer was given but no expected certificate SAN — "+
			"Sigstore requires both, so supply --keyless-identity as well: %w",
			ErrKeylessIdentityIncomplete, core.ErrInvalidRequest)
	}
	return nil
}

// sigVerifyOutcome is what verifyCosignSignature learned about a `.sig`
// manifest. keylessMaterialSeen is tracked separately from valid so the caller
// can distinguish "this image is not keyless-signed" (nothing to constrain,
// carry on) from "this image IS keyless-signed and we could not judge it"
// (must fail closed).
type sigVerifyOutcome struct {
	valid    bool
	identity string

	// keylessMaterialSeen reports whether any layer carried both a Fulcio
	// certificate and a Rekor bundle, i.e. whether a keyless verification was
	// structurally possible at all.
	keylessMaterialSeen bool

	// keylessErr is the last keyless verification failure, for logging. Nil
	// when no keyless attempt was made or the attempt succeeded.
	keylessErr error

	// staticSigSeenNoKey reports whether any layer carried a static-key
	// Cosign signature annotation while no static public key was configured
	// to check it against — the static-key analogue of keylessMaterialSeen,
	// so the caller can distinguish "there was something to verify and
	// nothing to verify it against" from an ordinary verification failure.
	staticSigSeenNoKey bool

	// verifierMissing reports whether any layer carried signature material
	// that could not be judged because the Resolver has no injected verifier
	// for it — no ports.CosignSigner for a static-key signature, or no
	// ports.KeylessVerifier for Fulcio/Rekor material.
	//
	// It is a third, distinct member of the same family as keylessMaterialSeen
	// and staticSigSeenNoKey, and exists for exactly the same reason: without
	// it, "no verifier wired" collapses onto valid == false with no error,
	// which a caller cannot tell apart from "checked and found invalid" or
	// from "unsigned image". The caller fails closed on it with
	// ErrVerifierNotInjected.
	verifierMissing bool

	// verifierMissingWhat names the missing dependency for the caller's error
	// message (e.g. "ports.CosignSigner (provenance.WithCosignSigner)"), so
	// the message points at the injection that is absent rather than making
	// the reader guess which of the three it was.
	verifierMissingWhat string
}

func (r *Resolver) verifyCosignSignature(ctx context.Context, repo string, digest v1.Hash, sigImg v1.Image, pubKeyPEM []byte, req ports.ProvenanceResolverRequest) sigVerifyOutcome {
	var out sigVerifyOutcome

	m, err := sigImg.Manifest()
	if err != nil {
		return out
	}
	layers, err := sigImg.Layers()
	if err != nil || len(layers) == 0 {
		return out
	}

	for i, layer := range layers {
		rc, err := layer.Uncompressed()
		if err != nil {
			rc, err = layer.Compressed()
			if err != nil {
				continue
			}
		}
		payloadBytes, err := readCapped(rc, "cosign signature payload blob")
		_ = rc.Close()
		if err != nil {
			r.log.WarnContext(ctx, "skipping unreadable signature layer", "repo", repo, "err", err)
			continue
		}

		var sigStr string
		var certPEM []byte
		var chainPEM []byte
		var bundleJSON []byte

		if i < len(m.Layers) && m.Layers[i].Annotations != nil {
			ann := m.Layers[i].Annotations
			sigStr = ann[ports.CosignSignatureAnnotation]
			if sigStr == "" {
				sigStr = ann["org.opencontainers.image.signature"]
			}
			if c, ok := ann[ports.CosignCertificateAnnotation]; ok {
				certPEM = []byte(c)
			}
			if ch, ok := ann[ports.CosignChainAnnotation]; ok {
				chainPEM = []byte(ch)
			}
			if b, ok := ann[ports.CosignBundleAnnotation]; ok {
				bundleJSON = []byte(b)
			}
		}
		if sigStr == "" && m.Annotations != nil {
			sigStr = m.Annotations[ports.CosignSignatureAnnotation]
			if c, ok := m.Annotations[ports.CosignCertificateAnnotation]; ok {
				certPEM = []byte(c)
			}
			if ch, ok := m.Annotations[ports.CosignChainAnnotation]; ok {
				chainPEM = []byte(ch)
			}
			if b, ok := m.Annotations[ports.CosignBundleAnnotation]; ok {
				bundleJSON = []byte(b)
			}
		}

		// 1. Try static-key verification if signature string is present
		if sigStr != "" {
			switch {
			case len(pubKeyPEM) == 0:
				// There is something to verify (a static signature
				// annotation) and nothing configured to verify it against.
				// Recorded distinctly from a failed verification attempt —
				// see ErrStaticKeyRequired — rather than silently skipped,
				// which would be indistinguishable from "checked, invalid".
				out.staticSigSeenNoKey = true
			case r.signer == nil:
				// A key IS configured and there is a signature to check with
				// it, but no ports.CosignSigner was injected to do the
				// checking. Written as an explicit arm rather than left to
				// fall off the end of the switch: `case r.signer != nil:` with
				// no default silently produced valid == false and no recorded
				// reason, i.e. the same fail-open shape (SignatureValid: false,
				// nil error, genuinely signed image) that a149b28 removed from
				// this function.
				out.verifierMissing = true
				out.verifierMissingWhat = "ports.CosignSigner (provenance.WithCosignSigner)"
			default:
				sigBytes, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(sigStr))
				bundle := ports.CosignSignatureBundle{
					PayloadBytes:    payloadBytes,
					Base64Signature: sigStr,
					SignatureBytes:  sigBytes,
				}
				if err := r.signer.Verify(ctx, bundle, pubKeyPEM, repo, digest); err == nil {
					out.valid = true
					out.identity = "static-key"
					return out
				}
				// If payloadBytes was a tarball, try extracting inner payload
				tr := tar.NewReader(bytes.NewReader(payloadBytes))
				for {
					hdr, err := tr.Next()
					if err != nil {
						break
					}
					if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == 0 {
						innerBytes, rerr := readCapped(tr, "cosign signature payload tar entry")
						if rerr != nil {
							r.log.WarnContext(ctx, "skipping oversized signature tar entry", "repo", repo, "err", rerr)
							break
						}
						bundle.PayloadBytes = innerBytes
						if err := r.signer.Verify(ctx, bundle, pubKeyPEM, repo, digest); err == nil {
							out.valid = true
							out.identity = "static-key"
							return out
						}
					}
				}
			}
		}

		// 2. Try genuine keyless verification if certificate and Rekor bundle are present.
		//
		// The expected identity comes from req — the operator — and NEVER from
		// the certificate in certPEM. Deriving it from the certificate under
		// verification (as this code previously did, reading
		// cert.Issuer.CommonName and cert.EmailAddresses[0]) is wrong twice
		// over: cert.Issuer.CommonName is the *CA's* CN, e.g.
		// "sigstore-intermediate", while sigstore-go matches the OIDC issuer
		// extension 1.3.6.1.4.1.57264.1.1, e.g.
		// "https://accounts.google.com" — so the comparison never matched and
		// this whole branch was dead. Correcting only the extension lookup
		// would have been far worse than the dead branch: comparing the
		// certificate against itself always succeeds, so `pokkum verify` would
		// report SignatureValid for an image signed by anybody with a GitHub
		// account. ports.KeylessVerifier refuses an Empty() identity for
		// exactly this reason; a self-derived identity walks straight past that
		// guard, so the guard has to be respected here, at the point the
		// expectation is chosen.
		if len(certPEM) > 0 && len(bundleJSON) > 0 {
			// The nil-verifier test used to sit in this condition
			// (`&& r.keyless != nil`), which meant a missing verifier skipped
			// the branch entirely and left keylessMaterialSeen false — so the
			// caller's fail-closed gate on keylessMaterialSeen never fired for
			// a genuinely keyless-signed image. Material presence and verifier
			// availability are separate facts and are now recorded separately.
			if r.keyless == nil {
				out.verifierMissing = true
				out.verifierMissingWhat = "ports.KeylessVerifier (provenance.WithKeylessVerifier)"
				continue
			}

			out.keylessMaterialSeen = true

			if req.KeylessIdentity.Empty() {
				// Nothing to check against. Do not call the verifier with a
				// fabricated identity; the caller fails the whole resolve
				// closed on out.keylessMaterialSeen instead.
				continue
			}

			sigBytes, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(sigStr))
			if derr != nil || len(sigBytes) == 0 {
				out.keylessErr = fmt.Errorf("keyless signature annotation %s is not usable base64: %w",
					ports.CosignSignatureAnnotation, derr)
				continue
			}

			keylessRes, kerr := r.keyless.Verify(ctx, ports.KeylessVerifyRequest{
				PayloadBytes:    payloadBytes,
				SignatureBytes:  sigBytes,
				CertificatePEM:  certPEM,
				ChainPEM:        chainPEM,
				RekorBundleJSON: bundleJSON,
				Identity:        req.KeylessIdentity,
				TrustedRootJSON: req.TrustedRootJSON,
			})
			if kerr != nil {
				out.keylessErr = kerr
				continue
			}
			// sigstore-go proved the payload was signed by the expected
			// identity. It did not prove which image the payload describes —
			// that is what the simple-signing claims check is for, and it is
			// the only thing standing between a validly-signed payload for a
			// different repo/digest and a false accept here.
			if err := checkSimpleSigningClaims(payloadBytes, repo, digest); err != nil {
				out.keylessErr = fmt.Errorf("keyless signature verified for identity %q but %w", keylessRes.SAN, err)
				continue
			}
			out.valid = true
			out.identity = fmt.Sprintf("keyless:%s (issuer %s)", keylessRes.SAN, keylessRes.Issuer)
			return out
		}
	}

	return out
}

func (r *Resolver) extractSLSAStatement(ctx context.Context, attImg v1.Image, digest v1.Hash, pubKeyPEM []byte) (ports.SLSAStatement, error) {
	layers, err := attImg.Layers()
	if err != nil {
		return ports.SLSAStatement{}, err
	}

	for _, layer := range layers {
		rc, err := layer.Uncompressed()
		if err != nil {
			rc, err = layer.Compressed()
			if err != nil {
				continue
			}
		}
		data, err := readCapped(rc, "attestation layer blob")
		_ = rc.Close()
		if err != nil {
			r.log.WarnContext(ctx, "skipping unreadable attestation layer", "err", err)
			continue
		}

		// 1. Try parsing directly as JSON / DSSE
		stmt, ok, fatal := r.tryParseAndVerifySLSA(ctx, data, digest, pubKeyPEM)
		if fatal != nil {
			return ports.SLSAStatement{}, fatal
		}
		if ok {
			return stmt, nil
		}

		// 2. If data is a tar archive, walk entries
		tr := tar.NewReader(bytes.NewReader(data))
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == 0 {
				entryData, rerr := readCapped(tr, "attestation tar entry")
				if rerr != nil {
					r.log.WarnContext(ctx, "stopping attestation tar walk", "err", rerr)
					break
				}
				stmt, ok, fatal := r.tryParseAndVerifySLSA(ctx, entryData, digest, pubKeyPEM)
				if fatal != nil {
					return ports.SLSAStatement{}, fatal
				}
				if ok {
					return stmt, nil
				}
			}
		}
	}

	return ports.SLSAStatement{}, errors.New("no matching or verified SLSA statement found in attestation layers")
}

// tryParseAndVerifySLSA reports whether data is a DSSE envelope carrying a
// verified SLSA statement for digest.
//
// The third return value is a *fatal* error: non-nil only when the blob cannot
// be judged at all because a dependency is missing, never for "this blob is not
// a statement" or "this envelope failed to verify" (both of which are an
// ordinary false and let the caller keep walking the remaining layers/entries).
// It exists so a missing ports.DSSESigner cannot masquerade as "this image has
// no provenance": those two look identical in the bool, and only one of them is
// a real observation about the image.
func (r *Resolver) tryParseAndVerifySLSA(ctx context.Context, data []byte, digest v1.Hash, pubKeyPEM []byte) (ports.SLSAStatement, bool, error) {
	// Try DSSE envelope
	var env ports.DSSEEnvelope
	if err := json.Unmarshal(data, &env); err == nil && env.Payload != "" && len(env.Signatures) > 0 {
		if r.dsse == nil {
			return ports.SLSAStatement{}, false, fmt.Errorf(
				"%w: a DSSE-enveloped attestation is present but no ports.DSSESigner "+
					"(provenance.WithDSSESigner) was injected into the provenance resolver: %w",
				ErrVerifierNotInjected, core.ErrInvalidRequest)
		}
		if len(pubKeyPEM) == 0 {
			// Distinct from the wiring gap above and deliberately NOT fatal: no
			// key configured is the operator-facing case, and it is already
			// handled by the signature path's ErrStaticKeyRequired refusal.
			// Here it only means this envelope stays unverified, so no
			// provenance is reported from it — never that it is trusted.
			return ports.SLSAStatement{}, false, nil
		}
		payloadBytes, err := r.dsse.Verify(ctx, env, pubKeyPEM)
		if err != nil {
			r.log.DebugContext(ctx, "dsse signature verification failed", "err", err)
			return ports.SLSAStatement{}, false, nil
		}
		var stmt ports.SLSAStatement
		if err := json.Unmarshal(payloadBytes, &stmt); err == nil {
			if statementMatchesDigest(stmt, digest) {
				return stmt, true, nil
			}
		}
		return ports.SLSAStatement{}, false, nil
	}

	return ports.SLSAStatement{}, false, nil
}

// statementMatchesDigest reports whether a cryptographically-verified SLSA
// statement actually describes digest. The DSSE signature proves who wrote the
// statement; this is what binds it to *this* image, so a loose match here lets
// a legitimately-signed attestation for one image be replayed as the
// provenance of another.
//
// The structured subject digest is the authoritative check. The URI form is
// accepted only as an exact "@<alg>:<hex>" suffix — the shape
// slsa.Generator emits ("<repo>@<alg>:<hex>") — rather than as a substring of
// the free-text URI. A bare strings.Contains(sub.URI, digest.Hex) accepted any
// subject whose URI merely contained the hex anywhere, and a registry
// repository name may legitimately consist of lowercase hex characters, so an
// attacker able to get one build of a repository they control signed by a
// shared key could name that repository after the victim image's digest and
// have the resulting attestation match an image it never described.
func statementMatchesDigest(stmt ports.SLSAStatement, digest v1.Hash) bool {
	if digest.Algorithm == "" || digest.Hex == "" {
		return false
	}
	for _, sub := range stmt.Subject {
		if sub.Digest != nil {
			if h, ok := sub.Digest[digest.Algorithm]; ok && h == digest.Hex {
				return true
			}
		}
		if sub.URI != "" && strings.HasSuffix(sub.URI, "@"+digest.String()) {
			return true
		}
	}
	return false
}

// checkSimpleSigningClaims verifies that a Simple Signing payload actually
// names repo at digest.
//
// For the keyless path this is load-bearing: ports.KeylessVerifier proves only
// that the payload bytes were signed by the expected identity, not which image
// the payload describes. Kept deliberately in lockstep with the two sibling
// copies in internal/adapters/baseimage/resolver.go and
// internal/adapters/remotecacheutils — including the Critical.Type check this
// copy previously omitted, without which a payload of some other cosign type
// (an attestation, a blob signature) could be accepted as an image signature.
//
// Both payload type strings are accepted: real upstream Cosign signatures
// write ports.CosignContainerImageSignatureType, while Pokkum's own static-key
// signer writes ports.CosignSimpleSigningType.
//
// The repo and digest comparisons are unconditional and exact. They used to be
// skipped when the expected value was empty, which meant a caller that lost
// track of either one silently downgraded to "no check at all"; an empty
// expectation is a bug in the caller, not permission to accept anything.
func checkSimpleSigningClaims(payloadBytes []byte, repo string, digest v1.Hash) error {
	var payload ports.CosignSimpleSigningPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Errorf("signed payload is not valid Simple Signing JSON: %w", err)
	}

	switch payload.Critical.Type {
	case ports.CosignSimpleSigningType, ports.CosignContainerImageSignatureType:
		// Valid container image signature payload type.
	default:
		return fmt.Errorf("signed payload type is %q, expected %q or %q",
			payload.Critical.Type, ports.CosignContainerImageSignatureType, ports.CosignSimpleSigningType)
	}

	expectedRepo := strings.TrimSpace(repo)
	if expectedRepo == "" {
		return errors.New("cannot validate signed payload: no expected repository was supplied")
	}
	if actualRepo := payload.Critical.Identity.DockerReference; actualRepo != expectedRepo {
		return fmt.Errorf("payload docker-reference %q != expected %q", actualRepo, expectedRepo)
	}

	if digest.Algorithm == "" || digest.Hex == "" {
		return errors.New("cannot validate signed payload: no expected image digest was supplied")
	}
	if actualDigest := payload.Critical.Image.DockerManifestDigest; actualDigest != digest.String() {
		return fmt.Errorf("payload docker-manifest-digest %q != expected %q", actualDigest, digest.String())
	}
	return nil
}

// populateInputsFromSLSA copies resolved dependencies from a
// cryptographically verified SLSA statement into inputs. It reports whether
// the statement carried a "source-code" resolved dependency at all — the
// caller uses that, not just whether inputs.Commit ended up non-empty, to
// decide whether PinnedInputs.SourceProvenance may be upgraded to
// SourceProvenanceVerified: slsa.Generator never emits a "source-code"
// dependency without a non-empty gitCommit digest (see
// internal/adapters/slsa/generator.go), so "dependency present" and "commit
// present" are the same condition in practice, but checking the dependency's
// presence directly keeps this function correct even if that generator-side
// invariant ever changes.
func (r *Resolver) populateInputsFromSLSA(inputs *ports.PinnedBuildInputs, stmt ports.SLSAStatement) (sawSourceCode bool) {
	for _, dep := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
		switch dep.Name {
		case "base-image":
			inputs.BaseImageRef = dep.URI
			if h, ok := dep.Digest["sha256"]; ok {
				inputs.BaseImageHash = "sha256:" + h
			}
		case "source-code":
			sawSourceCode = true
			inputs.Repo = dep.URI
			if c, ok := dep.Digest["gitCommit"]; ok {
				inputs.Commit = c
			}
		case "bun":
			inputs.BunVersion = strings.TrimPrefix(dep.URI, "pkg:generic/bun@")
			if h, ok := dep.Digest["sha256"]; ok {
				inputs.BunBinaryHash = "sha256:" + h
			}
		case "go":
			inputs.GoVersion = strings.TrimPrefix(dep.URI, "pkg:generic/go@")
		}
		if strings.HasSuffix(dep.Name, ".lock") || strings.HasSuffix(dep.Name, "bun.lockb") {
			if inputs.LockfileHashes == nil {
				inputs.LockfileHashes = make(map[string]string)
			}
			if h, ok := dep.Digest["sha256"]; ok {
				inputs.LockfileHashes[dep.Name] = h
			}
		}
	}

	if ip := stmt.Predicate.BuildDefinition.InternalParameters; ip != nil {
		if v, ok := ip["builderOSArch"].(string); ok {
			inputs.BuilderOSArch = v
		}
	}

	if ep := stmt.Predicate.BuildDefinition.ExternalParameters; ep != nil {
		if ps, ok := ep["platforms"].([]any); ok {
			for _, p := range ps {
				if s, ok := p.(string); ok {
					inputs.Platforms = append(inputs.Platforms, s)
				}
			}
		}
		// The application runtime (--runtime) the build recorded; absent in
		// statements generated before the field existed, all of which ran
		// Bun (see ports.PinnedBuildInputs.Runtime).
		if v, ok := ep["runtime"].(string); ok {
			inputs.Runtime = v
		}
	}
	return sawSourceCode
}

// normalizeRepoURI reduces a source-repository identifier to a comparable
// form: transport scheme dropped, ".git" suffix dropped, trailing slash
// dropped. This is deliberately normalization only — it never makes the
// comparison itself fuzzy.
func normalizeRepoURI(s string) string {
	s = strings.TrimSpace(s)
	for _, scheme := range []string{"git+https://", "git+ssh://", "https://", "http://", "ssh://", "git://"} {
		if rest, ok := strings.CutPrefix(s, scheme); ok {
			s = rest
			break
		}
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.TrimSuffix(s, "/")
}

// validateSourceMatch checks a --expect-source assertion against the repository
// and commit resolved from the image.
//
// The repository comparison is an exact match on the normalized form, never a
// substring one. strings.Contains here meant an assertion of
// "github.com/acme/app" was satisfied by a resolved repository of
// "github.com/evil/github.com/acme/app" — an attacker-chosen repository that
// merely embeds the expected name. This matches the exact-match discipline
// already established in internal/adapters/remotecacheutils and
// internal/adapters/baseimage.
//
// The commit comparison stays a prefix match, which is intentional: abbreviated
// commit SHAs are how people actually refer to commits, and the value being
// abbreviated is the caller's own assertion, not attacker-chosen input.
//
// It is nevertheless bounded on two axes that an unqualified strings.HasPrefix
// left open, both found by a real field test:
//
//   - A minimum length. "@b" is a legal one-character prefix that matches
//     roughly one commit in sixteen, and returned success. Git itself refuses
//     to abbreviate below 4 and defaults to 7, so anything shorter than
//     minAbbreviatedCommitLen is a mistake rather than an abbreviation, and is
//     rejected as one instead of being honoured as an assertion that asserts
//     almost nothing.
//   - The dirty marker. A resolved commit of "<sha>-dirty" is prefixed by the
//     clean "<sha>", so asserting the exact 40-character commit silently
//     accepted an image built from uncommitted modifications — the precise
//     case --expect-source exists to catch. A caller who does not write
//     "-dirty" is asserting a clean tree, so a dirty build must fail.
//
// minAbbreviatedCommitLen is the shortest commit prefix --expect-source will
// honour. Git refuses to abbreviate below 4 characters and defaults to 7;
// anything shorter is a typo or a mistake, not an abbreviation, and treating
// it as a satisfied assertion is worse than rejecting it.
const minAbbreviatedCommitLen = 7

// validateCommitMatch reports whether expectedCommit is an acceptable
// assertion about resolvedCommit. See validateSourceMatch's doc comment for
// why a prefix match is correct here and what bounds it needs.
func validateCommitMatch(resolvedCommit, expectedCommit string) error {
	expectedCommit = strings.TrimSpace(expectedCommit)
	if expectedCommit == "" {
		return nil
	}
	if len(expectedCommit) < minAbbreviatedCommitLen {
		return fmt.Errorf("expected commit %q is shorter than %d characters, too ambiguous to assert anything: %w",
			expectedCommit, minAbbreviatedCommitLen, core.ErrInvalidRequest)
	}
	if !strings.HasPrefix(resolvedCommit, expectedCommit) {
		return fmt.Errorf("expected commit %q does not match resolved provenance %q: %w", expectedCommit, resolvedCommit, core.ErrInvalidRequest)
	}
	// The prefix matched, but a clean assertion must not be satisfied by a
	// dirty build: "<sha>" is a prefix of "<sha>-dirty".
	if strings.HasSuffix(resolvedCommit, dirtyCommitSuffix) && !strings.HasSuffix(expectedCommit, dirtyCommitSuffix) {
		return fmt.Errorf("expected commit %q matches, but the image was built from a DIRTY working tree (provenance records %q); "+
			"assert %q%s explicitly if that is intended: %w",
			expectedCommit, resolvedCommit, expectedCommit, dirtyCommitSuffix, core.ErrInvalidRequest)
	}
	return nil
}

// dirtyCommitSuffix is the marker slsa.WorkingTreeDirty causes to be appended
// to a recorded commit when the build's working tree carried changes.
const dirtyCommitSuffix = "-dirty"

func validateSourceMatch(resolvedRepo, resolvedCommit, expectSource string) error {
	expectSource = strings.TrimSpace(expectSource)
	cleanResolvedRepo := normalizeRepoURI(resolvedRepo)

	if strings.Contains(expectSource, "@") {
		parts := strings.SplitN(expectSource, "@", 2)
		expectedRepo := normalizeRepoURI(parts[0])
		expectedCommit := strings.TrimSpace(parts[1])

		if expectedRepo != "" && cleanResolvedRepo != expectedRepo {
			return fmt.Errorf("expected source repo %q does not match resolved provenance %q: %w", expectedRepo, resolvedRepo, core.ErrInvalidRequest)
		}
		if err := validateCommitMatch(resolvedCommit, expectedCommit); err != nil {
			return err
		}
		return nil
	}

	// No "@": the single value is either the repository or a commit prefix.
	if normalizeRepoURI(expectSource) != cleanResolvedRepo && validateCommitMatch(resolvedCommit, expectSource) != nil {
		return fmt.Errorf("expected source %q does not match resolved provenance (%s@%s): %w",
			expectSource, resolvedRepo, resolvedCommit, core.ErrInvalidRequest)
	}
	return nil
}
