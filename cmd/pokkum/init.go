package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
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

# Generated output. adapter-node writes to build/ and adapter-static to a
# configured output dir; a previous run's artifacts are not inputs to this
# build, and the copy that actually ships is scanned separately after the
# build. Delete this line if your project keeps real source under build/.
build
`
		if err := os.WriteFile(ignorePath, []byte(defaultContent), 0644); err != nil {
			msg := fmt.Sprintf("failed to create .pokkumignore: %v", err)
			if outputFormat == ports.FormatJSON {
				return failJSON("init", "ERR_INIT_FAILED", msg, "")
			}
			return fmt.Errorf("%s", msg)
		}
		createdIgnore = true
	}

	// 2. .pokkum.yaml creation
	configPath := filepath.Join(opts.dir, ports.ConfigFilename)
	createdConfig := false
	var effectiveCfg *ports.ProjectConfig

	cfgMgr, err := config.New(opts.dir, logger)
	if err != nil {
		msg := fmt.Sprintf("failed to create config manager: %v", err)
		if outputFormat == ports.FormatJSON {
			return failJSON("init", "ERR_INIT_FAILED", msg, "")
		}
		return fmt.Errorf("%s", msg)
	}

	// Look at the project before asking about it. Every default below that can
	// be derived from the source is derived, and the prompts show the evidence
	// rather than a bare list of options.
	analysis := analyzeProject(opts.dir)

	initOpts := ports.InitConfigOptions{
		BasePreset:         string(ports.BaseImageDistroless),
		Strategy:           string(ports.StrategyLayered),
		EnableLocalProfile: true,
	}
	if strategy, _ := analysis.suggestedStrategy(); strategy != "" {
		initOpts.Strategy = strategy
	}
	if analysis.runtime.Runtime != "" {
		initOpts.Runtime = string(analysis.runtime.Runtime)
		if base, _ := sveltekitutils.RecommendedBaseForRuntime(analysis.runtime.Runtime); base != "" {
			initOpts.BasePreset = string(base)
		}
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
			initOpts = promptInitOptions(opts.inReader, initOpts, analysis)
		}

		newCfg := cfgMgr.GenerateDefault(initOpts)

		// Validate what we are about to write, with exactly the checks
		// `pokkum config validate` applies, before it reaches disk.
		//
		// This binary already contained a validator that rejects a bad
		// sbom.attach — init simply never ran it on its own output, so a
		// generated `sbom.attach: attestation` (a real bug, shipped) meant every
		// `pokkum init` produced a config that `pokkum build` refused to start
		// with. The two commands a first-time user runs back to back did not
		// work together, and nothing in the tool noticed even though something
		// in the tool could have.
		//
		// Failing here rather than at the next build keeps the diagnosis at the
		// point of creation, and turns any future generator typo into an
		// immediate, self-inflicted error instead of a user's first impression.
		if problems := validateGeneratedConfig(newCfg); len(problems) > 0 {
			msg := fmt.Sprintf("internal error: generated %s is invalid (%s) — this is a bug in pokkum, not in your project; please report it",
				ports.ConfigFilename, strings.Join(problems, "; "))
			if outputFormat == ports.FormatJSON {
				return failJSON("init", "ERR_INIT_FAILED", msg, "")
			}
			return fmt.Errorf("%s", msg)
		}

		if err := cfgMgr.Save(opts.dir, newCfg); err != nil {
			msg := fmt.Sprintf("failed to create %s: %v", ports.ConfigFilename, err)
			if outputFormat == ports.FormatJSON {
				return failJSON("init", "ERR_INIT_FAILED", msg, "")
			}
			return fmt.Errorf("%s", msg)
		}
		createdConfig = true
		effectiveCfg = newCfg
	}

	// For an existing config, load it so the closing advice is right there too —
	// re-running init on a configured project should not hand out advice derived
	// from defaults it did not write.
	if effectiveCfg == nil {
		if loaded, loadErr := cfgMgr.Load(opts.dir); loadErr == nil {
			effectiveCfg = loaded
		}
	}
	nextCmd, nextWhy := suggestedNextCommand(effectiveCfg)

	payload := map[string]interface{}{
		"directory":      opts.dir,
		"created_ignore": createdIgnore,
		"ignore_path":    ignorePath,
		"created_config": createdConfig,
		"config_path":    configPath,
		"status":         "initialized",
		"next_command":   nextCmd,
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
	fmt.Printf("✓ Pokkum initialization complete. You can now run `%s`.\n", nextCmd)
	if nextWhy != "" {
		fmt.Printf("  %s\n", nextWhy)
	}

	return nil
}

// promptChoice asks for a value constrained to a fixed set, re-asking on an
// unrecognised answer instead of accepting it.
//
// The prompts used to take whatever was typed, verbatim, so a typo — or one of
// the options the prompt itself offered but the code did not implement — went
// straight into .pokkum.yaml and surfaced much later as a build failure a long
// way from its cause. Empty input keeps the default.
//
// Returns "" when input runs out (a piped/EOF session), which callers treat as
// "leave the default in place" rather than as a choice.
// suggestedNextCommand returns the command to run next, and an optional line
// explaining why it differs from the obvious one.
//
// This used to be the constant "pokkum build", which is wrong for exactly the
// setup init's own first prompt invites: that prompt offers "empty for local
// only", and accepting it leaves no destination repository, so plain
// `pokkum build` — whose default output mode is push — fails immediately with
// "destination repository is required in push mode". init was recommending a
// command it had just guaranteed could not work.
//
// Reported from a real first run, and the same shape as the generated-config bug
// before it: two things that are individually correct and do not compose. Advice
// is output too, and output that the next command rejects is a defect.
func suggestedNextCommand(cfg *ports.ProjectConfig) (string, string) {
	if cfg == nil || strings.TrimSpace(cfg.Docker.Repo) != "" {
		return "pokkum build", ""
	}
	// No destination repository. If init also wrote a local profile, that is the
	// path that actually works; otherwise say what is missing rather than
	// naming a command that will fail.
	if _, ok := cfg.Profiles["local"]; ok {
		return "pokkum build --local",
			"No registry is configured, so --local builds into your container runtime instead of pushing. " +
				"Set docker.repo in " + ports.ConfigFilename + " (or POKKUM_DOCKER_REPO) when you want to push."
	}
	return "pokkum build --local",
		"No registry is configured, so plain `pokkum build` (which pushes) will refuse to start. " +
			"Set docker.repo in " + ports.ConfigFilename + " or POKKUM_DOCKER_REPO to push instead."
}

func promptChoice(scanner *bufio.Scanner, number int, label string, allowed []string, def string) string {
	for attempts := 0; attempts < 5; attempts++ {
		// number 0 means the caller already printed its own numbered header
		// (the base-image prompt prints one line per preset above the
		// question), so the prompt line carries the label alone rather than
		// repeating the number and title under them.
		if number == 0 {
			fmt.Printf("   %s [%s] (default: %s): ", label, strings.Join(allowed, " / "), def)
		} else {
			fmt.Printf("%d. %s [%s] (default: %s): ", number, label, strings.Join(allowed, " / "), def)
		}
		if !scanner.Scan() {
			return ""
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			return ""
		}
		if slices.Contains(allowed, strings.ToLower(input)) {
			return strings.ToLower(input)
		}
		fmt.Printf("   %q is not one of: %s — please pick one of those.\n", input, strings.Join(allowed, ", "))
	}
	fmt.Printf("   Too many unrecognised answers; keeping the default (%s).\n", def)
	return ""
}

// promptInitOptions asks the questions the project cannot answer for itself.
//
// defaults arrives pre-filled from analyzeProject, so each prompt offers what
// the project implies rather than a constant, and the findings above it explain
// why. Answers still override everything.
func promptInitOptions(r io.Reader, defaults ports.InitConfigOptions, analysis projectAnalysis) ports.InitConfigOptions {
	scanner := bufio.NewScanner(r)
	opts := defaults

	fmt.Println("=== Interactive Pokkum Project Setup ===")
	fmt.Println()
	fmt.Println("Looked at your project first:")
	writeStaticFindings(os.Stdout, analysis)
	writeRuntimeFinding(os.Stdout, analysis.runtime)
	fmt.Println()

	// Prompt numbers are a running counter, not literals: the runtime question
	// is skipped for strategy=static, and a hardcoded sequence then presents
	// the user with 1, 2, 4, 5, 6 — a gap that reads as a bug in the tool.
	n := 0
	next := func() int { n++; return n }

	// Docker Repo
	fmt.Printf("%d. Target Container Registry (e.g. ghcr.io/example/my-app, or empty for local only) []: ", next())
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" {
			opts.Repo = input
		}
	}

	// Strategy, asked before runtime and base because it constrains both.
	//
	// exe is deliberately not offered here — it is an advanced, single-binary
	// mode rather than a sensible default for a new project — but it stays
	// accepted in a hand-written config.
	if v := promptChoice(scanner, next(), "Build Strategy",
		[]string{string(ports.StrategyLayered), string(ports.StrategyStatic)}, opts.Strategy); v != "" {
		opts.Strategy = v
	}

	// Runtime — asked ONLY for layered.
	//
	// A static image ships no JavaScript runtime at all (pokkum-static serves
	// the prerendered tree), and core rejects runtime=node outside
	// strategy=layered. Asking a question whose every answer is either
	// meaningless or invalid is how a prompt talks a user into a config that
	// does not build; the two previous init bugs were both this shape.
	if opts.Strategy == string(ports.StrategyLayered) {
		if v := promptChoice(scanner, next(), "Application Runtime",
			[]string{string(ports.RuntimeBun), string(ports.RuntimeNode)},
			runtimePromptDefault(opts.Runtime)); v != "" {
			opts.Runtime = v
			if base, _ := sveltekitutils.RecommendedBaseForRuntime(ports.AppRuntime(v)); base != "" {
				// Keep the paired base in step with an explicit runtime answer:
				// runtime=node on a base with no Node binary is rejected by
				// core, so the answer to prompt 3 changes the default for
				// prompt 4, which prompt 4 then still lets the user override.
				opts.BasePreset = string(base)
			}
		}
	} else {
		// Not asked, so not written. Leaving a stale detected runtime in place
		// would emit `runtime: node` next to `strategy: static`.
		opts.Runtime = ""
	}

	// Base image preset. The offered set is exactly the presets that exist:
	// this prompt used to offer "chainguard-static", which is an unimplemented
	// roadmap item rather than a preset, so anyone picking option 3 got a
	// .pokkum.yaml that pokkum build refused — and it omitted distroless-node,
	// which is real.
	presets := initBasePresets()
	allowed := make([]string, 0, len(presets))
	for _, p := range presets {
		allowed = append(allowed, string(p))
	}
	fmt.Printf("%d. Base Image Preset:\n", next())
	for _, p := range presets {
		marker := " "
		if string(p) == opts.BasePreset {
			marker = "*"
		}
		fmt.Printf("   %s %-18s %s\n", marker, p, basePresetAdvice(p))
	}
	if v := promptChoice(scanner, 0, "Choose", allowed, opts.BasePreset); v != "" {
		opts.BasePreset = v
	}

	// Local profile
	fmt.Printf("%d. Configure local development profile (--local)? [Y/n] (default: Y): ", next())
	if scanner.Scan() {
		input := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if input == "n" || input == "no" {
			opts.EnableLocalProfile = false
		}
	}

	// CVE policy
	if v := promptChoice(scanner, next(), "Fail build on vulnerability threshold",
		[]string{"none", "low", "medium", "high", "critical"}, "none"); v != "" {
		opts.FailOnCVE = v
	}

	fmt.Println()
	return opts
}

// runtimePromptDefault renders the runtime default for the prompt line. An
// empty Runtime means "the port default", and showing the real name is more
// useful to a reader than showing a blank.
func runtimePromptDefault(runtime string) string {
	if runtime == "" {
		return string(ports.DefaultAppRuntime)
	}
	return runtime
}
