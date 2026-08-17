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
	s, _ := newTestRegistryWithReferrers(t)
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

// TestAttachSBOM_ReferrerMode_FailsLoudlyWhenUnsupported is PR-8's other
// half: before this fix, an explicit --sbom-attach=referrer against a
// registry lacking OCI 1.1 support silently succeeded by landing on
// go-containerregistry's own internal fallback tag ("sha256-<hex>", no
// ".sbom" suffix) — a genuinely different tag than ports.SBOMTag, invisible
// to `cosign download sbom` and to this adapter's own tag-mode read path.
// An explicit request for referrer mode must now fail outright against an
// unsupported registry, not silently attach the SBOM somewhere nothing else
// looks for it.
func TestAttachSBOM_ReferrerMode_FailsLoudlyWhenUnsupported(t *testing.T) {
	s, _ := newTestRegistry(t) // referrers support OFF
	repo := registryRepo(t, s, "app/sbom-unsupported")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("d", 64)}

	a := NewAdapter(nil)
	_, err := a.AttachSBOM(context.Background(), ports.AttachSBOMRequest{
		Repo:       repo,
		Subject:    subject,
		AttachMode: ports.SBOMAttachReferrer,
		Document: ports.SBOMDocument{
			MediaType: ports.MediaTypeSPDXJSON,
			Content:   []byte(`{"spdxVersion":"SPDX-2.3"}`),
		},
	})
	if err == nil {
		t.Fatal("expected an error for explicit referrer mode against a registry without referrers support")
	}
	if !strings.Contains(err.Error(), referrersUnsupportedSubstring) {
		t.Errorf("expected error to mention unsupported referrers, got: %v", err)
	}

	// And critically: nothing should have landed at go-containerregistry's
	// own incompatible fallback tag ("sha256-<hex>", no ".sbom" suffix).
	wrongTag := "sha256-" + subject.Hex
	wrongRef, rErr := name.ParseReference(registryRepo(t, s, "app/sbom-unsupported") + ":" + wrongTag)
	if rErr != nil {
		t.Fatalf("ParseReference: %v", rErr)
	}
	if _, gErr := remote.Get(wrongRef); gErr == nil {
		t.Error("expected nothing to have been pushed at go-containerregistry's own incompatible fallback tag")
	}
}

// TestAttachSBOM_AutoMode_FallsBackWhenUnsupported is PR-8's core fix: auto
// mode (the new default) must still produce a real, discoverable SBOM
// attachment — at ports.SBOMTag, the tag `cosign download sbom` and this
// adapter's own tag-mode read path actually look for — against a registry
// that lacks OCI 1.1 referrers support, without the caller needing to know
// that in advance.
func TestAttachSBOM_AutoMode_FallsBackWhenUnsupported(t *testing.T) {
	s, _ := newTestRegistry(t) // referrers support OFF
	repo := registryRepo(t, s, "app/sbom-auto-fallback")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("e", 64)}
	content := []byte(`{"spdxVersion":"SPDX-2.3","name":"pokkum-test-auto"}`)

	a := NewAdapter(nil)
	res, err := a.AttachSBOM(context.Background(), ports.AttachSBOMRequest{
		Repo:       repo,
		Subject:    subject,
		AttachMode: ports.SBOMAttachAuto,
		Document: ports.SBOMDocument{
			MediaType: ports.MediaTypeSPDXJSON,
			Content:   content,
		},
	})
	if err != nil {
		t.Fatalf("AttachSBOM (auto, unsupported registry): %v", err)
	}

	wantTag := "sha256-" + subject.Hex + ".sbom"
	if len(res.Tags) != 1 || res.Tags[0] != wantTag {
		t.Fatalf("expected auto mode to fall back to tag mode at %q, got Tags = %v", wantTag, res.Tags)
	}

	ref, err := name.ParseReference(repo + ":" + wantTag)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if _, err := remote.Get(ref); err != nil {
		t.Fatalf("expected the SBOM to actually be readable at the real tag: %v", err)
	}
}

// TestAttachSBOM_AutoMode_UsesReferrerWhenSupported confirms auto mode
// doesn't unconditionally fall back — a registry that genuinely supports OCI
// 1.1 referrers gets the referrer-mode result (no tag), not a needless tag
// fallback.
func TestAttachSBOM_AutoMode_UsesReferrerWhenSupported(t *testing.T) {
	s, _ := newTestRegistryWithReferrers(t)
	repo := registryRepo(t, s, "app/sbom-auto-supported")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("f", 64)}

	a := NewAdapter(nil)
	res, err := a.AttachSBOM(context.Background(), ports.AttachSBOMRequest{
		Repo:       repo,
		Subject:    subject,
		AttachMode: ports.SBOMAttachAuto,
		Document: ports.SBOMDocument{
			MediaType: ports.MediaTypeSPDXJSON,
			Content:   []byte(`{"spdxVersion":"SPDX-2.3"}`),
		},
	})
	if err != nil {
		t.Fatalf("AttachSBOM (auto, supported registry): %v", err)
	}
	if len(res.Tags) != 0 {
		t.Errorf("expected auto mode to use referrer mode (no tag) when supported, got Tags = %v", res.Tags)
	}
	if !strings.Contains(res.Ref, "@sha256:") {
		t.Errorf("Ref = %q, expected a digest reference", res.Ref)
	}
}

// TestAttachSBOM_DefaultModeIsAuto confirms leaving AttachMode unset behaves
// as auto (falls back on an unsupported registry), not as a hard-fail
// referrer-only request — the behavior change this fix makes the default.
func TestAttachSBOM_DefaultModeIsAuto(t *testing.T) {
	s, _ := newTestRegistry(t) // referrers support OFF
	repo := registryRepo(t, s, "app/sbom-default")
	subject := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("1", 64)}

	a := NewAdapter(nil)
	_, err := a.AttachSBOM(context.Background(), ports.AttachSBOMRequest{
		Repo:    repo,
		Subject: subject,
		// AttachMode deliberately left unset.
		Document: ports.SBOMDocument{
			MediaType: ports.MediaTypeSPDXJSON,
			Content:   []byte(`{"spdxVersion":"SPDX-2.3"}`),
		},
	})
	if err != nil {
		t.Fatalf("expected the default (unset) attach mode to fall back gracefully, got: %v", err)
	}
}
