package registry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestSBOMTag pins the cosign/ko tag convention this package relies on:
// ':' in the digest becomes '-', then the ".sbom" suffix. ports.SBOMTag is
// implemented in internal/ports, not here, but AttachSBOM's entire addressing
// scheme depends on this mapping, so it earns a direct assertion in this
// package too.
func TestSBOMTag(t *testing.T) {
	h := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}
	got := ports.SBOMTag(h)
	want := "sha256-" + strings.Repeat("a", 64) + ".sbom"
	if got != want {
		t.Errorf("SBOMTag(%v) = %q, want %q", h, got, want)
	}
}

func TestAttachSBOM_EmptyRepo(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.AttachSBOM(context.Background(), ports.AttachSBOMRequest{
		Subject:  v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
		Document: ports.SBOMDocument{MediaType: ports.MediaTypeSPDXJSON, Content: []byte("{}")},
	})
	if !errors.Is(err, core.ErrNoDockerRepo) {
		t.Fatalf("err = %v, want core.ErrNoDockerRepo", err)
	}
}

func TestAttachSBOM_EmptyDocument(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.AttachSBOM(context.Background(), ports.AttachSBOMRequest{
		Repo:     "example.com/app",
		Subject:  v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)},
		Document: ports.SBOMDocument{MediaType: ports.MediaTypeSPDXJSON},
	})
	if !errors.Is(err, core.ErrPushFailed) {
		t.Fatalf("err = %v, want core.ErrPushFailed", err)
	}
}

func TestAttachSBOM_RoundTripTag(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/sbom")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("b", 64)}
	content := []byte(`{"spdxVersion":"SPDX-2.3","name":"pokkum-test"}`)

	a := NewAdapter(nil)
	res, err := a.AttachSBOM(context.Background(), ports.AttachSBOMRequest{
		Repo:       repo,
		Subject:    subject,
		AttachMode: ports.SBOMAttachTag,
		Document: ports.SBOMDocument{
			Format:    ports.SBOMFormatSPDXJSON,
			MediaType: ports.MediaTypeSPDXJSON,
			Content:   content,
		},
	})
	if err != nil {
		t.Fatalf("AttachSBOM: %v", err)
	}

	wantTag := "sha256-" + subject.Hex + ".sbom"
	if len(res.Tags) != 1 || res.Tags[0] != wantTag {
		t.Fatalf("Tags = %v, want [%q]", res.Tags, wantTag)
	}
	wantRef := repo + ":" + wantTag
	if res.Ref != wantRef {
		t.Errorf("Ref = %q, want %q", res.Ref, wantRef)
	}

	ref, err := name.ParseReference(wantRef)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	desc, err := remote.Get(ref)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}
	pulled, err := desc.Image()
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	layers, err := pulled.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("len(layers) = %d, want 1", len(layers))
	}

	mt, err := layers[0].MediaType()
	if err != nil {
		t.Fatalf("MediaType: %v", err)
	}
	if string(mt) != ports.MediaTypeSPDXJSON {
		t.Errorf("layer media type = %q, want %q", mt, ports.MediaTypeSPDXJSON)
	}

	rc, err := layers[0].Uncompressed()
	if err != nil {
		t.Fatalf("Uncompressed: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("layer content = %q, want %q", got, content)
	}
}

func TestAttachSBOM_RoundTripReferrer(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/sbom-ref")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("c", 64)}
	content := []byte(`{"spdxVersion":"SPDX-2.3","name":"pokkum-test-ref"}`)

	a := NewAdapter(nil)
	res, err := a.AttachSBOM(context.Background(), ports.AttachSBOMRequest{
		Repo:       repo,
		Subject:    subject,
		AttachMode: ports.SBOMAttachReferrer,
		Document: ports.SBOMDocument{
			Format:    ports.SBOMFormatSPDXJSON,
			MediaType: ports.MediaTypeSPDXJSON,
			Content:   content,
		},
	})
	if err != nil {
		t.Fatalf("AttachSBOM: %v", err)
	}

	if len(res.Tags) != 0 {
		t.Fatalf("Referrer mode Tags = %v, want empty", res.Tags)
	}
	if !strings.Contains(res.Ref, "@sha256:") {
		t.Errorf("Ref = %q, expected digest reference with @sha256:", res.Ref)
	}

	ref, err := name.ParseReference(res.Ref)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", res.Ref, err)
	}
	desc, err := remote.Get(ref)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}
	pulled, err := desc.Image()
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	layers, err := pulled.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("len(layers) = %d, want 1", len(layers))
	}
}
