package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

type rollbackFlags struct {
	file   string
	toRef  string
	output string
}

// RollbackResult holds output details for rollback operations.
type RollbackResult struct {
	File         string `json:"file"`
	TargetRef    string `json:"target_ref"`
	Replacements int    `json:"replacements"`
}

func newRollbackCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &rollbackFlags{}

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Roll back image references in Kubernetes manifests to a previous reference",
		Long: `Rollback updates image references in Kubernetes deployment manifests to point to a
specified previous image digest or tag reference.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runRollback(ctx, logger, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.file, "file", "f", "", "Manifest file to roll back (required)")
	cmd.Flags().StringVar(&flags.toRef, "to", "", "Target image reference or digest to roll back to (required)")
	cmd.Flags().StringVar(&flags.output, "output", "text", "Output format (text or json)")

	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}

func runRollback(ctx context.Context, logger *slog.Logger, flags *rollbackFlags) error {
	if flags.file == "" || flags.toRef == "" {
		return fmt.Errorf("--file and --to are required: %w", core.ErrInvalidRequest)
	}

	content, err := os.ReadFile(flags.file)
	if err != nil {
		return fmt.Errorf("read manifest file %s: %w", flags.file, err)
	}

	// Regex replacing image: <ref> lines in Kubernetes manifests
	imgRegex := regexp.MustCompile(`(?m)^(\s*image:\s*)([^\s#]+)`)
	count := 0
	rewritten := imgRegex.ReplaceAllFunc(content, func(match []byte) []byte {
		parts := imgRegex.FindSubmatch(match)
		if len(parts) >= 3 {
			count++
			var buf bytes.Buffer
			buf.Write(parts[1])
			buf.WriteString(flags.toRef)
			return buf.Bytes()
		}
		return match
	})

	if err := os.WriteFile(flags.file, rewritten, 0o644); err != nil {
		return fmt.Errorf("write rewritten manifest %s: %w", flags.file, err)
	}

	res := RollbackResult{
		File:         flags.file,
		TargetRef:    flags.toRef,
		Replacements: count,
	}

	if flags.output == "json" {
		envelope := ports.JSONEnvelope{
			SchemaVersion: "1.0",
			Command:       "rollback",
			Status:        "ok",
			Data:          res,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(envelope)
	}

	fmt.Printf("Rollback complete for %s: updated %d container image reference(s) to %s\n", flags.file, count, flags.toRef)
	logger.Info("rollback complete", "file", flags.file, "to", flags.toRef, "count", count)
	return nil
}
