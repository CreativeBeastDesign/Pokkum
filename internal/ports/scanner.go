package ports

import (
	"context"
	"fmt"
	"strings"
)

// Severity indicates the security risk level of a vulnerability.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Valid reports whether s is a recognized severity level.
func (s Severity) Valid() bool {
	switch Severity(strings.ToLower(string(s))) {
	case SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
		return true
	default:
		return false
	}
}

// Rank returns a numerical ordering for severity comparison (higher = more severe).
func (s Severity) Rank() int {
	switch Severity(strings.ToLower(string(s))) {
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	default:
		return 0
	}
}

// ParseSeverity parses a string into a valid Severity.
func ParseSeverity(raw string) (Severity, error) {
	s := Severity(strings.ToLower(strings.TrimSpace(raw)))
	if !s.Valid() {
		return "", fmt.Errorf("invalid severity level %q: choose from low, medium, high, critical", raw)
	}
	return s, nil
}

// Vulnerability details a security advisory or CVE.
type Vulnerability struct {
	ID           string   `json:"id"`
	Severity     Severity `json:"severity"`
	Package      string   `json:"package"`
	Version      string   `json:"version"`
	FixedVersion string   `json:"fixed_version,omitempty"`
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	URL          string   `json:"url,omitempty"`
	Ecosystem    string   `json:"ecosystem,omitempty"`
}

// ScanRequest specifies parameters for vulnerability scanning.
type ScanRequest struct {
	// Target is an image reference, tarball path, or project directory.
	Target string `json:"target"`

	// FailOn sets the minimum severity threshold that causes Scan to fail.
	FailOn Severity `json:"fail_on"`

	// ToolchainOnly restricts scanning to embedded runtime & dependency advisories.
	ToolchainOnly bool `json:"toolchain_only"`

	// Offline disables remote vulnerability database lookups (OSV.dev).
	Offline bool `json:"offline"`

	// AllowIncomplete permits Scan to report success even when an OSV.dev
	// lookup failed partway through (e.g. a network blip) and coverage is
	// therefore reduced. Without it, Scan fails closed on incompleteness —
	// a scan that silently degrades to "0 vulnerabilities found" because a
	// query failed, rather than because nothing was there, is a false
	// clean bill of health, worse than no scan at all. Not needed with
	// Offline, which is an intentional, expected reduction in coverage.
	AllowIncomplete bool `json:"allow_incomplete"`
}

// ScanResult contains the discovered security vulnerabilities.
type ScanResult struct {
	Target              string          `json:"target"`
	Vulnerabilities     []Vulnerability `json:"vulnerabilities"`
	ToolchainAdvisories []Vulnerability `json:"toolchain_advisories"`
	Passed              bool            `json:"passed"`
	MaxSeverityFound    Severity        `json:"max_severity_found"`

	// Incomplete is true if one or more OSV.dev lookups failed partway
	// through the scan (network error, API error), reducing real coverage
	// below what a clean scan would have checked. See Warnings for which
	// lookups failed. A Passed:true, Incomplete:true result must not be
	// read the same as a genuinely clean scan.
	Incomplete bool `json:"incomplete,omitempty"`

	// Warnings lists non-fatal problems encountered during the scan (e.g.
	// which specific OSV.dev query failed and why).
	Warnings []string `json:"warnings,omitempty"`
}

// Scanner scans container images, tarballs, or toolchain versions for CVEs and advisories.
type Scanner interface {
	Scan(ctx context.Context, req ScanRequest) (ScanResult, error)
}
