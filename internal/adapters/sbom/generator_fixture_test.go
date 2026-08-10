package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// fixtureDir is testdata/fixtures/sveltekit-basic: a real SvelteKit project
// with a committed bun.lock and package.json. Only the two tests in this
// file run a real syft scan against it — syft's own initialization is slow
// enough (per the W12 brief) that every other case in this package is a
// pure-Go unit test against buildExcludePatterns/makeDeterministic/etc.
// directly, not a full Generate call.
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bun.lock")); err != nil {
		t.Fatalf("fixture missing bun.lock at %s: %v", dir, err)
	}
	return dir
}

// TestGenerator_SPDX_ReproducibilityAndPackages is the headline test W12
// asks for: generating the same SBOM twice must produce byte-identical
// output, and the document must actually list dependencies read from the
// fixture's committed bun.lock -- not a silently empty catalog.
func TestGenerator_SPDX_ReproducibilityAndPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real syft scan in -short mode")
	}

	g := NewGenerator(nil)
	req := ports.SBOMRequest{
		ProjectDir: fixtureDir(t),
		Format:     ports.SBOMFormatSPDXJSON,
		CreatedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	doc1, err := g.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("first Generate() failed: %v", err)
	}
	doc2, err := g.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("second Generate() failed: %v", err)
	}

	if !bytes.Equal(doc1.Content, doc2.Content) {
		t.Fatalf("SBOM bytes differ across two generations of the same input (len %d vs %d)",
			len(doc1.Content), len(doc2.Content))
	}
	if doc1.SHA256 != doc2.SHA256 {
		t.Fatalf("SHA256 differs across two generations: %s vs %s", doc1.SHA256, doc2.SHA256)
	}
	if doc1.SHA256 == "" {
		t.Fatal("SHA256 was not computed")
	}
	t.Logf("SPDX-JSON reproducibility: both generations hashed to sha256:%s (%d bytes, %d packages)",
		doc1.SHA256, len(doc1.Content), doc1.PackageCount)

	if doc1.MediaType != ports.MediaTypeSPDXJSON {
		t.Errorf("MediaType = %q, want %q", doc1.MediaType, ports.MediaTypeSPDXJSON)
	}
	if doc1.PackageCount == 0 {
		t.Fatal("PackageCount is zero -- the scan silently produced an empty catalog")
	}

	// The fixture's package.json/bun.lock declare these; assert a couple of
	// them actually made it into the document, so an empty-but-well-formed
	// SBOM can't pass this test by accident.
	for _, want := range []string{"svelte", "vite", "@sveltejs/kit", "@jesterkit/exe-sveltekit"} {
		if !bytes.Contains(doc1.Content, []byte(`"`+want+`"`)) {
			t.Errorf("expected package %q from the fixture's bun.lock to appear in the SPDX document", want)
		}
	}

	var parsed map[string]any
	if err := json.Unmarshal(doc1.Content, &parsed); err != nil {
		t.Fatalf("SPDX document is not valid JSON: %v", err)
	}
	if parsed["spdxVersion"] == nil {
		t.Error("SPDX document missing spdxVersion")
	}
	created, _ := parsed["creationInfo"].(map[string]any)["created"].(string)
	if created != "2024-01-01T00:00:00Z" {
		t.Errorf("creationInfo.created = %q, want the CreatedAt from the request", created)
	}
}

// TestGenerator_CycloneDX_ValidAndListsPackages covers the CycloneDX-JSON
// path against the same fixture: valid output, and the same fixture
// packages present.
func TestGenerator_CycloneDX_ValidAndListsPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real syft scan in -short mode")
	}

	g := NewGenerator(nil)
	req := ports.SBOMRequest{
		ProjectDir: fixtureDir(t),
		Format:     ports.SBOMFormatCycloneDXJSON,
		CreatedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	doc, err := g.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}
	if doc.MediaType != ports.MediaTypeCycloneDXJSON {
		t.Errorf("MediaType = %q, want %q", doc.MediaType, ports.MediaTypeCycloneDXJSON)
	}

	var parsed map[string]any
	if err := json.Unmarshal(doc.Content, &parsed); err != nil {
		t.Fatalf("CycloneDX document is not valid JSON: %v", err)
	}
	if parsed["bomFormat"] != "CycloneDX" {
		t.Errorf("bomFormat = %v, want CycloneDX", parsed["bomFormat"])
	}
	serial, _ := parsed["serialNumber"].(string)
	if serial == "" {
		t.Error("serialNumber is empty")
	}
	if doc.PackageCount == 0 {
		t.Fatal("PackageCount is zero -- the scan silently produced an empty catalog")
	}

	for _, want := range []string{"svelte", "vite"} {
		if !bytes.Contains(doc.Content, []byte(`"`+want+`"`)) {
			t.Errorf("expected package %q from the fixture's bun.lock to appear in the CycloneDX document", want)
		}
	}
}

// TestGenerator_DisabledFormatIsSkippedByCallers documents the "disabled
// mode produces nothing and no error" contract from the W12 brief at the
// level it actually applies: a caller must check Format.Enabled() before
// ever calling Generate. See TestGenerate_RejectsDisabledFormat in
// generator_unit_test.go for what Generate itself does if that check is
// skipped -- it fails loudly, per the ports.SBOMGenerator doc's explicit
// "core.ErrInvalidSBOMFormat ... if Format ... is SBOMFormatNone".
func TestGenerator_DisabledFormatIsSkippedByCallers(t *testing.T) {
	req := ports.SBOMRequest{
		ProjectDir: fixtureDir(t),
		Format:     ports.SBOMFormatNone,
		CreatedAt:  time.Unix(0, 0),
	}
	if req.Format.Enabled() {
		t.Fatal("SBOMFormatNone must report Enabled() == false")
	}
	// A caller that honours Enabled() never calls Generate at all, so no
	// SBOM and no error is exactly what happens -- nothing to assert
	// beyond the Enabled() gate itself, since there is nothing left to
	// call.
}

// TestGenerator_PokkumIgnore_ExcludesFile proves an excluded file genuinely
// never reaches the SBOM: a package.json nested under a directory the
// project's own .pokkumignore excludes must not produce a cataloged
// package, while a sibling that is not excluded must.
func TestGenerator_PokkumIgnore_ExcludesFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real syft scan in -short mode")
	}

	// syft's default cataloger selection for a plain directory source
	// catalogs packages from LOCKFILES (javascript-lock-cataloger, tagged
	// pkgcataloging.DirectoryTag), not from bare package.json files
	// (javascript-package-cataloger is tagged installed/image only, i.e.
	// it activates for a container image scan, not a directory one) --
	// confirmed by reading internal/task/package_tasks.go. So the fixture
	// for this test needs a lockfile in each subtree, matching how a real
	// project's dependencies actually get catalogued.
	bunLock := func(pkgName string) string {
		return `{"lockfileVersion":1,"packages":{"` + pkgName + `":["` + pkgName + `@1.0.0","",{},"sha512-x"]}}`
	}

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "package.json"), `{"name":"root-project","version":"0.0.1"}`)
	mustMkdirAll(t, filepath.Join(dir, "included"))
	mustWriteFile(t, filepath.Join(dir, "included", "bun.lock"), bunLock("pokkum-included-pkg"))
	mustMkdirAll(t, filepath.Join(dir, "excluded"))
	mustWriteFile(t, filepath.Join(dir, "excluded", "bun.lock"), bunLock("pokkum-excluded-pkg"))
	mustWriteFile(t, filepath.Join(dir, ".pokkumignore"), "excluded/\n")

	g := NewGenerator(nil)
	doc, err := g.Generate(context.Background(), ports.SBOMRequest{
		ProjectDir: dir,
		Format:     ports.SBOMFormatSPDXJSON,
		CreatedAt:  time.Unix(0, 0),
	})
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	if !bytes.Contains(doc.Content, []byte("pokkum-included-pkg")) {
		t.Error("expected the non-excluded package to appear in the SBOM")
	}
	if bytes.Contains(doc.Content, []byte("pokkum-excluded-pkg")) {
		t.Error(".pokkumignore-excluded package leaked into the SBOM")
	}
}
