package registry

import (
	"context"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestPush_NewTagOnExistingDigest guards the interaction between idempotency
// and tagging.
//
// Skipping the upload when the digest is already present is correct, but the
// tags requested by *this* push still have to end up pointing at it. Two builds
// of identical content under different tags produce the same digest, so a skip
// keyed on the digest alone would silently never create the second tag while
// still reporting success — and a pipeline that then deployed repo:v2 would be
// referencing something the registry does not have.
func TestPush_NewTagOnExistingDigest(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/retag")
	img := randomImage(t)
	a := NewAdapter(nil)
	ctx := context.Background()

	// First push establishes the digest under tag v1.
	first, err := a.Push(ctx, ports.PushRequest{
		Repo:    repo,
		Payload: ports.Payload{Image: img},
		Tags:    []string{"v1"},
	})
	if err != nil {
		t.Fatalf("first push: %v", err)
	}

	// Second push: identical content, brand-new tag. The blobs can legitimately
	// be skipped; the tag cannot.
	second, err := a.Push(ctx, ports.PushRequest{
		Repo:    repo,
		Payload: ports.Payload{Image: img},
		Tags:    []string{"v2"},
	})
	if err != nil {
		t.Fatalf("second push: %v", err)
	}

	if first.Digest != second.Digest {
		t.Fatalf("identical content should yield identical digests, got %s and %s",
			first.Digest, second.Digest)
	}

	// The reported tag must actually resolve in the registry.
	ref, err := name.NewTag(repo + ":v2")
	if err != nil {
		t.Fatalf("parse tag: %v", err)
	}
	desc, err := remote.Head(ref)
	if err != nil {
		t.Fatalf("tag v2 was reported as pushed but does not resolve in the registry: %v", err)
	}
	if desc.Digest.String() != second.Digest.String() {
		t.Fatalf("tag v2 resolves to %s, want %s", desc.Digest, second.Digest)
	}
}
