package core

// Internal test package: imageLabels and baseNameForLabel are unexported, and
// exporting them purely so an external test could reach them would widen the
// package's API for a test's convenience.

import (
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestImageLabels_BaseNameIsInvariantAcrossBuildState is the regression guard
// for a real reproducibility bug, found by benchmarks/three-way on its first
// run against a live daemon.
//
// org.opencontainers.image.base.name is baked into the image config, so its
// value is part of the image's bytes: anything that changes it changes the
// config digest, then the manifest digest, then the index digest. It used to
// be set from BaseImageInfo.Ref, which the resolver rebinds depending on local
// state — to the lockfile's pinned digest form once pokkum.lock exists, and to
// the mirror tag when an escrow mirror is in use.
//
// The observable effect was that the FIRST build of a project produced a
// different image digest from every subsequent build of identical source
// (measured: five consecutive builds, only the first differed, and the config
// diff was this one annotation and nothing else).
//
// Each case below is the SAME logical build with Ref rebound the way the
// resolver rebinds it. All must produce one value.
func TestImageLabels_BaseNameIsInvariantAcrossBuildState(t *testing.T) {
	const (
		upstream = "gcr.io/distroless/cc-debian12:nonroot"
		pinned   = "gcr.io/distroless/cc-debian12@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f"
		mirror   = "ghcr.io/acme/escrow:sha256-9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f"
	)

	states := []struct {
		name string
		base BaseImageInfo
	}{
		{
			// Build 1: no pokkum.lock yet, so Ref is still the tag.
			name: "first build, no lockfile",
			base: BaseImageInfo{Ref: upstream, UpstreamRef: upstream, PinnedRef: pinned},
		},
		{
			// Build 2+: the lockfile exists, so the resolver rebinds Ref to
			// the locked pinned-digest form. This is the case that used to
			// disagree with the one above.
			name: "later build, lockfile present",
			base: BaseImageInfo{Ref: pinned, UpstreamRef: upstream, PinnedRef: pinned},
		},
		{
			// A build pulling through an escrow mirror. Same source, same
			// upstream image, different fetch route — the published image
			// must not record the mirror, or two colleagues building the same
			// commit get different digests.
			name: "build through an escrow mirror",
			base: BaseImageInfo{Ref: mirror, UpstreamRef: upstream, PinnedRef: pinned},
		},
	}

	var first string
	for i, st := range states {
		t.Run(st.name, func(t *testing.T) {
			labels := imageLabels(BuildRequest{}, st.base, Toolchain{}, ports.BunResolverResult{})
			got := labels[ports.LabelBaseName]

			if got == "" {
				t.Fatalf("%s produced no %s label at all", ports.LabelBaseName, st.name)
			}
			// Naming the offending value explicitly, so a failure sends the
			// next reader to the rebinding rather than to the label plumbing.
			if got == st.base.Ref && st.base.Ref != upstream {
				t.Errorf("%s = %q, which is BaseImageInfo.Ref — a value the resolver rebinds with local build state. "+
					"Recording it makes identical source produce different image digests.", ports.LabelBaseName, got)
			}
			if i == 0 {
				first = got
			} else if got != first {
				t.Errorf("%s = %q, but the first build state produced %q. "+
					"This annotation is inside the image config, so two builds of identical source now have different digests.",
					ports.LabelBaseName, got, first)
			}
		})
	}

	if first != upstream {
		t.Errorf("base.name = %q, want the human-readable upstream reference %q "+
			"(the digest is carried separately in %s)", first, upstream, ports.LabelBaseDigest)
	}
}

// TestBaseNameForLabel_NeverFallsBackToRef pins the rule that makes the fix
// hold under future edits: Ref is not an eligible source on ANY path.
//
// A fallback chain ending in Ref would look defensive and would silently
// reintroduce the bug for exactly the inputs that triggered it, which is why
// the chain stops at PinnedRef — also invariant — and then at "".
func TestBaseNameForLabel_NeverFallsBackToRef(t *testing.T) {
	tests := []struct {
		name string
		base BaseImageInfo
		want string
	}{
		{
			name: "upstream wins when present",
			base: BaseImageInfo{Ref: "mirror.example.com/x@sha256:aa", UpstreamRef: "up:tag", PinnedRef: "pin@sha256:bb"},
			want: "up:tag",
		},
		{
			name: "falls back to the pinned digest, not to Ref",
			base: BaseImageInfo{Ref: "mirror.example.com/x@sha256:aa", PinnedRef: "pin@sha256:bb"},
			want: "pin@sha256:bb",
		},
		{
			name: "omits the annotation rather than recording Ref",
			base: BaseImageInfo{Ref: "mirror.example.com/x@sha256:aa"},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := baseNameForLabel(tc.base); got != tc.want {
				t.Errorf("baseNameForLabel = %q, want %q", got, tc.want)
			}
		})
	}
}
