package provenance_test

import (
	"context"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/provenance"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestResolveProvenance_Success(t *testing.T) {
	r := provenance.NewResolver(nil)
	ctx := context.Background()

	summary, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef: "ghcr.io/example/my-app:latest",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !summary.HasProvenance {
		t.Error("expected HasProvenance to be true")
	}
	if summary.PinnedInputs.Repo == "" {
		t.Error("expected non-empty PinnedInputs.Repo")
	}
}

func TestResolveProvenance_ExpectSourceMismatch(t *testing.T) {
	r := provenance.NewResolver(nil)
	ctx := context.Background()

	_, err := r.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef:     "ghcr.io/example/my-app:latest",
		ExpectSource: "github.com/wrong/repo",
	})
	if err == nil {
		t.Fatal("expected error on source mismatch, got nil")
	}
}
