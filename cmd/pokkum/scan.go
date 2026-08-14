package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scanner"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

type scanFlags struct {
	failOn    string
	toolchain bool
	output    string
	offline   bool
}

func newScanCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &scanFlags{}

	cmd := &cobra.Command{
		Use:   "scan [target]",
		Short: "Scan a SvelteKit project, container image, or toolchain for vulnerabilities",
		Long: `Scan inspects a project directory, container image, or toolchain dependencies for
known security advisories and CVE vulnerabilities. It supports offline advisory checks
and threshold enforcement via --fail-on.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runScan(ctx, logger, flags, args)
		},
	}

	cmd.Flags().StringVar(&flags.failOn, "fail-on", "critical", "Minimum vulnerability severity threshold causing scan failure (low, medium, high, critical)")
	cmd.Flags().BoolVar(&flags.toolchain, "toolchain", false, "Restrict scan to embedded runtime & toolchain advisories")
	cmd.Flags().StringVar(&flags.output, "output", "text", "Output format (text or json)")
	cmd.Flags().BoolVar(&flags.offline, "offline", false, "Disable remote vulnerability database queries")

	return cmd
}

func runScan(ctx context.Context, logger *slog.Logger, flags *scanFlags, args []string) error {
	target := "."
	if len(args) > 0 {
		target = args[0]
	}

	sev, err := ports.ParseSeverity(flags.failOn)
	if err != nil {
		return err
	}

	adapter := scanner.NewAdapter(logger)
	req := ports.ScanRequest{
		Target:        target,
		FailOn:        sev,
		ToolchainOnly: flags.toolchain,
		Offline:       flags.offline,
	}

	res, scanErr := adapter.Scan(ctx, req)

	if flags.output == "json" {
		envelope := ports.JSONEnvelope{
			SchemaVersion: "1.0",
			Command:       "scan",
			Status:        "success",
			Data:          res,
		}
		if scanErr != nil {
			envelope.Status = "error"
			envelope.Error = &ports.ErrorData{Message: scanErr.Error()}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(envelope); err != nil {
			return fmt.Errorf("json encode: %w", err)
		}
		return scanErr
	}

	// Text format output
	fmt.Printf("Pokkum Security Scan Summary\n")
	fmt.Printf("===========================\n")
	fmt.Printf("Target: %s\n", res.Target)
	fmt.Printf("Status: %t\n", res.Passed)
	fmt.Printf("Max Severity Found: %s\n\n", res.MaxSeverityFound)

	if len(res.Vulnerabilities) > 0 {
		fmt.Printf("Image & OS Package Vulnerabilities (%d):\n", len(res.Vulnerabilities))
		for _, v := range res.Vulnerabilities {
			fixStr := v.FixedVersion
			if fixStr == "" {
				fixStr = "N/A"
			}
			fmt.Printf(" - [%s] %s (%s) %s: %s (Fix: %s)\n", v.Severity, v.Package, v.Ecosystem, v.Version, v.Title, fixStr)
		}
		fmt.Println()
	}

	if len(res.ToolchainAdvisories) > 0 {
		fmt.Printf("Toolchain & Runtime Advisories (%d):\n", len(res.ToolchainAdvisories))
		for _, adv := range res.ToolchainAdvisories {
			fixStr := adv.FixedVersion
			if fixStr == "" {
				fixStr = "N/A"
			}
			fmt.Printf(" - [%s] %s %s: %s (Fix: %s)\n", adv.Severity, adv.Package, adv.Version, adv.Title, fixStr)
		}
		fmt.Println()
	}

	if len(res.Vulnerabilities) == 0 && len(res.ToolchainAdvisories) == 0 {
		fmt.Println("No vulnerabilities or advisories found exceeding threshold.")
	}

	return scanErr
}
