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

// mixedResolutionFixture writes a project directory containing one
// dependency the real resolution path CAN pin (an installed copy under
// node_modules, exactly as sveltekitutils.ResolveVersion consults) and one
// it genuinely CANNOT (a caret range with no lockfile entry and no
// node_modules install) -- driven entirely through package.json/node_modules
// on disk, the same inputs g.Generate itself reads, rather than a
// hand-constructed scannerutils.CatalogPackage that could bypass the real
// resolution logic. A single-item fixture cannot prove the marker is
// per-package (mem:self_review_checklist row 3): only a mix in which one
// sibling resolves and the other doesn't can show the flag tracks each
// package independently rather than, say, being set unconditionally or
// never at all.
func mixedResolutionFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "package.json"), `{
  "name": "mixed-resolution-app",
  "version": "1.0.0",
  "dependencies": {
    "resolved-pkg": "^2.0.0",
    "totally-unresolvable-pkg": "^1.2.3"
  }
}`)
	// resolved-pkg has a real installed copy in node_modules, so
	// sveltekitutils.ResolveVersion pins it to the concrete "2.3.4" instead
	// of falling back to the "^2.0.0" declared range.
	mustMkdirAll(t, filepath.Join(dir, "node_modules", "resolved-pkg"))
	mustWriteFile(t, filepath.Join(dir, "node_modules", "resolved-pkg", "package.json"),
		`{"name":"resolved-pkg","version":"2.3.4"}`)
	// totally-unresolvable-pkg has no lockfile entry and no node_modules
	// install anywhere: nothing on disk can resolve it.
	return dir
}

// TestGenerator_SPDX_MixedFixture_OnlyUnresolvedPackageIsMarked drives the
// real generator against a fixture containing both a resolved and an
// unresolvable package and asserts the SPDX "comment" marker appears on
// exactly the unresolvable one, matching the exact sentinel text the
// generator defines (unresolvedVersionComment), not merely a non-empty
// string.
func TestGenerator_SPDX_MixedFixture_OnlyUnresolvedPackageIsMarked(t *testing.T) {
	dir := mixedResolutionFixture(t)

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

	byName := make(map[string]string, len(parsed.Packages))
	for _, p := range parsed.Packages {
		byName[p.Name] = p.Comment
	}

	resolvedComment, ok := byName["resolved-pkg"]
	if !ok {
		t.Fatal("resolved-pkg missing from SBOM entirely")
	}
	if resolvedComment != "" {
		t.Errorf("resolved-pkg: comment = %q, want empty -- a genuinely resolved package must not carry the unresolved marker", resolvedComment)
	}

	unresolvedComment, ok := byName["totally-unresolvable-pkg"]
	if !ok {
		t.Fatal("totally-unresolvable-pkg missing from SBOM entirely")
	}
	if unresolvedComment != unresolvedVersionComment {
		t.Errorf("totally-unresolvable-pkg: comment = %q, want %q", unresolvedComment, unresolvedVersionComment)
	}
}

// TestGenerator_CycloneDX_MixedFixture_OnlyUnresolvedPackageIsMarked is the
// CycloneDX-format sibling of the SPDX mixed-fixture test above: the
// "pokkum:versionResolved" component property must appear (value "false")
// on exactly the unresolvable package and be absent from the resolved one.
// Before this test, `grep -rn 'unresolvedVersionComment\|versionResolved'
// internal/adapters/sbom/*_test.go` matched nothing for the CycloneDX
// property at all -- the marker had zero coverage in this format.
func TestGenerator_CycloneDX_MixedFixture_OnlyUnresolvedPackageIsMarked(t *testing.T) {
	dir := mixedResolutionFixture(t)

	g := NewGenerator(nil)
	doc, err := g.Generate(context.Background(), ports.SBOMRequest{
		ProjectDir: dir,
		Format:     ports.SBOMFormatCycloneDXJSON,
		CreatedAt:  time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	var parsed struct {
		Components []struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			Properties []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"properties"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc.Content, &parsed); err != nil {
		t.Fatalf("CycloneDX document is not valid JSON: %v", err)
	}

	byName := make(map[string][]struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}, len(parsed.Components))
	for _, c := range parsed.Components {
		byName[c.Name] = c.Properties
	}

	resolvedProps, ok := byName["resolved-pkg"]
	if !ok {
		t.Fatal("resolved-pkg missing from SBOM entirely")
	}
	for _, prop := range resolvedProps {
		if prop.Name == "pokkum:versionResolved" {
			t.Errorf("resolved-pkg: unexpected pokkum:versionResolved property (value %q) -- a genuinely resolved package must not carry the unresolved marker", prop.Value)
		}
	}

	unresolvedProps, ok := byName["totally-unresolvable-pkg"]
	if !ok {
		t.Fatal("totally-unresolvable-pkg missing from SBOM entirely")
	}
	var foundMarker bool
	for _, prop := range unresolvedProps {
		if prop.Name == "pokkum:versionResolved" {
			foundMarker = true
			if prop.Value != "false" {
				t.Errorf("totally-unresolvable-pkg: pokkum:versionResolved value = %q, want \"false\"", prop.Value)
			}
		}
	}
	if !foundMarker {
		t.Errorf("totally-unresolvable-pkg: no pokkum:versionResolved property found; "+
			"a consumer cannot tell this range apart from a genuinely resolved version. properties = %#v", unresolvedProps)
	}
}
