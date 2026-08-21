package secretguard_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/secretguard"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// This file is the measured half of roadmap item generic-secret-rule-key-coverage.
//
// Widening the generic rule's key side from an exact word to a suffix match
// increases how often that rule's KEY can fire, and the immediately preceding
// change (generic-secret-rule-matched-minified-code) had just spent effort
// recovering false-positive budget on exactly this rule. The item's
// recommendation was therefore explicit that the widening be "validated against
// a corpus of real minified bundles so the false-positive cost is measured
// rather than assumed."
//
// Hand-written "minified-looking" strings cannot do that job: a fixture built
// from one's own mental model of minified output agrees with a regex built from
// the same model while both are wrong (mem:self_review_checklist row 50). So
// this test scans the repository's own real SvelteKit build output — genuine
// Vite/Rollup chunks, produced by a real build, with single lines in the tens of
// thousands of characters — and asserts the generic rule reports nothing on it.
//
// Two things make that zero meaningful rather than vacuous:
//
//   - the corpusFloor entries in buildCorpora below assert that the corpus
//     actually walked is big enough and minified enough to be worth the name
//     (mem row 47: a green result is a claim about what RAN, and "scanned
//     nothing" must not look like "found nothing").
//   - TestGenericRule_RealMinifiedCorpusCanProduceAFinding takes the same real
//     chunk, splices a suffixed-key credential into its longest line, and
//     requires a finding — proving the scan is capable of failing on this exact
//     content (mem row 50a).

// corpusFloor pins the minimum shape of the real build-output corpus each
// subtest must have walked. These are floors, not exact values: a fixture
// rebuild may add files or lengthen lines, and that is fine. What must never
// silently happen is the corpus shrinking to nothing — an empty or
// non-minified corpus would make the zero-findings assertion prove nothing.
type corpusFloor struct {
	dir string // repo-relative

	minFiles       int // total regular files walked
	minBytes       int64
	minLongestLine int // longest single line, in bytes, across the dir's .js files
}

// These directories are checked into the repository, so a missing one is a real
// defect, not an environmental condition — hence t.Fatalf rather than t.Skip.
//
// Every entry is genuine Vite/Rollup output from a real SvelteKit build. Note
// what is NOT here: testdata/fixtures/sveltekit-basic/build is a 1838-byte
// hand-written stub whose longest line is 37 characters. Including it would
// have inflated the file count of a corpus that, for this rule, is only worth
// anything to the extent it contains real minified content — so sveltekit-basic
// contributes its actual Vite client output instead.
//
// Floors sit under the values measured when the widening was validated:
//
//	adapter-node/build                 79 files  1 517 096 B  longest line 29 215
//	adapter-node/.svelte-kit/output    37 files    481 035 B  longest line 29 215
//	static/build                       15 files     80 837 B  longest line 29 346
//	static/.svelte-kit/output          72 files    577 280 B  longest line 29 346
//	basic/.svelte-kit/…/client         16 files    121 865 B  longest line 23 348
var buildCorpora = []corpusFloor{
	{dir: "testdata/fixtures/sveltekit-adapter-node/build", minFiles: 50, minBytes: 900_000, minLongestLine: 20_000},
	{dir: "testdata/fixtures/sveltekit-adapter-node/.svelte-kit/output", minFiles: 25, minBytes: 300_000, minLongestLine: 20_000},
	{dir: "testdata/fixtures/sveltekit-static/build", minFiles: 10, minBytes: 60_000, minLongestLine: 20_000},
	{dir: "testdata/fixtures/sveltekit-static/.svelte-kit/output", minFiles: 50, minBytes: 400_000, minLongestLine: 20_000},
	{dir: "testdata/fixtures/sveltekit-basic/.svelte-kit/jesterkit-sveltekit/client", minFiles: 10, minBytes: 80_000, minLongestLine: 20_000},
}

// repoRoot resolves the repository root from this package's directory
// (internal/adapters/secretguard), where `go test` sets the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// measureCorpus walks dir and reports what is actually there, independently of
// the scanner. It exists so the assertions below describe the corpus that was
// scanned rather than the corpus that was hoped for.
func measureCorpus(t *testing.T, dir string) (files int, bytes int64, longestLine int) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += info.Size()
		if strings.EqualFold(filepath.Ext(path), ".js") {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(data), "\n") {
				if len(line) > longestLine {
					longestLine = len(line)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("measure corpus %s: %v", dir, err)
	}
	return files, bytes, longestLine
}

// TestGenericRule_NoFindingsOnRealMinifiedBuildCorpus is the false-positive
// measurement the roadmap item asked for, made permanent so a future widening
// of any rule has to re-pay the same cost check.
//
// Measured when the suffix widening was validated: 0 findings with the old
// word-anchored key (`\b(?:password|secret|api_key|token)`) and 0 with the
// widened suffix-anchored one, over all 219 files / ~2.65 MiB of real
// Vite/Rollup output listed in buildCorpora. Two wider sweeps of the same
// regexes over real bundles on disk are recorded in the roadmap item's
// decision note; they found the same 0, plus one false-positive class that
// lives exclusively under node_modules, which ScanDirectory never walks.
func TestGenericRule_NoFindingsOnRealMinifiedBuildCorpus(t *testing.T) {
	root := repoRoot(t)

	for _, c := range buildCorpora {
		t.Run(c.dir, func(t *testing.T) {
			dir := filepath.Join(root, filepath.FromSlash(c.dir))
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("real build-output corpus %s is missing (it is checked into the repository, so this is a defect, not a skip): %v", c.dir, err)
			}

			files, bytes, longestLine := measureCorpus(t, dir)
			if files < c.minFiles {
				t.Fatalf("corpus %s walked %d files, need at least %d — a shrunken corpus makes the zero-findings assertion below vacuous", c.dir, files, c.minFiles)
			}
			if bytes < c.minBytes {
				t.Fatalf("corpus %s is %d bytes, need at least %d", c.dir, bytes, c.minBytes)
			}
			if longestLine < c.minLongestLine {
				t.Fatalf("corpus %s's longest .js line is %d chars, need at least %d — without genuinely minified content this corpus does not exercise the false-positive shape the rule was tightened for", c.dir, longestLine, c.minLongestLine)
			}

			res, err := secretguard.NewAdapter().ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
			if err != nil {
				t.Fatalf("ScanDirectory(%s): %v", c.dir, err)
			}

			// A skip is "I could not look at this file", which is not the same
			// claim as "I looked and found nothing" — folding the two together
			// is the exact bug Lessons.md's 2026-08-18 secretguard entry
			// records. Fail on either.
			if len(res.Skipped) != 0 {
				t.Errorf("corpus %s produced %d skipped file(s); a skipped file is unscanned, so the zero below would not cover it: %+v", c.dir, len(res.Skipped), res.Skipped)
			}
			if len(res.Matches) != 0 {
				t.Errorf("corpus %s (%d files, %d bytes, longest line %d) produced %d finding(s) on REAL build output; every one is a false positive: %+v",
					c.dir, files, bytes, longestLine, len(res.Matches), res.Matches)
			}
			if !res.Passed {
				t.Errorf("corpus %s must scan clean, got Passed=false", c.dir)
			}
		})
	}
}

// TestGenericRule_RealMinifiedCorpusCanProduceAFinding is the companion proof
// for the zero above: a check that agrees with everything cannot corroborate
// anything (mem:self_review_checklist row 50a). It takes a REAL minified chunk
// out of the corpus, appends a suffixed-key credential assignment to its
// longest line — so the credential sits inside genuinely minified content
// rather than on a tidy line of its own — and requires the scanner to find it.
//
// The fixture is copied into t.TempDir() first: testdata/fixtures is real build
// output shared with other packages' tests and is never written to here.
func TestGenericRule_RealMinifiedCorpusCanProduceAFinding(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, filepath.FromSlash("testdata/fixtures/sveltekit-adapter-node/build/client/_app/immutable/chunks/BchoDuwy.js"))

	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read real minified chunk: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	longest := 0
	for i, line := range lines {
		if len(line) > len(lines[longest]) {
			longest = i
		}
	}
	if len(lines[longest]) < 20_000 {
		t.Fatalf("premise broken: the chosen chunk's longest line is %d chars, which is not minified content", len(lines[longest]))
	}

	// Assembled from parts rather than written as one literal, so no
	// credential-shaped string is ever at rest in the repository (Lessons.md's
	// push-protection entry).
	const key = "refresh" + "Token"
	secret := "hunter2" + "hunter2" + "deadbeef"
	lines[longest] += `;var ` + key + `="` + secret + `";`

	dir := t.TempDir()
	dst := filepath.Join(dir, "build", "client", "_app", "immutable", "chunks", "BchoDuwy.js")
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := secretguard.NewAdapter().ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("the spliced chunk was skipped, not scanned: %+v", res.Skipped)
	}
	found := false
	for _, m := range res.Matches {
		if m.RuleName == "Generic Hardcoded Password Assignment" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a %s= credential spliced into a real %d-char minified line produced no generic-rule finding; the corpus scan above is therefore incapable of failing and its zero proves nothing (matches=%+v)",
			key, len(lines[longest]), res.Matches)
	}
}
