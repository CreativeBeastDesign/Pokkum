package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type initOptions struct {
	dir      string
	defaults bool
	output   string
}

func newInitCommand(_ context.Context, logger *slog.Logger) *cobra.Command {
	opts := &initOptions{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize Pokkum project configuration and .pokkumignore",
		Long:  `Init bootstraps a SvelteKit workspace for Pokkum container compilation by creating default .pokkumignore entries and verifying project structure.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			return runInit(logger, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.dir, "dir", "d", ".", "Path to SvelteKit project directory")
	cmd.Flags().BoolVar(&opts.defaults, "defaults", false, "Accept all default configuration options without prompting")

	return cmd
}

func runInit(logger *slog.Logger, opts *initOptions) error {
	outputFormat := ports.OutputFormat(opts.output)

	ignorePath := filepath.Join(opts.dir, ".pokkumignore")
	createdIgnore := false

	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		defaultContent := `# Pokkum exclude patterns
.env
.env.local
.env.*
node_modules
.git
.pokkum
coverage
`
		if err := os.WriteFile(ignorePath, []byte(defaultContent), 0644); err != nil {
			msg := fmt.Sprintf("failed to create .pokkumignore: %v", err)
			if outputFormat == ports.FormatJSON {
				return jsonutils.WriteError(os.Stdout, "init", "ERR_INIT_FAILED", msg, "")
			}
			return fmt.Errorf("%s", msg)
		}
		createdIgnore = true
	}

	payload := map[string]interface{}{
		"directory":        opts.dir,
		"created_ignore":   createdIgnore,
		"ignore_path":      ignorePath,
		"status":           "initialized",
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "init", payload)
	}

	fmt.Println("=== Pokkum Project Initialization ===")
	if createdIgnore {
		fmt.Printf("✓ Created default .pokkumignore at %s\n", ignorePath)
	} else {
		fmt.Printf("✓ Existing .pokkumignore found at %s\n", ignorePath)
	}
	fmt.Println("✓ Pokkum initialization complete. You can now run `pokkum build`.")

	return nil
}
