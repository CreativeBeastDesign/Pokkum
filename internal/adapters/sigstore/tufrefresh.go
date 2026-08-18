package sigstore

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// TrustedRootTUFTarget is the target name the Sigstore TUF repository serves
// the trust root under. It is the same name sigstore-go's own
// root.FetchTrustedRoot uses; it is spelled out here because this package
// fetches the *raw* target bytes rather than sigstore-go's parsed struct (see
// FetchTrustedRootJSON).
const TrustedRootTUFTarget = "trusted_root.json"

// TrustedRootTUFRepository is the public-good Sigstore TUF repository. Same
// value as tuf.DefaultMirror; named here so callers and messages do not have
// to reach into the vendored package for it.
const TrustedRootTUFRepository = tuf.DefaultMirror

// TrustedRootOrigin says where a resolved trust root came from. It exists so a
// caller can log or display the provenance of the anchor a verification
// actually used — "verified against a live TUF snapshot" and "verified against
// a six-month-old embedded snapshot" are very different claims.
type TrustedRootOrigin string

const (
	// TrustedRootOriginEmbedded means the snapshot compiled into this binary.
	TrustedRootOriginEmbedded TrustedRootOrigin = "embedded snapshot"

	// TrustedRootOriginTUF means a snapshot resolved through the Sigstore TUF
	// repository (network or its on-disk TUF cache), with TUF signature
	// verification performed by sigstore-go.
	TrustedRootOriginTUF TrustedRootOrigin = "TUF repository"
)

// TUFOptions configures trust-root resolution through Sigstore's TUF
// repository. The zero value is not usable; start from DefaultTUFOptions.
type TUFOptions struct {
	// Offline refuses all network access, the way hermetic builds require.
	// When set, FetchTrustedRootJSON fails closed with
	// core.ErrHermeticViolation *before constructing a TUF client at all*,
	// and ResolveTrustedRootJSON goes straight to the embedded snapshot
	// without touching the network.
	//
	// This is a hard precondition rather than a hint because sigstore-go's TUF
	// client offers no guaranteed-offline mode: tuf.Options.ForceCache and
	// CacheValidity both still perform a full network refresh when the local
	// metadata is missing or expired (documented on those fields upstream). So
	// "hermetic" cannot be expressed as a TUF option; it has to be a decision
	// taken before the client exists.
	Offline bool

	// CachePath is where the TUF metadata and target cache lives. Empty means
	// <os.UserCacheDir>/pokkum/sigstore-tuf, matching the bunruntime
	// resolver's ~/.cache/pokkum/<component> convention rather than sharing
	// cosign's ~/.sigstore/root.
	CachePath string

	// RepositoryBaseURL is the TUF repository. Empty means
	// TrustedRootTUFRepository (the public-good instance).
	RepositoryBaseURL string

	// CacheValidityDays reuses an existing cache without a network refresh for
	// this many days after it was last refreshed. Zero refreshes on every
	// call, which is what the TUF specification mandates but is wasteful on a
	// verification path.
	CacheValidityDays int

	// DisableLocalCache works entirely in memory, for read-only filesystems.
	// It forces a network refresh on every call.
	DisableLocalCache bool
}

// DefaultTUFOptions returns options for the public-good Sigstore instance with
// a one-day cache validity: fresh enough that a new Rekor shard is picked up
// within a day of being published, cheap enough that repeated verifications in
// one CI run do not each hit the network.
func DefaultTUFOptions() TUFOptions {
	return TUFOptions{
		CacheValidityDays: 1,
	}
}

// cachePath resolves the effective cache directory.
func (o TUFOptions) cachePath() string {
	if o.CachePath != "" {
		return o.CachePath
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "pokkum", "sigstore-tuf")
}

// repositoryBaseURL resolves the effective repository URL.
func (o TUFOptions) repositoryBaseURL() string {
	if o.RepositoryBaseURL != "" {
		return o.RepositoryBaseURL
	}
	return TrustedRootTUFRepository
}

// FetchTrustedRootJSON resolves the current Sigstore trust root through the
// TUF repository and returns the raw, TUF-signature-verified target bytes.
//
// It returns the *raw* target rather than sigstore-go's parsed-and-remarshalled
// struct on purpose: the raw bytes are exactly what the repository signed, so
// they can be embedded, digested and compared byte-for-byte, whereas a
// round trip through protojson is only as faithful as the current version of
// that struct.
//
// This is the only function in this package that performs network I/O, and
// nothing on the verification path calls it. Verifier.Verify stays fully
// offline; a caller that wants live trust-root material calls this explicitly
// and passes the result as KeylessVerifyRequest.TrustedRootJSON.
//
// Fails closed: any TUF error (bad signature, expired metadata, unreachable
// mirror) is returned, never silently downgraded to the embedded snapshot.
// Use ResolveTrustedRootJSON for the degrade-with-a-warning behaviour.
func FetchTrustedRootJSON(ctx context.Context, opts TUFOptions) ([]byte, error) {
	if opts.Offline {
		return nil, fmt.Errorf(
			"sigstore: refusing to refresh the Sigstore trust root from %s in hermetic/offline mode — "+
				"verification will use the embedded snapshot instead; pre-fetch a snapshot and pass "+
				"--sigstore-trusted-root=<file> if the embedded one is too old: %w",
			opts.repositoryBaseURL(), core.ErrHermeticViolation)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sigstore: trust root refresh cancelled before it started: %w", err)
	}

	tufOpts := tuf.DefaultOptions().
		WithContext(ctx).
		WithRepositoryBaseURL(opts.repositoryBaseURL()).
		WithCacheValidity(opts.CacheValidityDays)
	if opts.DisableLocalCache {
		tufOpts = tufOpts.WithDisableLocalCache()
	} else {
		tufOpts = tufOpts.WithCachePath(opts.cachePath())
	}

	client, err := tuf.New(tufOpts)
	if err != nil {
		return nil, fmt.Errorf(
			"sigstore: cannot initialise the Sigstore TUF client for %s (cache %s): %w",
			opts.repositoryBaseURL(), opts.cachePath(), err)
	}

	data, err := client.GetTarget(TrustedRootTUFTarget)
	if err != nil {
		return nil, fmt.Errorf(
			"sigstore: cannot fetch TUF target %q from %s: %w",
			TrustedRootTUFTarget, opts.repositoryBaseURL(), err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf(
			"sigstore: TUF target %q from %s verified but is empty",
			TrustedRootTUFTarget, opts.repositoryBaseURL())
	}

	// The TUF client verified the target's signature and digest; this checks
	// that what it handed back is a trust root this codebase can actually
	// parse, so a caller never embeds or verifies against bytes that only turn
	// out to be unusable later, on a security-relevant path.
	if _, err := root.NewTrustedRootFromJSON(data); err != nil {
		return nil, fmt.Errorf(
			"sigstore: TUF target %q from %s verified but does not parse as a Sigstore trusted root (%d bytes): %w",
			TrustedRootTUFTarget, opts.repositoryBaseURL(), len(data), err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sigstore: trust root refresh cancelled: %w", err)
	}
	return data, nil
}

// ResolveTrustedRootJSON returns the trust root to verify against, preferring
// a live TUF snapshot and degrading to the embedded one.
//
// The degradation is deliberately loud, because a silent degradation is the
// exact failure this whole mechanism exists to prevent: an operator who thinks
// they are verifying against a current trust root, while actually holding a
// snapshot that predates the Rekor shard the signature was logged to, sees
// only a signature-verification failure.
//
//   - opts.Offline: returns the embedded snapshot immediately, no network
//     attempted, no warning about the network (being offline was asked for) —
//     but still warns if the embedded snapshot is itself stale.
//   - TUF succeeds: returns the live bytes and TrustedRootOriginTUF.
//   - TUF fails: logs a warning naming the TUF error, returns the embedded
//     snapshot, and additionally warns if that snapshot is stale.
//
// It never returns an error for the TUF path failing — that is the whole point
// of a fallback — but it does return an error if the embedded snapshot itself
// cannot be assessed, since that means this binary was built wrong.
func ResolveTrustedRootJSON(ctx context.Context, log *slog.Logger, opts TUFOptions, now time.Time) ([]byte, TrustedRootOrigin, error) {
	if log == nil {
		log = slog.Default()
	}

	if !opts.Offline {
		data, err := FetchTrustedRootJSON(ctx, opts)
		if err == nil {
			log.Debug("resolved Sigstore trust root from the TUF repository",
				"repository", opts.repositoryBaseURL(),
				"target", TrustedRootTUFTarget,
				"bytes", len(data),
				"sha256", digestOf(data))
			return data, TrustedRootOriginTUF, nil
		}
		log.Warn("could not refresh the Sigstore trust root from the TUF repository; "+
			"falling back to the snapshot embedded in this binary",
			"repository", opts.repositoryBaseURL(),
			"error", err)
	}

	embedded := DefaultTrustedRootJSON()
	freshness, err := CheckDefaultTrustedRootFreshness(now)
	if err != nil {
		return nil, "", fmt.Errorf("sigstore: cannot assess the embedded trust root: %w", err)
	}
	if freshness.Stale() {
		log.Warn("keyless verification is using a stale embedded Sigstore trust root", "detail", freshness.String())
	}
	return embedded, TrustedRootOriginEmbedded, nil
}
