package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/assetoverlay"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/comparator"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type verifyOptions struct {
	noRebuild             bool
	expectSource          string
	allowUnverifiedSource bool
	against               string
	registryConfig        string
	output                string
	keylessIdentity       string
	keylessIssuer         string
	publicKey             string
	trustedRoot           string
	sigstoreTUFRefresh    bool
}

var exitFunc = os.Exit

func newVerifyCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	opts := &verifyOptions{}

	cmd := &cobra.Command{
		Use:   "verify <image-ref>",
		Short: "Independently rebuild from source and verify container image provenance",
		Long:  `Verify proves that a remote registry image or container provably matches its source commit by checking SLSA attestations, Cosign signatures, and independently comparing against a rebuilt artifact.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			imageRef := args[0]
			return runVerify(ctx, logger, opts, imageRef)
		},
	}

	cmd.Flags().BoolVar(&opts.noRebuild, "no-rebuild", false, "Validate attestations and toolchain metadata without running a rebuild")
	cmd.Flags().StringVar(&opts.expectSource, "expect-source", "", "Assert expected source repository and commit (repo@commit)")
	cmd.Flags().BoolVar(&opts.allowUnverifiedSource, "allow-unverified-source", false,
		"Allow --expect-source to compare against source information that is not backed by a cryptographically verified SLSA attestation (e.g. only the image's own unsigned annotations). Without this, --expect-source refuses to run against unverified source information. The result is always marked unverified when this is used")
	cmd.Flags().StringVar(&opts.against, "against", "", "Explicit local tarball path to compare against instead of remote registry")
	cmd.Flags().StringVar(&opts.registryConfig, "registry-config", "", "Path to docker config.json-style auth file for private registries")

	// Signature verification flags. Naming mirrors the existing
	// --base-keyless-identity/--base-keyless-issuer and
	// --cache-keyless-identity/--cache-keyless-issuer pairs on `pokkum build`;
	// `verify` has exactly one verification subject (the image named on the
	// command line), so it takes the unprefixed form.
	cmd.Flags().StringVar(&opts.keylessIdentity, "keyless-identity", "",
		"Expected Fulcio certificate Subject Alternative Name for keyless signature verification. Required to verify a keyless-signed image; without it verification is refused rather than accepting any Fulcio signer")
	cmd.Flags().StringVar(&opts.keylessIssuer, "keyless-issuer", "",
		"Expected OIDC issuer URL for keyless signature verification (e.g. https://token.actions.githubusercontent.com). Required alongside --keyless-identity")
	cmd.Flags().StringVar(&opts.publicKey, "public-key", "",
		"Path or PEM string for the static Cosign public key to verify the image signature against")
	cmd.Flags().StringVar(&opts.trustedRoot, "sigstore-trusted-root", "",
		"Path to a Sigstore trusted-root JSON snapshot to verify keyless material against, instead of the embedded public-good snapshot")
	cmd.Flags().BoolVar(&opts.sigstoreTUFRefresh, "sigstore-tuf-refresh", false,
		"Opt-in: refresh the Sigstore trust root from the live TUF repository before verifying keyless material, falling back to the snapshot embedded in this binary (with a warning) if the refresh fails. Ignored when --sigstore-trusted-root is set, which always wins. verify has no hermetic/offline mode of its own -- it already reaches the registry to pull the image, its attestations and signatures -- so this performs the network fetch whenever set rather than refusing it; a fetch failure only warns and falls back, it never fails the command")

	return cmd
}

func runVerify(ctx context.Context, logger *slog.Logger, opts *verifyOptions, imageRef string) error {
	outputFormat := ports.OutputFormat(opts.output)

	provReq := ports.ProvenanceResolverRequest{
		ImageRef:              imageRef,
		ExpectSource:          opts.expectSource,
		AllowUnverifiedSource: opts.allowUnverifiedSource,
		RegistryConfigPath:    opts.registryConfig,
		KeylessIdentity: ports.KeylessIdentity{
			SAN:    opts.keylessIdentity,
			Issuer: opts.keylessIssuer,
		},
	}

	// Same "path first, literal PEM second" handling as
	// --cache-verify-key in build.go, so a key can be passed either way.
	if opts.publicKey != "" {
		if data, rerr := os.ReadFile(opts.publicKey); rerr == nil {
			provReq.PublicKeyPEM = data
		} else {
			provReq.PublicKeyPEM = []byte(opts.publicKey)
		}
	}
	if opts.trustedRoot != "" {
		data, rerr := os.ReadFile(opts.trustedRoot)
		if rerr != nil {
			// Fail closed: silently falling back to the embedded trust root
			// would verify against a different root than the operator asked
			// for, which is exactly the substitution they were trying to avoid.
			msg := fmt.Sprintf("cannot read --sigstore-trusted-root %q: %v", opts.trustedRoot, rerr)
			if outputFormat == ports.FormatJSON {
				_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_INVALID_ARGUMENT", msg, "")
				exitFunc(2)
				return nil
			}
			fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
			exitFunc(2)
			return nil
		}
		provReq.TrustedRootJSON = data
	} else if opts.sigstoreTUFRefresh {
		// Only reached when --sigstore-trusted-root was not given, so the
		// explicit file always wins over the refresh flag. verify has no
		// --hermetic of its own: it is never offline-guaranteed to begin
		// with (it already pulls the image, attestations and signatures
		// from the registry named on the command line), so Offline is
		// deliberately false here -- unlike build's binding to --hermetic,
		// there is no air-gapped invariant to protect on this path. A TUF
		// fetch failure never fails verification; ResolveTrustedRootJSON
		// warns and degrades to the embedded snapshot.
		log := logger
		if log == nil {
			log = slog.Default()
		}
		tufOpts := sigstoreTUFOptionsFactory()
		tufOpts.Offline = false
		data, origin, rerr := sigstore.ResolveTrustedRootJSON(ctx, log, tufOpts, time.Now())
		if rerr != nil {
			// Only fails if the embedded snapshot itself cannot be assessed,
			// which means this binary was built wrong -- fail closed rather
			// than verify against material that could not be characterized.
			msg := fmt.Sprintf("cannot resolve Sigstore trust root: %v", rerr)
			if outputFormat == ports.FormatJSON {
				_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_INVALID_ARGUMENT", msg, "")
				exitFunc(2)
				return nil
			}
			fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
			exitFunc(2)
			return nil
		}
		provReq.TrustedRootJSON = data
		log.Info("resolved Sigstore trust root for verification", "origin", string(origin))
	}

	provResolver := newProvenanceResolver(logger)
	provSummary, err := provResolver.ResolveProvenance(ctx, provReq)

	if err != nil {
		msg := fmt.Sprintf("failed to resolve provenance for %s: %v", imageRef, err)
		if outputFormat == ports.FormatJSON {
			_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_PROVENANCE_FAILED", msg, "")
			exitFunc(2)
			return nil
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		exitFunc(2)
		return nil
	}

	commitDisplay := provSummary.PinnedInputs.Commit
	if len(commitDisplay) > 8 {
		commitDisplay = commitDisplay[:8]
	}
	if commitDisplay == "" {
		commitDisplay = "(unrecorded)"
	}
	// Visible marker so a reader of the text report — not just the JSON
	// payload's pinned_inputs.source_provenance field — cannot mistake an
	// unverified (or --allow-unverified-source) comparison for a real
	// cryptographic one. See ports.SourceProvenance's doc comment.
	sourceProvenanceDisplay := "none recorded"
	switch provSummary.PinnedInputs.SourceProvenance {
	case ports.SourceProvenanceVerified:
		sourceProvenanceDisplay = "verified (SLSA attestation)"
	case ports.SourceProvenanceUnverified:
		sourceProvenanceDisplay = "UNVERIFIED (unsigned image annotations only)"
	}

	repoDisplay := provSummary.PinnedInputs.Repo
	if repoDisplay == "" {
		repoDisplay = "(unrecorded)"
	}

	if opts.noRebuild {
		if !provSummary.HasProvenance && !provSummary.SignatureValid {
			msg := unverifiedImageMessage(ctx, imageRef, provSummary.ImageDigest, opts.registryConfig)
			if outputFormat == ports.FormatJSON {
				_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_PROVENANCE_FAILED", msg, "")
				exitFunc(2)
				return nil
			}
			fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
			exitFunc(2)
			return nil
		}

		verdict := "ATTESTATION_VALIDATED"
		if !provSummary.HasProvenance && provSummary.SignatureValid {
			verdict = "SIGNATURE_VALIDATED_NO_PROVENANCE"
		}

		out := ports.VerifyOutput{
			ImageRef:   imageRef,
			Verdict:    verdict,
			Level:      "NONE",
			Provenance: provSummary,
		}

		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteSuccess(os.Stdout, "verify", out)
		}

		fmt.Println("=== Pokkum Rebuild Verification Report (No-Rebuild Mode) ===")
		fmt.Printf("Image:       %s\n", provSummary.ImageRef)
		fmt.Printf("Digest:      %s\n", provSummary.ImageDigest)
		fmt.Printf("Source Repo: %s @ %s\n", repoDisplay, commitDisplay)
		fmt.Printf("Source Verify: %s\n", sourceProvenanceDisplay)
		fmt.Printf("Attestation: %t\n", provSummary.HasProvenance)
		fmt.Printf("Signature:   %t (%s)\n", provSummary.SignatureValid, provSummary.SignerIdentity)
		if provSummary.PinnedInputs.Runtime != "" {
			fmt.Printf("Runtime:     %s\n", provSummary.PinnedInputs.Runtime)
		}
		if provSummary.PinnedInputs.BunVersion != "" {
			fmt.Printf("Toolchain:   Bun %s\n", provSummary.PinnedInputs.BunVersion)
		}
		if provSummary.ToolchainNotes != "" {
			fmt.Printf("Notes:       %s\n", provSummary.ToolchainNotes)
		}
		fmt.Printf("Verdict:     %s\n", verdict)
		return nil
	}

	// Full Rebuild / Comparison Mode
	if opts.against == "" {
		msg := fmt.Sprintf("rebuild comparison requires an artifact to compare against: specify --against <path/to/rebuilt.tar> or pass --no-rebuild for attestation check only (source commit: %s @ %s)", repoDisplay, commitDisplay)
		if outputFormat == ports.FormatJSON {
			_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_REBUILD_REQUIRED", msg, "")
			exitFunc(2)
			return nil
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		exitFunc(2)
		return nil
	}

	// NewComparatorWithAssetOverlay, not the bare NewComparator: verify is
	// cmd/pokkum's composition root for the comparator, the sole place
	// permitted to construct concrete adapters (assetoverlay.Resolver,
	// packager.LayerBuilderAdapter) and inject them behind their ports
	// interfaces — see ports.LayerBuilder's doc comment and
	// docs/items/asset-overlay-verify-gap.md. Without this, comparing any
	// image built with --asset-overlay would hard-fail rather than
	// reconstruct the overlay layer (Comparator fails closed when it
	// carries the annotation but has no reconstruction support configured).
	comp := comparator.NewComparatorWithAssetOverlay(logger, assetoverlay.NewResolver(), packager.NewLayerBuilder())
	compResult, err := comp.CompareImages(ctx, ports.ImageComparatorRequest{
		RemoteImageRef:     imageRef,
		LocalTarball:       opts.against,
		RegistryConfigPath: opts.registryConfig,
	})

	if err != nil {
		msg := fmt.Sprintf("rebuild comparison failed: %v", err)
		if outputFormat == ports.FormatJSON {
			_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_COMPARISON_FAILED", msg, "")
			exitFunc(1)
			return nil
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		exitFunc(1)
		return nil
	}

	verdict := "VERIFIED_" + compResult.Level
	if compResult.Level == "L3" {
		verdict = "MISMATCH"
	}

	out := ports.VerifyOutput{
		ImageRef:        imageRef,
		Verdict:         verdict,
		Level:           compResult.Level,
		Provenance:      provSummary,
		MismatchDetails: compResult.L3FileDiffs,
	}

	if outputFormat == ports.FormatJSON {
		if compResult.Level == "L3" {
			_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_COMPARISON_MISMATCH", compResult.Summary, "")
			exitFunc(1)
			return nil
		}
		return jsonutils.WriteSuccess(os.Stdout, "verify", out)
	}

	fmt.Println("=== Pokkum Rebuild Verification Report ===")
	fmt.Printf("Image:       %s\n", provSummary.ImageRef)
	fmt.Printf("Digest:      %s\n", provSummary.ImageDigest)
	fmt.Printf("Source Repo: %s @ %s\n", repoDisplay, commitDisplay)
	fmt.Printf("Source Verify: %s\n", sourceProvenanceDisplay)
	fmt.Printf("Level:       %s\n", compResult.Level)
	fmt.Printf("Summary:     %s\n", compResult.Summary)
	fmt.Printf("Verdict:     %s\n", verdict)

	if len(compResult.L3FileDiffs) > 0 {
		fmt.Println("\nFile-Level Differences:")
		for _, d := range compResult.L3FileDiffs {
			fmt.Printf("  - %s\n", d)
		}
	}

	if compResult.Level == "L3" {
		exitFunc(1)
	}

	return nil
}

// unverifiedImageMessage builds the error text for the "no attestation,
// no verified signature" case at --no-rebuild's default log level (F10).
//
// Before this, `!provSummary.HasProvenance && !provSummary.SignatureValid`
// always produced the exact same sentence regardless of *why*: an image
// nobody ever signed reads identically to an image signed with a real
// signature that fails to verify against the configured key/identity (wrong
// --public-key, wrong --keyless-identity/--keyless-issuer, or genuine
// tampering). cosign distinguishes these ("no signatures found" vs "invalid
// signature ..."); this did not.
//
// The distinguishing fact -- was signature/attestation material present at
// all -- is computed inside internal/adapters/provenance.Resolver
// (sigVerifyOutcome tracks keylessMaterialSeen/staticSigSeenNoKey, and
// tryParseAndVerifySLSA's DSSE failure is logged at slog.LevelDebug) but is
// discarded before it reaches ports.ProvenanceSummary: only the two
// booleans survive. cmd/pokkum does not own internal/adapters/provenance or
// internal/ports, so it cannot widen that struct here. As a client-side
// stopgap, this independently re-derives *presence* (never validity) by
// checking for the same "<repo>:<alg>-<hex>.sig/.att" tags
// ResolveProvenance itself looks for (probeSignatureMaterialPresence).
//
// This is a real fix for the symptom, but it is not the durable one: it
// duplicates ResolveProvenance's tag-naming convention instead of reusing
// information the resolver already computed, so the two can silently drift
// if that convention ever changes on one side and not the other. The
// durable fix is to have internal/adapters/provenance carry its own
// materialPresent/validity distinction out on ports.ProvenanceSummary (e.g.
// a SignatureMaterialPresent/AttestationMaterialPresent pair, or a small
// VerificationDiagnostic string) instead of reducing it to two booleans, and
// have this function read that field instead of re-probing the registry.
func unverifiedImageMessage(ctx context.Context, imageRef, imageDigest, registryConfigPath string) string {
	sigPresent, attPresent, probeErr := probeSignatureMaterialPresence(ctx, imageRef, imageDigest, registryConfigPath)

	switch {
	case probeErr != nil:
		// Inconclusive: don't assert a distinction this couldn't actually
		// confirm. Keep the original wording, but say so rather than
		// silently guessing one way or the other.
		return fmt.Sprintf(
			"image %s has neither a valid SLSA provenance attestation nor a verified signature "+
				"(could not determine whether the image is unsigned or carries a failed signature: %v)",
			imageRef, probeErr)

	case sigPresent || attPresent:
		var present []string
		if sigPresent {
			present = append(present, "a Cosign signature")
		}
		if attPresent {
			present = append(present, "an SLSA provenance attestation")
		}
		return fmt.Sprintf(
			"image %s carries %s that did NOT verify -- the signature/attestation is present but invalid "+
				"(wrong key, wrong --keyless-identity/--keyless-issuer, or tampering), not an absence of "+
				"signing. Re-run with --log-level=debug for the underlying verification error",
			imageRef, strings.Join(present, " and "))

	default:
		return fmt.Sprintf(
			"image %s has no Cosign signature or SLSA provenance attestation at all -- it was never signed. "+
				"Sign it with `pokkum build --sign` (the default) to enable verification",
			imageRef)
	}
}

// probeSignatureMaterialPresence is a best-effort, read-only check for
// whether imageRef carries Cosign signature (.sig) and/or SLSA attestation
// (.att) tags at all, independent of whether that material actually
// verifies. See unverifiedImageMessage's doc comment for why this exists and
// what the non-duplicated fix would look like.
func probeSignatureMaterialPresence(ctx context.Context, imageRef, imageDigest, registryConfigPath string) (sigPresent, attPresent bool, err error) {
	ref, err := name.ParseReference(imageRef, name.WeakValidation)
	if err != nil {
		return false, false, fmt.Errorf("parse image reference %q: %w", imageRef, err)
	}
	digest, err := v1.NewHash(imageDigest)
	if err != nil {
		return false, false, fmt.Errorf("parse image digest %q: %w", imageDigest, err)
	}
	kc, err := registryutils.ResolveKeychain(registryConfigPath)
	if err != nil {
		return false, false, err
	}
	opts := []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(kc)}
	repo := ref.Context().Name()

	sigPresent, err = registryTagExists(fmt.Sprintf("%s:%s-%s.sig", repo, digest.Algorithm, digest.Hex), opts)
	if err != nil {
		return false, false, err
	}
	attPresent, err = registryTagExists(fmt.Sprintf("%s:%s-%s.att", repo, digest.Algorithm, digest.Hex), opts)
	if err != nil {
		return false, false, err
	}
	return sigPresent, attPresent, nil
}

// registryTagExists reports whether tagStr resolves to a manifest, treating
// a 404 as "does not exist" and any other transport error (auth rejection,
// DNS failure, timeout, 5xx) as inconclusive -- mirroring
// internal/adapters/registry.Adapter.digestExists's classification, since a
// transient network problem must not be silently reinterpreted as proof a
// tag is absent.
func registryTagExists(tagStr string, opts []remote.Option) (bool, error) {
	tagRef, err := name.ParseReference(tagStr, name.WeakValidation)
	if err != nil {
		return false, fmt.Errorf("parse tag %q: %w", tagStr, err)
	}
	_, err = remote.Head(tagRef, opts...)
	if err == nil {
		return true, nil
	}
	var terr *transport.Error
	if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("check %q: %w", tagStr, err)
}
