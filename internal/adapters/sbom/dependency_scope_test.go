package sbom

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// scopedFixture writes a project directory whose package.json/bun.lock
// mirror the real-world shape that produced the SBOM/image npm-package gap
// this feature closes: a production dependency ("prod-pkg"), a
// devDependency ("dev-pkg") that itself pulls in a transitive,
// build-tool-only package ("dev-transitive", matching how a devDependency
// like vite pulls in esbuild) that the project never declares directly, and
// a production dependency ("prod-transitive-only") reachable ONLY via a
// transitive edge from a production root (matching how the real project
// this fix was measured against pulled in @sveltejs/kit through a
// production dependency's own package.json).
func scopedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "package.json"), `{
  "name": "scoped-app",
  "version": "1.0.0",
  "dependencies": { "prod-pkg": "1.0.0" },
  "devDependencies": { "dev-pkg": "1.0.0" }
}`)
	mustWriteFile(t, filepath.Join(dir, "bun.lock"), `{
  "lockfileVersion": 1,
  "workspaces": {
    "": {
      "name": "scoped-app",
      "dependencies": { "prod-pkg": "1.0.0" },
      "devDependencies": { "dev-pkg": "1.0.0" }
    }
  },
  "packages": {
    "prod-pkg": ["prod-pkg@1.0.0", "", {"dependencies": {"prod-transitive-only": "1.0.0"}}, "sha512-a"],
    "prod-transitive-only": ["prod-transitive-only@1.0.0", "", {}, "sha512-b"],
    "dev-pkg": ["dev-pkg@1.0.0", "", {"dependencies": {"dev-transitive": "1.0.0"}}, "sha512-c"],
    "dev-transitive": ["dev-transitive@1.0.0", "", {}, "sha512-d"]
  }
}`)
	return dir
}

// TestGenerate_ProductionScopeExcludesDevOnlyPackages is the central
// regression test for the SBOM/produced-image npm-package gap: it drives
// the real Generate() path (not scanProject or partitionByDependencyScope
// directly) against a fixture shaped like the real project the gap was
// measured against, and proves the default catalogue matches what
// `bun install --production` actually stages -- production packages and
// their transitive production-only dependencies present, devDependencies
// and packages reachable only through them absent -- in both SBOM formats.
func TestGenerate_ProductionScopeExcludesDevOnlyPackages(t *testing.T) {
	dir := scopedFixture(t)

	for _, tc := range []struct {
		name   string
		format ports.SBOMFormat
	}{
		{"spdx-json", ports.SBOMFormatSPDXJSON},
		{"cyclonedx-json", ports.SBOMFormatCycloneDXJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGenerator(nil)
			doc, err := g.Generate(context.Background(), ports.SBOMRequest{
				ProjectDir: dir,
				Format:     tc.format,
				CreatedAt:  time.Unix(0, 0).UTC(),
			})
			if err != nil {
				t.Fatalf("Generate() failed: %v", err)
			}

			for _, want := range []string{`"prod-pkg"`, `"prod-transitive-only"`} {
				if !bytes.Contains(doc.Content, []byte(want)) {
					t.Errorf("expected production package %s in the document; got:\n%s", want, doc.Content)
				}
			}
			for _, notWant := range []string{`"dev-pkg"`, `"dev-transitive"`} {
				if bytes.Contains(doc.Content, []byte(notWant)) {
					t.Errorf("dev-only package %s leaked into the document; got:\n%s", notWant, doc.Content)
				}
			}

			// PackageCount must reflect exactly the two kept packages, not
			// the four discovered -- a consumer relying on PackageCount
			// alone (the build summary does) must see the shipped count.
			if doc.PackageCount != 2 {
				t.Errorf("PackageCount = %d, want 2 (prod-pkg + prod-transitive-only)", doc.PackageCount)
			}
		})
	}
}

// TestGenerate_NpmScopeMetadataAlwaysPresent proves the transparency
// markers this feature adds -- SPDX's "annotations" entry and CycloneDX's
// "pokkum:npmDependencyScope"/"pokkum:npmDevDependenciesExcluded"
// properties -- are always present with the correct exclusion count,
// matching the same "always present, not conditional" idiom
// "pokkum:osPackagesScanned" already uses (see os_packages_test.go).
func TestGenerate_NpmScopeMetadataAlwaysPresent(t *testing.T) {
	dir := scopedFixture(t)
	g := NewGenerator(nil)

	t.Run("spdx-json", func(t *testing.T) {
		doc, err := g.Generate(context.Background(), ports.SBOMRequest{
			ProjectDir: dir, Format: ports.SBOMFormatSPDXJSON, CreatedAt: time.Unix(0, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("Generate() failed: %v", err)
		}
		var parsed struct {
			Annotations []struct {
				Comment string `json:"comment"`
			} `json:"annotations"`
		}
		if err := json.Unmarshal(doc.Content, &parsed); err != nil {
			t.Fatalf("unmarshal SPDX doc: %v", err)
		}
		if len(parsed.Annotations) != 1 {
			t.Fatalf("annotations = %d, want exactly 1", len(parsed.Annotations))
		}
		want := "pokkum:npmDependencyScope=production pokkum:npmDevDependenciesExcluded=2"
		if parsed.Annotations[0].Comment != want {
			t.Errorf("annotation comment = %q, want %q", parsed.Annotations[0].Comment, want)
		}
	})

	t.Run("cyclonedx-json", func(t *testing.T) {
		doc, err := g.Generate(context.Background(), ports.SBOMRequest{
			ProjectDir: dir, Format: ports.SBOMFormatCycloneDXJSON, CreatedAt: time.Unix(0, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("Generate() failed: %v", err)
		}
		var parsed cdxDoc
		if err := json.Unmarshal(doc.Content, &parsed); err != nil {
			t.Fatalf("unmarshal CycloneDX doc: %v", err)
		}
		if v, ok := parsed.property("pokkum:npmDependencyScope"); !ok || v != "production" {
			t.Errorf("pokkum:npmDependencyScope = (%q, %v), want (\"production\", true)", v, ok)
		}
		if v, ok := parsed.property("pokkum:npmDevDependenciesExcluded"); !ok || v != "2" {
			t.Errorf("pokkum:npmDevDependenciesExcluded = (%q, %v), want (\"2\", true)", v, ok)
		}
	})
}

// TestGenerate_UnknownScopeIsKeptAndMarked proves the other half of the
// design decision: a package whose production/development scope this
// generator could not determine (here, a bun.lock with no "workspaces"
// object, so ParseBunLock's reachability walk has no roots to walk from)
// is kept in the document -- never silently excluded -- and carries a
// distinguishing marker so a consumer can tell "excluded because confirmed
// dev-only" apart from "kept because scope is unknown", in both formats.
func TestGenerate_UnknownScopeIsKeptAndMarked(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "package.json"), `{"name":"unknown-scope-app","version":"1.0.0"}`)
	// No "workspaces" object: ParseBunLock cannot place this package via
	// its reachability walk, so it must come back scannerutils.ScopeUnknown.
	mustWriteFile(t, filepath.Join(dir, "bun.lock"),
		`{"lockfileVersion":1,"packages":{"mystery-pkg":["mystery-pkg@1.0.0","",{},"sha512-x"]}}`)

	g := NewGenerator(nil)

	t.Run("spdx-json", func(t *testing.T) {
		doc, err := g.Generate(context.Background(), ports.SBOMRequest{
			ProjectDir: dir, Format: ports.SBOMFormatSPDXJSON, CreatedAt: time.Unix(0, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("Generate() failed: %v", err)
		}
		if !bytes.Contains(doc.Content, []byte(`"mystery-pkg"`)) {
			t.Fatalf("unknown-scope package must be kept, not excluded; got:\n%s", doc.Content)
		}
		if !bytes.Contains(doc.Content, []byte(unknownScopeComment)) {
			t.Errorf("unknown-scope package missing its distinguishing comment; got:\n%s", doc.Content)
		}
		// Nothing was confidently excluded here (the one package's scope is
		// unknown, not development) -- the exclusion count must say so.
		if !bytes.Contains(doc.Content, []byte("pokkum:npmDevDependenciesExcluded=0")) {
			t.Errorf("expected pokkum:npmDevDependenciesExcluded=0; got:\n%s", doc.Content)
		}
	})

	t.Run("cyclonedx-json", func(t *testing.T) {
		doc, err := g.Generate(context.Background(), ports.SBOMRequest{
			ProjectDir: dir, Format: ports.SBOMFormatCycloneDXJSON, CreatedAt: time.Unix(0, 0).UTC(),
		})
		if err != nil {
			t.Fatalf("Generate() failed: %v", err)
		}
		var parsed cdxDoc
		if err := json.Unmarshal(doc.Content, &parsed); err != nil {
			t.Fatalf("unmarshal CycloneDX doc: %v", err)
		}
		found := false
		for _, c := range parsed.Components {
			if c.Name == "mystery-pkg" {
				found = true
			}
		}
		if !found {
			t.Fatalf("unknown-scope package must be kept, not excluded; components = %+v", parsed.Components)
		}
		if v, ok := parsed.property("pokkum:npmDevDependenciesExcluded"); !ok || v != "0" {
			t.Errorf("pokkum:npmDevDependenciesExcluded = (%q, %v), want (\"0\", true)", v, ok)
		}
	})
}

// TestGenerate_ProductionScopeAndOSPackagesCoexist is the hard-constraint
// regression test: production-only npm scoping must not regress the
// base-image OS-package coverage that landed immediately before this
// feature, and OS-package coverage must not exempt npm devDependencies from
// exclusion. Both directions are asserted against the same document.
func TestGenerate_ProductionScopeAndOSPackagesCoexist(t *testing.T) {
	dir := scopedFixture(t)
	img := realDistrolessDebian12Image(t)
	images := map[ports.Platform]v1.Image{ports.LinuxAMD64: img}

	g := NewGenerator(nil)
	doc, err := g.GenerateForImage(context.Background(), ports.SBOMRequest{
		ProjectDir: dir,
		Format:     ports.SBOMFormatSPDXJSON,
		CreatedAt:  time.Unix(0, 0).UTC(),
	}, images)
	if err != nil {
		t.Fatalf("GenerateForImage() failed: %v", err)
	}

	var parsed spdxDoc
	if err := json.Unmarshal(doc.Content, &parsed); err != nil {
		t.Fatalf("unmarshal SPDX doc: %v", err)
	}

	// Direction 1: OS packages still fully present (no regression of the
	// coverage that landed immediately before this feature).
	wantDebPurls := []string{
		"pkg:deb/debian/libssl3@3.0.20-1~deb12u2?arch=amd64",
		"pkg:deb/debian/libc6@2.36-9+deb12u14?arch=amd64",
		"pkg:deb/debian/base-files@12.4+deb12u15?arch=amd64",
	}
	got := map[string]bool{}
	for _, purl := range parsed.purls() {
		got[purl] = true
	}
	for _, want := range wantDebPurls {
		if !got[want] {
			t.Errorf("missing OS purl %q -- production npm scoping must not regress OS-package coverage", want)
		}
	}

	// Direction 2: npm devDependencies are still excluded even when OS
	// packages are present in the same document.
	if !got["pkg:npm/prod-pkg@1.0.0"] {
		t.Error("missing production npm purl pkg:npm/prod-pkg@1.0.0")
	}
	if bytes.Contains(doc.Content, []byte(`"dev-pkg"`)) {
		t.Error("dev-only npm package leaked into a document that also carries OS packages")
	}
}
