package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type initOptions struct {
	dir      string
	defaults bool
	output   string
	inReader io.Reader
}

func newInitCommand(_ context.Context, logger *slog.Logger) *cobra.Command {
	opts := &initOptions{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize Pokkum project configuration (.pokkum.yaml) and .pokkumignore",
		Long:  `Init bootstraps a SvelteKit workspace for Pokkum container compilation by creating .pokkum.yaml with customizable build profiles and .pokkumignore entries.`,
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

	// 1. .pokkumignore creation
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

	// 2. .pokkum.yaml creation
	configPath := filepath.Join(opts.dir, ports.ConfigFilename)
	createdConfig := false

	cfgMgr, err := config.New(opts.dir, logger)
	if err != nil {
		msg := fmt.Sprintf("failed to create config manager: %v", err)
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteError(os.Stdout, "init", "ERR_INIT_FAILED", msg, "")
		}
		return fmt.Errorf("%s", msg)
	}

	initOpts := ports.InitConfigOptions{
		BasePreset:         "distroless",
		Strategy:           "layered",
		EnableLocalProfile: true,
	}

	isInteractive := false
	if !opts.defaults && outputFormat != ports.FormatJSON {
		if opts.inReader != nil {
			isInteractive = true
		} else if term.IsTerminal(int(os.Stdin.Fd())) {
			isInteractive = true
			opts.inReader = os.Stdin
		}
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if isInteractive && opts.inReader != nil {
			initOpts = promptInitOptions(opts.inReader)
		}

		newCfg := cfgMgr.GenerateDefault(initOpts)
		if err := cfgMgr.Save(opts.dir, newCfg); err != nil {
			msg := fmt.Sprintf("failed to create %s: %v", ports.ConfigFilename, err)
			if outputFormat == ports.FormatJSON {
				return jsonutils.WriteError(os.Stdout, "init", "ERR_INIT_FAILED", msg, "")
			}
			return fmt.Errorf("%s", msg)
		}
		createdConfig = true
	}

	payload := map[string]interface{}{
		"directory":      opts.dir,
		"created_ignore": createdIgnore,
		"ignore_path":    ignorePath,
		"created_config": createdConfig,
		"config_path":    configPath,
		"status":         "initialized",
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "init", payload)
	}

	fmt.Println("=== Pokkum Project Initialization ===")
	if createdConfig {
		fmt.Printf("✓ Created %s with build profiles at %s\n", ports.ConfigFilename, configPath)
	} else {
		fmt.Printf("✓ Existing %s found at %s\n", ports.ConfigFilename, configPath)
	}

	if createdIgnore {
		fmt.Printf("✓ Created default .pokkumignore at %s\n", ignorePath)
	} else {
		fmt.Printf("✓ Existing .pokkumignore found at %s\n", ignorePath)
	}
	fmt.Println("✓ Pokkum initialization complete. You can now run `pokkum build`.")

	return nil
}

func promptInitOptions(r io.Reader) ports.InitConfigOptions {
	scanner := bufio.NewScanner(r)
	opts := ports.InitConfigOptions{
		BasePreset:         "distroless",
		Strategy:           "layered",
		EnableLocalProfile: true,
	}

	fmt.Println("=== Interactive Pokkum Project Setup ===")

	// 1. Docker Repo
	fmt.Print("1. Target Container Registry (e.g. ghcr.io/example/my-app, or empty for local only) []: ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" {
			opts.Repo = input
		}
	}

	// 2. Base image preset
	fmt.Print("2. Base Image Preset [distroless / chainguard / chainguard-static] (default: distroless): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" {
			opts.BasePreset = input
		}
	}

	// 3. Strategy
	fmt.Print("3. Build Strategy [layered / static] (default: layered): ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" {
			opts.Strategy = input
		}
	}

	// 4. Local profile
	fmt.Print("4. Configure local development profile (--local)? [Y/n] (default: Y): ")
	if scanner.Scan() {
		input := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if input == "n" || input == "no" {
			opts.EnableLocalProfile = false
		}
	}

	// 5. CVE policy
	fmt.Print("5. Fail build on vulnerability threshold [none / low / medium / high / critical] (default: none): ")
	if scanner.Scan() {
		input := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if input != "" && input != "none" {
			opts.FailOnCVE = input
		}
	}

	fmt.Println()
	return opts
}
