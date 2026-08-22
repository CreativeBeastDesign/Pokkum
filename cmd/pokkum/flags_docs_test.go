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
// Flag <-> Vocabulary.md consistency.
//
// Why this exists: Vocabulary.md is the authoritative CLI reference, but
// nothing mechanically checked it against the real CLI surface — and that
// already shipped a real bug (action.yml invoking --repo/--tag, neither of
// which ever existed as pokkum build flags; see actionyml_test.go for the
// dedicated regression covering that exact class). This file is the more
// general version of the same discipline: every flag registered in code
// must be documented, and every flag Vocabulary.md claims exists must be
// real. mem:self_review_checklist row 16 names this exact failure mode: a
// documented invocation/flag is an unverified claim until checked against
// the real binary/command tree.
//
// Enumeration approach, and why: three options were on the table.
//
//  1. AST-walk cmd/pokkum/*.go for `.Flags().StringVar(...)`-shaped call
//     expressions. Rejected as the primary mechanism: it has to be taught
//     about every registration method that exists today (StringVar, BoolVar,
//     StringSliceVarP, IntVar, Float64Var, ...) and every one added in the
//     future, and about any conditional registration a future refactor might
//     introduce. A parser that has to be kept in sync with cobra/pflag's API
//     surface is itself a drift risk, which is exactly the failure mode this
//     test exists to close.
//  2. Build the real `pokkum` binary and parse `--help` output. This is the
//     most faithful option in the abstract (it's literally what a user
//     sees), but it pays a `go build` + subprocess round trip on every test
//     run, and still needs a second brittle parser to turn pflag's rendered
//     usage text back into flag names.
//  3. Chosen: construct the real *cobra.Command tree in-process, by calling
//     the exact same unexported constructors main.go uses
//     (newRootCommand -> newBuildCommand, newVerifyCommand, ...), then walk
//     every command's own Flags()/PersistentFlags() pflag.FlagSets directly.
//     This is exactly as faithful as option 2 — it runs the literal
//     production registration code, not a re-implementation of it — without
//     the build/exec cost, and it hands back structured flag names instead
//     of text to re-parse. cmd/pokkum/verify_flags_test.go already
//     established this exact pattern for one command
//     (`cmd.Flags().Lookup(name)`); this file generalizes it to the whole
//     command tree via VisitAll.
//
// Vocabulary.md parsing: per the task's own guidance, this deliberately does
// NOT try to parse markdown tables by column position (a reworded row would
// silently break that). Instead it finds every `--flag-like-token` anywhere
// in the document text via regexp — robust to reformatting, at the cost of
// needing a small, explicit allowlist for the handful of places the prose
// uses a `--flag`-shaped backtick span to name something that is NOT a
// pokkum flag (another tool's flag used as precedent, or a placeholder in an
// example). Both allowlists below are that: narrow, and each entry says why.

// realCLIFlagSurface builds the full command tree (mirroring main.go's
// newRootCommand wiring) and returns the set of every non-hidden,
// long-form flag name (e.g. "--platform") registered anywhere in it.
//
// Hidden commands and hidden flags are excluded: a command/flag never shown
// by --help carries no documentation obligation. Today the only hidden
// command is __hermetic-reexec (cmd/pokkum/hermetic_reexec.go), and it
// registers no flags at all, so this exclusion is currently a no-op in
// practice — kept anyway so a future hidden command/flag doesn't need this
// file touched to stay correct.
func realCLIFlagSurface(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	root := newRootCommand(ctx, logger)

	// flag name -> command path(s) it was found on, for actionable errors.
	flags := map[string]string{}
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Hidden {
			return
		}
		record := func(f *pflag.Flag) {
			if f.Hidden {
				return
			}
			name := "--" + f.Name
			if existing, ok := flags[name]; ok {
				flags[name] = existing + ", " + cmd.CommandPath()
			} else {
				flags[name] = cmd.CommandPath()
			}
		}
		cmd.Flags().VisitAll(record)
		cmd.PersistentFlags().VisitAll(record)
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return flags
}

// vocabularyPath resolves Vocabulary.md relative to this test file's own
// package directory (cmd/pokkum), which is the cwd `go test` uses both for
// `go test ./cmd/pokkum/...` and for a direct `go test` invocation from
// inside cmd/pokkum.
func vocabularyPath() string {
	return filepath.Join("..", "..", "Vocabulary.md")
}

func readVocabulary(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(vocabularyPath())
	if err != nil {
		t.Fatalf("read Vocabulary.md: %v (resolved path: %s)", err, vocabularyPath())
	}
	return string(data)
}

var flagTokenRe = regexp.MustCompile(`--[a-zA-Z][a-zA-Z0-9-]*`)

// extractFlagTokens finds every `--flag-like` token in text, long-form only
// (single-letter shorthands like `-p` are not unique enough to check
// meaningfully and are intentionally not part of this comparison).
func extractFlagTokens(text string) map[string]bool {
	tokens := map[string]bool{}
	for _, m := range flagTokenRe.FindAllString(text, -1) {
		tokens[m] = true
	}
	return tokens
}

// backlogSectionHeading marks the start of Vocabulary.md's "Beyond v1.0 /
// Backlog" section (and the "Open Naming Questions" section that follows
// it), which lists deliberately-unimplemented, proposed future flags
// (--target-env, --skip-hooks, --policy, --mtls, ...) alongside naming
// collisions that don't correspond to any real flag ("--env"). Those are
// not claims that the flag exists today, so they must not feed the
// doc-claims-a-real-flag check below. stripBacklogSection fails loudly
// (rather than silently including everything) if the heading text ever
// changes, so a reworded heading surfaces as a test failure pointing here,
// not as a silently-widened check.
const backlogSectionHeading = "## 20. Beyond v1.0 / Backlog"

func stripBacklogSection(t *testing.T, text string) string {
	t.Helper()
	idx := strings.Index(text, backlogSectionHeading)
	if idx < 0 {
		t.Fatalf("[TEST SETUP] could not find %q in Vocabulary.md — it may have "+
			"been renamed or removed. This heading is used to exclude the backlog's "+
			"deliberately-unimplemented proposed flags from the "+
			"doc-flag-must-exist-in-code check; update backlogSectionHeading in "+
			"flags_docs_test.go to match, don't ignore this.", backlogSectionHeading)
	}
	return text[:idx]
}

// nonFlagDocTokens are `--flag`-shaped backtick spans in Vocabulary.md that
// are deliberately NOT pokkum flags: another tool's flag cited as precedent,
// or a placeholder name in a generic example. Each is pinned to the exact
// context it appears in so a new, unrelated "--foo" added to this map by
// mistake would be an obviously wrong comment, not a silent blanket
// exclusion.
var nonFlagDocTokens = map[string]string{
	"--bare":              `§1 prose: "--bare, --insecure-registry in ko" — cited as ko's own precedent, not a pokkum flag.`,
	"--insecure-registry": `§1 prose: same ko-precedent sentence as --bare above.`,
	"--push":              `§1/§3 prose: ko's "--local/--push=false distinction" — pokkum's equivalent is the absence of a flag (push is the unflagged default); "--push" itself is never registered.`,
	"--base-preset":       `§1 convention #5: "not --base-preset=hardened" is explicitly the naming NOT chosen for --hardened.`,
	"--x":                 `§1 convention #6's generic example error message ("cannot specify both --x and --y"), a placeholder pair, not real flags.`,
	"--y":                 `§1 convention #6: same placeholder-pair example as --x above.`,
	"--compile":           `§3a prose: "bun build --compile" is Bun's own flag, quoted verbatim while explaining pokkum's --strategy=exe.`,
	"--preload":           `§3a prose: "bun --preload <path>" is Bun's own flag, quoted verbatim while explaining pokkum's --strategy=layered telemetry bootstrap.`,
	"--config":            `§3's --inject description quotes the literal subprocess command line ("bunx vite build --config .pokkum/vite.config.ts") — --config there is Vite's own flag, not pokkum's.`,
	"--production":        `§3's --sbom description quotes "bun install --production", Bun's own flag, when explaining which dependency set the SBOM's npm catalogue is scoped to.`,
	"--type":              `§3's --sbom-attach description quotes "cosign verify-attestation --type spdxjson", cosign's own flag, when explaining how a consumer reads the signed SBOM attestation.`,
}

// TestFlagsDocumentedInVocabulary is Direction 1: every real, non-hidden CLI
// flag must be mentioned somewhere in Vocabulary.md. This is the direction
// that would have caught POKKUM_LOG_LEVEL-shaped gaps for flags (a real
// input silently missing from the reference doc) had one existed at the
// flag level; it did not before this test, so there was nothing forcing
// symmetry between the two POKKUM_SIGNING_PUBKEY/POKKUM_BASE_IMAGE_PUBKEY
// misses this task's brief calls out — those were env vars, but the same
// class of gap is just as possible for a flag.
func TestFlagsDocumentedInVocabulary(t *testing.T) {
	cliFlags := realCLIFlagSurface(t)
	doc := readVocabulary(t)

	missing := make([]string, 0)
	for name, cmdPath := range cliFlags {
		if strings.Contains(doc, name) {
			continue
		}
		missing = append(missing, name+" (registered on `"+cmdPath+"`)")
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("[DOCS DRIFT] %d CLI flag(s) are registered in code but not mentioned anywhere in Vocabulary.md:\n  %s\n"+
			"Document each in its command's flag table (or, if deliberately internal/undocumented, add it with a justification to nonFlagDocTokens' sibling allowlist for the code->docs direction in flags_docs_test.go).",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestFlagsExistInCLIFromVocabulary is Direction 2, the direction that actually
// caught the action.yml --repo/--tag bug in spirit: every `--flag` token
// Vocabulary.md presents as a real, currently-implemented pokkum flag must
// resolve to an actual registration in the command tree. The backlog and
// naming-questions sections are excluded (see stripBacklogSection) because
// they explicitly document flags that do not exist yet by design.
func TestFlagsExistInCLIFromVocabulary(t *testing.T) {
	cliFlags := realCLIFlagSurface(t)
	doc := readVocabulary(t)
	scoped := stripBacklogSection(t, doc)
	docTokens := extractFlagTokens(scoped)

	unknown := make([]string, 0)
	for token := range docTokens {
		if _, ok := cliFlags[token]; ok {
			continue
		}
		if reason, ok := nonFlagDocTokens[token]; ok {
			t.Logf("[ALLOWLISTED] %s: %s", token, reason)
			continue
		}
		unknown = append(unknown, token)
	}
	sort.Strings(unknown)

	if len(unknown) > 0 {
		t.Errorf("[DOCS DRIFT] Vocabulary.md references %d flag-shaped token(s) that do not exist as a "+
			"registered CLI flag on any command:\n  %s\n"+
			"This is the same class of bug action.yml shipped with (--repo/--tag, neither ever a real flag; "+
			"fixed in 7cf62c3). Either the flag was renamed/removed and the doc is stale (fix Vocabulary.md), "+
			"or this is deliberately not a real flag (a precedent citation, a placeholder example) and belongs "+
			"in nonFlagDocTokens in flags_docs_test.go with a comment explaining why.",
			len(unknown), strings.Join(unknown, "\n  "))
	}
}
