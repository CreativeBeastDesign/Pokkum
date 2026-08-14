package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type adoptCmdOptions struct {
	dir              string
	dryRun           bool
	removeDockerfile bool
	writeConfig      bool
	output           string
}

func newAdoptCommand(_ context.Context, logger *slog.Logger) *cobra.Command {
	opts := &adoptCmdOptions{}

	cmd := &cobra.Command{
		Use:   "adopt [dir]",
		Short: "Auto-convert Vercel/Node adapter projects to Pokkum",
		Long: `Adopt is a migration codemod that inspects an existing SvelteKit repository
(configured for @sveltejs/adapter-node, adapter-vercel, adapter-auto, etc.),
adds @jesterkit/exe-sveltekit to package.json, generates .pokkumignore, and
optionally removes legacy Dockerfile configurations. It refuses to run
against a project with no @sveltejs/kit dependency.

svelte.config.js is NOT rewritten by default: pokkum build already injects
the adapter and SOURCE_DATE_EPOCH pin into a virtual copy at build time (see
ARCHITECTURE.md's Zero-Mutation Build Sandbox), so no on-disk edit is
actually required. Pass --write-config if you want the adapter swap made
permanent and visible in the file itself anyway (e.g. for editor tooling).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.dir = args[0]
			} else if opts.dir == "" {
				opts.dir = "."
			}

			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			return runAdopt(logger, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.dir, "dir", "d", ".", "Path to SvelteKit project directory")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Report planned migration changes without writing to disk")
	cmd.Flags().BoolVar(&opts.removeDockerfile, "remove-dockerfile", false, "Remove legacy Dockerfile, .dockerignore, and compose files")
	cmd.Flags().BoolVar(&opts.writeConfig, "write-config", false, "Also permanently rewrite svelte.config.js (not required for pokkum build, which injects this virtually at build time)")

	return cmd
}

func runAdopt(logger *slog.Logger, opts *adoptCmdOptions) error {
	outputFormat := ports.OutputFormat(opts.output)

	result, err := sveltekitutils.Adopt(sveltekitutils.AdoptOptions{
		Dir:              opts.dir,
		DryRun:           opts.dryRun,
		RemoveDockerfile: opts.removeDockerfile,
		WriteConfig:      opts.writeConfig,
	})
	if err != nil {
		msg := fmt.Sprintf("failed to adopt project: %v", err)
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteError(os.Stdout, "adopt", "ERR_ADOPT_FAILED", msg, "")
		}
		return fmt.Errorf("%s", msg)
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "adopt", result)
	}

	fmt.Println("=== Pokkum Project Adoption ===")
	if result.DryRun {
		fmt.Printf("ℹ Dry run mode: planned changes for %s:\n", result.Directory)
	} else {
		fmt.Printf("✓ Project successfully adopted at %s:\n", result.Directory)
	}

	for _, change := range result.ChangesSummary {
		fmt.Printf("  • %s\n", change)
	}

	if len(result.ChangesSummary) == 0 {
		fmt.Println("  • Project is already configured for Pokkum; no changes needed.")
	}

	if result.DryRun {
		fmt.Println("\nRun without --dry-run to apply these changes.")
	} else {
		fmt.Println("\nYou can now run `pokkum build` to compile your reproducible container image.")
	}

	return nil
}
