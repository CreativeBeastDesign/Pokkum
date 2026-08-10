package packager

import (
	"context"
	"errors"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func buildImages(t *testing.T, plats ...ports.Platform) map[ports.Platform]v1.Image {
	t.Helper()
	p := NewPackager(testLogger())
	out := make(map[ports.Platform]v1.Image, len(plats))
	for _, plat := range plats {
		img, err := p.Build(context.Background(), newRequest(t, plat))
		if err != nil {
			t.Fatalf("build %s: %v", plat, err)
		}
		out[plat] = img
	}
	return out
}

// TestIndexDescriptorOrder pins the descriptor order to the sorted order of
// Platform.String(). "linux/amd64" sorts before "linux/arm64", and that must be
// the order in the manifest regardless of how the caller's map was built.
func TestIndexDescriptorOrder(t *testing.T) {
	idx, err := NewPackager(testLogger()).Index(context.Background(), ports.IndexRequest{
		Images:    buildImages(t, ports.LinuxARM64, ports.LinuxAMD64),
		CreatedAt: buildEpoch,
	})
	if err != nil {
		t.Fatalf("index: %v", err)
	}

	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("index manifest: %v", err)
	}
	if len(im.Manifests) != 2 {
		t.Fatalf("got %d manifests, want 2", len(im.Manifests))
	}

	want := []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64}
	for i, m := range im.Manifests {
		if m.Platform == nil {
			t.Fatalf("manifest %d has no platform", i)
		}
		got, ok := ports.PlatformFromV1(m.Platform)
		if !ok || got != want[i] {
			t.Errorf("manifest %d platform = %v, want %s", i, m.Platform, want[i])
		}
		if m.MediaType != types.OCIManifestSchema1 {
			t.Errorf("manifest %d media type = %s, want %s", i, m.MediaType, types.OCIManifestSchema1)
		}
	}
}

// TestIndexMediaTypeIsOCI checks the index itself is OCI, for the same reason
// the image manifest is: Docker's manifest list has no annotations field.
func TestIndexMediaTypeIsOCI(t *testing.T) {
	idx, err := NewPackager(testLogger()).Index(context.Background(), ports.IndexRequest{
		Images:      buildImages(t, ports.LinuxAMD64, ports.LinuxARM64),
		CreatedAt:   buildEpoch,
		Annotations: map[string]string{ports.LabelSource: "https://github.com/example/app"},
	})
	if err != nil {
		t.Fatalf("index: %v", err)
	}

	mt, err := idx.MediaType()
	if err != nil {
		t.Fatalf("media type: %v", err)
	}
	if mt != types.OCIImageIndex {
		t.Errorf("index media type = %s, want %s", mt, types.OCIImageIndex)
	}

	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("index manifest: %v", err)
	}
	if im.MediaType != types.OCIImageIndex {
		t.Errorf("indexManifest.mediaType = %s, want %s", im.MediaType, types.OCIImageIndex)
	}
	if im.Annotations[ports.LabelSource] != "https://github.com/example/app" {
		t.Errorf("annotations = %v, want the caller's source annotation", im.Annotations)
	}
	if im.Annotations[ports.LabelCreated] != buildEpoch.Format(time.RFC3339) {
		t.Errorf("created annotation = %q, want %q", im.Annotations[ports.LabelCreated], buildEpoch.Format(time.RFC3339))
	}
}

// TestIndexSinglePlatform covers the case ports documents explicitly: a caller
// with one platform may still call Index.
func TestIndexSinglePlatform(t *testing.T) {
	idx, err := NewPackager(testLogger()).Index(context.Background(), ports.IndexRequest{
		Images:    buildImages(t, ports.LinuxAMD64),
		CreatedAt: buildEpoch,
	})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("index manifest: %v", err)
	}
	if len(im.Manifests) != 1 {
		t.Fatalf("got %d manifests, want 1", len(im.Manifests))
	}
}

func TestIndexValidation(t *testing.T) {
	ctx := context.Background()
	p := NewPackager(testLogger())

	t.Run("no images", func(t *testing.T) {
		_, err := p.Index(ctx, ports.IndexRequest{CreatedAt: buildEpoch})
		if !errors.Is(err, core.ErrPackageFailed) {
			t.Fatalf("err = %v, want %v", err, core.ErrPackageFailed)
		}
	})

	t.Run("zero timestamp", func(t *testing.T) {
		_, err := p.Index(ctx, ports.IndexRequest{Images: buildImages(t, ports.LinuxAMD64)})
		if !errors.Is(err, core.ErrPackageFailed) {
			t.Fatalf("err = %v, want %v", err, core.ErrPackageFailed)
		}
	})

	t.Run("nil image", func(t *testing.T) {
		_, err := p.Index(ctx, ports.IndexRequest{
			Images:    map[ports.Platform]v1.Image{ports.LinuxAMD64: nil},
			CreatedAt: buildEpoch,
		})
		if !errors.Is(err, core.ErrPackageFailed) {
			t.Fatalf("err = %v, want %v", err, core.ErrPackageFailed)
		}
	})

	t.Run("unsupported platform", func(t *testing.T) {
		images := buildImages(t, ports.LinuxAMD64)
		images[ports.Platform{OS: "windows", Arch: "amd64"}] = images[ports.LinuxAMD64]
		_, err := p.Index(ctx, ports.IndexRequest{Images: images, CreatedAt: buildEpoch})
		if !errors.Is(err, core.ErrUnsupportedPlatform) {
			t.Fatalf("err = %v, want %v", err, core.ErrUnsupportedPlatform)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		images := buildImages(t, ports.LinuxAMD64)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := p.Index(cctx, ports.IndexRequest{Images: images, CreatedAt: buildEpoch})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}
