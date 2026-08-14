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

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/lockfileutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scanner"
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

	// Check 3: Registry Auth Credentials
	authCheck := checkRegistryAuth()
	checks = append(checks, authCheck)
	if !authCheck.Passed {
		allPassed = false
	}

	// Check 4: .pokkumignore file
	ignoreCheck := checkPokkumIgnore(opts.dir, opts.fix)
	checks = append(checks, ignoreCheck)
	if !ignoreCheck.Passed {
		allPassed = false
	}

	// Check 5: Base image security & CVEs
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
		if allPassed {
			return jsonutils.WriteSuccess(os.Stdout, "doctor", doctorPayload)
		}
		return jsonutils.WriteError(os.Stdout, "doctor", "ERR_DOCTOR_FAILED", summary, "")
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
			// A failed lookup is not a confirmed vulnerability — reporting
			// it as one would be worse than not knowing, since it would
			// get "fixed" by re-running rather than by actually checking.
			incompleteBases = append(incompleteBases, fmt.Sprintf("%s (%s)", name, target))
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
		Message: fmt.Sprintf("all %d locked base images passed security vulnerability checks", len(lf.Bases)),
	}
}
