package baseimage

import (
	"context"
	"errors"
	"testing"
	"time"

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

	r := NewResolver(nil)
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
