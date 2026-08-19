package packager

import (
	"context"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// LayerBuilderAdapter implements ports.LayerBuilder by wrapping
// BuildDirectoryTreeLayerWithPruning with NoPrune — the exact same call the
// packager itself makes for --asset-overlay's merged layer
// (appendAssetOverlayLayer, above). It exists so a different adapter
// package (pokkum verify's comparator) can reconstruct that exact layer
// without an adapter-to-adapter import, which the hexagonal architecture
// rules forbid — see ports.LayerBuilder's doc comment.
type LayerBuilderAdapter struct{}

// NewLayerBuilder returns a ready-to-use LayerBuilderAdapter. The zero value
// holds no state; every call derives everything it needs from its arguments.
func NewLayerBuilder() *LayerBuilderAdapter { return &LayerBuilderAdapter{} }

var _ ports.LayerBuilder = (*LayerBuilderAdapter)(nil)

// BuildLayer implements ports.LayerBuilder.
func (b *LayerBuilderAdapter) BuildLayer(ctx context.Context, platform ports.Platform, hostDir, targetPrefix string, modTime time.Time, compression ports.CompressionAlgorithm) (v1.Layer, error) {
	layer, _, _, err := BuildDirectoryTreeLayerWithPruning(ctx, platform, hostDir, targetPrefix, modTime, compression, pruneutils.PruneOptions{NoPrune: true})
	return layer, err
}
