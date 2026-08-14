package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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
