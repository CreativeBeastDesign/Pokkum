package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README's install instructions must point at things that exist.
//
// Why this exists: three of the four documented install paths were broken at
// once, on a public repository, and nothing noticed.
//   - `curl …/main/install.sh | sh` — install.sh was not in the repo at all; the
//     raw URL returned 404.
//   - `setup-pokkum@v1` — no `v1` tag or branch has ever existed, only full
//     versions, so the ref could not resolve.
//   - `npx @pokkum/cli` — the package was never published; the release
//     pipeline's npm step had failed on every release.
//
// This is the same failure class as cmd/pokkum/flagmentions_test.go (telling a
// user to run something that cannot work), one level up: there it was a flag
// inside a Go string, here it is an install command in Markdown. The flag guard
// could not see these, which is exactly the point of mem:self_review_checklist
// row 46 — guard the *class* of claim, not the surface it was first seen on.
//
// Deliberately offline. Every check resolves against this repository's own files
// and git refs, so the test is deterministic and cannot flake on network or rate
// limits. That leaves one gap, stated rather than hidden: a claim about an
// EXTERNAL registry (npm, a Homebrew tap) cannot be verified here. That is what
// broke with @pokkum/cli, so if you add an external-registry install path, it
// needs its own check somewhere that is allowed to reach the network.

const readmeRepoSlug = "CreativeBeastDesign/pokkum"

func readmeBody(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading README.md: %v", err)
	}
	return string(data)
}

func repoRootPath(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

// TestReadmeRawFileURLsExistInRepo covers the install.sh case: a
// raw.githubusercontent.com URL naming a path in this repo is a promise that the
// file is committed at that path on that branch.
func TestReadmeRawFileURLsExistInRepo(t *testing.T) {
	body := readmeBody(t)

	// https://raw.githubusercontent.com/<owner>/<repo>/<ref>/<path>
	re := regexp.MustCompile(`https://raw\.githubusercontent\.com/` + regexp.QuoteMeta(readmeRepoSlug) + `/([A-Za-z0-9._/-]+?)/([A-Za-z0-9._/-]+)`)

	matches := re.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Skip("README references no raw file URLs for this repo; nothing to check")
	}
	for _, m := range matches {
		ref, path := m[1], m[2]
		if _, err := os.Stat(repoRootPath(path)); err != nil {
			t.Errorf("README links https://raw.githubusercontent.com/%s/%s/%s but %q does not exist in the repo — that URL 404s for every reader.",
				readmeRepoSlug, ref, path, path)
		}
	}
}

// TestReadmeActionRefsResolve covers the setup-pokkum@v1 case: a `uses:` naming
// this repo promises both that the action path exists AND that the ref does.
func TestReadmeActionRefsResolve(t *testing.T) {
	body := readmeBody(t)

	// uses: <owner>/<repo>/<path>@<ref>   (also matches <owner>/<repo>@<ref>)
	re := regexp.MustCompile(`uses:\s*` + regexp.QuoteMeta(readmeRepoSlug) + `(?:/([A-Za-z0-9._/-]+))?@([A-Za-z0-9._/-]+)`)

	matches := re.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Skip("README references no GitHub Action from this repo; nothing to check")
	}

	tags := gitRefNames(t)

	for _, m := range matches {
		path, ref := m[1], m[2]
		if path != "" {
			if _, err := os.Stat(repoRootPath(path)); err != nil {
				t.Errorf("README documents `uses: %s/%s@%s` but %q does not exist in the repo.",
					readmeRepoSlug, path, ref, path)
			}
		}
		if tags == nil {
			continue // no git metadata available; the path check above still ran
		}
		if !tags[ref] {
			t.Errorf("README documents `uses: %s/%s@%s`, but %q is not a tag or branch in this repo, so the ref cannot resolve.\n"+
				"\tPin a released version (there is no moving major tag unless one is actually published and re-pointed on each release).",
				readmeRepoSlug, path, ref, ref)
		}
	}
}

// gitRefNames returns every local tag and branch name, or nil when git metadata
// is unavailable (a source archive rather than a checkout).
func gitRefNames(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRootPath(), "for-each-ref",
		"--format=%(refname:short)", "refs/tags", "refs/heads").Output()
	if err != nil {
		t.Logf("git refs unavailable (%v); skipping ref-resolution half of this check", err)
		return nil
	}
	refs := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			refs[line] = true
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

// TestReadmeDoesNotPresentUnpublishedNpmPackageAsWorking covers the third case.
// The package's existence cannot be checked offline, so this asserts the weaker
// but still useful property: the README must not hand the reader an @pokkum/cli
// command without saying, nearby, that it is not published yet. Delete this test
// when the package actually publishes — and replace it with a real check.
func TestReadmeDoesNotPresentUnpublishedNpmPackageAsWorking(t *testing.T) {
	body := readmeBody(t)

	const pkg = "@pokkum/cli"
	if !strings.Contains(body, pkg) {
		t.Skip("README no longer mentions the npm package")
	}

	// The disclaimer must appear before the first runnable mention, so a reader
	// scanning top-down cannot copy a command that fails.
	firstMention := strings.Index(body, pkg)
	notice := regexp.MustCompile(`(?i)not (?:yet )?(?:on npm|published)|not yet published|coming soon`)
	loc := notice.FindStringIndex(body)

	if loc == nil {
		t.Errorf("README documents %s but never says it is unpublished. As of this test's writing the package 404s on npm, "+
			"so every reader who copies that command hits a failure.\n"+
			"\tIf it has since been published, replace this test with a real registry check rather than deleting the guard.", pkg)
		return
	}
	if loc[0] > firstMention {
		t.Errorf("README's %q disclaimer appears at byte %d, after the first %s mention at byte %d — "+
			"a reader scanning top-down copies the broken command before reaching the caveat.",
			body[loc[0]:loc[1]], loc[0], pkg, firstMention)
	}
}
