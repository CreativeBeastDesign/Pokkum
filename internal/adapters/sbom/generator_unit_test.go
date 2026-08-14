package sbom

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scannerutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func validRequest(t *testing.T, dir string) ports.SBOMRequest {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return ports.SBOMRequest{
		ProjectDir: dir,
		Format:     ports.SBOMFormatSPDXJSON,
		CreatedAt:  time.Unix(0, 0).UTC(),
	}
}

func TestGenerate_RejectsDisabledFormat(t *testing.T) {
	g := NewGenerator(nil)
	req := validRequest(t, t.TempDir())
	req.Format = ports.SBOMFormatNone

	_, err := g.Generate(context.Background(), req)
	if !errors.Is(err, core.ErrInvalidSBOMFormat) {
		t.Fatalf("Generate() error = %v, want wrapping core.ErrInvalidSBOMFormat", err)
	}
}

func TestGenerate_RejectsUnknownFormat(t *testing.T) {
	g := NewGenerator(nil)
	req := validRequest(t, t.TempDir())
	req.Format = ports.SBOMFormat("made-up")

	_, err := g.Generate(context.Background(), req)
	if !errors.Is(err, core.ErrInvalidSBOMFormat) {
		t.Fatalf("Generate() error = %v, want wrapping core.ErrInvalidSBOMFormat", err)
	}
}

func TestGenerate_RequiresProjectDir(t *testing.T) {
	g := NewGenerator(nil)
	req := validRequest(t, t.TempDir())
	req.ProjectDir = "  "

	_, err := g.Generate(context.Background(), req)
	if !errors.Is(err, core.ErrSBOMFailed) {
		t.Fatalf("Generate() error = %v, want wrapping core.ErrSBOMFailed", err)
	}
}

func TestGenerate_RequiresCreatedAt(t *testing.T) {
	g := NewGenerator(nil)
	req := validRequest(t, t.TempDir())
	req.CreatedAt = time.Time{}

	_, err := g.Generate(context.Background(), req)
	if !errors.Is(err, core.ErrSBOMFailed) {
		t.Fatalf("Generate() error = %v, want wrapping core.ErrSBOMFailed", err)
	}
}

func TestGenerate_RespectsCancelledContext(t *testing.T) {
	g := NewGenerator(nil)
	req := validRequest(t, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := g.Generate(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v, want context.Canceled", err)
	}
}

func TestReadPackageIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"my-app","version":"2.3.4"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	name, version := readPackageIdentity(dir)
	if name != "my-app" || version != "2.3.4" {
		t.Fatalf("readPackageIdentity() = (%q, %q), want (my-app, 2.3.4)", name, version)
	}
}

func TestReadPackageIdentity_MissingFileIsNotAnError(t *testing.T) {
	name, version := readPackageIdentity(t.TempDir())
	if name != "" || version != "" {
		t.Fatalf("readPackageIdentity() = (%q, %q), want empty strings for a missing package.json", name, version)
	}
}

func TestRenderSPDXJSON_Deterministic(t *testing.T) {
	pkgs := []scannerutils.CatalogPackage{{Name: "a", Version: "1.0.0", Type: scannerutils.PkgTypeNpm}}
	createdAt := time.Unix(1700000000, 0).UTC().Format(time.RFC3339)
	id := contentIdentityUUID("app", "1.0.0", pkgs)

	out1, err := renderSPDXJSON("app", "1.0.0", id, createdAt, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := renderSPDXJSON("app", "1.0.0", id, createdAt, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if string(out1) != string(out2) {
		t.Fatalf("renderSPDXJSON produced different bytes for identical input:\n%s\n---\n%s", out1, out2)
	}

	var doc map[string]any
	if err := json.Unmarshal(out1, &doc); err != nil {
		t.Fatal(err)
	}
	ns, _ := doc["documentNamespace"].(string)
	if !strings.Contains(ns, "app-") {
		t.Errorf("documentNamespace not set sensibly: %q", ns)
	}
	ci, _ := doc["creationInfo"].(map[string]any)
	if ci["created"] != "2023-11-14T22:13:20Z" {
		t.Errorf("creationInfo.created = %v, want 2023-11-14T22:13:20Z", ci["created"])
	}
}

func TestRenderCycloneDXJSON_Deterministic(t *testing.T) {
	pkgs := []scannerutils.CatalogPackage{{Name: "a", Version: "1.0.0", Type: scannerutils.PkgTypeNpm}}
	createdAt := time.Unix(1700000000, 0).UTC().Format(time.RFC3339)
	id := contentIdentityUUID("app", "1.0.0", pkgs)

	out, err := renderCycloneDXJSON("app", "1.0.0", id, createdAt, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	serial, _ := doc["serialNumber"].(string)
	if !strings.HasPrefix(serial, "urn:uuid:") {
		t.Errorf("serialNumber not a urn:uuid: value: %q", serial)
	}
	md, _ := doc["metadata"].(map[string]any)
	if md["timestamp"] != "2023-11-14T22:13:20Z" {
		t.Errorf("metadata.timestamp = %v, want 2023-11-14T22:13:20Z", md["timestamp"])
	}
}

func TestContentIdentityUUID_Deterministic(t *testing.T) {
	pkgs := []scannerutils.CatalogPackage{
		{Name: "b", Version: "2.0.0", Type: scannerutils.PkgTypeNpm},
		{Name: "a", Version: "1.0.0", Type: scannerutils.PkgTypeNpm},
	}
	id1 := contentIdentityUUID("app", "1.0.0", pkgs)
	id2 := contentIdentityUUID("app", "1.0.0", []scannerutils.CatalogPackage{pkgs[1], pkgs[0]})
	if id1 != id2 {
		t.Errorf("contentIdentityUUID depends on package slice order: %v != %v", id1, id2)
	}

	id3 := contentIdentityUUID("app", "1.0.1", pkgs)
	if id1 == id3 {
		t.Error("contentIdentityUUID did not change when the version changed")
	}
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
