package comparator_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/comparator"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestComparator_CompareImages(t *testing.T) {
	c := comparator.NewComparator(slog.Default())
	ctx := context.Background()

	t.Run("empty remote image ref returns ErrInvalidRequest", func(t *testing.T) {
		_, err := c.CompareImages(ctx, ports.ImageComparatorRequest{})
		if err == nil {
			t.Fatalf("expected error for empty remote image ref")
		}
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest, got %v", err)
		}
	})

	t.Run("valid comparison request succeeds", func(t *testing.T) {
		res, err := c.CompareImages(ctx, ports.ImageComparatorRequest{
			RemoteImageRef: "ghcr.io/acme/app:v1.0.0",
			LocalTarball:   "/tmp/local.tar",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.L1ExactMatch || !res.L2SemanticMatch {
			t.Errorf("expected L1 and L2 matches to be true, got L1=%t, L2=%t", res.L1ExactMatch, res.L2SemanticMatch)
		}
		if res.Level != "L2" {
			t.Errorf("expected Level L2, got %q", res.Level)
		}
	})
}
