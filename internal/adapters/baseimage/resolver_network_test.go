package baseimage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestResolve_LiveRegistries resolves all three presets against the real
// distroless and chainguard registries for both v0.1 platforms. It is the one
// place this package touches the network, and it is skipped under -short so
// the default `go test ./...` stays hermetic. Run explicitly with:
//
//	go test ./internal/adapters/baseimage/... -run LiveRegistries -v
func TestResolve_LiveRegistries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent base image resolution in -short mode")
	}

	type target struct {
		name   string
		preset ports.BaseImagePreset
		ref    string // empty means "use the preset's own default"
	}

	targets := []target{
		{name: "distroless", preset: ports.BaseImageDistroless},
		{name: "chainguard", preset: ports.BaseImageChainguard},
		// The custom preset has no default of its own; point it at the same
		// distroless ref to prove a user-supplied ref round-trips identically
		// to the built-in preset.
		{name: "custom(distroless-ref)", preset: ports.BaseImageCustom, ref: ports.DistrolessBaseRef},
	}

	r := NewResolver(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, tgt := range targets {
		t.Run(tgt.name, func(t *testing.T) {
			for _, p := range []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64} {
				t.Run(p.String(), func(t *testing.T) {
					got, err := r.Resolve(ctx, ports.BaseImageRequest{
						Preset:    tgt.preset,
						Ref:       tgt.ref,
						Platforms: []ports.Platform{p},
					})
					if err != nil {
						t.Fatalf("Resolve(%s, %s) = %v", tgt.name, p, err)
					}
					if got.Digest.String() == "" {
						t.Errorf("Digest is empty")
					}
					if got.PinnedRef == "" {
						t.Errorf("PinnedRef is empty")
					}
					img, ok := got.Images[p]
					if !ok {
						t.Fatalf("Images has no entry for %s", p)
					}
					cf, err := img.ConfigFile()
					if err != nil {
						t.Fatalf("ConfigFile: %v", err)
					}
					if cf.OS != p.OS || cf.Architecture != p.Arch {
						t.Errorf("resolved image is %s/%s, want %s/%s", cf.OS, cf.Architecture, p.OS, p.Arch)
					}
					t.Logf("%s %s: ref=%s pinned_ref=%s digest=%s is_index=%v config_os_arch=%s/%s",
						tgt.name, p, got.Ref, got.PinnedRef, got.Digest, got.IsIndex, cf.OS, cf.Architecture)
				})
			}
		})
	}
}

// TestResolve_LiveRegistries_IncompatibleCustomBase is a light network check
// that the static-base rejection also fires for a real, resolvable-but-wrong
// reference (gcr.io/distroless/static-debian12), not just for refs that
// happen to match the string heuristic in isolation. It is skipped under
// -short for the same reason as TestResolve_LiveRegistries.
func TestResolve_LiveRegistries_IncompatibleCustomBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent base image resolution in -short mode")
	}

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       "gcr.io/distroless/static-debian12:nonroot",
		Platforms: []ports.Platform{ports.LinuxAMD64},
	})
	if err == nil {
		t.Fatal("expected an error resolving distroless/static-debian12")
	}
	if !errors.Is(err, core.ErrBaseImageIncompatible) {
		t.Fatalf("err = %v, want core.ErrBaseImageIncompatible", err)
	}
}

// TestResolve_LiveKeylessVerification resolves the real distroless and
// chainguard presets with keyless Sigstore signature verification enabled,
// against the live upstream registries, using the realistic default
// configuration a real `pokkum build` takes (VerifySignature: true,
// VerifyMode left at its zero value so each preset's own default — keyless —
// is what actually runs).
//
// This is the one test in the whole keyless-verification effort that would
// catch a real upstream identity rotation — distroless changing its signer's
// GCP service account, or Chainguard renaming its release workflow — because
// it is the only test that checks a live certificate against
// internal/ports/baseimage.go's DistrolessKeylessIssuer/SAN and
// ChainguardKeylessIssuer/SAN constants. Every other keyless test in this
// package and in internal/adapters/sigstore is hermetic and verifies against
// a frozen fixture captured on a specific date; none of them can ever notice
// that upstream has started signing with a different identity. That
// blind spot is exactly why this test exists despite the redundancy with the
// hermetic coverage.
//
// It is skipped under -short so the default `go test ./...` stays hermetic.
// Run explicitly with:
//
//	go test ./internal/adapters/baseimage/... -run LiveKeyless -v
//
// Note for anyone investigating a CI failure here: cgr.dev/chainguard/glibc-
// dynamic:latest is rebuilt frequently (potentially hourly, per earlier
// research), so a transient failure on the chainguard case is more likely to
// be a flaky or mid-rebuild registry than a real regression. Re-run this test
// before concluding that the identity constants in internal/ports/baseimage.go
// need updating.
func TestResolve_LiveKeylessVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent base image resolution in -short mode")
	}

	type target struct {
		name   string
		preset ports.BaseImagePreset
	}

	targets := []target{
		{name: "distroless", preset: ports.BaseImageDistroless},
		{name: "chainguard", preset: ports.BaseImageChainguard},
	}

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, tgt := range targets {
		t.Run(tgt.name, func(t *testing.T) {
			got, err := r.Resolve(ctx, ports.BaseImageRequest{
				Preset:          tgt.preset,
				Platforms:       []ports.Platform{ports.LinuxAMD64},
				VerifySignature: true,
			})
			if err != nil {
				t.Fatalf("Resolve(%s) with keyless signature verification = %v", tgt.name, err)
			}
			t.Logf("%s: keyless signature verified, digest=%s pinned_ref=%s", tgt.name, got.Digest, got.PinnedRef)
		})
	}

	// Small addition: prove the KeylessIdentity override path actually gets
	// checked against real registry data, not just the hermetic fixture.
	// An obviously wrong identity must make a live resolve fail.
	t.Run("wrong identity override is rejected live", func(t *testing.T) {
		_, err := r.Resolve(ctx, ports.BaseImageRequest{
			Preset:          ports.BaseImageDistroless,
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			VerifySignature: true,
			KeylessIdentity: ports.KeylessIdentity{
				Issuer: "https://example.com/not-the-real-issuer",
				SAN:    "nobody@example.com",
			},
		})
		if err == nil {
			t.Fatal("expected resolve to fail: KeylessIdentity override does not match the real distroless signer")
		}
		if !errors.Is(err, core.ErrBaseSignatureInvalid) {
			t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
		}
	})
}
