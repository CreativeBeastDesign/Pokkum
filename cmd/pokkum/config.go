package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

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

	// Base (top-level) config fields.
	validationErrors = append(validationErrors, validateConfigFields(configFieldsToValidate{
		strategy:      cfg.Strategy,
		base:          cfg.Base,
		platforms:     cfg.Platforms,
		dockerRepo:    cfg.Docker.Repo,
		dockerTags:    cfg.Docker.Tags,
		failOnCVE:     cfg.Security.FailOnCVE,
		sbomFormat:    cfg.SBOM.Format,
		vexExemptions: cfg.Security.VEXExemptions,
	})...)

	// Every named profile, using the exact same field validation logic as the
	// base config above — a profile with an invalid strategy/base/sbom/repo
	// must not pass validation silently just because only the top-level
	// config was checked. Iterated in sorted order so error output (and test
	// assertions on it) is deterministic despite cfg.Profiles being a map.
	profileNames := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	for _, name := range profileNames {
		profile := cfg.Profiles[name]
		validationErrors = append(validationErrors, validateConfigFields(configFieldsToValidate{
			profileName:   name,
			strategy:      profile.Strategy,
			base:          profile.Base,
			platforms:     profile.Platforms,
			dockerRepo:    profile.Docker.Repo,
			dockerTags:    profile.Docker.Tags,
			failOnCVE:     profile.Security.FailOnCVE,
			sbomFormat:    profile.SBOM.Format,
			vexExemptions: profile.Security.VEXExemptions,
		})...)
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

// configFieldsToValidate carries the subset of ports.ProjectConfig /
// ports.BuildProfile fields that validateConfigFields checks. profileName is
// empty for the base (top-level) config and set to the profile's key
// otherwise, so a single validation implementation serves both call sites in
// runConfigValidate without duplicating the checks.
type configFieldsToValidate struct {
	profileName   string
	strategy      string
	base          string
	platforms     []string
	dockerRepo    string
	dockerTags    []string
	failOnCVE     string
	sbomFormat    string
	vexExemptions []ports.VEXExemptionConfig
}

// validateConfigFields runs the schema/value checks shared by the base
// .pokkum.yaml config and every named profile within it. Errors are prefixed
// with the profile name (`profile "production": ...`) when profileName is
// set, so a user with multiple profiles can tell which one is broken.
func validateConfigFields(f configFieldsToValidate) []string {
	prefix := ""
	if f.profileName != "" {
		prefix = fmt.Sprintf("profile %q: ", f.profileName)
	}

	var errs []string

	if f.strategy != "" && f.strategy != "layered" && f.strategy != "static" && f.strategy != "exe" {
		errs = append(errs, fmt.Sprintf("%sinvalid strategy %q (must be layered or static)", prefix, f.strategy))
	}

	if f.base != "" {
		if _, err := core.ParseBaseImagePreset(f.base); err != nil {
			errs = append(errs, fmt.Sprintf("%sinvalid base preset %q: %v", prefix, f.base, err))
		}
	}

	if len(f.platforms) > 0 {
		if _, err := core.ParsePlatforms(f.platforms); err != nil {
			errs = append(errs, fmt.Sprintf("%sinvalid platforms %v: %v", prefix, f.platforms, err))
		}
	}

	if f.dockerRepo != "" {
		if err := core.ValidateDockerRepo(f.dockerRepo); err != nil {
			errs = append(errs, fmt.Sprintf("%sinvalid docker repo %q: %v", prefix, f.dockerRepo, err))
		}
	}

	if len(f.dockerTags) > 0 {
		if err := core.ValidateDockerTags(f.dockerTags); err != nil {
			errs = append(errs, fmt.Sprintf("%sinvalid docker tags %v: %v", prefix, f.dockerTags, err))
		}
	}

	if f.failOnCVE != "" {
		if _, err := core.ParseSeverity(f.failOnCVE); err != nil {
			errs = append(errs, fmt.Sprintf("%sinvalid fail_on_cve %q: %v", prefix, f.failOnCVE, err))
		}
	}

	if f.sbomFormat != "" {
		if _, err := core.ParseSBOMFormat(f.sbomFormat); err != nil {
			errs = append(errs, fmt.Sprintf("%sinvalid sbom format %q: %v", prefix, f.sbomFormat, err))
		}
	}

	if len(f.vexExemptions) > 0 {
		if _, err := core.ParseVEXExemptions(f.vexExemptions, time.Now()); err != nil {
			errs = append(errs, fmt.Sprintf("%sinvalid vex_exemptions: %v", prefix, err))
		}
	}

	return errs
}
