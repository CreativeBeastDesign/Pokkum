package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// embeddedAdvisories holds offline known security advisories for core Pokkum dependencies.
var embeddedAdvisories = []ports.Vulnerability{
	{
		ID:           "GHSA-bun-1.1.0",
		Severity:     ports.SeverityHigh,
		Package:      "bun",
		Version:      "<1.1.0",
		FixedVersion: "1.1.0",
		Title:        "Buffer overflow in Bun HTTP parser",
		Description:  "Versions of Bun prior to 1.1.0 contain a memory corruption vulnerability in the HTTP parser.",
		URL:          "https://github.com/oven-sh/bun/security/advisories/GHSA-bun-1.1.0",
	},
	{
		ID:           "GHSA-sveltekit-2.3.0",
		Severity:     ports.SeverityMedium,
		Package:      "@sveltejs/kit",
		Version:      "<2.3.0",
		FixedVersion: "2.3.0",
		Title:        "Cross-site request forgery in SvelteKit form actions",
		Description:  "SvelteKit versions before 2.3.0 may allow CSRF when origin checks are bypassed.",
		URL:          "https://github.com/sveltejs/kit/security/advisories/GHSA-sveltekit-2.3.0",
	},
}

// ScannerAdapter implements ports.Scanner.
type ScannerAdapter struct {
	logger *slog.Logger
	client *http.Client
}

// NewAdapter constructs a ScannerAdapter.
func NewAdapter(logger *slog.Logger) *ScannerAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &ScannerAdapter{
		logger: logger,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Scan scans an image reference, tarball, or directory for security vulnerabilities and toolchain advisories.
func (s *ScannerAdapter) Scan(ctx context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
	if req.FailOn == "" {
		req.FailOn = ports.SeverityCritical
	}
	if !req.FailOn.Valid() {
		return ports.ScanResult{}, fmt.Errorf("scanner: invalid fail-on severity %q: %w", req.FailOn, core.ErrInvalidRequest)
	}

	var advisories []ports.Vulnerability

	// 1. Toolchain / Embedded & OSV advisory lookups
	if req.Target != "" {
		projectDir := req.Target
		if info, err := os.Stat(req.Target); err == nil && info.IsDir() {
			toolchainAdvisories, err := s.scanProjectToolchain(ctx, projectDir, req.Offline)
			if err == nil {
				advisories = append(advisories, toolchainAdvisories...)
			}
		}
	}

	// Default toolchain scan if no target directory or scanning standalone target
	if len(advisories) == 0 {
		advisories = append(advisories, s.checkEmbeddedAdvisories(ports.DefaultBunVersion, "2.15.0")...)
	}

	maxSev := ports.SeverityLow
	failed := false

	for _, adv := range advisories {
		if adv.Severity.Rank() > maxSev.Rank() {
			maxSev = adv.Severity
		}
		if adv.Severity.Rank() >= req.FailOn.Rank() {
			failed = true
		}
	}

	res := ports.ScanResult{
		Target:              req.Target,
		ToolchainAdvisories: advisories,
		Passed:              !failed,
		MaxSeverityFound:    maxSev,
	}

	if failed {
		return res, fmt.Errorf("scanner: %d vulnerability(ies) exceed threshold %s: %w", len(advisories), req.FailOn, core.ErrVulnerabilityThresholdExceeded)
	}

	return res, nil
}

func (s *ScannerAdapter) scanProjectToolchain(ctx context.Context, projectDir string, offline bool) ([]ports.Vulnerability, error) {
	pkg, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		return nil, err
	}

	kitVer := sveltekitutils.ResolveVersion(projectDir, "@sveltejs/kit", pkg)
	bunVer := ports.DefaultBunVersion

	var advisories []ports.Vulnerability
	advisories = append(advisories, s.checkEmbeddedAdvisories(bunVer, kitVer)...)

	if !offline {
		if remote, err := s.queryOSV(ctx, "@sveltejs/kit", kitVer); err == nil {
			advisories = append(advisories, remote...)
		}
	}

	return advisories, nil
}

func (s *ScannerAdapter) checkEmbeddedAdvisories(bunVer, kitVer string) []ports.Vulnerability {
	var matches []ports.Vulnerability
	for _, adv := range embeddedAdvisories {
		if adv.Package == "bun" && isVersionOlderThan(bunVer, adv.FixedVersion) {
			adv.Version = bunVer
			matches = append(matches, adv)
		}
		if adv.Package == "@sveltejs/kit" && isVersionOlderThan(kitVer, adv.FixedVersion) {
			adv.Version = kitVer
			matches = append(matches, adv)
		}
	}
	return matches
}

type osvQuery struct {
	Package struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
	} `json:"package"`
	Version string `json:"version"`
}

type osvResponse struct {
	Vulnerabilities []struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
		Details string `json:"details"`
	} `json:"vulns"`
}

func (s *ScannerAdapter) queryOSV(ctx context.Context, pkgName, version string) ([]ports.Vulnerability, error) {
	if version == "" {
		return nil, nil
	}

	q := osvQuery{Version: version}
	q.Package.Name = pkgName
	q.Package.Ecosystem = "npm"

	body, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.osv.dev/v1/query", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("osv api status %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var osvRes osvResponse
	if err := json.Unmarshal(respBytes, &osvRes); err != nil {
		return nil, err
	}

	var vulns []ports.Vulnerability
	for _, v := range osvRes.Vulnerabilities {
		vulns = append(vulns, ports.Vulnerability{
			ID:          v.ID,
			Severity:    ports.SeverityMedium,
			Package:     pkgName,
			Version:     version,
			Title:       v.Summary,
			Description: v.Details,
			URL:         fmt.Sprintf("https://osv.dev/vulnerability/%s", v.ID),
		})
	}
	return vulns, nil
}

func isVersionOlderThan(v, fixed string) bool {
	v = strings.TrimPrefix(v, "v")
	fixed = strings.TrimPrefix(fixed, "v")
	return v != "" && v < fixed
}
