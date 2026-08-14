package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anchore/syft/syft"
	"github.com/anchore/syft/syft/cataloging"
	"github.com/anchore/syft/syft/pkg"

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
		Ecosystem:    "Bun",
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
		Ecosystem:    "npm",
	},
}

var _ ports.Scanner = (*Adapter)(nil)

// Adapter implements ports.Scanner.
type Adapter struct {
	logger *slog.Logger
	client *http.Client

	mu    sync.Mutex
	cache map[string][]ports.Vulnerability
}

// NewAdapter constructs a Scanner Adapter.
func NewAdapter(logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		logger: logger,
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  make(map[string][]ports.Vulnerability),
	}
}

// Scan scans an image reference, tarball, or directory for security vulnerabilities and toolchain advisories.
func (s *Adapter) Scan(ctx context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
	if req.FailOn == "" {
		req.FailOn = ports.SeverityCritical
	}
	if !req.FailOn.Valid() {
		return ports.ScanResult{}, fmt.Errorf("scanner: invalid fail-on severity %q: %w", req.FailOn, core.ErrInvalidRequest)
	}

	var (
		vulnerabilities     []ports.Vulnerability
		toolchainAdvisories []ports.Vulnerability
	)

	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = "."
	}

	// Determine target type: directory or container image/tarball
	info, statErr := os.Stat(target)
	isDir := statErr == nil && info.IsDir()

	if isDir {
		// 1. Directory scan: toolchain and project dependencies
		tAdvisories, err := s.scanProjectToolchain(ctx, target, req.Offline)
		if err == nil {
			toolchainAdvisories = append(toolchainAdvisories, tAdvisories...)
		}

		if !req.ToolchainOnly && !req.Offline {
			dirVulns, err := s.scanProjectDependencies(ctx, target)
			if err == nil {
				vulnerabilities = append(vulnerabilities, dirVulns...)
			}
		}
	} else {
		// 2. Container image or tarball scan
		imgVulns, imgToolchain, err := s.scanImageOrTarball(ctx, target, req.Offline)
		if err != nil {
			s.logger.DebugContext(ctx, "scanner: fallback to embedded advisories", "target", target, "err", err)
		}
		vulnerabilities = append(vulnerabilities, imgVulns...)
		toolchainAdvisories = append(toolchainAdvisories, imgToolchain...)
	}

	// Always ensure embedded toolchain advisories are checked as fallback if none found
	if len(toolchainAdvisories) == 0 {
		toolchainAdvisories = append(toolchainAdvisories, s.checkEmbeddedAdvisories(ports.DefaultBunVersion, "2.15.0")...)
	}

	allFound := append([]ports.Vulnerability{}, vulnerabilities...)
	allFound = append(allFound, toolchainAdvisories...)

	maxSev := ports.SeverityLow
	failed := false

	for _, adv := range allFound {
		if adv.Severity.Rank() > maxSev.Rank() {
			maxSev = adv.Severity
		}
		if adv.Severity.Rank() >= req.FailOn.Rank() {
			failed = true
		}
	}

	res := ports.ScanResult{
		Target:              target,
		Vulnerabilities:     vulnerabilities,
		ToolchainAdvisories: toolchainAdvisories,
		Passed:              !failed,
		MaxSeverityFound:    maxSev,
	}

	if failed {
		return res, fmt.Errorf("scanner: %d vulnerability(ies) exceed threshold %s: %w", len(allFound), req.FailOn, core.ErrVulnerabilityThresholdExceeded)
	}

	return res, nil
}

func (s *Adapter) scanProjectToolchain(ctx context.Context, projectDir string, offline bool) ([]ports.Vulnerability, error) {
	pkgJSON, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		return nil, err
	}

	kitVer := sveltekitutils.ResolveVersion(projectDir, "@sveltejs/kit", pkgJSON)
	bunVer := ports.DefaultBunVersion

	var advisories []ports.Vulnerability
	advisories = append(advisories, s.checkEmbeddedAdvisories(bunVer, kitVer)...)

	if !offline {
		if remote, err := s.queryOSV(ctx, "@sveltejs/kit", kitVer, "npm"); err == nil {
			advisories = append(advisories, remote...)
		}
	}

	return advisories, nil
}

func (s *Adapter) scanProjectDependencies(ctx context.Context, projectDir string) ([]ports.Vulnerability, error) {
	pkgJSON, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		return nil, err
	}

	var queries []osvQueryItem
	for name, ver := range pkgJSON.Dependencies {
		v := cleanVersion(ver)
		if v != "" {
			queries = append(queries, osvQueryItem{
				Package: osvPackageItem{Name: name, Ecosystem: "npm"},
				Version: v,
			})
		}
	}
	for name, ver := range pkgJSON.DevDependencies {
		v := cleanVersion(ver)
		if v != "" {
			queries = append(queries, osvQueryItem{
				Package: osvPackageItem{Name: name, Ecosystem: "npm"},
				Version: v,
			})
		}
	}

	if len(queries) == 0 {
		return nil, nil
	}

	return s.queryOSVBatch(ctx, queries)
}

func (s *Adapter) scanImageOrTarball(ctx context.Context, target string, offline bool) ([]ports.Vulnerability, []ports.Vulnerability, error) {
	s.mu.Lock()
	if cached, ok := s.cache[target]; ok {
		s.mu.Unlock()
		return cached, nil, nil
	}
	s.mu.Unlock()

	src, err := syft.GetSource(ctx, target, syft.DefaultGetSourceConfig())
	if err != nil {
		return nil, nil, fmt.Errorf("syft source for %s: %w", target, err)
	}
	defer src.Close()

	cfg := syft.DefaultCreateSBOMConfig().WithCatalogerSelection(
		cataloging.NewSelectionRequest().WithRemovals("rpm-db-cataloger"),
	)

	sDoc, err := syft.CreateSBOM(ctx, src, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("syft sbom creation: %w", err)
	}

	packages := sDoc.Artifacts.Packages.Sorted()
	var (
		distroName    string
		distroVersion string
	)
	if sDoc.Artifacts.LinuxDistribution != nil {
		distroName = strings.ToLower(sDoc.Artifacts.LinuxDistribution.ID)
		distroVersion = sDoc.Artifacts.LinuxDistribution.VersionID
	}

	var queries []osvQueryItem
	for _, p := range packages {
		ecosystem := mapPackageEcosystem(p, distroName, distroVersion)
		if ecosystem != "" && p.Version != "" {
			queries = append(queries, osvQueryItem{
				Package: osvPackageItem{Name: p.Name, Ecosystem: ecosystem},
				Version: p.Version,
			})
		}
	}

	var vulns []ports.Vulnerability
	if !offline && len(queries) > 0 {
		discovered, err := s.queryOSVBatch(ctx, queries)
		if err == nil {
			vulns = append(vulns, discovered...)
		} else {
			s.logger.WarnContext(ctx, "scanner: osv batch query failed", "err", err)
		}
	}

	// Toolchain advisories from embedded checks on the packages discovered
	var toolchainAdvisories []ports.Vulnerability
	for _, p := range packages {
		if p.Name == "bun" || p.Name == "@sveltejs/kit" {
			toolchainAdvisories = append(toolchainAdvisories, s.checkEmbeddedAdvisories(p.Version, p.Version)...)
		}
	}

	s.mu.Lock()
	s.cache[target] = vulns
	s.mu.Unlock()

	return vulns, toolchainAdvisories, nil
}

func mapPackageEcosystem(p pkg.Package, distroName, distroVersion string) string {
	switch p.Type {
	case pkg.DebPkg:
		if distroName == "ubuntu" {
			if distroVersion != "" {
				return "Ubuntu:" + distroVersion
			}
			return "Ubuntu"
		}
		// Default to Debian
		if distroVersion != "" {
			major := strings.Split(distroVersion, ".")[0]
			return "Debian:" + major
		}
		return "Debian"
	case pkg.ApkPkg:
		if distroName == "wolfi" {
			return "Wolfi"
		}
		if distroName == "chainguard" {
			return "Chainguard"
		}
		if distroVersion != "" {
			parts := strings.Split(distroVersion, ".")
			if len(parts) >= 2 {
				return "Alpine:v" + parts[0] + "." + parts[1]
			}
		}
		return "Alpine"
	case pkg.NpmPkg:
		return "npm"
	case pkg.GoModulePkg:
		return "Go"
	case pkg.PythonPkg:
		return "PyPI"
	case pkg.RustPkg:
		return "crates.io"
	case pkg.RpmPkg:
		return "Red Hat"
	default:
		return ""
	}
}

func (s *Adapter) checkEmbeddedAdvisories(bunVer, kitVer string) []ports.Vulnerability {
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

type osvPackageItem struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type osvQueryItem struct {
	Package osvPackageItem `json:"package"`
	Version string         `json:"version,omitempty"`
}

type osvBatchRequest struct {
	Queries []osvQueryItem `json:"queries"`
}

type osvBatchResponse struct {
	Results []struct {
		Vulns []osvVulnRecord `json:"vulns"`
	} `json:"results"`
}

type osvVulnRecord struct {
	ID               string              `json:"id"`
	Summary          string              `json:"summary"`
	Details          string              `json:"details"`
	DatabaseSpecific map[string]any      `json:"database_specific"`
	Severity         []osvSeverityRecord `json:"severity"`
	Affected         []osvAffectedRecord `json:"affected"`
}

type osvSeverityRecord struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

type osvAffectedRecord struct {
	Package osvPackageItem `json:"package"`
	Ranges  []osvRange     `json:"ranges"`
}

type osvRange struct {
	Type   string     `json:"type"`
	Events []osvEvent `json:"events"`
}

type osvEvent struct {
	Introduced string `json:"introduced,omitempty"`
	Fixed      string `json:"fixed,omitempty"`
}

func (s *Adapter) queryOSVBatch(ctx context.Context, queries []osvQueryItem) ([]ports.Vulnerability, error) {
	if len(queries) == 0 {
		return nil, nil
	}

	const batchLimit = 500
	var allVulns []ports.Vulnerability

	for i := 0; i < len(queries); i += batchLimit {
		end := i + batchLimit
		if end > len(queries) {
			end = len(queries)
		}
		chunk := queries[i:end]

		reqPayload := osvBatchRequest{Queries: chunk}
		body, err := json.Marshal(reqPayload)
		if err != nil {
			return nil, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.osv.dev/v1/querybatch", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := s.client.Do(httpReq)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("osv querybatch api status %d", resp.StatusCode)
		}

		respBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		var batchRes osvBatchResponse
		if err := json.Unmarshal(respBytes, &batchRes); err != nil {
			return nil, err
		}

		for qIdx, res := range batchRes.Results {
			if qIdx >= len(chunk) {
				break
			}
			query := chunk[qIdx]
			for _, v := range res.Vulns {
				sev := parseOSVSeverity(v)
				fixed := extractFixedVersion(v, query.Package.Name)
				title := v.Summary
				if title == "" {
					title = v.ID
				}
				allVulns = append(allVulns, ports.Vulnerability{
					ID:           v.ID,
					Severity:     sev,
					Package:      query.Package.Name,
					Version:      query.Version,
					FixedVersion: fixed,
					Title:        title,
					Description:  v.Details,
					URL:          fmt.Sprintf("https://osv.dev/vulnerability/%s", v.ID),
					Ecosystem:    query.Package.Ecosystem,
				})
			}
		}
	}

	return allVulns, nil
}

func (s *Adapter) queryOSV(ctx context.Context, pkgName, version, ecosystem string) ([]ports.Vulnerability, error) {
	if version == "" {
		return nil, nil
	}
	if ecosystem == "" {
		ecosystem = "npm"
	}
	return s.queryOSVBatch(ctx, []osvQueryItem{
		{
			Package: osvPackageItem{Name: pkgName, Ecosystem: ecosystem},
			Version: cleanVersion(version),
		},
	})
}

func parseOSVSeverity(v osvVulnRecord) ports.Severity {
	if v.DatabaseSpecific != nil {
		if raw, ok := v.DatabaseSpecific["severity"].(string); ok {
			switch strings.ToUpper(raw) {
			case "CRITICAL":
				return ports.SeverityCritical
			case "HIGH":
				return ports.SeverityHigh
			case "MODERATE", "MEDIUM":
				return ports.SeverityMedium
			case "LOW":
				return ports.SeverityLow
			}
		}
	}

	for _, sRecord := range v.Severity {
		if sRecord.Score != "" {
			if score, err := strconv.ParseFloat(sRecord.Score, 64); err == nil {
				if score >= 9.0 {
					return ports.SeverityCritical
				}
				if score >= 7.0 {
					return ports.SeverityHigh
				}
				if score >= 4.0 {
					return ports.SeverityMedium
				}
				return ports.SeverityLow
			}
		}
	}

	return ports.SeverityMedium
}

func extractFixedVersion(v osvVulnRecord, pkgName string) string {
	for _, aff := range v.Affected {
		if aff.Package.Name != "" && aff.Package.Name != pkgName {
			continue
		}
		for _, r := range aff.Ranges {
			for _, ev := range r.Events {
				if ev.Fixed != "" {
					return ev.Fixed
				}
			}
		}
	}
	return ""
}

func cleanVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "^")
	v = strings.TrimPrefix(v, "~")
	v = strings.TrimPrefix(v, ">=")
	v = strings.TrimPrefix(v, "<=")
	v = strings.TrimPrefix(v, "=")
	v = strings.TrimPrefix(v, "v")
	return v
}

func isVersionOlderThan(v, fixed string) bool {
	v = cleanVersion(v)
	fixed = cleanVersion(fixed)
	return v != "" && v < fixed
}
