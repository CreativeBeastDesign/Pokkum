package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
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
	logFormat := flag(os.Args, "log-format", "auto")

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
//
// This pre-parse exists because the logger must be configured before cobra
// parses anything, so --log-level/--log-format cannot be read back off the
// command they are registered on. It therefore has to understand the same
// two spellings pflag does:
//
//	--log-format=json   (attached form)
//	--log-format json   (separated form)
//
// It handled only the attached form until 2026-08-23. Both flags are
// registered as ordinary cobra persistent flags, so the separated form parsed
// without error and then had no effect whatsoever — `pokkum build --log-format
// json` silently emitted text logs. That is what made the GitHub Action's
// digest/ref outputs come back empty on every run; see Lessons.md.
//
// Two restrictions bound the separated form, since this scans raw os.Args
// with no knowledge of the flag set:
//
//   - a bare "--" ends flag parsing, per convention, so a project directory
//     named like a flag cannot be mistaken for one;
//   - the following argument must not itself look like a flag, so a trailing
//     "--log-format" with nothing after it falls back to the default rather
//     than consuming the next flag as its value.
//
// One case is knowingly out of reach: passing the literal string
// "--log-format" as the value of some *other* value-taking flag (e.g.
// `--exclude-route --log-format json`) is read here as a --log-format. Ruling
// that out requires knowing which flags take values, which is precisely the
// information cobra has not parsed yet at this point. The blast radius is one
// wrong log format on an argument nobody has a reason to pass, so it is
// accepted rather than papered over with a partial flag table that would
// drift from the real one.
func flag(args []string, name, defaultValue string) string {
	attached := "--" + name + "="
	separated := "--" + name
	for i, arg := range args {
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, attached) {
			return arg[len(attached):]
		}
		if arg == separated && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args[i+1]
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

	switch format {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, opts)
	case "text":
		// Explicitly requested: always logfmt, even on a terminal. This is the
		// value scripts and CI pin when they parse the output.
		handler = slog.NewTextHandler(os.Stderr, opts)
	case "console":
		// Explicitly requested: always the human renderer, even when piped
		// (useful for `pokkum build 2>&1 | less -R`).
		_, color := consoleRenderingWanted(os.Stderr)
		handler = newConsoleHandler(os.Stderr, level, color)
	default:
		// "auto" and anything unrecognised: render for a human only when
		// stderr is definitely an interactive terminal, and keep byte-identical
		// logfmt everywhere else so log parsers and CI are unaffected.
		if console, color := consoleRenderingWanted(os.Stderr); console {
			handler = newConsoleHandler(os.Stderr, level, color)
		} else {
			handler = slog.NewTextHandler(os.Stderr, opts)
		}
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

		// Validate --output centrally. It is a persistent flag that four
		// subcommands also redefine locally, and every consumer compares it
		// against FormatJSON and falls through to text, so an unrecognised value
		// is silently ignored — `--output=jsonl` hands a script text output with
		// no warning. cmd.Flags() resolves the executing command's effective
		// flag, so the locally-redefined copies are covered too.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if f := cmd.Flags().Lookup("output"); f != nil {
				if _, err := ports.ParseOutputFormat(f.Value.String()); err != nil {
					return err
				}
			}
			return nil
		},
	}

	// SilenceUsage above suppresses usage for every error, including genuine
	// flag mistakes where it is actually useful. Restore it for that case only.
	rootCmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		_ = c.Usage()
		return err
	})

	// Add global flags
	rootCmd.PersistentFlags().String("log-level", "INFO", "Log level (DEBUG, INFO, WARN, ERROR)")
	rootCmd.PersistentFlags().String("log-format", "auto", "Log format: auto (human-readable on a terminal, text otherwise), console, text, or json")
	rootCmd.PersistentFlags().String("output", "text", "Output serialization format (text or json)")

	// Add subcommands
	rootCmd.AddCommand(newBuildCommand(ctx, logger))
	rootCmd.AddCommand(newDevCommand(ctx, logger))
	rootCmd.AddCommand(newBaseCommand(ctx, logger))
	rootCmd.AddCommand(newResolveCommand(ctx, logger))
	rootCmd.AddCommand(newApplyCommand(ctx, logger))
	rootCmd.AddCommand(newDoctorCommand(ctx, logger))
	rootCmd.AddCommand(newInitCommand(ctx, logger))
	rootCmd.AddCommand(newConfigCommand(ctx, logger))
	rootCmd.AddCommand(newAdoptCommand(ctx, logger))
	rootCmd.AddCommand(newExplainCommand(ctx, logger))
	rootCmd.AddCommand(newScanCommand(ctx, logger))
	rootCmd.AddCommand(newHistoryCommand(ctx, logger))
	rootCmd.AddCommand(newRollbackCommand(ctx, logger))
	rootCmd.AddCommand(newUpgradeCommand(ctx, logger, cosign.NewSigner(logger)))
	rootCmd.AddCommand(newVerifyCommand(ctx, logger))

	rootCmd.AddCommand(newReproDoctorCommand(ctx, logger))
	rootCmd.AddCommand(newVersionCommand(ctx, logger))
	rootCmd.AddCommand(newHermeticReexecCommand(ctx, logger))

	return rootCmd
}
