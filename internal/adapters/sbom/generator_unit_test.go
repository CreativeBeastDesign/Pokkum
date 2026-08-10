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

	"github.com/anchore/syft/syft/pkg"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignore"
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

	// The port's documented contract (internal/ports/sbom.go) is explicit:
	// "core.ErrInvalidSBOMFormat if Format is not Valid or is
	// SBOMFormatNone." core.BuildRequest.SBOM.Format defaulting to "none"
	// therefore means core itself must never call Generate — the
	// "disabled mode produces nothing and no error" behaviour lives at
	// that call site (see ports.SBOMFormat.Enabled), not inside Generate.
	// This test locks in what Generate itself does if it is ever called
	// with the disabled format anyway: it fails loudly rather than
	// silently returning an empty, misleading success.
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

func TestBuildExcludePatterns_DirectoryIsPrunedNotEnumerated(t *testing.T) {
	dir := t.TempDir()
	mustMkdirAll(t, filepath.Join(dir, "node_modules", ".cache"))
	mustWriteFile(t, filepath.Join(dir, "node_modules", ".cache", "a.tmp"), "x")
	mustWriteFile(t, filepath.Join(dir, "node_modules", ".cache", "b.tmp"), "x")
	mustMkdirAll(t, filepath.Join(dir, "src"))
	mustWriteFile(t, filepath.Join(dir, "src", "app.ts"), "x")
	mustWriteFile(t, filepath.Join(dir, "app.js.map"), "x")

	m, err := ignore.New(ignore.DefaultPatterns())
	if err != nil {
		t.Fatal(err)
	}

	patterns, err := buildExcludePatterns(context.Background(), dir, m)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"./node_modules/.cache": true,
		"./app.js.map":          true,
	}
	got := map[string]bool{}
	for _, p := range patterns {
		got[p] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("missing expected exclude pattern %q in %v", p, patterns)
		}
	}
	// The individual files inside node_modules/.cache must NOT show up as
	// their own patterns: the directory itself was pruned, so the walk
	// never descended into it.
	for _, p := range patterns {
		if strings.Contains(p, "a.tmp") || strings.Contains(p, "b.tmp") {
			t.Errorf("directory contents were individually enumerated instead of pruned: %v", patterns)
		}
	}
	if got["./src/app.ts"] || got["./src"] {
		t.Errorf("src/app.ts should not have been excluded: %v", patterns)
	}
}

func TestBuildExcludePatterns_NegationInProjectFile(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "debug.log"), "x")
	mustWriteFile(t, filepath.Join(dir, "important.log"), "x")

	m, err := ignore.New([]string{"*.log", "!important.log"})
	if err != nil {
		t.Fatal(err)
	}

	patterns, err := buildExcludePatterns(context.Background(), dir, m)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range patterns {
		got[p] = true
	}
	if !got["./debug.log"] {
		t.Errorf("debug.log should be excluded: %v", patterns)
	}
	if got["./important.log"] {
		t.Errorf("important.log should be spared by the negation: %v", patterns)
	}
}

func TestMakeDeterministic_SPDX_SameInputSameBytes(t *testing.T) {
	raw := []byte(`{"documentNamespace":"https://old","creationInfo":{"created":"2000-01-01T00:00:00Z","x":1}}`)
	pkgs := []pkg.Package{{Name: "a", Version: "1.0.0", Type: pkg.NpmPkg}}
	createdAt := time.Unix(1700000000, 0)

	out1, err := makeDeterministic(ports.SBOMFormatSPDXJSON, raw, "app", "1.0.0", createdAt, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := makeDeterministic(ports.SBOMFormatSPDXJSON, raw, "app", "1.0.0", createdAt, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if string(out1) != string(out2) {
		t.Fatalf("makeDeterministic produced different bytes for identical input:\n%s\n---\n%s", out1, out2)
	}

	var doc map[string]any
	if err := json.Unmarshal(out1, &doc); err != nil {
		t.Fatal(err)
	}
	ns, _ := doc["documentNamespace"].(string)
	if ns == "https://old" || !strings.Contains(ns, "app-") {
		t.Errorf("documentNamespace not rewritten sensibly: %q", ns)
	}
	ci, _ := doc["creationInfo"].(map[string]any)
	if ci["created"] != "2023-11-14T22:13:20Z" {
		t.Errorf("creationInfo.created = %v, want the CreatedAt-derived timestamp", ci["created"])
	}
}

func TestMakeDeterministic_SPDX_DifferentPackagesDifferentNamespace(t *testing.T) {
	raw := []byte(`{"documentNamespace":"https://old","creationInfo":{"created":"2000-01-01T00:00:00Z"}}`)
	createdAt := time.Unix(0, 0)

	out1, err := makeDeterministic(ports.SBOMFormatSPDXJSON, raw, "app", "1.0.0",
		createdAt, []pkg.Package{{Name: "a", Version: "1.0.0", Type: pkg.NpmPkg}})
	if err != nil {
		t.Fatal(err)
	}
	out2, err := makeDeterministic(ports.SBOMFormatSPDXJSON, raw, "app", "1.0.0",
		createdAt, []pkg.Package{{Name: "b", Version: "2.0.0", Type: pkg.NpmPkg}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out1) == string(out2) {
		t.Fatal("two different package sets produced the same document bytes/namespace")
	}
}

func TestMakeDeterministic_CycloneDX_RewritesSerialAndTimestamp(t *testing.T) {
	raw := []byte(`{"serialNumber":"urn:uuid:old","metadata":{"timestamp":"2000-01-01T00:00:00Z"}}`)
	pkgs := []pkg.Package{{Name: "a", Version: "1.0.0", Type: pkg.NpmPkg}}
	createdAt := time.Unix(1700000000, 0)

	out, err := makeDeterministic(ports.SBOMFormatCycloneDXJSON, raw, "app", "1.0.0", createdAt, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	serial, _ := doc["serialNumber"].(string)
	if !strings.HasPrefix(serial, "urn:uuid:") || serial == "urn:uuid:old" {
		t.Errorf("serialNumber not rewritten to a urn:uuid: value: %q", serial)
	}
	md, _ := doc["metadata"].(map[string]any)
	if md["timestamp"] != "2023-11-14T22:13:20Z" {
		t.Errorf("metadata.timestamp = %v, want the CreatedAt-derived timestamp", md["timestamp"])
	}
}

func TestContentIdentityUUID_Deterministic(t *testing.T) {
	pkgs := []pkg.Package{
		{Name: "b", Version: "2.0.0", Type: pkg.NpmPkg},
		{Name: "a", Version: "1.0.0", Type: pkg.NpmPkg},
	}
	id1 := contentIdentityUUID("app", "1.0.0", pkgs)
	// Reversed order: contentIdentityUUID sorts internally, so order of the
	// input slice must not matter.
	id2 := contentIdentityUUID("app", "1.0.0", []pkg.Package{pkgs[1], pkgs[0]})
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
