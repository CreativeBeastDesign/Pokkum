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
	id := contentIdentityUUID("app", "1.0.0", pkgs, "", "")

	out1, err := renderSPDXJSON("app", "1.0.0", id, createdAt, pkgs, "", "")
	if err != nil {
		t.Fatal(err)
	}
	out2, err := renderSPDXJSON("app", "1.0.0", id, createdAt, pkgs, "", "")
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
	id := contentIdentityUUID("app", "1.0.0", pkgs, "", "")

	out, err := renderCycloneDXJSON("app", "1.0.0", id, createdAt, pkgs, "", "")
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
	id1 := contentIdentityUUID("app", "1.0.0", pkgs, "", "")
	id2 := contentIdentityUUID("app", "1.0.0", []scannerutils.CatalogPackage{pkgs[1], pkgs[0]}, "", "")
	if id1 != id2 {
		t.Errorf("contentIdentityUUID depends on package slice order: %v != %v", id1, id2)
	}

	id3 := contentIdentityUUID("app", "1.0.1", pkgs, "", "")
	if id1 == id3 {
		t.Error("contentIdentityUUID did not change when the version changed")
	}

	id4 := contentIdentityUUID("app", "1.0.0", pkgs, "1.2.2", "abc123")
	if id1 == id4 {
		t.Error("contentIdentityUUID did not change when BunVersion/BunSHA256 changed")
	}
}

// TestRenderCycloneDXJSON_BunComponent is PR-7's regression guard: Bun's
// SHA-256 was recorded in SLSA provenance but never appeared in the SBOM at
// all, even though it is the single largest component shipped in a layered
// image. Empty pkgs on purpose — the Bun component must not depend on the
// project having any npm dependencies at all.
func TestRenderCycloneDXJSON_BunComponent(t *testing.T) {
	createdAt := time.Unix(1700000000, 0).UTC().Format(time.RFC3339)
	id := contentIdentityUUID("app", "1.0.0", nil, "1.2.2", "deadbeef")

	out, err := renderCycloneDXJSON("app", "1.0.0", id, createdAt, nil, "1.2.2", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Components []map[string]any `json:"components"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Components) != 1 {
		t.Fatalf("components = %d, want 1 (bun only, no npm packages)", len(doc.Components))
	}
	bun := doc.Components[0]
	if bun["type"] != "application" {
		t.Errorf("bun component type = %v, want application (not library, which is reserved for npm packages)", bun["type"])
	}
	if bun["name"] != "bun" || bun["version"] != "1.2.2" {
		t.Errorf("bun component name/version = %v/%v, want bun/1.2.2", bun["name"], bun["version"])
	}
	if bun["purl"] != "pkg:generic/bun@1.2.2" {
		t.Errorf("bun component purl = %v, want pkg:generic/bun@1.2.2 (matching the SLSA provenance convention)", bun["purl"])
	}
	hashes, _ := bun["hashes"].([]any)
	if len(hashes) != 1 {
		t.Fatalf("bun component hashes = %v, want exactly one entry", bun["hashes"])
	}
	h, _ := hashes[0].(map[string]any)
	if h["alg"] != "SHA-256" || h["content"] != "deadbeef" {
		t.Errorf("bun component hash = %v, want {alg: SHA-256, content: deadbeef}", h)
	}
}

// TestRenderCycloneDXJSON_NoBunComponentWhenVersionEmpty guards the other
// half of the same feature: a static-strategy build (or any build where Bun
// resolution didn't happen) must not get a fabricated Bun component with an
// empty version.
func TestRenderCycloneDXJSON_NoBunComponentWhenVersionEmpty(t *testing.T) {
	createdAt := time.Unix(1700000000, 0).UTC().Format(time.RFC3339)
	pkgs := []scannerutils.CatalogPackage{{Name: "a", Version: "1.0.0", Type: scannerutils.PkgTypeNpm}}
	id := contentIdentityUUID("app", "1.0.0", pkgs, "", "")

	out, err := renderCycloneDXJSON("app", "1.0.0", id, createdAt, pkgs, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components []map[string]any `json:"components"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Components {
		if c["name"] == "bun" {
			t.Fatalf("found a bun component with BunVersion empty: %v", c)
		}
	}
	if len(doc.Components) != 1 {
		t.Fatalf("components = %d, want exactly 1 (the npm package, no bun component)", len(doc.Components))
	}
}

// TestRenderSPDXJSON_BunComponent mirrors TestRenderCycloneDXJSON_BunComponent
// for the SPDX renderer: a bun package entry, DEPENDS_ON the root package,
// carrying the purl and checksum.
func TestRenderSPDXJSON_BunComponent(t *testing.T) {
	createdAt := time.Unix(1700000000, 0).UTC().Format(time.RFC3339)
	id := contentIdentityUUID("app", "1.0.0", nil, "1.2.2", "deadbeef")

	out, err := renderSPDXJSON("app", "1.0.0", id, createdAt, nil, "1.2.2", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Packages []map[string]any `json:"packages"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}

	var bun map[string]any
	for _, p := range doc.Packages {
		if p["name"] == "bun" {
			bun = p
			break
		}
	}
	if bun == nil {
		t.Fatalf("no bun package found among %d packages", len(doc.Packages))
	}
	if bun["versionInfo"] != "1.2.2" {
		t.Errorf("bun versionInfo = %v, want 1.2.2", bun["versionInfo"])
	}
	refs, _ := bun["externalRefs"].([]any)
	if len(refs) != 1 {
		t.Fatalf("bun externalRefs = %v, want exactly one purl ref", bun["externalRefs"])
	}
	ref, _ := refs[0].(map[string]any)
	if ref["referenceLocator"] != "pkg:generic/bun@1.2.2" {
		t.Errorf("bun purl = %v, want pkg:generic/bun@1.2.2", ref["referenceLocator"])
	}
	checksums, _ := bun["checksums"].([]any)
	if len(checksums) != 1 {
		t.Fatalf("bun checksums = %v, want exactly one entry", bun["checksums"])
	}
	cs, _ := checksums[0].(map[string]any)
	if cs["algorithm"] != "SHA256" || cs["checksumValue"] != "deadbeef" {
		t.Errorf("bun checksum = %v, want {algorithm: SHA256, checksumValue: deadbeef}", cs)
	}
}

// TestGenerate_BunComponent_PackageCount is an end-to-end check that
// Generate itself (not just the render functions in isolation) wires
// req.BunVersion/BunSHA256 through, and that PackageCount reflects the
// added component.
func TestGenerate_BunComponent_PackageCount(t *testing.T) {
	g := NewGenerator(nil)
	dir := t.TempDir()
	req := validRequest(t, dir)
	req.Format = ports.SBOMFormatCycloneDXJSON
	req.BunVersion = "1.2.2"
	req.BunSHA256 = "deadbeef"

	doc, err := g.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if doc.PackageCount != 1 {
		t.Errorf("PackageCount = %d, want 1 (bun only, no npm deps in this fixture)", doc.PackageCount)
	}
	if !strings.Contains(string(doc.Content), `"pkg:generic/bun@1.2.2"`) {
		t.Errorf("rendered document does not contain the bun purl:\n%s", doc.Content)
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
