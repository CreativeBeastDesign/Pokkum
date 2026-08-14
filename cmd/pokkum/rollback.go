package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

type rollbackFlags struct {
	file       string
	toRef      string
	generation int
	list       bool
	output     string
}

// RollbackResult holds output details for rollback operations.
type RollbackResult struct {
	File         string   `json:"file"`
	TargetRef    string   `json:"target_ref"`
	Replacements int      `json:"replacements"`
	Generation   int      `json:"generation,omitempty"`
	History      []string `json:"history,omitempty"`
}

// RollbackListResult holds list output for rollback history.
type RollbackListResult struct {
	File         string   `json:"file"`
	CurrentImage string   `json:"current_image,omitempty"`
	Generations  []string `json:"generations"`
}

func newRollbackCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &rollbackFlags{}

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Roll back image references in Kubernetes manifests across multiple generations",
		Long: `Rollback updates image references in Kubernetes deployment manifests to point to an
earlier image generation recorded in the manifest history (pokkum.dev/image-history and
pokkum.dev/previous-image) or to an explicitly specified reference (--to).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runRollback(ctx, logger, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.file, "file", "f", "", "Manifest file to roll back (required)")
	cmd.Flags().StringVar(&flags.toRef, "to", "", "Target image reference or digest to roll back to (optional, overrides generation lookup)")
	cmd.Flags().IntVarP(&flags.generation, "generation", "g", 1, "Number of generations back to roll back (default 1 = immediate previous image)")
	cmd.Flags().BoolVar(&flags.list, "list", false, "List available rollback generations recorded in the manifest history")
	cmd.Flags().StringVar(&flags.output, "output", "text", "Output format (text or json)")

	_ = cmd.MarkFlagRequired("file")

	return cmd
}

func runRollback(ctx context.Context, logger *slog.Logger, flags *rollbackFlags) error {
	outputFormat := ports.OutputFormat(flags.output)

	if flags.file == "" {
		return fmt.Errorf("--file is required: %w", core.ErrInvalidRequest)
	}

	content, err := os.ReadFile(flags.file)
	if err != nil {
		return fmt.Errorf("read manifest file %s: %w", flags.file, err)
	}

	// Regex extracting current image: <ref> lines in Kubernetes manifests
	imgRegex := regexp.MustCompile(`(?m)^(\s*image:\s*)([^\s#]+)`)
	var currentImage string
	if match := imgRegex.FindSubmatch(content); len(match) >= 3 {
		currentImage = string(match[2])
	}

	// Regex finding pokkum.dev/image-history annotation
	histRegex := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(ports.AnnotationImageHistory) + `:\s*["']?([^\r\n"']+)["']?`)
	// Regex finding pokkum.dev/previous-image annotation
	annRegex := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(ports.AnnotationPreviousImage) + `:\s*["']?([^\s"']+)["']?`)

	var history []string
	if histMatches := histRegex.FindSubmatch(content); len(histMatches) >= 2 {
		for _, part := range strings.Split(string(histMatches[1]), ",") {
			if p := strings.TrimSpace(part); p != "" {
				history = append(history, p)
			}
		}
	} else if prevMatches := annRegex.FindSubmatch(content); len(prevMatches) >= 2 {
		history = append(history, string(prevMatches[1]))
	}

	if flags.list {
		listRes := RollbackListResult{
			File:         flags.file,
			CurrentImage: currentImage,
			Generations:  history,
		}
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteSuccess(os.Stdout, "rollback", listRes)
		}
		fmt.Printf("Rollback history for %s:\n", flags.file)
		if currentImage != "" {
			fmt.Printf("  Current:    %s\n", currentImage)
		}
		if len(history) == 0 {
			fmt.Println("  (No previous image generations found in manifest annotations)")
		} else {
			for i, gen := range history {
				fmt.Printf("  [%d]         %s (%d generation(s) ago)\n", i+1, gen, i+1)
			}
		}
		return nil
	}

	targetRef := strings.TrimSpace(flags.toRef)
	targetGen := flags.generation
	if targetGen <= 0 {
		targetGen = 1
	}

	if targetRef == "" {
		if len(history) == 0 {
			return fmt.Errorf("no rollback history found in %s and --to flag was not provided: %w", flags.file, core.ErrInvalidRequest)
		}
		if targetGen < 1 || targetGen > len(history) {
			return fmt.Errorf("requested generation %d is out of range (available: 1..%d): %w", targetGen, len(history), core.ErrInvalidRequest)
		}
		targetRef = history[targetGen-1]
	}

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

	// Update history: new previous image is displacedRef; new history prepends displacedRef and drops targetRef
	var updatedHistory []string
	if displacedRef != "" && displacedRef != targetRef {
		updatedHistory = append(updatedHistory, displacedRef)
		for _, h := range history {
			if h != targetRef && h != displacedRef {
				updatedHistory = append(updatedHistory, h)
			}
		}
		if len(updatedHistory) > 20 {
			updatedHistory = updatedHistory[:20]
		}
	} else {
		updatedHistory = history
	}

	// Inject/update pokkum.dev/previous-image
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
			rewritten = injectAnnotation(rewritten, ports.AnnotationPreviousImage, displacedRef)
		}

		// Inject/update pokkum.dev/image-history
		histVal := strings.Join(updatedHistory, ",")
		if histRegex.Match(rewritten) {
			histReplaceRegex := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(ports.AnnotationImageHistory) + `:\s*)["']?[^\r\n"']+["']?`)
			rewritten = histReplaceRegex.ReplaceAllFunc(rewritten, func(match []byte) []byte {
				parts := histReplaceRegex.FindSubmatch(match)
				if len(parts) >= 2 {
					var buf bytes.Buffer
					buf.Write(parts[1])
					buf.WriteString(fmt.Sprintf("%q", histVal))
					return buf.Bytes()
				}
				return match
			})
		} else {
			rewritten = injectAnnotation(rewritten, ports.AnnotationImageHistory, histVal)
		}
	}

	if err := os.WriteFile(flags.file, rewritten, 0o644); err != nil {
		return fmt.Errorf("write rewritten manifest %s: %w", flags.file, err)
	}

	res := RollbackResult{
		File:         flags.file,
		TargetRef:    targetRef,
		Replacements: count,
		Generation:   targetGen,
		History:      updatedHistory,
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "rollback", res)
	}

	fmt.Printf("Rollback complete for %s: updated %d container image reference(s) to %s (generation %d)\n", flags.file, count, targetRef, targetGen)
	logger.Info("rollback complete", "file", flags.file, "to", targetRef, "generation", targetGen, "count", count)
	return nil
}

func injectAnnotation(content []byte, key, val string) []byte {
	annIndentRegex := regexp.MustCompile(`(?m)^(\s*)annotations:\s*$`)
	if loc := annIndentRegex.FindIndex(content); loc != nil {
		matches := annIndentRegex.FindSubmatch(content)
		indent := string(matches[1]) + "  "
		line := fmt.Sprintf("\n%s%s: %q", indent, key, val)
		var buf bytes.Buffer
		buf.Write(content[:loc[1]])
		buf.WriteString(line)
		buf.Write(content[loc[1]:])
		return buf.Bytes()
	}

	metaIndentRegex := regexp.MustCompile(`(?m)^(\s*)metadata:\s*$`)
	if loc := metaIndentRegex.FindIndex(content); loc != nil {
		matches := metaIndentRegex.FindSubmatch(content)
		indent := string(matches[1]) + "  "
		line := fmt.Sprintf("\n%sannotations:\n%s  %s: %q", indent, indent, key, val)
		var buf bytes.Buffer
		buf.Write(content[:loc[1]])
		buf.WriteString(line)
		buf.Write(content[loc[1]:])
		return buf.Bytes()
	}

	return content
}
