package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Environment variable <-> Vocabulary.md consistency.
//
// Vocabulary.md §1 states the load-bearing convention directly: "POKKUM_
// prefix for every environment variable" Pokkum itself defines. Nothing
// mechanically checked that until now, and it already drifted twice for
// real: POKKUM_SIGNING_PUBKEY and POKKUM_BASE_IMAGE_PUBKEY were both read by
// code (internal/adapters/provenance/resolver.go,
// internal/adapters/baseimage/resolver.go) with no corresponding row in this
// file, discovered only by manual review. This test makes that class of gap
// a build failure instead.
//
// Enumeration approach: AST, not grep/`--help`. Every POKKUM_-prefixed
// string literal is collected from every non-test .go file in the module —
// both `os.Getenv("POKKUM_...")` call arguments and `const
// envFoo = "POKKUM_..."` declarations (internal/ports/packager.go's Env*
// constants, and the two supervisor binaries' independently duplicated
// copies of the same literal names — see supervisor/cmd/pokkum-init/config.go
// and supervisor/cmd/pokkum-static/config.go's doc comments on why they
// duplicate rather than import internal/ports).
//
// This is AST-based rather than a raw grep specifically because a raw grep
// over file text also matches comments — e.g. internal/adapters/k8s/resolver.go
// has a comment mentioning "POKKUM_PROBE_PORT" in prose, which is not itself a
// place the env var is read or declared. go/ast's node walk only visits
// actual string-literal *expressions*, not comment text, so it does not need
// a separate carve-out for that.
//
// There is no --help-based alternative here at all: `pokkum --help` and
// every subcommand's --help render flags, never environment variables, so
// the built-binary approach used for flags (see flags_docs_test.go) has
// nothing to inspect for this direction.

var pokkumEnvLiteralRe = regexp.MustCompile(`^POKKUM_[A-Z0-9_]+$`)

// skipDirNames mirrors internal/architecture_test.go's own walk exclusions
// (testdata/node_modules/.git/.pokkum), so this scan and that one treat the
// repo tree the same way.
var skipDirNames = map[string]bool{
	"testdata":     true,
	"node_modules": true,
	".git":         true,
	".pokkum":      true,
}

// collectPokkumEnvLiterals walks repoRoot for every non-test .go file and
// returns the set of distinct POKKUM_-prefixed string literals appearing as
// an actual string-literal expression (not comment text), along with one
// example file:line each for actionable error messages.
func collectPokkumEnvLiterals(t *testing.T, repoRoot string) map[string]string {
	t.Helper()
	found := map[string]string{}

	walkErr := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDirNames[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		node, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Errorf("failed to parse %s: %v", path, parseErr)
			return nil
		}
		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		relPath = filepath.ToSlash(relPath)

		ast.Inspect(node, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil || !pokkumEnvLiteralRe.MatchString(val) {
				return true
			}
			if _, exists := found[val]; !exists {
				pos := fset.Position(lit.Pos())
				found[val] = relPath + ":" + strconv.Itoa(pos.Line)
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("failed walking %s: %v", repoRoot, walkErr)
	}
	return found
}

// testOnlyEnvVarsInDocs are POKKUM_-prefixed variables Vocabulary.md names
// deliberately in order to say they are NOT part of the CLI or runtime
// surface. They are read only by _test.go files, never by the pokkum binary,
// .pokkum.yaml, or a produced image — so they correctly fail the
// production-literal check above, and documenting the boundary is worth more
// than the silence that omitting them would buy.
//
// Narrow and justified, matching flags_docs_test.go's sibling allowlists. An
// entry here is a claim that the variable is test-only; if one ever starts
// being read by production code, delete its entry rather than widening this.
var testOnlyEnvVarsInDocs = map[string]string{
	"POKKUM_REQUIRE_RUNTIME_SMOKE":   `tests/integration: turns the runtime smoke tests' environmental skips into hard failures where the preconditions are guaranteed. Set by CI, never read by the binary.`,
	"POKKUM_REQUIRE_MINIFIED_CORPUS": `internal/adapters/secretguard: turns an absent real-minified-bundle corpus into a hard failure in jobs that build the fixtures. Set by CI, never read by the binary.`,
}

// allowlistedInternalEnvVars are real POKKUM_-prefixed literals that are
// deliberately NOT documented as user-facing environment variables in
// Vocabulary.md, each with its own justification. Kept narrow and
// per-entry-justified per this task's own instructions — never a blanket
// category skip.
var allowlistedInternalEnvVars = map[string]string{
	"POKKUM_AUTO_INJECT": "internal/adapters/sveltekitutils/injector.go's BuildEnv sets this into a " +
		"bunx vite build subprocess's environment, but nothing reads it anywhere in the tree — confirmed " +
		"by grepping the whole repo (including .ts/.js) for the literal. Lessons.md's 2026-08-17 " +
		"'Zero-Config Auto-Injection had no effect on real builds' entry documents this exact write as " +
		"one of two compounding causes ('POKKUM_AUTO_INJECT env var was... write-only, read by nothing'); " +
		"the feature was subsequently redesigned around Option C (detect-and-fail-clearly) and this write " +
		"was never removed. It is dead code, not a real configuration input — flagged here rather than " +
		"documented as if it were a working env var, since documenting it would itself be a false claim.",
	"POKKUM_HERMETIC_REEXEC_TARGET": "internal/adapters/bunexec/hermetic_reexec_linux.go's " +
		"HermeticReexecEnvVar: a private process-to-itself channel the /proc/self/exe __hermetic-reexec " +
		"re-exec (cmd/pokkum/hermetic_reexec.go, itself Hidden: true) uses to pass its real target argv. " +
		"Never meant to be set by a person; excluding it here mirrors that command's own exclusion from " +
		"--help.",
	"POKKUM_HERMETIC_REEXEC_MASK_PATHS_TEST_OVERRIDE": "same file's hermeticMaskPathsTestOverrideEnvVar " +
		"— an explicit, narrow, test-only channel (mem:self_review_checklist row 19's origin incident) " +
		"for redirecting the real bind-mask target under test. Not a real configuration surface; its own " +
		"name says so.",
}

// TestEnvVarsDocumentedInVocabulary is Direction 1: every real POKKUM_*
// environment variable literal found in production code must be documented
// in Vocabulary.md, unless explicitly and individually allowlisted above.
func TestEnvVarsDocumentedInVocabulary(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	literals := collectPokkumEnvLiterals(t, repoRoot)
	doc := readVocabulary(t)

	missing := make([]string, 0)
	for name, loc := range literals {
		if _, allowed := allowlistedInternalEnvVars[name]; allowed {
			continue
		}
		if strings.Contains(doc, name) {
			continue
		}
		missing = append(missing, name+" (first seen at "+loc+")")
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("[DOCS DRIFT] %d POKKUM_* environment variable(s) are read/declared in production code "+
			"but not documented anywhere in Vocabulary.md:\n  %s\n"+
			"This is the exact class of gap this repo already shipped twice (POKKUM_SIGNING_PUBKEY, "+
			"POKKUM_BASE_IMAGE_PUBKEY, both found only by manual review). Document each in the relevant "+
			"section of Vocabulary.md, or — if it is genuinely internal/not user-facing — add it to "+
			"allowlistedInternalEnvVars in envvar_docs_test.go with a justification.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestEnvVarsExistInCodeFromVocabulary is Direction 2: every POKKUM_* token
// Vocabulary.md mentions must correspond to a real literal somewhere in
// production code — the mirror-image check, so a renamed or removed env var
// left behind in prose (the same shape of bug as action.yml's stale
// --repo/--tag) is also a test failure, not just a docs-review finding.
func TestEnvVarsExistInCodeFromVocabulary(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	literals := collectPokkumEnvLiterals(t, repoRoot)
	doc := readVocabulary(t)

	docTokens := map[string]bool{}
	for _, m := range regexp.MustCompile(`POKKUM_[A-Z0-9_]+`).FindAllString(doc, -1) {
		docTokens[m] = true
	}

	stale := make([]string, 0)
	for token := range docTokens {
		if _, ok := literals[token]; ok {
			continue
		}
		if _, ok := testOnlyEnvVarsInDocs[token]; ok {
			continue
		}
		stale = append(stale, token)
	}
	sort.Strings(stale)

	if len(stale) > 0 {
		t.Errorf("[DOCS DRIFT] Vocabulary.md mentions %d POKKUM_* token(s) that do not appear as an actual "+
			"string literal anywhere in production Go code:\n  %s\n"+
			"Either the env var was renamed/removed and the doc is stale (fix Vocabulary.md), or the name "+
			"was mistyped.", len(stale), strings.Join(stale, "\n  "))
	}
}
