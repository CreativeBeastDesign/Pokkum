package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifierDependenciesAreInjectedAtEveryConstructionSite is the structural
// half of the fail-closed verification wiring.
//
// baseimage.NewResolver, provenance.NewResolver and remotecacheutils.New used to
// default their verifier dependencies by constructing concrete peer adapters
// inside themselves (cosign.NewSigner, sigstore.NewVerifier, dsse.NewSigner) —
// the adapter-imports-adapter edges internal/architecture_test.go's
// allowedAdapterAdapterEdges used to allowlist. Those defaults are gone, and a
// missing verifier now fails CLOSED at the point of use: it refuses rather than
// skipping the check.
//
// Failing closed is the correct behaviour, but on its own it converts a wiring
// mistake into a runtime error — a `pokkum verify` that refuses every signed
// image, discovered by a user rather than by CI. This test closes that gap at
// compile-test time instead: every construction site in the composition root
// must name the verifier options in its own argument list, so a newly added
// command cannot produce a resolver that silently cannot verify.
//
// It deliberately checks the argument list at each call site rather than
// asserting "all calls go through the two factory helpers": a helper is easy to
// bypass with a direct call, whereas the requirement enforced here is the actual
// invariant — this construction, wherever it is, injects what verification
// needs. The factory helpers satisfy it like any other site.
func TestVerifierDependenciesAreInjectedAtEveryConstructionSite(t *testing.T) {
	// required maps "<pkg>.<constructor>" to the option calls that must appear
	// among its arguments. Keep in sync with each adapter's Option doc comment.
	required := map[string][]string{
		"baseimage.NewResolver": {
			"baseimage.WithCosignSigner",
			"baseimage.WithKeylessVerifier",
		},
		"provenance.NewResolver": {
			"provenance.WithCosignSigner",
			"provenance.WithKeylessVerifier",
			"provenance.WithDSSESigner",
		},
		"remotecacheutils.New": {
			"remotecacheutils.WithCosignSigner",
			"remotecacheutils.WithKeylessVerifier",
		},
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/pokkum: %v", err)
	}

	fset := token.NewFileSet()
	found := map[string]int{}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			key := pkgIdent.Name + "." + sel.Sel.Name
			opts, watched := required[key]
			if !watched {
				return true
			}
			found[key]++

			// Render the whole call so an option passed as part of a nested
			// expression is still seen. Injection has to be visible in the
			// arguments of this call — a resolver assembled somewhere else and
			// assigned in afterwards would defeat the point of the check.
			var buf bytes.Buffer
			if err := printer.Fprint(&buf, fset, call); err != nil {
				t.Fatalf("render call in %s: %v", name, err)
			}
			rendered := buf.String()

			for _, opt := range opts {
				if !strings.Contains(rendered, opt) {
					pos := fset.Position(call.Pos())
					t.Errorf("[COMPOSITION ROOT] %s:%d: %s is constructed without %s.\n"+
						"This constructor has no default for that dependency on purpose — it used to build a "+
						"concrete peer adapter itself, which is the adapter-imports-adapter violation "+
						"internal/architecture_test.go forbids. A resolver built without it refuses to verify "+
						"(fail-closed) rather than skipping verification, so leaving it out does not disable "+
						"verification, it breaks it. Inject it here, or use the newBaseImageResolver / "+
						"newProvenanceResolver helper in build.go.",
						filepath.Base(pos.Filename), pos.Line, key, opt)
				}
			}
			return true
		})
	}

	// Guard against the check silently becoming vacuous: if these constructors
	// are renamed, moved, or the composition root stops calling them, every
	// assertion above passes by never running. The counts are lower bounds, not
	// exact expectations, so adding a command that resolves base images does
	// not need this test edited.
	for key := range required {
		if found[key] == 0 {
			t.Errorf("no %s call sites found in cmd/pokkum — this test is vacuous. "+
				"Was the constructor renamed, or the composition root restructured? "+
				"Update the `required` map above to match.", key)
		}
	}
}
