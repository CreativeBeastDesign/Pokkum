// Command snapshot-advisories regenerates internal/adapters/scanner/
// advisories.json, the offline toolchain advisory snapshot the scanner
// embeds.
//
// It pulls real, published advisories from two sources:
//
//   - OSV.dev, for the npm packages @sveltejs/kit and bun (the npm "bun"
//     package's versions are the runtime's versions);
//   - GitHub's repository advisories for oven-sh/bun, where most Bun runtime
//     advisories are published and which OSV.dev does not carry under any
//     package a version query reaches.
//
// Withdrawn advisories are dropped. An advisory present in both sources keeps
// OSV's record. The output is sorted, and is validated with the scanner's own
// ParseAdvisorySnapshot before it is written, so a snapshot this tool writes
// is one the scanner will load.
//
// Usage: go run ./scripts/snapshot-advisories [-o path]
//
// GITHUB_TOKEN, when set, is sent to the GitHub API to lift the
// unauthenticated rate limit; the endpoint itself is public.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scanner"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

const (
	osvQueryURL      = "https://api.osv.dev/v1/query"
	githubAdvisories = "https://api.github.com/repos/oven-sh/bun/security-advisories"
)

var osvPackages = []string{"@sveltejs/kit", "bun"}

func main() {
	out := flag.String("o", "internal/adapters/scanner/advisories.json", "output path")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}

	byKey := map[string]scanner.EmbeddedAdvisory{}
	for _, pkg := range osvPackages {
		advs, err := fetchOSV(ctx, client, pkg)
		if err != nil {
			fail(err)
		}
		for _, a := range advs {
			byKey[a.Package+"/"+a.ID] = a
		}
	}
	gh, err := fetchGitHub(ctx, client)
	if err != nil {
		fail(err)
	}
	for _, a := range gh {
		if _, ok := byKey[a.Package+"/"+a.ID]; !ok {
			byKey[a.Package+"/"+a.ID] = a
		}
	}

	snap := scanner.AdvisorySnapshot{
		Snapshot: time.Now().UTC().Format("2006-01-02"),
		Sources: []string{
			osvQueryURL + " (npm: " + strings.Join(osvPackages, ", ") + ")",
			githubAdvisories,
		},
	}
	for _, a := range byKey {
		snap.Advisories = append(snap.Advisories, a)
	}
	sort.Slice(snap.Advisories, func(i, j int) bool {
		a, b := snap.Advisories[i], snap.Advisories[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.ID < b.ID
	})

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		fail(err)
	}
	data = append(data, '\n')
	if _, err := scanner.ParseAdvisorySnapshot(data); err != nil {
		fail(fmt.Errorf("generated snapshot does not validate: %w", err))
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("snapshot-advisories: wrote %d advisories to %s\n", len(snap.Advisories), *out)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "snapshot-advisories: %v\n", err)
	os.Exit(1)
}

type osvRecord struct {
	ID               string         `json:"id"`
	Aliases          []string       `json:"aliases"`
	Summary          string         `json:"summary"`
	Published        string         `json:"published"`
	Withdrawn        string         `json:"withdrawn"`
	DatabaseSpecific map[string]any `json:"database_specific"`
	Affected         []struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
		Ranges []struct {
			Type   string `json:"type"`
			Events []struct {
				Introduced   string `json:"introduced"`
				Fixed        string `json:"fixed"`
				LastAffected string `json:"last_affected"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

func fetchOSV(ctx context.Context, client *http.Client, pkg string) ([]scanner.EmbeddedAdvisory, error) {
	var out []scanner.EmbeddedAdvisory
	pageToken := ""
	for {
		body := map[string]any{"package": map[string]string{"name": pkg, "ecosystem": "npm"}}
		if pageToken != "" {
			body["page_token"] = pageToken
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Vulns         []osvRecord `json:"vulns"`
			NextPageToken string      `json:"next_page_token"`
		}
		if _, err := doJSON(ctx, client, http.MethodPost, osvQueryURL, payload, &resp); err != nil {
			return nil, fmt.Errorf("osv %s: %w", pkg, err)
		}
		for _, v := range resp.Vulns {
			if v.Withdrawn != "" {
				continue
			}
			adv := scanner.EmbeddedAdvisory{
				ID:        v.ID,
				Aliases:   v.Aliases,
				Package:   pkg,
				Severity:  osvSeverity(v.DatabaseSpecific),
				Title:     strings.TrimSpace(v.Summary),
				URL:       "https://github.com/advisories/" + v.ID,
				Published: dateOnly(v.Published),
			}
			if !strings.HasPrefix(v.ID, "GHSA-") {
				adv.URL = "https://osv.dev/vulnerability/" + v.ID
			}
			for _, aff := range v.Affected {
				if aff.Package.Name != pkg || aff.Package.Ecosystem != "npm" {
					continue
				}
				for _, r := range aff.Ranges {
					if r.Type != "SEMVER" && r.Type != "ECOSYSTEM" {
						continue
					}
					adv.Ranges = append(adv.Ranges, rangesFromEvents(r.Events)...)
				}
			}
			if len(adv.Ranges) > 0 {
				out = append(out, adv)
			}
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		pageToken = resp.NextPageToken
	}
}

// rangesFromEvents turns an OSV event list (introduced, then fixed or
// last_affected, possibly repeated) into closed intervals. A trailing
// introduced with no close is an interval with no fix.
func rangesFromEvents(events []struct {
	Introduced   string `json:"introduced"`
	Fixed        string `json:"fixed"`
	LastAffected string `json:"last_affected"`
}) []scanner.AdvisoryRange {
	var out []scanner.AdvisoryRange
	var cur *scanner.AdvisoryRange
	for _, ev := range events {
		switch {
		case ev.Introduced != "":
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &scanner.AdvisoryRange{Introduced: ev.Introduced}
		case ev.Fixed != "" || ev.LastAffected != "":
			if cur == nil {
				cur = &scanner.AdvisoryRange{}
			}
			cur.Fixed, cur.LastAffected = ev.Fixed, ev.LastAffected
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

type githubAdvisory struct {
	GHSAID          string `json:"ghsa_id"`
	CVEID           string `json:"cve_id"`
	HTMLURL         string `json:"html_url"`
	Summary         string `json:"summary"`
	Severity        string `json:"severity"`
	PublishedAt     string `json:"published_at"`
	WithdrawnAt     string `json:"withdrawn_at"`
	Vulnerabilities []struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
		VulnerableVersionRange string `json:"vulnerable_version_range"`
	} `json:"vulnerabilities"`
}

func fetchGitHub(ctx context.Context, client *http.Client) ([]scanner.EmbeddedAdvisory, error) {
	var out []scanner.EmbeddedAdvisory
	// The endpoint paginates by cursor and ignores a page number, so the next
	// page is only reachable through the Link header's rel="next" URL.
	url := githubAdvisories + "?state=published&per_page=100"
	for url != "" {
		var advs []githubAdvisory
		hdr, err := doJSON(ctx, client, http.MethodGet, url, nil, &advs)
		if err != nil {
			return nil, fmt.Errorf("github advisories: %w", err)
		}
		url = nextLink(hdr.Get("Link"))
		for _, a := range advs {
			if a.WithdrawnAt != "" {
				continue
			}
			adv := scanner.EmbeddedAdvisory{
				ID:        a.GHSAID,
				Package:   "bun",
				Severity:  githubSeverity(a.Severity),
				Title:     strings.TrimSpace(a.Summary),
				URL:       a.HTMLURL,
				Published: dateOnly(a.PublishedAt),
			}
			if a.CVEID != "" {
				adv.Aliases = []string{a.CVEID}
			}
			for _, v := range a.Vulnerabilities {
				if v.Package.Name != "bun" {
					continue
				}
				r, err := parseGitHubRange(v.VulnerableVersionRange)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", a.GHSAID, err)
				}
				adv.Ranges = append(adv.Ranges, r)
			}
			if len(adv.Ranges) > 0 {
				out = append(out, adv)
			}
		}
	}
	return out, nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextLink extracts the rel="next" URL from an RFC 8288 Link header, or "".
func nextLink(h string) string {
	if m := linkNext.FindStringSubmatch(h); m != nil {
		return m[1]
	}
	return ""
}

var githubRangeTerm = regexp.MustCompile(`^(>=|<=|<|=)\s*v?([0-9][0-9A-Za-z.\-]*)$`)

// parseGitHubRange parses GitHub's vulnerable_version_range, a
// comma-separated conjunction such as ">= 1.3.14, < 1.4.4". Anything it does
// not understand is an error, not a silently widened or dropped range.
func parseGitHubRange(s string) (scanner.AdvisoryRange, error) {
	var r scanner.AdvisoryRange
	for _, term := range strings.Split(s, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		m := githubRangeTerm.FindStringSubmatch(term)
		if m == nil {
			return r, fmt.Errorf("unrecognised version range term %q in %q", term, s)
		}
		switch m[1] {
		case ">=":
			r.Introduced = m[2]
		case "<":
			r.Fixed = m[2]
		case "<=":
			r.LastAffected = m[2]
		case "=":
			r.Introduced, r.LastAffected = m[2], m[2]
		}
	}
	return r, nil
}

func osvSeverity(db map[string]any) ports.Severity {
	if raw, ok := db["severity"].(string); ok {
		return githubSeverity(raw)
	}
	return ports.SeverityMedium
}

func githubSeverity(raw string) ports.Severity {
	switch strings.ToLower(raw) {
	case "critical":
		return ports.SeverityCritical
	case "high":
		return ports.SeverityHigh
	case "low":
		return ports.SeverityLow
	default:
		return ports.SeverityMedium
	}
}

func dateOnly(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func doJSON(ctx context.Context, client *http.Client, method, url string, body []byte, into any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" && strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, url, resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 300)])))
	}
	return resp.Header, json.Unmarshal(data, into)
}
