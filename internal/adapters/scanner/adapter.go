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

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scannerutils"
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
const defaultOSVBaseURL = "https://api.osv.dev"

type Adapter struct {
	logger *slog.Logger
	client *http.Client

	// osvBaseURL is the OSV.dev API base, overridable in tests so
	// queryOSVBatch can be exercised against a local mock server instead of
	// the real network.
	osvBaseURL string

	mu    sync.Mutex
	cache map[string][]ports.Vulnerability
}

// NewAdapter constructs a Scanner Adapter.
func NewAdapter(logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		logger:     logger,
		client:     &http.Client{Timeout: 10 * time.Second},
		osvBaseURL: defaultOSVBaseURL,
		cache:      make(map[string][]ports.Vulnerability),
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
		incomplete          bool
		warnings            []string
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
		tAdvisories, toolchainIncomplete, err := s.scanProjectToolchain(ctx, req.AppRuntime, target, req.Offline)
		if err == nil {
			toolchainAdvisories = append(toolchainAdvisories, tAdvisories...)
		}
		if toolchainIncomplete {
			incomplete = true
			warnings = append(warnings, "toolchain (@sveltejs/kit) OSV lookup failed, coverage reduced")
		}

		switch {
		case req.ToolchainOnly:
			// Caller asked for toolchain advisories only; nothing was skipped
			// against their intent, so the result is not incomplete.
		case req.Offline:
			// --offline skips the dependency lookup entirely. That is the point
			// of the flag, but the result must still say so: without this the
			// scan reported Passed with zero vulnerabilities and Incomplete
			// unset, which is indistinguishable from "scanned everything and
			// found nothing". On a project with six real CVEs that reads as a
			// clean bill of health — a security control reporting success while
			// doing nothing, which is the failure mode this codebase keeps
			// finding. Marking it incomplete lets a CI gate tell the two apart;
			// the fail-closed exemption for --offline below is deliberate and
			// unchanged, since air-gapped scanning is a supported workflow.
			incomplete = true
			warnings = append(warnings, "project dependency OSV lookup skipped (--offline), coverage reduced: no dependency CVEs were checked")
			s.logger.WarnContext(ctx, "scanner: project dependency scan skipped because --offline is set; no dependency CVEs were checked", "target", target)
		default:
			dirVulns, err := s.scanProjectDependencies(ctx, target)
			if err == nil {
				vulnerabilities = append(vulnerabilities, dirVulns...)
			} else {
				incomplete = true
				warnings = append(warnings, fmt.Sprintf("project dependency OSV lookup failed, coverage reduced: %v", err))
				s.logger.WarnContext(ctx, "scanner: project dependency OSV lookup failed, scan is incomplete", "target", target, "err", err)
			}
		}
	} else {
		// 2. Container image or tarball scan
		imgVulns, imgToolchain, imgIncomplete, err := s.scanImageOrTarball(ctx, target, req.Offline)
		if err != nil {
			incomplete = true
			warnings = append(warnings, fmt.Sprintf("image/tarball scan failed for %s: %v", target, err))
			s.logger.DebugContext(ctx, "scanner: fallback to embedded advisories", "target", target, "err", err)
		}
		if imgIncomplete {
			incomplete = true
			warnings = append(warnings, fmt.Sprintf("image/tarball OS-package OSV lookup failed, coverage reduced for %s", target))
		}
		vulnerabilities = append(vulnerabilities, imgVulns...)
		toolchainAdvisories = append(toolchainAdvisories, imgToolchain...)
	}

	// Always ensure embedded toolchain advisories are checked as fallback if none found
	// (e.g. no readable package.json, so scanProjectToolchain couldn't resolve a real
	// @sveltejs/kit version). "2.2.0" is a placeholder that is deliberately OLDER than
	// embeddedAdvisories' "@sveltejs/kit" FixedVersion ("2.3.0"), so this fallback
	// path deterministically still has something to report/fail on.
	//
	// This was previously "2.15.0" — a genuinely newer, non-vulnerable version — which
	// only appeared vulnerable because isVersionOlderThan did a plain lexicographic
	// string compare ("2.15.0" < "2.3.0" byte-wise, since '1' < '3') instead of a
	// numeric one. Fixing that comparator bug (see isVersionOlderThan/compareVersions)
	// made this literal correctly evaluate as NOT older/vulnerable, which silently
	// broke the "there's always a fallback advisory" contract this code comments on.
	if len(toolchainAdvisories) == 0 {
		toolchainAdvisories = append(toolchainAdvisories, s.checkEmbeddedAdvisories(req.AppRuntime, ports.DefaultBunVersion, "2.2.0")...)
	}

	allFound := append([]ports.Vulnerability{}, vulnerabilities...)
	allFound = append(allFound, toolchainAdvisories...)

	maxSev := ports.SeverityLow
	failed := false
	var exempted []ports.Vulnerability
	var countedTowardThreshold int

	for _, adv := range allFound {
		if activeVEXExemption(adv, req.VEXExemptions, req.Now) {
			exempted = append(exempted, adv)
			continue
		}
		countedTowardThreshold++
		if adv.Severity.Rank() > maxSev.Rank() {
			maxSev = adv.Severity
		}
		if adv.Severity.Rank() >= req.FailOn.Rank() {
			failed = true
		}
	}

	res := ports.ScanResult{
		Target:                  target,
		Vulnerabilities:         vulnerabilities,
		ToolchainAdvisories:     toolchainAdvisories,
		Passed:                  !failed,
		MaxSeverityFound:        maxSev,
		Incomplete:              incomplete,
		Warnings:                warnings,
		ExemptedVulnerabilities: exempted,
	}

	if failed {
		return res, fmt.Errorf("scanner: %d vulnerability(ies) exceed threshold %s: %w", countedTowardThreshold, req.FailOn, core.ErrVulnerabilityThresholdExceeded)
	}

	// A scan that silently degraded to "0 vulnerabilities" because a
	// lookup failed, rather than because nothing was there, must not
	// report Passed without qualification — that is a false clean bill of
	// health. Fail closed unless the caller explicitly opted in to
	// best-effort results, or this was an intentional --offline scan
	// (where reduced coverage is expected, not a failure).
	if incomplete && !req.Offline && !req.AllowIncomplete {
		res.Passed = false
		return res, fmt.Errorf("scanner: %s: %w", strings.Join(warnings, "; "), core.ErrScanIncomplete)
	}

	return res, nil
}

// activeVEXExemption reports whether adv is covered by a non-expired entry
// in exemptions as of now. An exemption that matches but has expired does
// NOT exempt — the CVE counts toward the threshold again, which is the
// entire point of a mandatory expiry (see ports.VEXExemption's doc comment).
func activeVEXExemption(adv ports.Vulnerability, exemptions []ports.VEXExemption, now time.Time) bool {
	for _, ex := range exemptions {
		if ex.Matches(adv) && !ex.Expired(now) {
			return true
		}
	}
	return false
}

func (s *Adapter) scanProjectToolchain(ctx context.Context, appRuntime, projectDir string, offline bool) ([]ports.Vulnerability, bool, error) {
	pkgJSON, err := sveltekitutils.ReadPackageJSON(projectDir)
	if err != nil {
		return nil, false, err
	}

	kitVer := sveltekitutils.ResolveVersion(projectDir, "@sveltejs/kit", pkgJSON)
	bunVer := ports.DefaultBunVersion

	var advisories []ports.Vulnerability
	advisories = append(advisories, s.checkEmbeddedAdvisories(appRuntime, bunVer, kitVer)...)

	incomplete := false
	if !offline {
		remote, err := s.queryOSV(ctx, "@sveltejs/kit", kitVer, "npm")
		if err == nil {
			advisories = append(advisories, remote...)
		} else {
			incomplete = true
			s.logger.WarnContext(ctx, "scanner: toolchain OSV lookup failed, scan is incomplete", "err", err)
		}
	}

	return advisories, incomplete, nil
}

func (s *Adapter) scanProjectDependencies(ctx context.Context, projectDir string) ([]ports.Vulnerability, error) {
	pkgs, err := scannerutils.ExtractProjectDependencies(projectDir)
	if err != nil {
		return nil, err
	}

	var queries []osvQueryItem
	for _, p := range pkgs {
		v := cleanVersion(p.Version)
		if v != "" {
			queries = append(queries, osvQueryItem{
				Package: osvPackageItem{Name: p.Name, Ecosystem: "npm"},
				Version: v,
			})
		}
	}

	if len(queries) == 0 {
		return nil, nil
	}

	return s.queryOSVBatch(ctx, queries)
}

func (s *Adapter) scanImageOrTarball(ctx context.Context, target string, offline bool) ([]ports.Vulnerability, []ports.Vulnerability, bool, error) {
	s.mu.Lock()
	if cached, ok := s.cache[target]; ok {
		s.mu.Unlock()
		return cached, nil, false, nil
	}
	s.mu.Unlock()

	var (
		img v1.Image
		err error
	)

	if _, statErr := os.Stat(target); statErr == nil || strings.HasSuffix(target, ".tar") {
		img, err = tarball.ImageFromPath(target, nil)
		if err != nil {
			return nil, nil, false, fmt.Errorf("loading tarball %s: %w", target, err)
		}
	} else {
		ref, parseErr := name.ParseReference(target, name.WeakValidation)
		if parseErr != nil {
			return nil, nil, false, fmt.Errorf("parsing image reference %s: %w", target, parseErr)
		}
		kc, kerr := registryutils.ResolveKeychain("")
		if kerr != nil {
			return nil, nil, false, fmt.Errorf("resolving registry auth: %w", kerr)
		}
		desc, getErr := remote.Get(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))
		if getErr != nil {
			return nil, nil, false, fmt.Errorf("fetching remote image %s: %w", target, getErr)
		}
		img, err = desc.Image()
		if err != nil {
			return nil, nil, false, fmt.Errorf("resolving image from descriptor for %s: %w", target, err)
		}
	}

	packages, _, err := scannerutils.ExtractImagePackages(ctx, img)
	if err != nil {
		return nil, nil, false, fmt.Errorf("extracting packages from image %s: %w", target, err)
	}

	var queries []osvQueryItem
	for _, p := range packages {
		if p.Ecosystem != "" && p.Version != "" {
			queries = append(queries, osvQueryItem{
				Package: osvPackageItem{Name: p.Name, Ecosystem: p.Ecosystem},
				Version: p.Version,
			})
		}
	}

	var vulns []ports.Vulnerability
	incomplete := false
	if !offline && len(queries) > 0 {
		discovered, err := s.queryOSVBatch(ctx, queries)
		if err == nil {
			vulns = append(vulns, discovered...)
		} else {
			incomplete = true
			s.logger.WarnContext(ctx, "scanner: osv batch query failed, scan is incomplete", "err", err)
		}
	}

	// Toolchain advisories from embedded checks on the packages discovered.
	// appRuntime is deliberately passed as "bun" (i.e. ungated) here: these
	// packages were positively OBSERVED inside the image being scanned, and
	// evidence of presence beats whatever runtime the build requested — see
	// checkEmbeddedAdvisories' doc comment.
	var toolchainAdvisories []ports.Vulnerability
	for _, p := range packages {
		if p.Name == "bun" || p.Name == "@sveltejs/kit" {
			toolchainAdvisories = append(toolchainAdvisories, s.checkEmbeddedAdvisories(string(ports.RuntimeBun), p.Version, p.Version)...)
		}
	}

	// Only cache a complete result — caching an incomplete one would make a
	// transient network failure permanently sticky for the process
	// lifetime, silently hiding real findings from every scan after it.
	if !incomplete {
		s.mu.Lock()
		s.cache[target] = vulns
		s.mu.Unlock()
	}

	return vulns, toolchainAdvisories, incomplete, nil
}

// checkEmbeddedAdvisories matches the embedded advisory list against the
// given toolchain versions. appRuntime keys which runtime advisories can
// apply at all (the second dimension the --runtime flag introduced): a
// "node" build ships no Bun anywhere in the produced image, so a Bun
// advisory reported against it would be a false claim — the advisory's
// subject simply is not present. Empty appRuntime means bun, matching
// ports.ScanRequest.AppRuntime's documented default and every pre-existing
// caller. An advisory for a package the scan positively OBSERVED (e.g. a
// "bun" package found inside an image's own metadata — see
// scanImageOrTarball) is deliberately NOT gated by this: evidence of
// presence beats the requested runtime, so those call sites pass "bun".
func (s *Adapter) checkEmbeddedAdvisories(appRuntime, bunVer, kitVer string) []ports.Vulnerability {
	var matches []ports.Vulnerability
	for _, adv := range embeddedAdvisories {
		if adv.Package == "bun" && appRuntime != string(ports.RuntimeNode) && isVersionOlderThan(bunVer, adv.FixedVersion) {
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

		httpReq, err := http.NewRequestWithContext(ctx, "POST", s.osvBaseURL+"/v1/querybatch", bytes.NewReader(body))
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
			//nolint:gosec // G602 false positive: the `qIdx >= len(chunk)` guard
			// directly above bounds this index, which gosec's taint analysis does
			// not follow. Excluded at this site rather than disabling G602
			// repo-wide, where it could catch a genuine unguarded index.
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
	if v == "" || fixed == "" {
		return false
	}
	return compareVersions(v, fixed) < 0
}

// compareVersions performs a dot-segment-wise, numeric-aware comparison of
// two already-cleaned version strings (see cleanVersion), returning -1, 0,
// or 1 depending on whether v is older than, equal to, or newer than fixed.
//
// Unlike a plain byte-wise string compare, each '.'-delimited segment of
// the NUMERIC core is compared as an integer whenever both sides parse as
// one, so width differences ("1.9.0" vs "1.10.0") compare correctly instead
// of lexicographically. A version with fewer core segments than the other
// is padded with implicit trailing zeros ("1.2" == "1.2.0"), matching
// ordinary semantic-version comparison.
//
// A trailing "-suffix" (a semver pre-release tag such as "-beta", or a
// distro build/package revision such as Debian/Ubuntu's "-1ubuntu4") is
// split off the numeric core before the core comparison runs. If the
// numeric cores are equal, a version carrying a suffix is treated as OLDER
// than the bare numeric version with no suffix. That is correct per semver
// for pre-release tags ("a pre-release version precedes the associated
// normal version", semver.org #11) and is a deliberately conservative
// choice for unrecognized distro-style suffixes: this is a CVE gate, so
// when the exact meaning of a suffix can't be determined, treating the
// suffixed build as still-vulnerable (rather than silently trusting an
// unfamiliar suffix format to mean "already patched") is the fail-safe
// direction. A false positive here costs a human a few seconds re-reading a
// report; a false negative would let an unpatched dependency silently pass
// the exact check meant to catch it.
//
// Any core segment that isn't a clean non-negative integer on both sides
// (and isn't byte-identical to its counterpart) can't be numerically
// ordered at all. Rather than fall back to an incidental byte-wise compare
// — whose result would depend on arbitrary ASCII code points and carries no
// real security meaning — this also resolves in the fail-safe direction:
// v is treated as NOT provably newer-or-equal, i.e. as older /
// potentially-still-vulnerable. Same reasoning applies when both versions
// carry different, mutually-unordered suffixes.
func compareVersions(v, fixed string) int {
	vCore, vSuffix := splitVersionSuffix(v)
	fCore, fSuffix := splitVersionSuffix(fixed)

	if c := compareNumericCores(vCore, fCore); c != 0 {
		return c
	}

	switch {
	case vSuffix == "" && fSuffix == "":
		return 0
	case vSuffix == "" && fSuffix != "":
		return 1
	case vSuffix != "" && fSuffix == "":
		return -1
	case vSuffix == fSuffix:
		return 0
	default:
		// Two differently-suffixed builds of the same numeric core: no
		// universal ordering exists (pre-release ordering rules vary, and
		// distro revision schemes aren't standardized), so this also
		// resolves fail-safe rather than via an arbitrary byte compare.
		return -1
	}
}

// splitVersionSuffix separates a version's numeric dotted core from a
// trailing "-suffix" (pre-release tag or distro build revision), e.g.
// "1.2.0-beta" -> ("1.2.0", "beta"), "1.2.3-1ubuntu4" -> ("1.2.3", "1ubuntu4").
// A version with no hyphen returns itself as the core with an empty suffix.
func splitVersionSuffix(v string) (core, suffix string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// compareNumericCores compares two dot-delimited numeric cores segment by
// segment, returning -1, 0, or 1. Missing trailing segments on the shorter
// side are treated as an implicit "0". A segment that isn't a clean integer
// on both sides falls back to the fail-safe -1 (treat v as not provably
// newer-or-equal) rather than an arbitrary byte-wise compare — see
// compareVersions' doc comment for the full justification.
func compareNumericCores(v, fixed string) int {
	vSegs := strings.Split(v, ".")
	fSegs := strings.Split(fixed, ".")
	n := len(vSegs)
	if len(fSegs) > n {
		n = len(fSegs)
	}
	for i := 0; i < n; i++ {
		vSeg, fSeg := "0", "0"
		if i < len(vSegs) {
			vSeg = vSegs[i]
		}
		if i < len(fSegs) {
			fSeg = fSegs[i]
		}
		if vSeg == fSeg {
			continue
		}
		vNum, vErr := strconv.Atoi(vSeg)
		fNum, fErr := strconv.Atoi(fSeg)
		if vErr != nil || fErr != nil {
			// Non-numeric segment(s) that aren't byte-identical: no
			// reliable numeric ordering exists. Fail safe (see
			// compareVersions' doc comment).
			return -1
		}
		if vNum != fNum {
			if vNum < fNum {
				return -1
			}
			return 1
		}
	}
	return 0
}
