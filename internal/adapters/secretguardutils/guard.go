package secretguardutils

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// IsUtilityPackage is the sentinel marker identifying this package as a non-adapter utility.
const IsUtilityPackage = true

type rule struct {
	Name    string
	Pattern *regexp.Regexp
}

var defaultSecretRules = []rule{
	{
		Name:    "RSA Private Key",
		Pattern: regexp.MustCompile(`-----BEGIN (?:RSA )?PRIVATE KEY-----`),
	},
	{
		Name:    "AWS Access Key ID",
		Pattern: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	},
	{
		Name:    "GitHub Personal Access Token",
		Pattern: regexp.MustCompile(`\bghp_[a-zA-Z0-9]{36}\b`),
	},
	{
		Name:    "Google API Key",
		Pattern: regexp.MustCompile(`\bAIzaSy[a-zA-Z0-9_-]{33}\b`),
	},
	{
		Name:    "Generic Hardcoded Password Assignment",
		Pattern: regexp.MustCompile(`(?i)\b(?:password|secret|api_key|token)\s*[:=]\s*["']([^"'\s]{8,})["']`),
	},
}

// SecretGuardAdapter implements ports.SecretGuard.
type SecretGuardAdapter struct{}

// NewAdapter constructs a new SecretGuardAdapter.
func NewAdapter() *SecretGuardAdapter {
	return &SecretGuardAdapter{}
}

// ScanDirectory walks req.ProjectDir and checks files for hardcoded secret leaks.
func (a *SecretGuardAdapter) ScanDirectory(ctx context.Context, req ports.SecretScanRequest) (ports.SecretScanResult, error) {
	if req.ProjectDir == "" {
		return ports.SecretScanResult{Passed: true}, nil
	}

	ignorer, _ := ignoreutils.Load(req.ProjectDir)

	var compiledAllow []*regexp.Regexp
	for _, pat := range req.AllowPatterns {
		if strings.TrimSpace(pat) == "" {
			continue
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return ports.SecretScanResult{}, fmt.Errorf("secretguardutils: invalid allow pattern %q: %w", pat, err)
		}
		compiledAllow = append(compiledAllow, re)
	}

	var matches []ports.SecretMatch

	err := filepath.WalkDir(req.ProjectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rel, err := filepath.Rel(req.ProjectDir, path)
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if rel != "." && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".svelte-kit" || d.Name() == ".pokkum") {
				return filepath.SkipDir
			}
			if rel != "." && ignorer != nil && ignorer.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if ignorer != nil && ignorer.Match(rel, false) {
			return nil
		}

		// Skip binary files and large files (>2MB)
		info, err := d.Info()
		if err != nil || info.Size() > 2*1024*1024 {
			return nil
		}

		fileMatches, err := scanFile(path, rel, compiledAllow)
		if err != nil {
			return nil
		}
		matches = append(matches, fileMatches...)
		return nil
	})

	if err != nil {
		return ports.SecretScanResult{}, fmt.Errorf("secretguardutils: scan %s: %w", req.ProjectDir, err)
	}

	return ports.SecretScanResult{
		Matches: matches,
		Passed:  len(matches) == 0,
	}, nil
}

func scanFile(absPath, relPath string, allowPatterns []*regexp.Regexp) ([]ports.SecretMatch, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var matches []ports.SecretMatch
	scanner := bufio.NewScanner(f)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		// Skip if line matches any allowed exception pattern
		allowed := false
		for _, allowRE := range allowPatterns {
			if allowRE.MatchString(line) {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}

		for _, r := range defaultSecretRules {
			if loc := r.Pattern.FindStringIndex(line); loc != nil {
				snippet := line[loc[0]:loc[1]]
				if len(snippet) > 40 {
					snippet = snippet[:37] + "..."
				}
				matches = append(matches, ports.SecretMatch{
					FilePath:      relPath,
					LineNumber:    lineNo,
					RuleName:      r.Name,
					SecretSnippet: snippet,
				})
				break // one match per line is enough
			}
		}
	}
	return matches, scanner.Err()
}
