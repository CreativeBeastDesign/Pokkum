package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/lockfileutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scanner"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type doctorOptions struct {
	fix    bool
	dir    string
	output string
}

func newDoctorCommand(_ context.Context, logger *slog.Logger) *cobra.Command {
	opts := &doctorOptions{}

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run environment preflight checks and diagnostics",
		Long:  `Doctor checks Bun binary presence, registry credentials, SvelteKit version compatibility, and .pokkumignore sanity. Use --fix to automatically apply mechanical repairs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			return runDoctor(logger, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.fix, "fix", false, "Automatically repair mechanical issues (e.g. create missing .pokkumignore)")
	cmd.Flags().StringVarP(&opts.dir, "dir", "d", ".", "Path to SvelteKit project directory")

	return cmd
}

func runDoctor(logger *slog.Logger, opts *doctorOptions) error {
	outputFormat := ports.OutputFormat(opts.output)
	checks := []ports.DoctorCheck{}
	allPassed := true

	// Check 1: Bun runtime
	bunCheck := checkBunRuntime()
	checks = append(checks, bunCheck)
	if !bunCheck.Passed {
		allPassed = false
	}

	// Check 2: SvelteKit workspace
	skCheck := checkSvelteKitWorkspace(opts.dir)
	checks = append(checks, skCheck)
	if !skCheck.Passed {
		allPassed = false
	}

	// Check 3: SvelteKit adapter (must agree with bunexec's build preflight —
	// see checkSvelteKitAdapter's doc comment)
	adapterCheck := checkSvelteKitAdapter(opts.dir, logger)
	checks = append(checks, adapterCheck)
	if !adapterCheck.Passed {
		allPassed = false
	}

	// Check 4: Registry Auth Credentials
	authCheck := checkRegistryAuth()
	checks = append(checks, authCheck)
	if !authCheck.Passed {
		allPassed = false
	}

	// Check 5: .pokkumignore file
	ignoreCheck := checkPokkumIgnore(opts.dir, opts.fix)
	checks = append(checks, ignoreCheck)
	if !ignoreCheck.Passed {
		allPassed = false
	}

	// Check 6: Base image security & CVEs
	baseCheck := checkBaseImageSecurity(opts.dir, logger)
	checks = append(checks, baseCheck)
	if !baseCheck.Passed {
		allPassed = false
	}

	summary := "All environment preflight checks passed successfully."
	if !allPassed {
		summary = "One or more preflight checks failed or require attention."
	}

	doctorPayload := ports.DoctorOutput{
		Passed:  allPassed,
		Checks:  checks,
		Summary: summary,
	}

	if outputFormat == ports.FormatJSON {
		// Both the pass and fail cases carry the full doctorPayload (its
		// per-check Checks array, each with Name/Passed/Message/Remediation)
		// in Data — matching how `scan` reports its findings on both paths.
		// Before this, a failing doctor run went through jsonutils.WriteError,
		// which only carries a summary string and a code: a machine consumer
		// got per-check structure exactly when everything passed and nothing
		// needed reading, and lost it — including the SvelteKit Adapter
		// check's Remediation, the exact `bun add -D` command to run — in the
		// one case where it needed to know which check failed and how to fix
		// it. Status/Error mirror WriteError's shape so existing "status" ==
		// "error" consumers keep working; doctorPayload.Passed remains the
		// authoritative top-level pass/fail field either way.
		envelope := ports.JSONEnvelope{
			SchemaVersion: jsonutils.CurrentSchemaVersion,
			Command:       "doctor",
			Status:        "success",
			Data:          doctorPayload,
		}
		if !allPassed {
			envelope.Status = "error"
			envelope.Error = &ports.ErrorData{
				Code:    "ERR_DOCTOR_FAILED",
				Message: summary,
			}
		}
		// Marshal + Fprintln, mirroring jsonutils.FormatSuccess/WriteSuccess
		// exactly (same indent, same trailing newline via Fprintln) so this
		// is byte-for-byte what WriteSuccess already produced on the pass
		// path. Returning the write error (not a "doctor failed" error)
		// preserves today's JSON-mode exit code on both paths: a failing
		// `doctor --output json` run exits the same way it does right now.
		bytes, err := json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			return fmt.Errorf("jsonutils adapter: failed to marshal doctor payload: %w", err)
		}
		if _, err = fmt.Fprintln(os.Stdout, string(bytes)); err != nil {
			return err
		}
		// A red doctor must exit non-zero in BOTH output modes. Until this
		// return existed, `--output json` wrote status:"error", passed:false
		// and a list of failing checks -- and then exited 0, so a CI step
		// gating on `pokkum doctor --output json` passed while doctor was red.
		// Text mode had always exited 1; only the JSON branch returned early,
		// before the shared failure signal at the end of this function.
		//
		// The error is silent because the envelope above is already the
		// complete report; a second, less informative line on stderr would add
		// nothing and would contradict the structured output above it.
		if !allPassed {
			return errDoctorChecksFailed
		}
		return nil
	}

	// Text output mode
	fmt.Println("=== Pokkum Environment Preflight Diagnostics ===")
	fmt.Println()
	for _, c := range checks {
		statusStr := "[✓ PASS]"
		if !c.Passed {
			statusStr = "[✗ FAIL]"
		}
		fmt.Printf("%s %s: %s\n", statusStr, c.Name, c.Message)
		if c.Remediation != "" && !c.Passed {
			fmt.Printf("   -> Remediation: %s\n", c.Remediation)
		}
	}
	fmt.Println()
	fmt.Printf("Result: %s\n", summary)

	if !allPassed {
		return fmt.Errorf("doctor preflight checks failed")
	}
	return nil
}

// errDoctorChecksFailed signals a red doctor in JSON mode without printing a
// second message: the envelope on stdout is already the complete report.
// Reuses silentExitError (deploy_check.go), which main.go's isSilentExit
// recognises, so the process exits 1 with no redundant log line.
var errDoctorChecksFailed = &silentExitError{}

func checkBunRuntime() ports.DoctorCheck {
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		return ports.DoctorCheck{
			Name:        "Bun Runtime",
			Passed:      false,
			Message:     "bun executable not found in PATH",
			Remediation: "Install Bun (https://bun.sh) or pass --bun-binary=<path>",
		}
	}

	out, err := exec.Command(bunPath, "--version").Output()
	if err != nil {
		return ports.DoctorCheck{
			Name:        "Bun Runtime",
			Passed:      false,
			Message:     fmt.Sprintf("found bun at %s but failed to execute bun --version", bunPath),
			Remediation: "Ensure bun binary is executable for the current user",
		}
	}

	ver := strings.TrimSpace(string(out))
	return ports.DoctorCheck{
		Name:    "Bun Runtime",
		Passed:  true,
		Message: fmt.Sprintf("bun binary found (%s, v%s)", bunPath, ver),
	}
}

func checkSvelteKitWorkspace(dir string) ports.DoctorCheck {
	pkgPath := filepath.Join(dir, "package.json")
	bytes, err := os.ReadFile(pkgPath)
	if err != nil {
		return ports.DoctorCheck{
			Name:        "SvelteKit Workspace",
			Passed:      false,
			Message:     fmt.Sprintf("package.json not found at %s", pkgPath),
			Remediation: "Run pokkum doctor inside a SvelteKit project directory",
		}
	}

	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}

	if err := json.Unmarshal(bytes, &pkg); err != nil {
		return ports.DoctorCheck{
			Name:        "SvelteKit Workspace",
			Passed:      false,
			Message:     "package.json is not valid JSON",
			Remediation: "Fix syntax errors in package.json",
		}
	}

	var skVersion string
	if v, ok := pkg.Dependencies["@sveltejs/kit"]; ok {
		skVersion = v
	} else if v, ok := pkg.DevDependencies["@sveltejs/kit"]; ok {
		skVersion = v
	}

	if skVersion == "" {
		return ports.DoctorCheck{
			Name:        "SvelteKit Workspace",
			Passed:      false,
			Message:     "@sveltejs/kit not found in package.json dependencies",
			Remediation: "Install @sveltejs/kit in your project (`bun add -d @sveltejs/kit`)",
		}
	}

	return ports.DoctorCheck{
		Name:    "SvelteKit Workspace",
		Passed:  true,
		Message: fmt.Sprintf("valid SvelteKit project detected (@sveltejs/kit %s)", skVersion),
	}
}

// checkSvelteKitAdapter resolves the SvelteKit adapter that will actually
// govern a real `pokkum build` and reports it as a doctor check, with the
// same remediation build's own preflight gives.
//
// This check exists because, before it did, `pokkum doctor` only verified
// @sveltejs/kit was a dependency (checkSvelteKitWorkspace) and said nothing
// about which adapter was actually configured. A fresh `sv create` scaffold —
// @sveltejs/adapter-auto only, which produces no build output pokkum can
// package — passed doctor cleanly and was refused by `pokkum build`
// immediately after. Doctor's entire purpose is to catch exactly this before
// anything expensive runs, and it did not.
//
// The verdict is derived from the exact same decision function bunexec's own
// build preflight uses — sveltekitutils.EffectiveAdapterConfigured, called
// with the exact same three inputs bunexec.Compiler.Prepare's
// checkEffectiveAdapter would use for this project (the strategy's required
// adapter package from ports.BuildStrategy.RequiredAdapterPackage,
// svelte.config.js's raw source, and whichever bunexec.ViteConfigCandidates
// file governs) — never a second, independently-written comparison. That
// sharing is the actual fix: before it, the only way to make doctor agree
// with build would have been to keep two hand-written copies of "is the
// adapter configured" in sync by hand, which is exactly the kind of drift
// this check exists to prevent (see Lessons.md, 2026-08-19, "Preflight again
// made an independent, untested assumption about the target adapter").
//
// It deliberately reports neither a clean pass nor a confident failure when
// it cannot actually read what it needs to decide. An absent
// svelte.config.js is not evidence of anything by itself — current
// `sv create` scaffolds ship none at all, configuring the adapter entirely
// through vite.config.ts instead (see EffectiveAdapterConfigured's own doc
// comment) — so that case still yields a normal, confident PASS or FAIL.
// What it will not do is turn a *present-but-unreadable* config file, or a
// .pokkum.yaml whose strategy: value it cannot recognize, into either
// verdict: doctor genuinely does not know which adapter is required or what
// the governing file says in that case, and guessing would be exactly the
// false confidence this whole check exists to remove — the same shape
// checkBaseImageSecurity above already handles the same way for an
// unreachable vulnerability database (Passed: false, worded as "could not
// complete the check", not as a confirmed problem).
func checkSvelteKitAdapter(dir string, logger *slog.Logger) ports.DoctorCheck {
	const name = "SvelteKit Adapter"

	strategy, strategyErr := resolveDoctorBuildStrategy(dir, logger)
	if strategyErr != nil {
		return ports.DoctorCheck{
			Name:    name,
			Passed:  false,
			Message: fmt.Sprintf("cannot determine the effective SvelteKit adapter: %v", strategyErr),
			Remediation: fmt.Sprintf(
				"fix `strategy:` in %s (supported: layered, exe, static), or run `pokkum build` directly for a definitive check",
				ports.ConfigFilename,
			),
		}
	}
	targetAdapter := strategy.RequiredAdapterPackage()

	svelteSource, svelteErr := readIfExists(filepath.Join(dir, "svelte.config.js"))
	if svelteErr != nil {
		return ports.DoctorCheck{
			Name:        name,
			Passed:      false,
			Message:     fmt.Sprintf("cannot determine the effective SvelteKit adapter: svelte.config.js exists but could not be read: %v", svelteErr),
			Remediation: "check svelte.config.js's file permissions, or run `pokkum build` directly for a definitive check",
		}
	}

	viteName, viteSource, viteErr := readEffectiveViteConfig(dir)
	if viteErr != nil {
		return ports.DoctorCheck{
			Name:        name,
			Passed:      false,
			Message:     fmt.Sprintf("cannot determine the effective SvelteKit adapter: %s exists but could not be read: %v", viteName, viteErr),
			Remediation: fmt.Sprintf("check %s's file permissions, or run `pokkum build` directly for a definitive check", viteName),
		}
	}

	configured, readFrom, overridden := sveltekitutils.EffectiveAdapterConfigured(svelteSource, viteSource, viteName, targetAdapter)
	if configured {
		return ports.DoctorCheck{
			Name:    name,
			Passed:  true,
			Message: fmt.Sprintf("%s is configured as the effective adapter for --strategy=%s (read from %s)", targetAdapter, strategy, readFrom),
		}
	}

	if overridden {
		alsoInSvelteConfig := ""
		if sveltekitutils.AdapterConfigured(svelteSource, targetAdapter) {
			alsoInSvelteConfig = " (svelte.config.js does reference it, but SvelteKit never reads that file here)"
		}
		return ports.DoctorCheck{
			Name:   name,
			Passed: false,
			Message: fmt.Sprintf(
				"--strategy=%s requires %s, but %s passes options to its sveltekit() plugin call, so SvelteKit ignores svelte.config.js entirely (\"svelte.config.js is ignored when options are passed via your Vite config\") and takes the adapter from %s — which does not reference %s%s",
				strategy, targetAdapter, readFrom, readFrom, targetAdapter, alsoInSvelteConfig,
			),
			Remediation: fmt.Sprintf(
				"fix it in %s: run `bun add -D %s`, then `import adapter from '%s'` and pass `adapter: adapter()` inside sveltekit({ ... }). If the adapter is re-exported from a local module, import it directly so pokkum can see it.",
				readFrom, targetAdapter, targetAdapter,
			),
		}
	}

	return ports.DoctorCheck{
		Name:   name,
		Passed: false,
		Message: fmt.Sprintf(
			"--strategy=%s requires %s, but svelte.config.js does not reference it (a fresh `sv create` project ships @sveltejs/adapter-auto, which does not produce the build output pokkum packages)",
			strategy, targetAdapter,
		),
		Remediation: fmt.Sprintf(
			"fix it in svelte.config.js: run `bun add -D %s`, then `import adapter from '%s'` and set `kit.adapter: adapter()`. If the adapter is re-exported from a local module, import it directly so pokkum can see it.",
			targetAdapter, targetAdapter,
		),
	}
}

// resolveDoctorBuildStrategy determines the strategy a real `pokkum build`
// would use for dir: .pokkum.yaml's top-level `strategy:` when the file is
// present, parseable, and sets a recognized value; ports.DefaultBuildStrategy
// otherwise (matching core.BuildRequest.Normalize()'s own default). It does
// not apply --profile overrides — doctor takes no --profile flag, so this
// deliberately checks the base config, the same one every profile inherits
// from and the one a bare `pokkum build` (no --profile) actually uses.
//
// An existing-but-unparseable .pokkum.yaml, or a strategy: value that is not
// one of layered/exe/static, is reported as an error rather than silently
// falling back to the default: silently defaulting could report an adapter
// requirement the project's own config explicitly says is wrong.
func resolveDoctorBuildStrategy(dir string, logger *slog.Logger) (ports.BuildStrategy, error) {
	mgr, err := config.New(dir, logger)
	if err != nil {
		return "", fmt.Errorf("load %s: %w", ports.ConfigFilename, err)
	}

	projCfg, err := mgr.Load(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ports.DefaultBuildStrategy, nil
		}
		return "", fmt.Errorf("%s: %w", ports.ConfigFilename, err)
	}

	raw := strings.TrimSpace(projCfg.Strategy)
	if raw == "" {
		return ports.DefaultBuildStrategy, nil
	}

	strategy := ports.BuildStrategy(raw)
	if !strategy.Valid() {
		return "", fmt.Errorf("%s sets strategy: %q, which is not a recognized build strategy (supported: layered, exe, static)", ports.ConfigFilename, projCfg.Strategy)
	}
	return strategy, nil
}

// readIfExists reads path, returning ("", nil) when it does not exist —
// absence is a normal, common project shape here, never an error — and
// ("", err) for any other read failure (permission denied, path is a
// directory, ...), which callers must treat as "cannot determine" rather
// than silently agreeing with content they never actually saw.
func readIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", err
}

// readEffectiveViteConfig finds the Vite config file that would actually
// govern dir, in bunexec.ViteConfigCandidates' resolution order (Vite's own
// precedence — the first candidate that exists on disk is the one Vite
// loads), and reads it.
//
// Existence is checked with os.Stat rather than folding it into the read
// itself, so that a candidate which exists but cannot be read (permission
// denied) is reported as "the file that governs could not be read" rather
// than silently skipped in favor of a lower-priority candidate — unlike
// bunexec's own internal readViteConfigSource, which only needs "some
// readable Vite config, or none" for a real build and treats any read error
// the same as absence. Doctor additionally needs to tell "no Vite config"
// apart from "the governing one could not be read", since only the latter is
// the honest-uncertainty case this check must not paper over.
func readEffectiveViteConfig(dir string) (name, source string, err error) {
	for _, candidate := range bunexec.ViteConfigCandidates {
		path := filepath.Join(dir, candidate)
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return candidate, "", readErr
		}
		return candidate, string(data), nil
	}
	return "", "", nil
}

func checkRegistryAuth() ports.DoctorCheck {
	home, err := os.UserHomeDir()
	if err != nil {
		return ports.DoctorCheck{
			Name:    "Registry Authentication",
			Passed:  true,
			Message: "unspecified (user home directory unavailable)",
		}
	}

	configPath := filepath.Join(home, ".docker", "config.json")
	if _, err := os.Stat(configPath); err == nil {
		return ports.DoctorCheck{
			Name:    "Registry Authentication",
			Passed:  true,
			Message: fmt.Sprintf("Docker auth config found at %s", configPath),
		}
	}

	return ports.DoctorCheck{
		Name:        "Registry Authentication",
		Passed:      true,
		Message:     "no ~/.docker/config.json found (will rely on environment variables or anonymous access)",
		Remediation: "Run docker login if pushing to private registries",
	}
}

func checkPokkumIgnore(dir string, fix bool) ports.DoctorCheck {
	ignorePath := filepath.Join(dir, ".pokkumignore")
	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		if fix {
			defaultIgnore := ".env\n.env.*\nnode_modules\n.git\n.pokkum\n"
			if err := os.WriteFile(ignorePath, []byte(defaultIgnore), 0644); err == nil {
				return ports.DoctorCheck{
					Name:    ".pokkumignore Exclude List",
					Passed:  true,
					Message: "created missing .pokkumignore with default exclude rules (--fix)",
				}
			}
		}

		return ports.DoctorCheck{
			Name:        ".pokkumignore Exclude List",
			Passed:      false,
			Message:     ".pokkumignore file is missing",
			Remediation: "Run `pokkum doctor --fix` or create a .pokkumignore file to exclude sensitive files like .env",
		}
	}

	return ports.DoctorCheck{
		Name:    ".pokkumignore Exclude List",
		Passed:  true,
		Message: ".pokkumignore file present",
	}
}

func checkBaseImageSecurity(dir string, logger *slog.Logger) ports.DoctorCheck {
	lockPath := filepath.Join(dir, ports.PokkumLockfileName)
	lf, err := lockfileutils.LoadLockfile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ports.DoctorCheck{
				Name:    "Base Image Security & CVEs",
				Passed:  true,
				Message: "no pokkum.lock present (will be pinned and verified on first build)",
			}
		}
		return ports.DoctorCheck{
			Name:        "Base Image Security & CVEs",
			Passed:      false,
			Message:     fmt.Sprintf("failed to parse %s: %v", lockPath, err),
			Remediation: "Re-generate lockfile with `pokkum base update`",
		}
	}

	adapter := scanner.NewAdapter(logger)
	var vulnerableBases []string
	var incompleteBases []string

	for name, entry := range lf.Bases {
		target := entry.PinnedRef
		if target == "" {
			target = entry.Ref
		}
		if target == "" {
			continue
		}

		res, err := adapter.Scan(context.Background(), ports.ScanRequest{
			Target:  target,
			FailOn:  ports.SeverityCritical,
			Offline: false,
		})
		switch {
		case errors.Is(err, core.ErrScanIncomplete):
			if entry.LastScannedAt != "" {
				if entry.VulnerabilitiesCount > 0 {
					if sev, err := ports.ParseSeverity(entry.MaxSeverity); err == nil {
						if sev == ports.SeverityCritical {
							vulnerableBases = append(vulnerableBases, fmt.Sprintf("%s (%s: %s [cached audit from %s])", name, target, entry.MaxSeverity, entry.LastScannedAt))
						}
					} else {
						incompleteBases = append(incompleteBases, fmt.Sprintf("%s (%s: invalid severity %q)", name, target, entry.MaxSeverity))
					}
				}
			} else {
				incompleteBases = append(incompleteBases, fmt.Sprintf("%s (%s)", name, target))
			}
		case err != nil || !res.Passed:
			vulnerableBases = append(vulnerableBases, fmt.Sprintf("%s (%s: %s)", name, target, res.MaxSeverityFound))
		}
	}

	if len(vulnerableBases) > 0 {
		msg := fmt.Sprintf("critical security vulnerabilities detected in locked base image(s): %s", strings.Join(vulnerableBases, ", "))
		if len(incompleteBases) > 0 {
			msg += fmt.Sprintf("; additionally, could not complete the check for: %s", strings.Join(incompleteBases, ", "))
		}
		return ports.DoctorCheck{
			Name:        "Base Image Security & CVEs",
			Passed:      false,
			Message:     msg,
			Remediation: "Update base images via `pokkum base update` or choose a patched preset (e.g. chainguard)",
		}
	}

	if len(incompleteBases) > 0 {
		return ports.DoctorCheck{
			Name:        "Base Image Security & CVEs",
			Passed:      false,
			Message:     fmt.Sprintf("could not complete the security check for locked base image(s), vulnerability database lookup failed: %s", strings.Join(incompleteBases, ", ")),
			Remediation: "Check network connectivity to api.osv.dev and re-run `pokkum doctor`; this is not a confirmed vulnerability, just an incomplete check",
		}
	}

	return ports.DoctorCheck{
		Name:    "Base Image Security & CVEs",
		Passed:  true,
		Message: fmt.Sprintf("all %d locked base image(s) passed security vulnerability checks", len(lf.Bases)),
	}
}
