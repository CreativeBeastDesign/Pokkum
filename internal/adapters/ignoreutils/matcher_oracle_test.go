package ignoreutils

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

// This file holds the pre-optimisation matcher, copied VERBATIM from
// matcher.go as of commit b6342d0 (before rule classification, before the
// index-sliced ancestor walk, before MatchUnvalidated). It is the oracle for
// TestMatchDifferential, FuzzMatchDifferential and FuzzRuleDifferential.
//
// It is deliberately a full copy — parsing included — rather than a wrapper
// that reuses the production compile(). The optimisation adds classification
// *inside* compile, so an oracle that called compile() and only re-ran the
// old matching would share the very code path most likely to be wrong. A
// standalone copy can drift from production semantics, but drift here shows
// up as a loud differential failure, never as a silent agreement.
//
// Do not "clean this up" by delegating to the production code. The whole
// value of this file is that it does not.

type oracleRule struct {
	glob     string
	negate   bool
	dirOnly  bool
	anchored bool
}

type oracleMatcher struct {
	rules []oracleRule
}

func newOracle(patterns []string) (*oracleMatcher, error) {
	m := &oracleMatcher{}
	for i, raw := range patterns {
		r, ok, err := oracleCompile(raw)
		if err != nil {
			return nil, fmt.Errorf("ignore: pattern %d %q: %w", i+1, raw, err)
		}
		if !ok {
			continue
		}
		m.rules = append(m.rules, r)
	}
	return m, nil
}

func oracleCompile(line string) (r oracleRule, ok bool, err error) {
	s := strings.TrimRight(line, " \t")
	s = strings.TrimLeft(s, " \t")
	if s == "" || strings.HasPrefix(s, "#") {
		return oracleRule{}, false, nil
	}

	if strings.HasPrefix(s, "\\#") || strings.HasPrefix(s, "\\!") {
		s = s[1:]
	} else if strings.HasPrefix(s, "!") {
		r.negate = true
		s = s[1:]
	}
	if s == "" {
		return oracleRule{}, false, nil
	}

	if strings.HasSuffix(s, "/") {
		r.dirOnly = true
		s = strings.TrimSuffix(s, "/")
	}
	if s == "" {
		return oracleRule{}, false, nil
	}

	if strings.HasPrefix(s, "/") {
		r.anchored = true
		s = strings.TrimPrefix(s, "/")
	} else if strings.Contains(s, "/") {
		r.anchored = true
	}

	if !r.anchored {
		s = "**/" + s
	}

	if _, err := doublestar.Match(s, "probe"); err != nil {
		return oracleRule{}, false, fmt.Errorf("invalid glob: %w", err)
	}

	r.glob = s
	return r, true, nil
}

func (r oracleRule) matchGlob(candidate string) bool {
	ok, _ := doublestar.Match(r.glob, candidate)
	return ok
}

func (m *oracleMatcher) Match(relPath string, isDir bool) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}
	clean := path.Clean(filepath.ToSlash(relPath))
	if clean == "." || clean == "" || clean == "/" {
		return false
	}
	clean = strings.TrimPrefix(clean, "/")

	parts := strings.Split(clean, "/")
	for i := 1; i < len(parts); i++ {
		if m.matchExact(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return m.matchExact(clean, isDir)
}

func (m *oracleMatcher) matchExact(candidate string, isDir bool) bool {
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.matchGlob(candidate) {
			ignored = !r.negate
		}
	}
	return ignored
}

// BenchmarkMatchImplementations runs the pre-optimisation oracle and the
// current implementation over the same corpus, in the same process,
// interleaved. Comparing two separate `go test -bench` runs is not sound on a
// machine doing other work — this repo's own benchmark numbers swung 3x
// between back-to-back runs while other builds were in flight — so the
// speedup claim is made from paired sub-benchmarks rather than from a
// remembered baseline number.
func BenchmarkMatchImplementations(b *testing.B) {
	corpus := benchPathCorpus()
	projectPatterns := []string{
		"coverage/", "*.tsbuildinfo", "playwright-report/", "test-results/",
		"src/lib/paraglide/", "!src/lib/paraglide/runtime.js", "**/*.stories.svelte",
	}
	sets := []struct {
		name     string
		patterns []string
	}{
		{"default_patterns", DefaultPatterns()},
		{"default_plus_project_patterns", append(DefaultPatterns(), projectPatterns...)},
	}

	for _, set := range sets {
		newM, err := New(set.patterns)
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		oldM, err := newOracle(set.patterns)
		if err != nil {
			b.Fatalf("newOracle: %v", err)
		}
		b.Run(set.name+"/old", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				benchMatchSink = oldM.Match(corpus[i%len(corpus)], false)
			}
		})
		b.Run(set.name+"/new", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				benchMatchSink = newM.Match(corpus[i%len(corpus)], false)
			}
		})
		b.Run(set.name+"/new_matchexact", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				benchMatchSink = newM.MatchExact(corpus[i%len(corpus)], false)
			}
		})
	}
}
