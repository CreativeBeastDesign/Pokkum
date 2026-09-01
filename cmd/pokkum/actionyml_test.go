package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ---------------------------------------------------------------------------
// action.yml <-> real `pokkum build` flags.
//
// This is the small, high-value regression the task brief calls out
// specifically: action.yml shipped invoking --repo and --tag on `pokkum
// build`, neither of which existed as flags at the time (--repo never did;
// --tag did, just not under that exact assumption at the time it was
// written). The documented GitHub Action quickstart failed at argument
// parsing. Found by human review, fixed in commit 7cf62c3. This test turns
// that exact failure mode into something `go test` catches.
//
// Extraction approach: action.yml's "Execute Pokkum Build" step builds an
// ARGS bash array and appends to it conditionally
// (`ARGS+=("--flag" "value")` or `ARGS+=("--flag")`), then invokes
// `pokkum build "${ARGS[@]}" ... --log-format json`. Two small, targeted
// regexes cover exactly those two shapes:
//
//   - `ARGS\+=\(\s*"(--flag)"` captures the flag token out of every
//     ARGS+=(...) append, ignoring any second (value) argument in the same
//     call.
//   - `\$\(pokkum build[^\n]*\)` isolates just the actual invocation's
//     command-substitution text, so a flag-shaped token mentioned in a
//     comment on an unrelated line (e.g. this file's own "pokkum build has
//     no --repo flag" explanatory comment, line ~115) is never picked up —
//     it does not appear inside a `$(pokkum build ...)` command
//     substitution, only in prose.
//
// A bare whole-file regexp over every `--flag`-shaped token (the same
// technique flags_docs_test.go uses for Vocabulary.md) was deliberately not
// used here: action.yml's own `inputs:` descriptions intentionally *discuss*
// flags that are NOT emitted on every path (e.g. "pokkum build has no --repo
// flag", "NOT the global --output flag") specifically to explain why the
// script does what it does. A whole-file scan would have to separately
// allowlist those explanatory mentions; scoping to the two real emission
// shapes avoids needing an allowlist at all.
//
// Updated 2026-08-23: the script now emits --log-level in the attached
// `--flag=value` form and invokes `pokkum build` as a plain pipeline rather
// than inside a `$(...)` command substitution. Both regexes had stopped
// matching those shapes — the extractor still returned a non-empty set from
// the remaining ARGS+=("--flag" "value") appends, so the len()==0 floor below
// would not have fired, and coverage of the changed lines would simply have
// lapsed unnoticed.
var argsAppendRe = regexp.MustCompile(`ARGS\+=\(\s*"(--[a-zA-Z][a-zA-Z0-9-]*)(?:"|=)`)
var buildInvocationRe = regexp.MustCompile(`(?m)^\s*(?:\S+=\S+\s+)*pokkum build[^\n]*$`)

func actionYMLPath() string {
	return filepath.Join("..", "..", "action.yml")
}

func readActionYML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(actionYMLPath())
	if err != nil {
		t.Fatalf("read action.yml: %v (resolved path: %s)", err, actionYMLPath())
	}
	return string(data)
}

// extractActionYMLBuildFlags returns every --flag token action.yml's
// "Execute Pokkum Build" step actually emits on a `pokkum build` command
// line, mapped to one example line of context for actionable errors.
func extractActionYMLBuildFlags(text string) map[string]string {
	flags := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		for _, m := range argsAppendRe.FindAllStringSubmatch(line, -1) {
			if _, exists := flags[m[1]]; !exists {
				flags[m[1]] = strings.TrimSpace(line)
			}
		}
	}
	for _, invocation := range buildInvocationRe.FindAllString(text, -1) {
		for _, tok := range flagTokenRe.FindAllString(invocation, -1) {
			if _, exists := flags[tok]; !exists {
				flags[tok] = strings.TrimSpace(invocation)
			}
		}
	}
	return flags
}

// buildCommandFlagSurface returns the set of --flag names actually usable
// on `pokkum build` specifically: its own registered flags plus every
// PersistentFlags() name declared by an ancestor (the root command's
// --log-level/--log-format/--output). This deliberately does not reuse
// flags_docs_test.go's realCLIFlagSurface, which unions flags across every
// command in the tree — action.yml only ever invokes `pokkum build`, so the
// correct check is "does `pokkum build` itself accept this flag", not "does
// this flag exist somewhere in the CLI".
func buildCommandFlagSurface(t *testing.T) map[string]bool {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	root := newRootCommand(ctx, logger)
	var buildCmd *cobra.Command
	for _, sub := range root.Commands() {
		if sub.Name() == "build" {
			buildCmd = sub
			break
		}
	}
	if buildCmd == nil {
		t.Fatalf("[TEST SETUP] could not find the `build` subcommand on the root command tree — " +
			"newBuildCommand may have been renamed or its registration removed from newRootCommand")
	}

	flags := map[string]bool{}
	record := func(f *pflag.Flag) { flags["--"+f.Name] = true }
	for c := buildCmd; c != nil; c = c.Parent() {
		c.Flags().VisitAll(record)
		c.PersistentFlags().VisitAll(record)
	}
	return flags
}

// TestActionYMLBuildFlagsExist is the regression for the exact bug this
// task calls out: every flag action.yml's build step actually emits must be
// a real flag `pokkum build` accepts.
func TestActionYMLBuildFlagsExist(t *testing.T) {
	emitted := extractActionYMLBuildFlags(readActionYML(t))
	if len(emitted) == 0 {
		t.Fatalf("[TEST SETUP] extracted zero flags from action.yml's ARGS+=(...)/pokkum build invocation — " +
			"the script's shape likely changed and argsAppendRe/buildInvocationRe in actionyml_test.go need " +
			"updating; this must not silently pass with nothing checked")
	}

	real := buildCommandFlagSurface(t)

	unknown := make([]string, 0)
	for flag, context := range emitted {
		if real[flag] {
			continue
		}
		unknown = append(unknown, flag+" (emitted by: "+context+")")
	}
	sort.Strings(unknown)

	if len(unknown) > 0 {
		t.Errorf("[ACTION.YML DRIFT] action.yml emits %d flag(s) on `pokkum build` that do not exist as a "+
			"registered flag on that command:\n  %s\n"+
			"This is exactly the class of bug fixed in 7cf62c3 (action.yml invoked --repo/--tag, neither a "+
			"real flag at the time). Fix action.yml's ARGS+=(...) line to use a real flag name — this test "+
			"file is not owned by this task and must not be edited to work around a real action.yml bug.",
			len(unknown), strings.Join(unknown, "\n  "))
	}
}
