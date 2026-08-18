package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scannerutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestScanner_EmbeddedAdvisories(t *testing.T) {
	adapter := NewAdapter(nil)

	res, err := adapter.Scan(context.Background(), ports.ScanRequest{
		FailOn:  ports.SeverityCritical,
		Offline: true,
	})
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if !res.Passed {
		t.Errorf("expected Scan with fail-on=critical to pass when max severity is high/medium")
	}
}

func TestScanner_FailsWhenThresholdExceeded(t *testing.T) {
	adapter := NewAdapter(nil)

	_, err := adapter.Scan(context.Background(), ports.ScanRequest{
		FailOn:  ports.SeverityLow,
		Offline: true,
	})
	if err == nil {
		t.Fatalf("expected Scan with fail-on=low to fail when advisories exist")
	}
	if !errors.Is(err, core.ErrVulnerabilityThresholdExceeded) {
		t.Errorf("expected ErrVulnerabilityThresholdExceeded, got %v", err)
	}
}

func TestActiveVEXExemption(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	adv := ports.Vulnerability{ID: "CVE-2024-12345", Package: "openssl"}

	t.Run("no exemptions", func(t *testing.T) {
		if activeVEXExemption(adv, nil, now) {
			t.Error("expected no exemption to apply with an empty list")
		}
	})

	t.Run("matching, non-expired exemption applies", func(t *testing.T) {
		exemptions := []ports.VEXExemption{{CVE: "CVE-2024-12345", Expires: future}}
		if !activeVEXExemption(adv, exemptions, now) {
			t.Error("expected a matching, non-expired exemption to apply")
		}
	})

	t.Run("matching but expired exemption does not apply", func(t *testing.T) {
		exemptions := []ports.VEXExemption{{CVE: "CVE-2024-12345", Expires: past}}
		if activeVEXExemption(adv, exemptions, now) {
			t.Error("expected an expired exemption not to apply")
		}
	})

	t.Run("non-matching exemption does not apply", func(t *testing.T) {
		exemptions := []ports.VEXExemption{{CVE: "CVE-2024-99999", Expires: future}}
		if activeVEXExemption(adv, exemptions, now) {
			t.Error("expected a non-matching exemption not to apply")
		}
	})
}

// TestScanner_VEXExemptionExcludesFromThreshold is PR-6's core regression
// guard, using the same offline embedded-advisories path
// TestScanner_FailsWhenThresholdExceeded already relies on for a real,
// deterministic (no network) failure to exempt — rather than hardcoding a
// specific CVE ID from the embedded advisory table (which could change),
// this discovers which CVE actually caused the failure first, then proves
// exempting exactly that CVE flips the same scan to Passed and reports it
// under ExemptedVulnerabilities instead of silently vanishing.
func TestScanner_VEXExemptionExcludesFromThreshold(t *testing.T) {
	adapter := NewAdapter(nil)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	baseline, err := adapter.Scan(context.Background(), ports.ScanRequest{
		FailOn:  ports.SeverityLow,
		Offline: true,
		Now:     now,
	})
	if err == nil || !errors.Is(err, core.ErrVulnerabilityThresholdExceeded) {
		t.Fatalf("expected the unexempted baseline scan to fail the threshold, got: %v", err)
	}
	if len(baseline.Vulnerabilities)+len(baseline.ToolchainAdvisories) == 0 {
		t.Fatal("test setup: expected at least one embedded advisory to exempt")
	}
	var target ports.Vulnerability
	if len(baseline.ToolchainAdvisories) > 0 {
		target = baseline.ToolchainAdvisories[0]
	} else {
		target = baseline.Vulnerabilities[0]
	}

	exempted, err := adapter.Scan(context.Background(), ports.ScanRequest{
		FailOn:  ports.SeverityLow,
		Offline: true,
		Now:     now,
		VEXExemptions: []ports.VEXExemption{{
			CVE:           target.ID,
			Justification: ports.VEXComponentNotPresent,
			Expires:       now.AddDate(1, 0, 0),
			Owner:         "test-owner",
		}},
	})
	if err != nil {
		t.Fatalf("expected the scan to pass once the failing CVE (%s) is exempted, got: %v", target.ID, err)
	}
	if !exempted.Passed {
		t.Errorf("expected Passed=true once the failing CVE is exempted")
	}

	found := false
	for _, v := range exempted.ExemptedVulnerabilities {
		if v.ID == target.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %s to appear in ExemptedVulnerabilities, got %+v", target.ID, exempted.ExemptedVulnerabilities)
	}
}

func TestScanner_EcosystemMapping(t *testing.T) {
	tests := []struct {
		pkgType       scannerutils.PackageType
		distroName    string
		distroVersion string
		want          string
	}{
		{scannerutils.PkgTypeDeb, "debian", "12.5", "Debian:12"},
		{scannerutils.PkgTypeDeb, "ubuntu", "22.04", "Ubuntu:22.04"},
		{scannerutils.PkgTypeApk, "alpine", "3.19.1", "Alpine:v3.19"},
		{scannerutils.PkgTypeApk, "wolfi", "20240101", "Wolfi"},
		{scannerutils.PkgTypeApk, "chainguard", "20240101", "Chainguard"},
		{scannerutils.PkgTypeNpm, "", "", "npm"},
	}

	for _, tt := range tests {
		got := scannerutils.MapDistroEcosystem(scannerutils.DistroInfo{
			ID:        tt.distroName,
			VersionID: tt.distroVersion,
		}, tt.pkgType)
		if got != tt.want {
			t.Errorf("MapDistroEcosystem(%v, %s, %s) = %q, want %q", tt.pkgType, tt.distroName, tt.distroVersion, got, tt.want)
		}
	}
}

func TestScanner_OSVBatchQueryMock(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/querybatch" {
			http.NotFound(w, r)
			return
		}

		var req osvBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp := osvBatchResponse{
			Results: make([]struct {
				Vulns []osvVulnRecord `json:"vulns"`
			}, len(req.Queries)),
		}

		for i, q := range req.Queries {
			if q.Package.Name == "libssl3" && q.Package.Ecosystem == "Debian:12" {
				resp.Results[i].Vulns = []osvVulnRecord{
					{
						ID:      "CVE-2024-0727",
						Summary: "Null pointer dereference in PKCS12_parse",
						Details: "A vulnerability in libssl3 PKCS12 processing.",
						DatabaseSpecific: map[string]any{
							"severity": "CRITICAL",
						},
						Affected: []osvAffectedRecord{
							{
								Package: osvPackageItem{Name: "libssl3", Ecosystem: "Debian:12"},
								Ranges: []osvRange{
									{
										Type: "ECOSYSTEM",
										Events: []osvEvent{
											{Introduced: "0"},
											{Fixed: "3.0.11-1~deb12u2"},
										},
									},
								},
							},
						},
					},
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	adapter := NewAdapter(nil)
	adapter.client = mockServer.Client()
	adapter.osvBaseURL = mockServer.URL

	queries := []osvQueryItem{
		{
			Package: osvPackageItem{Name: "libssl3", Ecosystem: "Debian:12"},
			Version: "3.0.11-1~deb12u1",
		},
		{
			Package: osvPackageItem{Name: "libc6", Ecosystem: "Debian:12"},
			Version: "2.36-9+deb12u4",
		},
	}

	vulns, err := adapter.queryOSVBatch(context.Background(), queries)
	if err != nil {
		t.Fatalf("queryOSVBatch failed: %v", err)
	}
	if len(vulns) != 1 {
		t.Fatalf("expected exactly 1 vulnerability (libssl3 only, libc6 has none in the mock response), got %d: %+v", len(vulns), vulns)
	}
	got := vulns[0]
	if got.ID != "CVE-2024-0727" {
		t.Errorf("expected ID CVE-2024-0727, got %q", got.ID)
	}
	if got.Package != "libssl3" {
		t.Errorf("expected Package libssl3, got %q", got.Package)
	}
	if got.Severity != ports.SeverityCritical {
		t.Errorf("expected Severity critical, got %q", got.Severity)
	}
	if got.FixedVersion != "3.0.11-1~deb12u2" {
		t.Errorf("expected FixedVersion 3.0.11-1~deb12u2, got %q", got.FixedVersion)
	}

	record := osvVulnRecord{
		ID: "CVE-2024-0727",
		DatabaseSpecific: map[string]any{
			"severity": "CRITICAL",
		},
		Affected: []osvAffectedRecord{
			{
				Package: osvPackageItem{Name: "libssl3"},
				Ranges: []osvRange{
					{
						Events: []osvEvent{
							{Fixed: "3.0.11-1~deb12u2"},
						},
					},
				},
			},
		},
	}

	sev := parseOSVSeverity(record)
	if sev != ports.SeverityCritical {
		t.Errorf("parseOSVSeverity() = %v, want %v", sev, ports.SeverityCritical)
	}

	fixed := extractFixedVersion(record, "libssl3")
	if fixed != "3.0.11-1~deb12u2" {
		t.Errorf("extractFixedVersion() = %q, want %q", fixed, "3.0.11-1~deb12u2")
	}
}

func TestScanner_DependencyOSVFailureFailsClosed(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{"name":"app","dependencies":{"lodash":"4.17.4"}}`
	if err := writeFile(t, dir+"/package.json", pkgJSON); err != nil {
		t.Fatal(err)
	}

	adapter := NewAdapter(nil)
	adapter.osvBaseURL = "http://127.0.0.1:1" // guaranteed-closed port, fails fast

	res, err := adapter.Scan(context.Background(), ports.ScanRequest{
		Target: dir,
		FailOn: ports.SeverityCritical,
	})
	if !errors.Is(err, core.ErrScanIncomplete) {
		t.Fatalf("expected ErrScanIncomplete when the OSV lookup fails, got: %v", err)
	}
	if res.Passed {
		t.Error("expected Passed=false for an incomplete scan, not a silent clean report")
	}
	if !res.Incomplete {
		t.Error("expected ScanResult.Incomplete=true")
	}

	// AllowIncomplete must opt back into the old best-effort behavior.
	res2, err2 := adapter.Scan(context.Background(), ports.ScanRequest{
		Target:          dir,
		FailOn:          ports.SeverityCritical,
		AllowIncomplete: true,
	})
	if err2 != nil {
		t.Fatalf("expected AllowIncomplete to suppress the error, got: %v", err2)
	}
	if !res2.Incomplete {
		t.Error("expected ScanResult.Incomplete=true even when AllowIncomplete suppresses the error")
	}
}

func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}

// TestIsVersionOlderThan_NumericDotSegmentComparison covers the CVE-gate
// version comparator's confirmed reproducer table (isVersionOlderThan used
// to do a plain lexicographic string compare, silently misclassifying any
// pair whose segment widths differ) plus the distro-suffix, pre-release,
// and non-numeric-garbage edge cases the fix must also handle without
// panicking or flipping to the unsafe (false-negative) direction.
func TestIsVersionOlderThan_NumericDotSegmentComparison(t *testing.T) {
	tests := []struct {
		name  string
		v     string
		fixed string
		want  bool
	}{
		// Confirmed reproducer table from the bug report: a plain byte-wise
		// compare gets every one of these wrong.
		{"width mismatch, minor: 1.2.0 vs 1.10.0", "1.2.0", "1.10.0", true},
		{"width mismatch, minor: 1.9.0 vs 1.10.0", "1.9.0", "1.10.0", true},
		{"width mismatch, minor: 2.9.0 vs 2.10.0", "2.9.0", "2.10.0", true},
		{"v-prefixed installed version", "v1.9.0", "1.10.0", true},
		{"pre-release precedes its own release", "1.2.0-beta", "1.2.0", true},

		// Sanity: equal and newer-than-fixed must not falsely trigger.
		{"equal versions are not older", "1.0.0", "1.0.0", false},
		{"newer patch is not older", "1.0.1", "1.0.0", false},
		{"major width mismatch, newer is not older", "10.0.0", "2.0.0", false},

		// Multi-digit segments beyond the confirmed table.
		{"double-digit major", "9.0.0", "10.0.0", true},
		{"triple-digit segment", "1.99.0", "1.100.0", true},

		// Differing segment counts: implicit trailing-zero padding.
		{"shorter core equal to zero-padded longer core", "1.2", "1.2.0", false},
		{"shorter core older than longer, non-zero tail", "1.2", "1.2.1", true},
		{"shorter core newer than longer", "1.3", "1.2.9", false},

		// cleanVersion-stripped range-prefix operators.
		{"caret and tilde prefixes both stripped", "^1.2.0", "~1.10.0", true},
		{"gte/lte prefixes stripped", ">=1.2.0", "<=1.10.0", true},

		// Distro-style build/package revision suffixes (e.g. Debian/Ubuntu).
		{"distro revision suffix is older than bare release", "1.2.3-1ubuntu4", "1.2.3", true},
		{"bare release is not older than a distro-suffixed release", "1.2.3", "1.2.3-1ubuntu4", false},
		{"distro suffix does not mask a genuinely newer core", "1.3.0-1ubuntu1", "1.2.3", false},

		// Non-numeric garbage: must not panic, must fail safe (lean toward
		// "older"/potentially-still-vulnerable rather than an ASCII-accident
		// byte compare with no security meaning).
		{"garbage vs garbage fails safe", "not-a-version", "also-not-a-version", true},
		{"garbage installed version fails safe against a clean fixed version", "garbage", "1.0.0", true},
		{"clean installed version vs garbage fixed version fails safe", "1.0.0", "garbage", true},

		// Blank version strings: unresolved/unknown version is not flagged
		// (pre-existing, unchanged contract — out of scope for this fix).
		{"both blank", "", "", false},
		{"blank installed version", "", "1.0.0", false},
		{"blank fixed version", "1.0.0", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isVersionOlderThan(tc.v, tc.fixed); got != tc.want {
				t.Errorf("isVersionOlderThan(%q, %q) = %v, want %v", tc.v, tc.fixed, got, tc.want)
			}
		})
	}
}

// TestCheckEmbeddedAdvisories_RuntimeKeysBunAdvisories pins the --runtime
// dimension of the toolchain advisory check: a Bun advisory must never be
// reported for a build whose image ships no Bun (--runtime=node), while the
// @sveltejs/kit advisory — runtime-independent application-framework code —
// must keep firing for both runtimes, and the bun/empty runtimes must keep
// the pre-existing behavior byte-for-byte.
func TestCheckEmbeddedAdvisories_RuntimeKeysBunAdvisories(t *testing.T) {
	s := NewAdapter(nil)
	vulnBun := "1.0.0" // older than the embedded bun advisory's FixedVersion 1.1.0
	vulnKit := "2.2.0" // older than the embedded kit advisory's FixedVersion 2.3.0

	countByPackage := func(advisories []ports.Vulnerability) map[string]int {
		out := map[string]int{}
		for _, a := range advisories {
			out[a.Package]++
		}
		return out
	}

	for _, runtime := range []string{"", "bun"} {
		got := countByPackage(s.checkEmbeddedAdvisories(runtime, vulnBun, vulnKit))
		if got["bun"] != 1 || got["@sveltejs/kit"] != 1 {
			t.Errorf("runtime %q: advisories = %v, want 1 bun + 1 kit", runtime, got)
		}
	}

	got := countByPackage(s.checkEmbeddedAdvisories("node", vulnBun, vulnKit))
	if got["bun"] != 0 {
		t.Errorf("runtime node: %d bun advisories reported against an image that ships no Bun", got["bun"])
	}
	if got["@sveltejs/kit"] != 1 {
		t.Errorf("runtime node: kit advisories = %d, want 1 (kit is runtime-independent)", got["@sveltejs/kit"])
	}
}
