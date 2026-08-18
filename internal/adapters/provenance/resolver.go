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

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/dsse"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
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
	// (Roadmap.md item 2h).
	ErrStaticKeyRequired = errors.New("provenance resolver: image carries a Cosign static-key signature but no verification key was configured")
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

// Option configures a Resolver instance.
type Option func(*Resolver)

// WithCosignSigner overrides the static-key Cosign signer.
func WithCosignSigner(signer ports.CosignSigner) Option {
	return func(r *Resolver) {
		if signer != nil {
			r.signer = signer
		}
	}
}

// WithKeylessVerifier overrides the keyless Sigstore verifier.
func WithKeylessVerifier(verifier ports.KeylessVerifier) Option {
	return func(r *Resolver) {
		if verifier != nil {
			r.keyless = verifier
		}
	}
}

// WithDSSESigner overrides the DSSE signer/verifier.
func WithDSSESigner(signer ports.DSSESigner) Option {
	return func(r *Resolver) {
		if signer != nil {
			r.dsse = signer
		}
	}
}

// NewResolver constructs a ProvenanceResolver instance.
func NewResolver(log *slog.Logger, opts ...Option) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	r := &Resolver{
		log:     log,
		signer:  cosign.NewSigner(log),
		keyless: sigstore.NewVerifier(log),
		dsse:    dsse.NewSigner(log),
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

	repo := parsedRef.Context().Name()

	pubKeyPEM := req.PublicKeyPEM
	if len(pubKeyPEM) == 0 {
		pubKeyPEM = []byte(os.Getenv("POKKUM_SIGNING_PUBKEY"))
	}
	if len(pubKeyPEM) == 0 {
		pubKeyPEM = []byte(os.Getenv("POKKUM_BASE_IMAGE_PUBKEY"))
	}
	// No fallback key beyond this. A shared, unattributed placeholder public
	// key used to live here (cosign.DefaultPublicKeyPEM) — no real signer
	// ever held its private half, so it could never actually verify
	// anything; it has been deleted (Roadmap.md item 2h). verifyCosignSignature
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
			if stmt, serr := r.extractSLSAStatement(ctx, attImg, desc.Digest, pubKeyPEM); serr == nil {
				summary.HasProvenance = true
				r.populateInputsFromSLSA(&summary.PinnedInputs, stmt)
			}
		}
	}

	// 3. Validate ExpectSource assertion if provided
	if req.ExpectSource != "" {
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
			sigStr = ann[sigstore.CosignSignatureAnnotation]
			if sigStr == "" {
				sigStr = ann["org.opencontainers.image.signature"]
			}
			if c, ok := ann[sigstore.CosignCertificateAnnotation]; ok {
				certPEM = []byte(c)
			}
			if ch, ok := ann[sigstore.CosignChainAnnotation]; ok {
				chainPEM = []byte(ch)
			}
			if b, ok := ann[sigstore.CosignBundleAnnotation]; ok {
				bundleJSON = []byte(b)
			}
		}
		if sigStr == "" && m.Annotations != nil {
			sigStr = m.Annotations[sigstore.CosignSignatureAnnotation]
			if c, ok := m.Annotations[sigstore.CosignCertificateAnnotation]; ok {
				certPEM = []byte(c)
			}
			if ch, ok := m.Annotations[sigstore.CosignChainAnnotation]; ok {
				chainPEM = []byte(ch)
			}
			if b, ok := m.Annotations[sigstore.CosignBundleAnnotation]; ok {
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
			case r.signer != nil:
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
		if len(certPEM) > 0 && len(bundleJSON) > 0 && r.keyless != nil {
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
					sigstore.CosignSignatureAnnotation, derr)
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
		if stmt, ok := r.tryParseAndVerifySLSA(ctx, data, digest, pubKeyPEM); ok {
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
				if stmt, ok := r.tryParseAndVerifySLSA(ctx, entryData, digest, pubKeyPEM); ok {
					return stmt, nil
				}
			}
		}
	}

	return ports.SLSAStatement{}, errors.New("no matching or verified SLSA statement found in attestation layers")
}

func (r *Resolver) tryParseAndVerifySLSA(ctx context.Context, data []byte, digest v1.Hash, pubKeyPEM []byte) (ports.SLSAStatement, bool) {
	// Try DSSE envelope
	var env ports.DSSEEnvelope
	if err := json.Unmarshal(data, &env); err == nil && env.Payload != "" && len(env.Signatures) > 0 {
		if r.dsse == nil || len(pubKeyPEM) == 0 {
			return ports.SLSAStatement{}, false
		}
		payloadBytes, err := r.dsse.Verify(ctx, env, pubKeyPEM)
		if err != nil {
			r.log.DebugContext(ctx, "dsse signature verification failed", "err", err)
			return ports.SLSAStatement{}, false
		}
		var stmt ports.SLSAStatement
		if err := json.Unmarshal(payloadBytes, &stmt); err == nil {
			if statementMatchesDigest(stmt, digest) {
				return stmt, true
			}
		}
		return ports.SLSAStatement{}, false
	}

	return ports.SLSAStatement{}, false
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

func (r *Resolver) populateInputsFromSLSA(inputs *ports.PinnedBuildInputs, stmt ports.SLSAStatement) {
	for _, dep := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
		switch dep.Name {
		case "base-image":
			inputs.BaseImageRef = dep.URI
			if h, ok := dep.Digest["sha256"]; ok {
				inputs.BaseImageHash = "sha256:" + h
			}
		case "source-code":
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
		if r, ok := ep["repository"].(string); ok && inputs.Repo == "" {
			inputs.Repo = r
		}
		if ps, ok := ep["platforms"].([]any); ok {
			for _, p := range ps {
				if s, ok := p.(string); ok {
					inputs.Platforms = append(inputs.Platforms, s)
				}
			}
		}
	}
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
		if expectedCommit != "" && !strings.HasPrefix(resolvedCommit, expectedCommit) {
			return fmt.Errorf("expected commit %q does not match resolved provenance %q: %w", expectedCommit, resolvedCommit, core.ErrInvalidRequest)
		}
		return nil
	}

	// No "@": the single value is either the repository or a commit prefix.
	if normalizeRepoURI(expectSource) != cleanResolvedRepo && !strings.HasPrefix(resolvedCommit, expectSource) {
		return fmt.Errorf("expected source %q does not match resolved provenance (%s@%s): %w",
			expectSource, resolvedRepo, resolvedCommit, core.ErrInvalidRequest)
	}
	return nil
}
