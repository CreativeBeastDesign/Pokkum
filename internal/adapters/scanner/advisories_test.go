package scanner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// ghsaID is GitHub's advisory ID format: three groups of four characters from
// a restricted alphabet. The embedded table this snapshot replaced carried
// IDs like "GHSA-bun-1.1.0" that no advisory database has ever issued.
var ghsaID = regexp.MustCompile(`^GHSA(-[23456789cfghjmpqrvwx]{4}){3}$`)

// TestEmbeddedSnapshot_OnlyRealAdvisories guards against fabricated entries
// in the offline advisory data: every ID must be a well-formed GHSA ID, every
// URL must point at a real advisory host, and both toolchain packages must be
// covered.
func TestEmbeddedSnapshot_OnlyRealAdvisories(t *testing.T) {
	if _, err := time.Parse("2006-01-02", embeddedSnapshot.Snapshot); err != nil {
		t.Errorf("snapshot date %q is not YYYY-MM-DD: %v", embeddedSnapshot.Snapshot, err)
	}
	perPackage := map[string]int{}
	for _, a := range embeddedSnapshot.Advisories {
		perPackage[a.Package]++
		if !ghsaID.MatchString(a.ID) {
			t.Errorf("advisory ID %q is not a GHSA ID; the snapshot must only carry advisories a real database issued", a.ID)
		}
		if !strings.HasPrefix(a.URL, "https://github.com/") && !strings.HasPrefix(a.URL, "https://osv.dev/") {
			t.Errorf("advisory %s: URL %q is not a GitHub or OSV advisory page", a.ID, a.URL)
		}
		if a.Title == "" {
			t.Errorf("advisory %s has no title", a.ID)
		}
	}
	for pkg := range toolchainAdvisoryPackages {
		if perPackage[pkg] == 0 {
			t.Errorf("snapshot has no advisories for %s", pkg)
		}
	}
}

func TestParseAdvisorySnapshot_RejectsInvalid(t *testing.T) {
	valid := `{"id":"GHSA-xxxx-xxxx-xxxx","package":"bun","severity":"high","title":"t","url":"u","ranges":[{"fixed":"1.0.0"}]}`
	for name, doc := range map[string]string{
		"no snapshot date":       `{"snapshot":"","sources":[],"advisories":[` + valid + `]}`,
		"no advisories":          `{"snapshot":"2026-10-25","sources":[],"advisories":[]}`,
		"unknown package":        `{"snapshot":"2026-10-25","sources":[],"advisories":[` + strings.Replace(valid, `"bun"`, `"svelte"`, 1) + `]}`,
		"invalid severity":       `{"snapshot":"2026-10-25","sources":[],"advisories":[` + strings.Replace(valid, `"high"`, `"moderate"`, 1) + `]}`,
		"no ranges":              `{"snapshot":"2026-10-25","sources":[],"advisories":[` + strings.Replace(valid, `[{"fixed":"1.0.0"}]`, `[]`, 1) + `]}`,
		"fixed and last":         `{"snapshot":"2026-10-25","sources":[],"advisories":[` + strings.Replace(valid, `{"fixed":"1.0.0"}`, `{"fixed":"1.0.0","last_affected":"0.9.0"}`, 1) + `]}`,
		"duplicate advisory":     `{"snapshot":"2026-10-25","sources":[],"advisories":[` + valid + `,` + valid + `]}`,
		"unknown field (typo'd)": `{"snapshot":"2026-10-25","sources":[],"advisories":[` + strings.Replace(valid, `"fixed"`, `"fixd"`, 1) + `]}`,
	} {
		if _, err := ParseAdvisorySnapshot([]byte(doc)); err == nil {
			t.Errorf("%s: snapshot accepted, want an error", name)
		}
	}
	if _, err := ParseAdvisorySnapshot([]byte(`{"snapshot":"2026-10-25","sources":[],"advisories":[` + valid + `]}`)); err != nil {
		t.Errorf("valid snapshot rejected: %v", err)
	}
}

func TestEmbeddedAdvisory_Affects(t *testing.T) {
	adv := EmbeddedAdvisory{Ranges: []AdvisoryRange{
		{Introduced: "2.0.0", Fixed: "2.4.3"},
		{Introduced: "3.0.0", LastAffected: "3.1.0"},
		{Introduced: "5.0.0"}, // no fix yet
	}}
	for _, tc := range []struct {
		v         string
		want      bool
		wantFixed string
	}{
		{"1.9.9", false, ""}, // below the first introduced
		{"2.0.0", true, "2.4.3"},
		{"^2.2.0", true, "2.4.3"}, // range specifiers are cleaned like elsewhere
		{"2.4.3", false, ""},      // fixed is exclusive
		{"2.10.0", false, ""},     // numeric, not lexicographic
		{"3.1.0", true, ""},       // last_affected is inclusive
		{"3.1.1", false, ""},
		{"5.2.0", true, ""},            // unfixed
		{"workspace:*", true, "2.4.3"}, // unorderable: fail safe, treat as affected
		{"", false, ""},                // unresolved: not a finding (reported as reduced coverage elsewhere)
	} {
		fixed, ok := adv.affects(tc.v)
		if ok != tc.want || fixed != tc.wantFixed {
			t.Errorf("affects(%q) = (%q, %v), want (%q, %v)", tc.v, fixed, ok, tc.wantFixed, tc.want)
		}
	}
}

func cleanKitProjectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app","devDependencies":{"@sveltejs/kit":"99.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func bunAdvisories(vs []ports.Vulnerability) []ports.Vulnerability {
	var out []ports.Vulnerability
	for _, v := range vs {
		if v.Package == "bun" {
			out = append(out, v)
		}
	}
	return out
}

// TestScan_ToolchainChecksRequestedBunVersion guards the directory scan
// against checking the default Bun instead of the one the build embeds. The
// default (1.4.4) is clean in the snapshot and 1.4.3 is not, so a scan that
// ignored BunVersion would report nothing for 1.4.3.
func TestScan_ToolchainChecksRequestedBunVersion(t *testing.T) {
	for _, tc := range []struct {
		bunVersion string
		wantBun    bool
	}{
		{"", false}, // DefaultBunVersion
		{"1.4.3", true},
		{"1.4.4", false},
	} {
		res, err := NewAdapter(nil).Scan(context.Background(), ports.ScanRequest{
			Target:     cleanKitProjectDir(t),
			Offline:    true,
			BunVersion: tc.bunVersion,
		})
		if err != nil {
			t.Fatalf("bun %q: scan: %v", tc.bunVersion, err)
		}
		got := bunAdvisories(res.ToolchainAdvisories)
		if (len(got) > 0) != tc.wantBun {
			t.Errorf("bun %q: bun advisories = %v, want any=%v", tc.bunVersion, got, tc.wantBun)
		}
		for _, a := range got {
			if a.Version != tc.bunVersion {
				t.Errorf("bun %q: advisory %s reported against version %q", tc.bunVersion, a.ID, a.Version)
			}
		}
	}
}

// TestScan_ImageTargetChecksRequestedBunVersion guards the build gate's path:
// it scans the base image, which contains no Bun, so only BunVersion can
// bring the runtime the build adds into the check.
func TestScan_ImageTargetChecksRequestedBunVersion(t *testing.T) {
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(t.TempDir(), "base.tar")
	ref, _ := name.ParseReference("example.test/base:latest")
	if err := tarball.WriteToFile(tarPath, ref, img); err != nil {
		t.Fatal(err)
	}

	scan := func(req ports.ScanRequest) ports.ScanResult {
		t.Helper()
		req.Target = tarPath
		req.Offline = true
		res, err := NewAdapter(nil).Scan(context.Background(), req)
		if err != nil {
			t.Fatalf("scan %+v: %v", req, err)
		}
		return res
	}

	if got := bunAdvisories(scan(ports.ScanRequest{}).ToolchainAdvisories); len(got) != 0 {
		t.Errorf("no BunVersion: bun advisories %v reported for an image containing no Bun", got)
	}
	if got := bunAdvisories(scan(ports.ScanRequest{BunVersion: "1.4.3"}).ToolchainAdvisories); len(got) == 0 {
		t.Error("BunVersion 1.4.3: no bun advisories; the runtime the build layers onto this base was never checked")
	}
	if got := bunAdvisories(scan(ports.ScanRequest{BunVersion: "1.4.3", AppRuntime: "node"}).ToolchainAdvisories); len(got) != 0 {
		t.Errorf("node runtime: bun advisories %v reported for an image that ships no Bun", got)
	}
}

// TestScan_OnlineToolchainAdvisoryReportedOnce: the snapshot now carries real
// IDs, so an advisory OSV also returns live must not be counted twice.
func TestScan_OnlineToolchainAdvisoryReportedOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req osvBatchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := osvBatchResponse{}
		for _, q := range req.Queries {
			var res struct {
				Vulns []osvVulnRecord `json:"vulns"`
			}
			if q.Package.Name == "@sveltejs/kit" {
				res.Vulns = []osvVulnRecord{{ID: "GHSA-g5m6-hxpp-fc49", Summary: "live record"}}
			}
			resp.Results = append(resp.Results, res)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"app","devDependencies":{"@sveltejs/kit":"2.2.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewAdapter(nil)
	a.client = srv.Client()
	a.osvBaseURL = srv.URL
	res, err := a.Scan(context.Background(), ports.ScanRequest{Target: dir, ToolchainOnly: true})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	n := 0
	for _, v := range res.ToolchainAdvisories {
		if v.ID == "GHSA-g5m6-hxpp-fc49" {
			n++
			if v.Title != "live record" {
				t.Errorf("kept the snapshot record over the live one: %q", v.Title)
			}
		}
	}
	if n != 1 {
		t.Errorf("GHSA-g5m6-hxpp-fc49 reported %d times, want once", n)
	}
}

// maxAdvisorySnapshotAge is how old advisories.json may get before the suite
// fails. Advisory data goes stale far faster than a trust root: three Bun
// advisories were published in the week this snapshot was first taken. The
// update-advisories workflow refreshes it weekly, so this only fires when that
// automation has stopped.
const maxAdvisorySnapshotAge = 45 * 24 * time.Hour

// TestEmbeddedSnapshot_IsFreshNow runs against the real wall clock on purpose:
// it is the only thing that notices the calendar moving. It fails rather than
// warns, matching the trust-root freshness guard, because a warning in test
// output is the kind of signal that gets scrolled past for months.
func TestEmbeddedSnapshot_IsFreshNow(t *testing.T) {
	taken, err := time.Parse("2006-01-02", embeddedSnapshot.Snapshot)
	if err != nil {
		t.Fatalf("snapshot date %q: %v", embeddedSnapshot.Snapshot, err)
	}
	if age := time.Since(taken); age > maxAdvisorySnapshotAge {
		t.Fatalf("The scanner's embedded advisory snapshot is %d days old (taken %s, limit %d days).\n\n"+
			"It is the only source of Bun runtime advisories, online or offline, so a stale snapshot "+
			"silently misses every Bun advisory published since. Refresh it with:\n\tmake advisories\n"+
			"then review `git diff internal/adapters/scanner/advisories.json` and commit it.",
			int(age.Hours()/24), embeddedSnapshot.Snapshot, int(maxAdvisorySnapshotAge.Hours()/24))
	}
}
