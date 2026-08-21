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
			Name string            `yaml:"name"`
			Run  string            `yaml:"run"`
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
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

// TestReleaseWorkflowTestStepMatchesCIGate keeps the release pipeline's test
// invocation aligned with ci.yml's.
//
// They diverged once and it cost two tags: release.yml ran `go test ./...` while
// ci.yml's gate ran `go test -short ...`, so the release job executed tests CI
// had never run, in a job with no Bun and no Docker. Both v1.0.2 and v1.0.3
// failed there — after the tag was pushed, and a tag's version can never be
// reused. A release gate that is stricter than the gate people actually merge
// against does not catch bugs earlier, it catches them at the worst moment.
func TestReleaseWorkflowTestStepMatchesCIGate(t *testing.T) {
	wf := loadReleaseWorkflow(t)

	var found int
	for jobName, job := range wf.Jobs {
		for _, step := range job.Steps {
			for _, cmd := range commandLines(step.Run) {
				if !strings.HasPrefix(cmd, "go test") {
					continue
				}
				found++
				if !strings.Contains(cmd, "-short") {
					t.Errorf("release.yml job %q step %q runs %q without -short.\n"+
						"\tci.yml's gate uses -short; a release job that runs more than the gate fails on a pushed tag "+
						"for tests nobody ran at merge time. Non-short coverage belongs in ci.yml's e2e job, where the "+
						"toolchain it needs is actually installed.",
						jobName, step.Name, cmd)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("[TEST SETUP] found no `go test` invocation in release.yml; this guard is watching nothing")
	}
}

// TestWorkflowsPinToolVersionsTheyDependOn covers the class that broke releasing
// v1.0.4: a third-party CLI installed at whatever version its installer action
// happens to default to, while a script in this repo depends on a flag only some
// versions have.
//
// scripts/cosign-sign-blob.sh passes --use-signing-config, which exists from
// cosign v3.1.0. sigstore/cosign-installer@v3 defaults to v3.0.6. The script was
// written and tested against a locally installed v3.1.3, so it worked everywhere
// except the one place it had to. GoReleaser had already built and archived every
// binary before signing failed with "unknown flag".
//
// The version an action picks by default is a value the environment resolves, not
// one this repo controls, and it can change under you when the action updates —
// mem:self_review_checklist row 48. Pin it, so the tool and the script that drives
// it move together.
func TestWorkflowsPinToolVersionsTheyDependOn(t *testing.T) {
	// action prefix -> the input that pins its version.
	pinned := map[string]string{
		"sigstore/cosign-installer": "cosign-release",
	}

	dir := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("[TEST SETUP] reading %s: %v", dir, err)
	}

	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("[TEST SETUP] reading %s: %v", e.Name(), err)
		}
		var wf workflowFile
		if err := yaml.Unmarshal(data, &wf); err != nil {
			t.Fatalf("[TEST SETUP] parsing %s: %v", e.Name(), err)
		}
		for jobName, job := range wf.Jobs {
			for _, step := range job.Steps {
				for action, input := range pinned {
					if !strings.HasPrefix(step.Uses, action) {
						continue
					}
					checked++
					if strings.TrimSpace(step.With[input]) == "" {
						t.Errorf("%s job %q uses %s without pinning %q.\n"+
							"\tThe action's default version is chosen by the action, not by this repo, and scripts here depend on "+
							"flags only some versions have (cosign's --use-signing-config needs >= v3.1.0, while the installer "+
							"defaults to v3.0.6). Pin it.",
							e.Name(), jobName, action, input)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("[TEST SETUP] found none of the pinned-tool actions in any workflow; this guard is watching nothing")
	}
}
