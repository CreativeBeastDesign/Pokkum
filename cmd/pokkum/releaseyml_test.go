package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// release.yml's npm publishing must use flags that exist, and must not omit the
// one flag a scoped first publish cannot work without.
//
// Why this exists: the release job ran
//
//	npx goreleaser-npm-publisher --name @pokkum/cli --bin pokkum
//
// and neither --name nor --bin is a flag that tool has. The step failed on both
// v1.0.0 and v1.0.1 — the GitHub release binaries and the Homebrew formula went
// out fine, so the failure looked like a flaky publish rather than a command
// that could never work, and it stayed broken across two releases.
//
// This is the same class as actionyml_test.go (which exists because action.yml
// invoked a nonexistent --repo) and flagmentions_test.go (a nonexistent flag in
// a Go string). Third surface, same mistake: an invocation nobody can run
// locally, so nothing catches it until a release fails. mem:self_review_checklist
// row 46 — guard the class, not the surface.
//
// Unlike those two, the flags here belong to a third-party tool, so they cannot
// be enumerated from our own code. The list below is transcribed from
// `npx -y goreleaser-npm-publisher build --help` (verified 2026-08-20, v1.x).
// If the tool gains or renames a flag, re-run that command and update this list —
// a wrong entry here is a false failure, but a missing entry is a broken release.
var npmPublisherFlags = map[string]bool{
	"--project":     true,
	"--builder":     true,
	"--clear":       true,
	"--clean":       true, // accepted alias of --clear
	"--prefix":      true,
	"--description": true,
	"--files":       true,
	"--keywords":    true,
	"--license":     true,
	"--verbose":     true,
	"--token":       true, // publish subcommand only
	"--help":        true,
}

type workflowFile struct {
	Jobs map[string]struct {
		Steps []struct {
			Name string `yaml:"name"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadReleaseWorkflow(t *testing.T) workflowFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading release.yml: %v", err)
	}
	var wf workflowFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("[TEST SETUP] parsing release.yml: %v", err)
	}
	if len(wf.Jobs) == 0 {
		t.Fatal("[TEST SETUP] release.yml parsed to zero jobs")
	}
	return wf
}

// commandLines returns the non-comment lines of a run block, joining backslash
// continuations so a flag on a wrapped line is still attributed to its command.
func commandLines(run string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(run, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		cur.WriteString(" ")
		cur.WriteString(strings.TrimSuffix(trimmed, "\\"))
		if !strings.HasSuffix(trimmed, "\\") {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

var releaseFlagRe = regexp.MustCompile(`--[a-z][a-z0-9-]+`)

func TestReleaseWorkflowNpmPublisherFlagsExist(t *testing.T) {
	wf := loadReleaseWorkflow(t)

	// Premise check: the extractor must actually find flags in a known command.
	if got := releaseFlagRe.FindAllString("npx -y goreleaser-npm-publisher build --clear --prefix @x", -1); len(got) != 2 {
		t.Fatalf("[TEST SETUP] flag extraction is broken: %v", got)
	}

	var invocations int
	for jobName, job := range wf.Jobs {
		for _, step := range job.Steps {
			for _, cmd := range commandLines(step.Run) {
				if !strings.Contains(cmd, "goreleaser-npm-publisher") {
					continue
				}
				invocations++
				for _, flag := range releaseFlagRe.FindAllString(cmd, -1) {
					if npmPublisherFlags[flag] {
						continue
					}
					t.Errorf("release.yml job %q step %q invokes goreleaser-npm-publisher with %q, which is not a flag that tool has.\n"+
						"\tThe package name is always prefix + binary name; there is no --name or --bin.\n"+
						"\tRun `npx -y goreleaser-npm-publisher build --help` and fix the invocation, or update npmPublisherFlags if the tool genuinely changed.",
						jobName, step.Name, flag)
				}
			}
		}
	}
	if invocations == 0 {
		t.Fatal("[TEST SETUP] found no goreleaser-npm-publisher invocation in release.yml; this guard is watching nothing")
	}
}

// TestReleaseWorkflowPublishesScopedPackagesPublicly guards the other half of the
// failure. A scoped package's first publish defaults to `restricted`, and a free
// org cannot host restricted packages, so `npm publish` without --access public
// fails for @pokkum/* — and it fails at release time, on a version number that
// can never be reused.
func TestReleaseWorkflowPublishesScopedPackagesPublicly(t *testing.T) {
	wf := loadReleaseWorkflow(t)

	var publishes int
	for jobName, job := range wf.Jobs {
		for _, step := range job.Steps {
			for _, cmd := range commandLines(step.Run) {
				if !strings.Contains(cmd, "npm publish") {
					continue
				}
				publishes++
				if !strings.Contains(cmd, "--access public") {
					t.Errorf("release.yml job %q step %q runs `npm publish` without --access public:\n\t%s\n"+
						"\tScoped packages default to restricted, which a free org cannot host, so this fails at release time.",
						jobName, step.Name, cmd)
				}
			}
		}
	}
	if publishes == 0 {
		t.Fatal("[TEST SETUP] found no `npm publish` invocation in release.yml; this guard is watching nothing")
	}
}
