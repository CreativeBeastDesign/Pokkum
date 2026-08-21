package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type reproDoctorOptions struct {
	fast    bool
	perturb bool
	dir     string
	output  string
}

func newReproDoctorCommand(_ context.Context, logger *slog.Logger) *cobra.Command {
	opts := &reproDoctorOptions{}

	cmd := &cobra.Command{
		Use:   "repro doctor [dir]",
		Short: "Perform stage-level non-determinism bisection and static checks",
		Long: `Repro Doctor runs static reproducibility preflight checks: it confirms
SOURCE_DATE_EPOCH is pinned and the git working tree is clean.

It does NOT build anything, and it cannot tell you whether your build is actually
reproducible — only that two common preconditions are met. To compare real
rebuilds, use "pokkum verify", which rebuilds the image by default (opt out with
--no-rebuild) and diffs it at three levels.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			return runReproDoctor(cmd.Context(), logger, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.fast, "fast", false, "Run static non-determinism checks only without building")
	cmd.Flags().BoolVar(&opts.perturb, "perturb", false, "Not implemented: dual perturbed builds for stage-level bisection. Refuses rather than reporting a pass it did not earn; use `pokkum verify` to compare real rebuilds")
	cmd.Flags().StringVarP(&opts.dir, "dir", "d", ".", "Path to SvelteKit project directory")

	return cmd
}

func runReproDoctor(ctx context.Context, _ *slog.Logger, opts *reproDoctorOptions) error {
	outputFormat := ports.OutputFormat(opts.output)

	// Check 1: Static timestamp check
	sdeEnv := os.Getenv("SOURCE_DATE_EPOCH")
	sdePresent := sdeEnv != ""

	// Check 2: Git worktree status.
	//
	// This used to be a constant: gitClean was initialised true and the only
	// assignment inside the stat set it true again, so no git command ran and
	// the check could never fail. It reported "no dirty modifications" for
	// every tree, fed allDeterministic, and was cited in a field report as
	// corroborating another signal — which it cannot do, since it agrees with
	// everything. It now asks git, through the same helper that decides what
	// provenance records, so the two cannot disagree about one tree.
	gitClean := true
	gitChecked := false
	var gitErr error
	if _, err := os.Stat(filepath.Join(opts.dir, ".git")); err == nil {
		gitChecked = true
		var dirty bool
		dirty, gitErr = slsa.WorkingTreeDirty(ctx, opts.dir)
		gitClean = !dirty
	}

	// --perturb advertised "dual builds in a perturbed environment to bisect
	// stage non-determinism" and did nothing whatsoever: the flag was echoed
	// into the output and never read again. A field test caught it completing in
	// 16ms — no build, no bisection — and printing the same "preflight passed"
	// as every other mode, on a project whose builds were demonstrably not
	// reproducible. Refusing is the honest behaviour until the mode exists;
	// silently succeeding is what CLAUDE.md calls a fake implementation.
	if opts.perturb {
		return fmt.Errorf("--perturb is not implemented: it would run dual builds in a perturbed environment to bisect stage-level non-determinism, but no such build is performed. "+
			"Refusing rather than reporting a pass it did not earn. To compare real rebuilds today, use `pokkum verify`, which rebuilds by default: %w", core.ErrInvalidRequest)
	}

	allDeterministic := sdePresent && gitClean

	checks := []map[string]interface{}{
		{
			"check":   "SOURCE_DATE_EPOCH Pinning",
			"passed":  sdePresent,
			"details": fmt.Sprintf("SOURCE_DATE_EPOCH=%s", sdeEnv),
		},
		{
			"check":   "Clean Git Repository",
			"passed":  gitClean,
			"details": gitCheckDetails(gitChecked, gitClean, gitErr),
		},
	}

	payload := map[string]interface{}{
		"mode":          "repro doctor",
		"fast_mode":     opts.fast,
		"perturb_mode":  opts.perturb,
		"deterministic": allDeterministic,
		"checks":        checks,
		"summary":       reproSummary(allDeterministic),
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "repro doctor", payload)
	}

	fmt.Println("=== Pokkum Reproducibility Doctor & Non-Determinism Bisection ===")
	fmt.Printf("Fast Mode:    %t\n", opts.fast)
	fmt.Printf("Perturb Mode: %t\n", opts.perturb)
	fmt.Println()

	for _, c := range checks {
		statusStr := "[✓ PASS]"
		if !c["passed"].(bool) {
			statusStr = "[! WARN]"
		}
		fmt.Printf("%s %s: %s\n", statusStr, c["check"], c["details"])
	}
	fmt.Println()
	fmt.Println("Result: " + reproSummary(allDeterministic))

	return nil
}

// reproSummary describes what the checks actually found.
//
// The previous text was the constant "Static reproducibility preflight passed",
// printed even when both checks reported [! WARN] — so a project with no
// SOURCE_DATE_EPOCH and a dirty tree was told its preflight passed. The
// individual check lines were correct; only the conclusion drawn from them was
// not.
func reproSummary(deterministic bool) string {
	if deterministic {
		return "Static reproducibility preflight passed."
	}
	return "Static reproducibility preflight found problems (see the checks above); these are preconditions only — a passing preflight still does not prove a build reproduces."
}

// gitCheckDetails describes what the git check actually observed, including
// the case where there was no repository to inspect — previously reported as a
// clean tree, which is a different claim from "there is nothing to check".
func gitCheckDetails(checked, clean bool, gitErr error) string {
	switch {
	case !checked:
		return "Not a git repository, so working-tree cleanliness could not be checked"
	case gitErr != nil:
		// Distinct from both "clean" and "dirty": git itself could not be
		// consulted, so this check has no verdict to offer. Reporting it as
		// clean would be a reproducibility claim made from no evidence — the
		// failure mode this check was rewritten to remove.
		return "INCONCLUSIVE: git could not be consulted, so working-tree cleanliness is unknown (treated as dirty): " + gitErr.Error()
	case clean:
		return "No dirty uncommitted working tree modifications detected (ignoring Pokkum's own .pokkum/ and pokkum.lock)"
	default:
		return "Uncommitted working tree modifications detected; a rebuild from this commit would not reproduce this build"
	}
}
