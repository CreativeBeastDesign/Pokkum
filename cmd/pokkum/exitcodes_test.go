package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// CLI exit code <-> documentation parity.
//
// Why this exists: `pokkum verify` distinguishes "the image was checked and
// did not pass" (exit 1) from "the check could not be performed at all"
// (exit 2). That distinction is the entire reason a CI gate can tell a
// tampered image from a broken verifier — and it was implemented, correct,
// and documented nowhere, so no caller could have relied on it.
//
// An undocumented exit code is worse than a missing feature: a caller that
// gates on `!= 0` silently collapses the two, and nothing tells them they
// have. This guard is the same discipline flags_docs_test.go applies to
// flags — every code the CLI can actually return must appear in the table
// that promises what it means.
//
// Scope: literal exits under cmd/pokkum. The one non-literal site is
// apply.go's kubectl passthrough, asserted separately below since a scanner
// cannot enumerate what kubectl might return.
// ---------------------------------------------------------------------------

// documentedExitCodes is the section of Vocabulary.md and the guide topic that
// must mention each code. Both are checked: Vocabulary.md is the human
// reference, `pokkum guide exit-codes` is what ships to someone who does not
// have this repository.
const (
	vocabExitSection = "## 18d. CLI Exit Codes"
	guideExitTopic   = "exit-codes"
)

// literalExitCodes AST-scans cmd/pokkum for os.Exit(N) and exitFunc(N) with an
// integer literal argument, returning the distinct set of N.
func literalExitCodes(t *testing.T) map[int][]string {
	t.Helper()
	found := map[int][]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("[TEST SETUP] reading cmd/pokkum: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("[TEST SETUP] parsing %s: %v", name, perr)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			var fn string
			switch f := call.Fun.(type) {
			case *ast.Ident:
				fn = f.Name
			case *ast.SelectorExpr:
				if pkg, ok := f.X.(*ast.Ident); ok {
					fn = pkg.Name + "." + f.Sel.Name
				}
			}
			if fn != "os.Exit" && fn != "exitFunc" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				return true // e.g. apply.go's kubectl passthrough; asserted separately
			}
			code, cerr := strconv.Atoi(lit.Value)
			if cerr != nil {
				return true
			}
			site := name + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
			found[code] = append(found[code], site)
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("[TEST SETUP] scanned zero .go files; the walk is broken")
	}
	return found
}

func TestExitCodesAreDocumented(t *testing.T) {
	codes := literalExitCodes(t)

	// Premise checks: a scan finding nothing, or missing the codes we know
	// exist, would let this test pass while verifying nothing.
	if len(codes) == 0 {
		t.Fatal("[TEST SETUP] found no literal exit codes; the AST scan has gone blind")
	}
	for _, must := range []int{1, 2} {
		if len(codes[must]) == 0 {
			t.Fatalf("[TEST SETUP] scan did not find exit code %d, which is known to exist "+
				"(main.go exits 1, verify.go exits 2) — the scan is wrong", must)
		}
	}

	vocab, err := os.ReadFile(filepath.Join("..", "..", "Vocabulary.md"))
	if err != nil {
		t.Fatalf("reading Vocabulary.md: %v", err)
	}
	section := sectionAfter(string(vocab), vocabExitSection)
	if section == "" {
		t.Fatalf("Vocabulary.md has no %q section; the CLI exit-code table is missing entirely", vocabExitSection)
	}

	var guideBody string
	for _, s := range guideSections {
		if s.Topic == guideExitTopic {
			guideBody = s.Body
		}
	}
	if guideBody == "" {
		t.Fatalf("pokkum guide has no %q topic; a reader without this repo has no exit-code reference", guideExitTopic)
	}

	var sorted []int
	for c := range codes {
		sorted = append(sorted, c)
	}
	sort.Ints(sorted)

	for _, code := range sorted {
		codeCell := "`" + strconv.Itoa(code) + "`"
		if !strings.Contains(section, codeCell) {
			t.Errorf("exit code %d is returned at %v but is absent from Vocabulary.md's %q table.\n"+
				"\tAn undocumented exit code is one a caller cannot gate on — add a row naming what it means.",
				code, codes[code], vocabExitSection)
		}
		if !strings.Contains(guideBody, strconv.Itoa(code)) {
			t.Errorf("exit code %d is returned at %v but `pokkum guide %s` never mentions it.",
				code, codes[code], guideExitTopic)
		}
	}
	t.Logf("checked %d distinct exit codes (%v) against Vocabulary.md and the guide", len(sorted), sorted)
}

// TestKubectlPassthroughIsDocumented covers the one exit site a literal scan
// cannot reach: apply.go forwards kubectl's own status verbatim rather than
// collapsing it to 1, which is a deliberate behaviour a caller must know about
// and which no enumeration of literals would ever surface.
func TestKubectlPassthroughIsDocumented(t *testing.T) {
	src, err := os.ReadFile("apply.go")
	if err != nil {
		t.Fatalf("[TEST SETUP] reading apply.go: %v", err)
	}
	if !strings.Contains(string(src), "os.Exit(exitErr.ExitCode())") {
		t.Skip("apply.go no longer forwards kubectl's exit code; this guard is obsolete and should be removed")
	}

	vocab, err := os.ReadFile(filepath.Join("..", "..", "Vocabulary.md"))
	if err != nil {
		t.Fatalf("reading Vocabulary.md: %v", err)
	}
	section := sectionAfter(string(vocab), vocabExitSection)
	if !strings.Contains(section, "kubectl") {
		t.Error("apply.go propagates kubectl's exit code verbatim, but Vocabulary.md's CLI exit-code " +
			"table never mentions kubectl — a caller gating on pokkum apply's status would not know " +
			"the code is not pokkum's own.")
	}
}

// sectionAfter returns the markdown between heading and the next "\n## ".
func sectionAfter(doc, heading string) string {
	i := strings.Index(doc, heading)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(heading):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		return rest[:j]
	}
	return rest
}

// TestDoctorExitStatusIsIndependentOfOutputFormat pins an invariant that was
// violated in exactly one place and would have been violated silently again.
//
// `--output` selects a serialization. It must never change whether the command
// succeeded. Before this guard, `pokkum doctor --output json` on a red project
// wrote status:"error", passed:false and a list of failing checks — and exited
// 0, because the JSON branch returned before the shared failure signal at the
// end of runDoctor. Text mode exited 1 on the same project. A CI step gating on
// `pokkum doctor --output json` therefore passed while doctor was red, which is
// the worst possible direction for a diagnostic command to be wrong in.
//
// This also keeps Vocabulary.md §18d honest: that table says exit 1 covers "a
// red doctor", with no format caveat, and a table that lies is worse than none.
func TestDoctorExitStatusIsIndependentOfOutputFormat(t *testing.T) {
	dir := t.TempDir() // not a SvelteKit project: several checks fail deterministically

	run := func(format string) error {
		t.Helper()
		// runDoctor writes the report to os.Stdout directly; discard it so the
		// test output stays readable.
		orig := os.Stdout
		devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("[TEST SETUP] opening %s: %v", os.DevNull, err)
		}
		os.Stdout = devnull
		defer func() {
			os.Stdout = orig
			_ = devnull.Close()
		}()
		return runDoctor(discardLogger(), &doctorOptions{dir: dir, output: format})
	}

	textErr := run("text")
	jsonErr := run("json")

	// Premise check: if text mode stopped failing here, this test would compare
	// two nils and pass while proving nothing.
	if textErr == nil {
		t.Fatal("[TEST SETUP] doctor passed on an empty temp dir in text mode; " +
			"the fixture no longer triggers a failure and this guard is blind")
	}

	if jsonErr == nil {
		t.Error("doctor --output json returned no error on a project that fails in text mode.\n" +
			"\tThe process therefore exits 0 while the envelope reports status:\"error\" and " +
			"passed:false —\n\ta CI gate on `pokkum doctor --output json` would pass on a red doctor.\n" +
			"\t--output selects a serialization; it must never change whether the command succeeded.")
	}
}
