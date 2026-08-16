package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type configViewOptions struct {
	dir     string
	profile string
	output  string
}

type configValidateOptions struct {
	dir    string
	output string
}

func newConfigCommand(_ context.Context, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate Pokkum project configuration (.pokkum.yaml)",
		Long:  `Config provides subcommands to inspect resolved project configurations and validate .pokkum.yaml schema and profiles.`,
	}

	viewOpts := &configViewOptions{}
	viewCmd := &cobra.Command{
		Use:   "view [dir]",
		Short: "Display resolved configuration with optional profile overrides",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				viewOpts.dir = args[0]
			}
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				viewOpts.output = outFlag
			}
			return runConfigView(logger, viewOpts)
		},
	}
	viewCmd.Flags().StringVarP(&viewOpts.profile, "profile", "P", "", "Profile to resolve and display")
	viewCmd.Flags().StringVarP(&viewOpts.dir, "dir", "d", ".", "Path to project directory")

	validateOpts := &configValidateOptions{}
	validateCmd := &cobra.Command{
		Use:   "validate [dir]",
		Short: "Validate .pokkum.yaml structure, syntax, and presets",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				validateOpts.dir = args[0]
			}
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				validateOpts.output = outFlag
			}
			return runConfigValidate(logger, validateOpts)
		},
	}
	validateCmd.Flags().StringVarP(&validateOpts.dir, "dir", "d", ".", "Path to project directory")

	cmd.AddCommand(viewCmd)
	cmd.AddCommand(validateCmd)

	return cmd
}

func runConfigView(logger *slog.Logger, opts *configViewOptions) error {
	outputFormat := ports.OutputFormat(opts.output)

	mgr, err := config.New(opts.dir, logger)
	if err != nil {
		return fmt.Errorf("config manager: %w", err)
	}

	cfg, err := mgr.Load(opts.dir)
	if err != nil {
		if os.IsNotExist(err) {
			msg := fmt.Sprintf("no %s found in %s (run `pokkum init` to create one)", ports.ConfigFilename, opts.dir)
			if outputFormat == ports.FormatJSON {
				return jsonutils.WriteError(os.Stdout, "config view", "ERR_CONFIG_NOT_FOUND", msg, "")
			}
			return fmt.Errorf("%s", msg)
		}
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteError(os.Stdout, "config view", "ERR_CONFIG_PARSE", err.Error(), "")
		}
		return err
	}

	if opts.profile != "" {
		merged, err := mgr.ApplyProfile(cfg, opts.profile)
		if err != nil {
			if outputFormat == ports.FormatJSON {
				return jsonutils.WriteError(os.Stdout, "config view", "ERR_INVALID_PROFILE", err.Error(), "")
			}
			return err
		}
		cfg = merged
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "config view", cfg)
	}

	outBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}

	fmt.Printf("# Resolved Pokkum Configuration (%s)\n", filepath.Join(opts.dir, ports.ConfigFilename))
	if opts.profile != "" {
		fmt.Printf("# Profile: %s\n", opts.profile)
	}
	fmt.Println(string(outBytes))
	return nil
}

func runConfigValidate(logger *slog.Logger, opts *configValidateOptions) error {
	outputFormat := ports.OutputFormat(opts.output)

	mgr, err := config.New(opts.dir, logger)
	if err != nil {
		return fmt.Errorf("config manager: %w", err)
	}

	cfgPath := filepath.Join(opts.dir, ports.ConfigFilename)
	cfg, err := mgr.Load(opts.dir)
	if err != nil {
		msg := fmt.Sprintf("validation failed: %v", err)
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteError(os.Stdout, "config validate", "ERR_CONFIG_INVALID", msg, "")
		}
		return fmt.Errorf("%s", msg)
	}

	// Schema & value checks
	var validationErrors []string

	if cfg.Version != ports.ConfigSchemaVersion {
		validationErrors = append(validationErrors, fmt.Sprintf("unsupported schema version %d (expected %d)", cfg.Version, ports.ConfigSchemaVersion))
	}

	if cfg.Strategy != "" && cfg.Strategy != "layered" && cfg.Strategy != "static" && cfg.Strategy != "exe" {
		validationErrors = append(validationErrors, fmt.Sprintf("invalid strategy %q (must be layered or static)", cfg.Strategy))
	}

	if cfg.Base != "" {
		if _, err := core.ParseBaseImagePreset(cfg.Base); err != nil {
			validationErrors = append(validationErrors, fmt.Sprintf("invalid base preset %q: %v", cfg.Base, err))
		}
	}

	if len(cfg.Platforms) > 0 {
		if _, err := core.ParsePlatforms(cfg.Platforms); err != nil {
			validationErrors = append(validationErrors, fmt.Sprintf("invalid platforms %v: %v", cfg.Platforms, err))
		}
	}

	if cfg.Security.FailOnCVE != "" {
		if _, err := core.ParseSeverity(cfg.Security.FailOnCVE); err != nil {
			validationErrors = append(validationErrors, fmt.Sprintf("invalid fail_on_cve %q: %v", cfg.Security.FailOnCVE, err))
		}
	}

	if cfg.SBOM.Format != "" {
		if _, err := core.ParseSBOMFormat(cfg.SBOM.Format); err != nil {
			validationErrors = append(validationErrors, fmt.Sprintf("invalid sbom format %q: %v", cfg.SBOM.Format, err))
		}
	}

	if len(validationErrors) > 0 {
		validationErr := fmt.Errorf("configuration validation failed with %d error(s)", len(validationErrors))
		if outputFormat == ports.FormatJSON {
			_ = jsonutils.WriteError(os.Stdout, "config validate", "ERR_VALIDATION_FAILED", fmt.Sprintf("%v", validationErrors), "")
		} else {
			fmt.Printf("✗ Validation errors in %s:\n", cfgPath)
			for _, errStr := range validationErrors {
				fmt.Printf("  - %s\n", errStr)
			}
		}
		return validationErr
	}

	payload := map[string]interface{}{
		"path":     cfgPath,
		"valid":    true,
		"profiles": len(cfg.Profiles),
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "config validate", payload)
	}

	fmt.Printf("✓ Configuration %s is valid (version %d, %d profiles defined)\n", cfgPath, cfg.Version, len(cfg.Profiles))
	return nil
}
