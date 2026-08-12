package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

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
previous image reference stored in the manifest annotation (pokkum.dev/previous-image)
or to an explicitly specified reference (--to).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runRollback(ctx, logger, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.file, "file", "f", "", "Manifest file to roll back (required)")
	cmd.Flags().StringVar(&flags.toRef, "to", "", "Target image reference or digest to roll back to (optional, defaults to pokkum.dev/previous-image annotation)")
	cmd.Flags().StringVar(&flags.output, "output", "text", "Output format (text or json)")

	_ = cmd.MarkFlagRequired("file")

	return cmd
}

func runRollback(ctx context.Context, logger *slog.Logger, flags *rollbackFlags) error {
	if flags.file == "" {
		return fmt.Errorf("--file is required: %w", core.ErrInvalidRequest)
	}

	content, err := os.ReadFile(flags.file)
	if err != nil {
		return fmt.Errorf("read manifest file %s: %w", flags.file, err)
	}

	targetRef := strings.TrimSpace(flags.toRef)

	// Regex finding pokkum.dev/previous-image annotation
	annRegex := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(ports.AnnotationPreviousImage) + `:\s*["']?([^\s"']+)["']?`)

	if targetRef == "" {
		matches := annRegex.FindSubmatch(content)
		if len(matches) < 2 {
			return fmt.Errorf("no previous image annotation (%s) found in %s and --to flag was not provided: %w", ports.AnnotationPreviousImage, flags.file, core.ErrInvalidRequest)
		}
		targetRef = string(matches[1])
	}

	// Regex replacing image: <ref> lines in Kubernetes manifests
	imgRegex := regexp.MustCompile(`(?m)^(\s*image:\s*)([^\s#]+)`)
	count := 0
	var displacedRef string

	rewritten := imgRegex.ReplaceAllFunc(content, func(match []byte) []byte {
		parts := imgRegex.FindSubmatch(match)
		if len(parts) >= 3 {
			count++
			if displacedRef == "" {
				displacedRef = string(parts[2])
			}
			var buf bytes.Buffer
			buf.Write(parts[1])
			buf.WriteString(targetRef)
			return buf.Bytes()
		}
		return match
	})

	if count == 0 {
		return fmt.Errorf("no image references found in manifest %s: %w", flags.file, core.ErrInvalidRequest)
	}

	// Update or inject pokkum.dev/previous-image annotation with displacedRef
	if displacedRef != "" && displacedRef != targetRef {
		if annRegex.Match(rewritten) {
			annReplaceRegex := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(ports.AnnotationPreviousImage) + `:\s*)["']?[^\s"']+["']?`)
			rewritten = annReplaceRegex.ReplaceAllFunc(rewritten, func(match []byte) []byte {
				parts := annReplaceRegex.FindSubmatch(match)
				if len(parts) >= 2 {
					var buf bytes.Buffer
					buf.Write(parts[1])
					buf.WriteString(fmt.Sprintf("%q", displacedRef))
					return buf.Bytes()
				}
				return match
			})
		} else {
			// Inject annotation into annotations: or metadata: block if missing
			annIndentRegex := regexp.MustCompile(`(?m)^(\s*)annotations:\s*$`)
			if loc := annIndentRegex.FindIndex(rewritten); loc != nil {
				matches := annIndentRegex.FindSubmatch(rewritten)
				indent := string(matches[1]) + "  "
				line := fmt.Sprintf("\n%s%s: %q", indent, ports.AnnotationPreviousImage, displacedRef)
				var buf bytes.Buffer
				buf.Write(rewritten[:loc[1]])
				buf.WriteString(line)
				buf.Write(rewritten[loc[1]:])
				rewritten = buf.Bytes()
			} else {
				metaIndentRegex := regexp.MustCompile(`(?m)^(\s*)metadata:\s*$`)
				if loc := metaIndentRegex.FindIndex(rewritten); loc != nil {
					matches := metaIndentRegex.FindSubmatch(rewritten)
					indent := string(matches[1]) + "  "
					line := fmt.Sprintf("\n%sannotations:\n%s  %s: %q", indent, indent, ports.AnnotationPreviousImage, displacedRef)
					var buf bytes.Buffer
					buf.Write(rewritten[:loc[1]])
					buf.WriteString(line)
					buf.Write(rewritten[loc[1]:])
					rewritten = buf.Bytes()
				}
			}
		}
	}

	if err := os.WriteFile(flags.file, rewritten, 0o644); err != nil {
		return fmt.Errorf("write rewritten manifest %s: %w", flags.file, err)
	}

	res := RollbackResult{
		File:         flags.file,
		TargetRef:    targetRef,
		Replacements: count,
	}

	if flags.output == "json" {
		envelope := ports.JSONEnvelope{
			SchemaVersion: "1.0",
			Command:       "rollback",
			Status:        "success",
			Data:          res,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(envelope)
	}

	fmt.Printf("Rollback complete for %s: updated %d container image reference(s) to %s\n", flags.file, count, targetRef)
	logger.Info("rollback complete", "file", flags.file, "to", targetRef, "count", count)
	return nil
}
