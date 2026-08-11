package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var (
	// These variables are set via ldflags during build (-ldflags -X)
	version   string
	commit    string
	buildDate string
)

func main() {
	// Build root context with signal handling for Ctrl-C
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Parse global flags early to set up logging
	// We need to determine log level and format before creating the root command
	logLevel := flag(os.Args, "log-level", "INFO")
	logFormat := flag(os.Args, "log-format", "text")

	// Set up structured logging
	logger := setupLogger(logLevel, logFormat)
	slog.SetDefault(logger)

	// Create and run root command
	rootCmd := newRootCommand(ctx, logger)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		logger.Error("command failed", "error", err)
		os.Exit(1)
	}
}

// flag extracts a flag value from args. Returns the default if not found.
// This is a simple implementation for pre-parsing global flags.
func flag(args []string, name, defaultValue string) string {
	prefix := "--" + name + "="
	for _, arg := range args {
		if len(arg) > len(prefix) && arg[:len(prefix)] == prefix {
			return arg[len(prefix):]
		}
	}
	return defaultValue
}

// setupLogger creates a structured logger from the specified level and format.
func setupLogger(levelStr, format string) *slog.Logger {
	var level slog.Level
	switch levelStr {
	case "DEBUG", "Debug", "debug":
		level = slog.LevelDebug
	case "INFO", "Info", "info":
		level = slog.LevelInfo
	case "WARN", "Warn", "warn":
		level = slog.LevelWarn
	case "ERROR", "Error", "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level}

	if format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}

// newRootCommand creates the root cobra command and registers subcommands.
func newRootCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "pokkum",
		Short: "Build and manage SvelteKit container images",
		Long: `Pokkum compiles SvelteKit applications into minimal, reproducible container images.

It handles the full lifecycle: building the application with Bun, assembling an OCI image
with a hardened base, and publishing to a registry or local Docker daemon.`,
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildDate),

		// A runtime failure (bad config, failed push) is not a usage error, so
		// dumping the help text just buries the real message in CI logs.
		SilenceUsage: true,
		// main() already logs the error through slog; without this cobra prints
		// it a second time on stderr.
		SilenceErrors: true,
	}

	// SilenceUsage above suppresses usage for every error, including genuine
	// flag mistakes where it is actually useful. Restore it for that case only.
	rootCmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		_ = c.Usage()
		return err
	})

	// Add global flags
	rootCmd.PersistentFlags().String("log-level", "INFO", "Log level (DEBUG, INFO, WARN, ERROR)")
	rootCmd.PersistentFlags().String("log-format", "text", "Log format (text or json)")
	rootCmd.PersistentFlags().String("output", "text", "Output serialization format (text or json)")

	// Add subcommands
	rootCmd.AddCommand(newBuildCommand(ctx, logger))
	rootCmd.AddCommand(newDevCommand(ctx, logger))
	rootCmd.AddCommand(newBaseCommand(ctx, logger))
	rootCmd.AddCommand(newResolveCommand(ctx, logger))
	rootCmd.AddCommand(newApplyCommand(ctx, logger))
	rootCmd.AddCommand(newDoctorCommand(ctx, logger))
	rootCmd.AddCommand(newInitCommand(ctx, logger))
	rootCmd.AddCommand(newExplainCommand(ctx, logger))
	rootCmd.AddCommand(newMetricsCommand(ctx, logger))
	rootCmd.AddCommand(newVersionCommand(ctx, logger))

	return rootCmd
}
