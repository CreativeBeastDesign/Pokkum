package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Flag names mentioned in user-facing strings must name flags that exist.
//
// Why this exists: `pokkum init` finished by telling the operator to "pass
// --repo" to choose a push destination. There is no --repo flag — the destination
// comes from docker.repo or POKKUM_DOCKER_REPO — so the one message whose whole
// job is telling a first-time user what to run named something that cannot work.
//
// The reason this warranted a general guard rather than a one-line fix:
// actionyml_test.go exists *because* action.yml once shipped invoking --repo and
// --tag, neither of which existed. That test watches action.yml. It cannot see a
// flag name embedded in a Go string, so the identical mistake came back through a
// different surface. This closes that surface: every flag mention in a string
// literal is checked against the real command tree.
//
// Scope is string literals in non-test files, because those are what reach a
// user. Comments are excluded deliberately — a stale flag name in a comment is
// worth fixing but is not a broken instruction, and actionyml_test.go's own
// comments legitimately discuss --repo as the flag that never existed.

// foreignFlags are flags belonging to tools Pokkum invokes or writes about. Each
// records its owner, because an unexplained allowlist entry is indistinguishable
// from a mistake somebody silenced. TestFlagMentionAllowlistHasNoDeadEntries
// keeps this list honest: an entry that stops being needed, or that becomes a real
// pokkum flag, fails the build rather than sitting here masking a future typo.
var foreignFlags = map[string]string{
	// bun build — internal/adapters/bunexec/args.go and friends
	"--compile": "bun build",
	"--minify":  "bun build",
	"--outfile": "bun build",
	"--target":  "bun build",
	"--preload": "bun run / bunfig preload",

	// git — gitutils, slsa/gitdiscovery, config source-date discovery
	"--always":    "git describe",
	"--dirty":     "git describe",
	"--tags":      "git describe",
	"--name-only": "git diff",
	"--porcelain": "git status",
	"--get":       "git config --get",
	"--pretty":    "git log",
	"--verify":    "git rev-parse --verify",

	// containers / binutils / otel
	"--rm":             "docker run (dev.go container invocation)",
	"--entrypoint":     "docker run (dev.go container invocation)",
	"--strip-unneeded": "strip (striputils)",
	"--config":         "vite build --config, and otel-collector's args in the sidecar spec",
}

// mentionFlagRe is deliberately narrower than flags_docs_test.go's flagTokenRe.
// That one scans an action.yml invocation, where every token really is a flag.
// This one scans arbitrary prose, which contains PEM armor: the broad pattern
// reads "-----BEGIN PGP SIGNATURE-----" as a mention of a --BEGIN flag. Requiring
// lowercase, and rejecting a preceding dash at the call site (Go regexp has no
// lookbehind), keeps armor and long rules out.
var mentionFlagRe = regexp.MustCompile(`--[a-z][a-z0-9-]+`)

func flagMentionsIn(s string) []string {
	var out []string
	for _, loc := range mentionFlagRe.FindAllStringIndex(s, -1) {
		if loc[0] > 0 && s[loc[0]-1] == '-' {
			continue
		}
		out = append(out, s[loc[0]:loc[1]])
	}
	return out
}

// realFlagNames walks the whole command tree, so a flag reachable only through a
// subcommand (`pokkum base update --mirror-registry`) counts as real. Walking the
// FlagSets rather than parsing `--help` output is the choice flags_docs_test.go
// made, for the same reason: help text is a rendering, the FlagSets are the truth.
// An earlier ad-hoc `--help` scrape while sizing this guard missed exactly the
// nested flags and produced a list of "unknown" flags that were real.
func realFlagNames(t *testing.T) map[string]bool {
	t.Helper()
	root := newRootCommand(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	names := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		record := func(f *pflag.Flag) { names["--"+f.Name] = true }
		c.Flags().VisitAll(record)
		c.PersistentFlags().VisitAll(record)
		c.InheritedFlags().VisitAll(record)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	// Premise check: the walk must reach nested subcommands, not just the root.
	// --mirror-registry lives on `pokkum base update`; --perturb on `repro doctor`.
	for _, nested := range []string{"--mirror-registry", "--perturb"} {
		if !names[nested] {
			t.Fatalf("[TEST SETUP] %s not discovered; the walk is not reaching nested subcommands", nested)
		}
	}
	return names
}

// scanFlagMentions walks production Go sources and calls visit for every
// flag-like token found inside a string literal.
func scanFlagMentions(t *testing.T, visit func(tok, file string, line int)) (files, mentions int) {
	t.Helper()
	for _, dir := range []string{"." /* cmd/pokkum */, filepath.Join("..", "..", "internal")} {
		err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() {
				if n := fi.Name(); n == "testdata" || n == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil // unparseable files are the compiler's problem, not this test's
			}
			files++
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					val = lit.Value
				}
				for _, tok := range flagMentionsIn(val) {
					mentions++
					visit(tok, path, fset.Position(lit.Pos()).Line)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	return files, mentions
}

func TestFlagMentionsInUserFacingStringsExist(t *testing.T) {
	real := realFlagNames(t)

	// Extractor premise checks, so a broken pattern cannot make this pass by
	// quietly finding nothing.
	if got := flagMentionsIn(`try --signing-key here`); len(got) != 1 || got[0] != "--signing-key" {
		t.Fatalf("[TEST SETUP] extractor no longer finds a plain flag mention: %v", got)
	}
	if got := flagMentionsIn("-----BEGIN PGP SIGNATURE-----"); len(got) != 0 {
		t.Fatalf("[TEST SETUP] extractor reads PEM armor as a flag mention: %v", got)
	}
	if !real["--signing-key"] {
		t.Fatal("[TEST SETUP] --signing-key should be a real flag; the command walk is wrong")
	}

	files, mentions := scanFlagMentions(t, func(tok, file string, line int) {
		if real[tok] {
			return
		}
		if _, ok := foreignFlags[tok]; ok {
			return
		}
		t.Errorf("%s:%d mentions %q in a user-facing string, but no such pokkum flag exists.\n"+
			"\tIf it belongs to another tool, add it to foreignFlags with its owner.\n"+
			"\tIf it is a pokkum flag, it is missing from the command tree — that is the bug.\n"+
			"\tDo not add an entry without naming the owner.", file, line, tok)
	})

	if files == 0 || mentions == 0 {
		t.Fatalf("[TEST SETUP] scanned %d files and found %d flag mentions; the scan has gone blind", files, mentions)
	}
	t.Logf("checked %d flag mentions across %d files against %d real flags", mentions, files, len(real))
}

// TestFlagMentionAllowlistHasNoDeadEntries stops the allowlist from rotting.
//
// This is not hygiene for its own sake. When this guard was first written its
// allowlist carried 23 entries copied from an earlier, sloppier scan that had
// included test files and comments — among them "--repo", the very flag whose
// reintroduction the guard exists to catch. Every dead entry is a pre-authorised
// blind spot, so the list is required to earn each one.
func TestFlagMentionAllowlistHasNoDeadEntries(t *testing.T) {
	real := realFlagNames(t)

	used := map[string]bool{}
	scanFlagMentions(t, func(tok, _ string, _ int) {
		if !real[tok] {
			used[tok] = true
		}
	})

	var dead, nowReal []string
	for tok := range foreignFlags {
		switch {
		case real[tok]:
			nowReal = append(nowReal, tok)
		case !used[tok]:
			dead = append(dead, tok)
		}
	}
	sort.Strings(dead)
	sort.Strings(nowReal)

	if len(dead) > 0 {
		t.Errorf("foreignFlags entries no longer mentioned anywhere: %v\n"+
			"\tRemove them. A dead entry silences a future typo of the same name.", dead)
	}
	if len(nowReal) > 0 {
		t.Errorf("foreignFlags entries that are now real pokkum flags: %v\n"+
			"\tRemove them; the real-flag check already covers these, and keeping them here "+
			"hides the fact that pokkum owns the name.", nowReal)
	}
}

// enumFlagValues are flags with a closed value set, mapped to the values the code
// itself defines. Built from the ports constants rather than typed out, so it
// cannot drift from the enum it guards.
var enumFlagValues = map[string][]string{
	"--output":      {string(ports.FormatText), string(ports.FormatJSON)},
	"--strategy":    {string(ports.StrategyLayered), string(ports.StrategyExe), string(ports.StrategyStatic)},
	"--runtime":     {string(ports.RuntimeBun), string(ports.RuntimeNode)},
	"--sbom":        {string(ports.SBOMFormatSPDXJSON), string(ports.SBOMFormatCycloneDXJSON), string(ports.SBOMFormatNone)},
	"--sbom-attach": {string(ports.SBOMAttachReferrer), string(ports.SBOMAttachTag), string(ports.SBOMAttachAuto)},
}

var flagValueRe = regexp.MustCompile(`(--[a-z][a-z0-9-]+)=([A-Za-z][A-Za-z0-9._-]*)`)

// TestFlagValueMentionsAreValid checks the VALUE side of a flag mention.
//
// TestFlagMentionsInUserFacingStringsExist proves the flag NAME exists. That is
// not enough, and the gap was live: three messages instructed users to pass
// `--output=push`, and `--output` is a real flag — the serialization format,
// whose only valid values are text and json. There has never been a push mode on
// it; a registry push is the default, and --local/--tarball are the alternatives.
// So the guard passed while the advice was impossible to follow.
//
// Worse, nothing rejected it: every consumer reads `if format == FormatJSON` and
// falls through to text, so `--output=push` was accepted in silence. That is now
// a hard error (ports.ParseOutputFormat), and this test stops the advice coming
// back.
func TestFlagValueMentionsAreValid(t *testing.T) {
	real := realFlagNames(t)

	// Premise checks: the extractor must find a value, and the data must be able
	// to reject a wrong one.
	if got := flagValueRe.FindStringSubmatch("pass --output=json here"); len(got) != 3 || got[2] != "json" {
		t.Fatalf("[TEST SETUP] value extraction is broken: %v", got)
	}
	if slices.Contains(enumFlagValues["--output"], "push") {
		t.Fatal("[TEST SETUP] --output must not list 'push' as valid; that is the bug this guards")
	}

	// scanFlagMentions reports names only, so this walks for name=value pairs.
	var checked int
	for _, dir := range []string{".", filepath.Join("..", "..", "internal")} {
		err := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() {
				if n := fi.Name(); n == "testdata" || n == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					val = lit.Value
				}
				for _, m := range flagValueRe.FindAllStringSubmatch(val, -1) {
					flag, value := m[1], m[2]
					allowed, guarded := enumFlagValues[flag]
					if !guarded || !real[flag] {
						continue
					}
					checked++
					if slices.Contains(allowed, value) {
						continue
					}
					rel := filepath.Clean(path)
					t.Errorf("%s:%d says %q, but %s only accepts %v.\n"+
						"\tThe flag name existing is not enough — check the value against the enum the code defines.",
						rel, fset.Position(lit.Pos()).Line, flag+"="+value, flag, allowed)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	if checked == 0 {
		t.Fatal("[TEST SETUP] found no enum flag=value mentions at all; this guard is watching nothing")
	}
	t.Logf("checked %d flag=value mentions against code-defined enums", checked)
}
