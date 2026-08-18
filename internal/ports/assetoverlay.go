package ports

import "context"

// AssetOverlayResolver implements the registry-side half of the
// --asset-overlay rolling-deploy feature: resolving which prior generations
// of an image were pushed to a given target, and pulling their /app/client
// immutable asset content by digest. Implemented by
// internal/adapters/assetoverlay.
type AssetOverlayResolver interface {
	// ResolvePredecessorChain resolves repo:tag's current digest and walks
	// its recorded predecessor chain up to maxDepth entries deep, most
	// recent first. Returns (nil, nil) — not an error — when repo:tag does
	// not resolve at all (the expected state for the first image ever
	// pushed to a target).
	ResolvePredecessorChain(ctx context.Context, repo, tag, registryConfigPath string, insecure bool, maxDepth int) ([]string, error)

	// BuildOverlayDir resolves sources' immutable client asset content and
	// merges it into one new temp directory the caller owns (must
	// os.RemoveAll it once packaging has consumed it). Returns an empty
	// string, not an error, when sources is empty.
	//
	// Each entry in sources is either a bare digest — resolved against
	// defaultRepo, the caller's own push target — or a fully-qualified
	// "repo@digest" ref that names a different repository entirely, as
	// ResolveDigest returns for an explicit --asset-overlay-from entry. The
	// implementation distinguishes the two automatically.
	BuildOverlayDir(ctx context.Context, defaultRepo string, sources []string, registryConfigPath string, insecure bool) (string, error)

	// ResolveDigest resolves an arbitrary image ref (as supplied via
	// --asset-overlay-from, not necessarily at this build's own push
	// target) to its fully-qualified "repo@digest" form — not just the bare
	// digest — for the explicit-override path that bypasses
	// predecessor-chain auto-discovery. The repository is included because
	// the ref may name an entirely different repository than this build's
	// own push target; a bare digest alone would lose that information and
	// cause BuildOverlayDir to pull from the wrong repo.
	ResolveDigest(ctx context.Context, ref, registryConfigPath string, insecure bool) (string, error)
}
