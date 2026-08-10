package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"
)

func newVersionCommand(_ context.Context, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version, commit, and build date",
		Long:  `Version prints the pokkum version, commit hash, and build date.`,
		Run: func(_ *cobra.Command, _ []string) {
			runVersion(logger)
		},
	}

	return cmd
}

func runVersion(_ *slog.Logger) {
	fmt.Printf("pokkum version %s\n", version)
	fmt.Printf("commit: %s\n", commit)
	fmt.Printf("built: %s\n", buildDate)
}
