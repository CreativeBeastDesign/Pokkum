package sbom

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestGenerator_SPDX_ResolvesVersionsFromLockfileNotPackageJSONRange is a
// fail-first regression test for F6: a field test against a real SvelteKit
// app found 874 of 981 packages in the generated SPDX document carrying an
// unresolved package.json requirement string ("^10.9.1", "*", "~5.0.0",
// ">1.0.0") instead of the concrete version the project's own lockfile (and
// node_modules) actually resolved — making the SBOM useless for CVE
// matching, since a range cannot be checked against a vulnerability
// database the way a pinned version can.
//
// The fixture below mirrors that real project's shape closely enough to
// reproduce the bug two different ways at once:
//
//  1. bun.lock is written the way a real `bun install` actually writes it —
//     JSONC-flavored, with a trailing comma before the closing '}' of
//     "packages" — which the old scannerutils.ParseBunLock rejected with a
//     strict encoding/json error ("invalid character '}' ..."), silently
//     discarding every package the lockfile could have resolved.
//  2. node_modules contains an unrelated, deeply nested dependency
//     ("some-tool") whose OWN package.json happens to declare a stale,
//     unrelated version range for "svelte" as one of ITS OWN dependencies.
//     Since node_modules sorts before package.json in a directory listing
//     ("node_modules" < "package.json"), the old scanProject walked into it
//     and let that irrelevant nested declaration claim "svelte" in the
//     `seen` map before the project's own root package.json/node_modules
//     ever got a chance to resolve it correctly.
//
// Four requirement-string forms are covered, matching the four the field
// test actually found: caret (mermaid), wildcard (bcryptjs), tilde
// (svelte), and an already-exact pin (@sveltejs/kit) that must not be
// mistaken for a range.
func TestGenerator_SPDX_ResolvesVersionsFromLockfileNotPackageJSONRange(t *testing.T) {
	dir := t.TempDir()

	pkgJSON := `{
  "name": "field-test-app",
  "version": "1.0.0",
  "dependencies": {
    "mermaid": "^10.9.1",
    "bcryptjs": "*",
    "svelte": "~5.0.0",
    "@sveltejs/kit": "2.68.0"
  }
}`
	mustWriteFile(t, filepath.Join(dir, "package.json"), pkgJSON)

	// A real bun.lock has a trailing comma before "packages"'s closing brace
	// -- this is not contrived, every `bun install` writes it this way.
	bunLock := `{
  "lockfileVersion": 1,
  "packages": {
    "mermaid": ["mermaid@10.9.6", "", {}, "sha512-x"],
    "bcryptjs": ["bcryptjs@3.0.3", "", {}, "sha512-x"],
    "svelte": ["svelte@5.56.4", "", {}, "sha512-x"],
    "@sveltejs/kit": ["@sveltejs/kit@2.68.0", "", {}, "sha512-x"],
  }
}`
	mustWriteFile(t, filepath.Join(dir, "bun.lock"), bunLock)

	// Installed copies matching the lockfile, so a correct implementation
	// has two independent ways to resolve each package: the lockfile, and
	// node_modules/<pkg>/package.json's own "version" field.
	for name, version := range map[string]string{
		"mermaid":       "10.9.6",
		"bcryptjs":      "3.0.3",
		"svelte":        "5.56.4",
		"@sveltejs/kit": "2.68.0",
	} {
		mustMkdirAll(t, filepath.Join(dir, "node_modules", name))
		mustWriteFile(t, filepath.Join(dir, "node_modules", name, "package.json"),
			`{"name":"`+name+`","version":"`+version+`"}`)
	}

	// An unrelated, deeply nested dependency whose OWN package.json declares
	// a stale, DIFFERENT range for "svelte" as one of its own dependencies.
	// A correct implementation must never let this claim "svelte" ahead of
	// the project's real lockfile/node_modules resolution.
	mustMkdirAll(t, filepath.Join(dir, "node_modules", "some-tool"))
	mustWriteFile(t, filepath.Join(dir, "node_modules", "some-tool", "package.json"),
		`{"name":"some-tool","version":"1.0.0","dependencies":{"svelte":"^3.0.0"}}`)

	g := NewGenerator(nil)
	doc, err := g.Generate(context.Background(), ports.SBOMRequest{
		ProjectDir: dir,
		Format:     ports.SBOMFormatSPDXJSON,
		CreatedAt:  time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	var parsed struct {
		Packages []struct {
			Name        string `json:"name"`
			VersionInfo string `json:"versionInfo"`
			Comment     string `json:"comment"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(doc.Content, &parsed); err != nil {
		t.Fatalf("SPDX document is not valid JSON: %v\n%s", err, doc.Content)
	}

	want := map[string]string{
		"mermaid":       "10.9.6",
		"bcryptjs":      "3.0.3",
		"svelte":        "5.56.4",
		"@sveltejs/kit": "2.68.0",
	}
	got := make(map[string]string, len(parsed.Packages))
	for _, p := range parsed.Packages {
		got[p.Name] = p.VersionInfo
	}

	for name, wantVersion := range want {
		gotVersion, ok := got[name]
		if !ok {
			t.Errorf("package %q missing from SBOM entirely", name)
			continue
		}
		if gotVersion != wantVersion {
			t.Errorf("package %q: versionInfo = %q, want resolved version %q (the lockfile/node_modules value, not the package.json range)",
				name, gotVersion, wantVersion)
		}
	}
}

// TestGenerator_SPDX_UnresolvablePackageIsMarkedDistinctly proves that a
// dependency Pokkum genuinely cannot resolve (no lockfile entry, no
// node_modules install) is visibly flagged as unresolved in the SBOM,
// rather than silently emitting the raw package.json range as if it were
// a trustworthy, resolved version indistinguishable from a real one.
func TestGenerator_SPDX_UnresolvablePackageIsMarkedDistinctly(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "package.json"), `{
  "name": "unresolvable-app",
  "version": "1.0.0",
  "dependencies": {
    "totally-unresolvable-pkg": "^1.2.3"
  }
}`)
	// No lockfile, no node_modules: nothing on disk can resolve this.

	g := NewGenerator(nil)
	doc, err := g.Generate(context.Background(), ports.SBOMRequest{
		ProjectDir: dir,
		Format:     ports.SBOMFormatSPDXJSON,
		CreatedAt:  time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	var parsed struct {
		Packages []struct {
			Name        string `json:"name"`
			VersionInfo string `json:"versionInfo"`
			Comment     string `json:"comment"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(doc.Content, &parsed); err != nil {
		t.Fatalf("SPDX document is not valid JSON: %v", err)
	}

	var found bool
	for _, p := range parsed.Packages {
		if p.Name != "totally-unresolvable-pkg" {
			continue
		}
		found = true
		if p.Comment == "" {
			t.Errorf("unresolvable package %q has no distinguishing marker (comment); "+
				"a consumer cannot tell this range apart from a genuinely resolved version", p.Name)
		}
	}
	if !found {
		t.Fatal("totally-unresolvable-pkg missing from SBOM entirely")
	}
}
