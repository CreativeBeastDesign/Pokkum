package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestNoReturnWriteErrorShape is the structural half of the row-73 guard.
//
// `return jsonutils.WriteError(...)` reads like "return this error" and does
// the opposite: WriteError returns the WRITE result, nil when the envelope was
// written fine. Thirteen call sites across six commands had that shape, each
// sitting directly above a `return fmt.Errorf(...)` for the text branch — so
// every one of them printed status:"error" and exited 0 while text mode exited
// 1 on the identical input.
//
// The shape is the bug, so the shape is what this forbids. failJSON() is the
// replacement: it writes the same envelope and returns a silent failure.
func TestNoReturnWriteErrorShape(t *testing.T) {
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
		// AST, not a string scan: jsonfail.go quotes the forbidden shape in its
		// own doc comment explaining why it is forbidden, and a textual match
		// cannot tell that prose from code. Parsing sees only real returns.
		ast.Inspect(file, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				return true
			}
			call, ok := ret.Results[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WriteError" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "jsonutils" {
				return true
			}
			t.Errorf("%s:%d returns jsonutils.WriteError directly.\n"+
				"\tWriteError returns the WRITE result — nil on success — so this exits 0 after\n"+
				"\tprinting status:\"error\". Use failJSON(command, code, message, details), which\n"+
				"\twrites the same envelope and returns a failure the caller propagates.",
				name, fset.Position(ret.Pos()).Line)
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("[TEST SETUP] scanned zero .go files; the walk is broken")
	}
	t.Logf("checked %d files for the return-WriteError shape", scanned)
}

// TestExitStatusIsIndependentOfOutputFormat is the empirical half, and the one
// that would have caught the original bug: it drives the real command tree.
//
// Only offline commands are covered — explain/history/verify need a registry
// and would make this flaky. Those three were verified by hand; adopt in
// particular is here because it was one of the three confirmed broken.
func TestExitStatusIsIndependentOfOutputFormat(t *testing.T) {
	plain := t.TempDir() // not a SvelteKit project
	if err := os.WriteFile(filepath.Join(plain, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatalf("[TEST SETUP] writing package.json: %v", err)
	}
	badCfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(badCfg, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatalf("[TEST SETUP] writing package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badCfg, ".pokkum.yaml"),
		[]byte("version: 1\nstrategy: nonsense-value\n"), 0o644); err != nil {
		t.Fatalf("[TEST SETUP] writing .pokkum.yaml: %v", err)
	}

	cases := []struct {
		name string
		args []string
	}{
		{"adopt on a non-SvelteKit project", []string{"adopt", "-d", plain, "--dry-run"}},
		{"config view with no config file", []string{"config", "view", "-d", plain}},
		{"config validate on an invalid strategy", []string{"config", "validate", "-d", badCfg}},
		{"deploy with no deploy target configured", []string{"deploy", "-d", plain}},
	}

	run := func(t *testing.T, args []string) error {
		t.Helper()
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

		root := newRootCommand(context.Background(), discardLogger())
		root.SetOut(devnull)
		root.SetErr(devnull)
		root.SetArgs(args)
		return root.Execute()
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			textErr := run(t, tc.args)
			jsonErr := run(t, append(append([]string{}, tc.args...), "--output", "json"))

			// Premise check: a case that stopped failing would compare two nils
			// and pass while proving nothing.
			if textErr == nil {
				t.Fatalf("[TEST SETUP] %v succeeded in text mode; this fixture no longer "+
					"triggers a failure and the comparison is blind", tc.args)
			}
			if jsonErr == nil {
				t.Errorf("%v failed in text mode but returned no error with --output json.\n"+
					"\tThe process exits 0 while the envelope reports status:\"error\" — a CI gate\n"+
					"\ton this command would pass on a failure. --output selects a serialization;\n"+
					"\tit must never change whether the command succeeded.", tc.args)
			}
		})
	}
}

// TestConfigSchemaPrintsTheCheckedInSchema asserts `pokkum config schema`
// emits the repository's schema verbatim.
//
// The embed makes the bytes correct by construction, so what this actually
// guards is the command around them: that it stays wired up, and that nobody
// later "improves" it by re-indenting, wrapping it in an envelope, or adding a
// trailing banner. The command's whole value is that its output can be written
// straight to a file an editor or a CI validator consumes, and any of those
// would break that silently while still looking like a schema.
func TestConfigSchemaPrintsTheCheckedInSchema(t *testing.T) {
	onDisk, err := os.ReadFile(filepath.Join("..", "..", "schema", "pokkum.schema.json"))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading schema/pokkum.schema.json: %v", err)
	}
	if len(onDisk) == 0 {
		t.Fatal("[TEST SETUP] the checked-in schema is empty; this guard would compare nothing")
	}

	var buf bytes.Buffer
	root := newRootCommand(context.Background(), discardLogger())
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"config", "schema"})
	if err := root.Execute(); err != nil {
		t.Fatalf("pokkum config schema: %v", err)
	}

	if !bytes.Equal(buf.Bytes(), onDisk) {
		t.Errorf("pokkum config schema output differs from schema/pokkum.schema.json "+
			"(%d bytes emitted vs %d on disk).\n"+
			"\tThe command must emit the schema verbatim so its output can be redirected "+
			"straight to a file an editor or CI validator reads.", buf.Len(), len(onDisk))
	}

	// And it must actually be a schema, not merely equal to a file.
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("pokkum config schema did not emit valid JSON: %v", err)
	}
	if doc["$schema"] == nil {
		t.Error("emitted document has no $schema key; it is not a JSON Schema")
	}
}
