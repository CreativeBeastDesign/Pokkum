package slsa

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test when no usable git binary is present, and
// isolates every git invocation (the helpers below *and* the code under
// test) from the developer's global/system git configuration. Without that
// isolation a global `status.showUntrackedFiles = all`, a `commit.gpgsign`,
// or a custom `core.excludesFile` would silently change what
// `git status --porcelain` emits and make these assertions depend on the
// machine they run on.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	// discoverGitCommit short-circuits on GITHUB_SHA and reports clean
	// unconditionally on that path; these tests exercise the local
	// working-tree path, so the CI environment must not leak in.
	t.Setenv("GITHUB_SHA", "")
	t.Setenv("GITHUB_SERVER_URL", "")
	t.Setenv("GITHUB_REPOSITORY", "")
}

// git runs a git command in dir and fails the test if it errors.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFile creates dir-relative path (including parents) with content.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// newRepo creates a real git repository with one commit containing a single
// tracked source file, and returns its root and the commit SHA.
func newRepo(t *testing.T) (root, commit string) {
	t.Helper()
	root = t.TempDir()
	gitCmd(t, root, "init", "--quiet")
	gitCmd(t, root, "config", "user.email", "test@pokkum.invalid")
	gitCmd(t, root, "config", "user.name", "Pokkum Test")
	gitCmd(t, root, "config", "commit.gpgsign", "false")
	writeFile(t, root, "src/app.ts", "export const answer = 42;\n")
	gitCmd(t, root, "add", ".")
	gitCmd(t, root, "commit", "--quiet", "-m", "initial")
	commit = gitCmd(t, root, "rev-parse", "HEAD")
	return root, commit
}

// ociVersionLabelDirty replicates, verbatim, the command
// cmd/pokkum/git_metadata.go's getGitVersion uses to build the
// org.opencontainers.image.version OCI label
// (`git describe --tags --always --dirty`) and reports whether that label
// would carry the "-dirty" suffix.
//
// This is deliberately a re-implementation rather than an import: package
// main is not importable, and the point of the assertion is that the two
// independent Pokkum outputs (the OCI label and the signed SLSA
// attestation) reach the same verdict about the same tree. If getGitVersion
// ever changes, this helper's comment is the breadcrumb back to it.
func ociVersionLabelDirty(t *testing.T, dir string) bool {
	t.Helper()
	return strings.HasSuffix(gitCmd(t, dir, "describe", "--tags", "--always", "--dirty"), "-dirty")
}

// TestDiscoverGitCommit_DirtyDetection is the F8 regression suite: Pokkum's
// own generated artifacts must not make the source tree it just built from
// look modified, while every genuine modification — tracked or untracked —
// still must.
func TestDiscoverGitCommit_DirtyDetection(t *testing.T) {
	t.Run("clean tree is clean and agrees with the OCI version label", func(t *testing.T) {
		requireGit(t)
		root, want := newRepo(t)

		commit, dirty := discoverGitCommit(context.Background(), root)
		if commit != want {
			t.Errorf("commit = %q, want %q", commit, want)
		}
		if dirty {
			t.Errorf("dirty = true on a clean tree, want false")
		}
		if got := ociVersionLabelDirty(t, root); got != dirty {
			t.Errorf("OCI version label dirty = %v but SLSA dirty = %v: the two Pokkum outputs disagree", got, dirty)
		}
	})

	// The actual F8 bug: `git status --porcelain` prints a line for
	// untracked paths too, so Pokkum's own .pokkum/ sandbox and pokkum.lock
	// made every build after the first report a "-dirty" source digest.
	t.Run("pokkum's own untracked artifacts do not make the tree dirty", func(t *testing.T) {
		requireGit(t)
		root, want := newRepo(t)
		// .pokkum/ now stages a whole node_modules tree; the nesting is
		// intentional, to prove the directory is matched as a subtree and
		// not only as a bare top-level entry.
		writeFile(t, root, ".pokkum/vendor/node_modules/left-pad/package.json", "{}\n")
		writeFile(t, root, ".pokkum/telemetry-entry.ts", "// generated\n")
		writeFile(t, root, "pokkum.lock", "{\"version\":1}\n")

		// Sanity: git really does report these, so the assertion below is
		// exercising the filter and not an empty status.
		if status := gitCmd(t, root, "status", "--porcelain"); status == "" {
			t.Fatalf("precondition failed: git status --porcelain is empty, nothing to filter")
		}

		commit, dirty := discoverGitCommit(context.Background(), root)
		if commit != want {
			t.Errorf("commit = %q, want %q", commit, want)
		}
		if dirty {
			t.Errorf("dirty = true from Pokkum's own generated artifacts alone, want false\ngit status:\n%s",
				gitCmd(t, root, "status", "--porcelain"))
		}
		if got := ociVersionLabelDirty(t, root); got != dirty {
			t.Errorf("OCI version label dirty = %v but SLSA dirty = %v: the two Pokkum outputs disagree", got, dirty)
		}
	})

	// The other half of the same coin: an over-broad fix (for example
	// passing --untracked-files=no) would also pass the test above while
	// throwing away a real reproducibility signal.
	t.Run("a genuinely untracked source file still reports dirty", func(t *testing.T) {
		requireGit(t)
		root, want := newRepo(t)
		writeFile(t, root, "src/routes/+page.svelte", "<h1>never committed</h1>\n")

		commit, dirty := discoverGitCommit(context.Background(), root)
		if commit != want {
			t.Errorf("commit = %q, want %q", commit, want)
		}
		if !dirty {
			t.Errorf("dirty = false with an untracked source file present, want true")
		}
	})

	t.Run("pokkum artifacts alongside an untracked source file still report dirty", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		writeFile(t, root, ".pokkum/vendor/node_modules/left-pad/package.json", "{}\n")
		writeFile(t, root, "pokkum.lock", "{\"version\":1}\n")
		writeFile(t, root, "src/lib/secretsauce.ts", "export const x = 1;\n")

		if _, dirty := discoverGitCommit(context.Background(), root); !dirty {
			t.Errorf("dirty = false, want true: the untracked source file must not be masked by the Pokkum artifacts")
		}
	})

	t.Run("an untracked source file with awkward characters still reports dirty", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		// A path git would C-quote in non -z porcelain output. If the
		// parser mishandles quoting it must err towards dirty, never away
		// from it.
		writeFile(t, root, "src/a file \"with\" quotes.ts", "export const x = 1;\n")

		if _, dirty := discoverGitCommit(context.Background(), root); !dirty {
			t.Errorf("dirty = false, want true for an awkwardly-named untracked source file")
		}
	})

	// ".pokkum.yaml" (ports.ConfigFilename) shares a prefix with the
	// ".pokkum" sandbox directory but is user-authored configuration, not a
	// Pokkum-generated artifact — a prefix match that is not
	// path-segment-aware would wrongly excuse it.
	t.Run("an untracked .pokkum.yaml config still reports dirty", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		writeFile(t, root, ".pokkum.yaml", "version: 1\n")

		if _, dirty := discoverGitCommit(context.Background(), root); !dirty {
			t.Errorf("dirty = false for an untracked .pokkum.yaml, want true: it is user-authored config, not a generated artifact")
		}
	})

	t.Run("a modified tracked file still reports dirty", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		writeFile(t, root, "src/app.ts", "export const answer = 43;\n")

		_, dirty := discoverGitCommit(context.Background(), root)
		if !dirty {
			t.Errorf("dirty = false with a modified tracked file, want true")
		}
		if got := ociVersionLabelDirty(t, root); got != dirty {
			t.Errorf("OCI version label dirty = %v but SLSA dirty = %v: the two Pokkum outputs disagree", got, dirty)
		}
	})

	t.Run("a staged new source file still reports dirty", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		writeFile(t, root, "src/staged.ts", "export const x = 1;\n")
		gitCmd(t, root, "add", "src/staged.ts")

		if _, dirty := discoverGitCommit(context.Background(), root); !dirty {
			t.Errorf("dirty = false with a staged-but-uncommitted file, want true")
		}
	})

	// A tracked-and-modified pokkum.lock is deliberately still dirty: the
	// tree genuinely diverges from the commit in a way `git describe
	// --dirty` also reports, so suppressing it here would recreate the very
	// label/attestation disagreement F8 is about, with the signs flipped.
	t.Run("a tracked pokkum.lock that Pokkum rewrote still reports dirty", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		writeFile(t, root, "pokkum.lock", "{\"version\":1}\n")
		gitCmd(t, root, "add", "pokkum.lock")
		gitCmd(t, root, "commit", "--quiet", "-m", "commit the lockfile")
		writeFile(t, root, "pokkum.lock", "{\"version\":2}\n")

		_, dirty := discoverGitCommit(context.Background(), root)
		if !dirty {
			t.Errorf("dirty = false for a modified tracked pokkum.lock, want true")
		}
		if got := ociVersionLabelDirty(t, root); got != dirty {
			t.Errorf("OCI version label dirty = %v but SLSA dirty = %v: the two Pokkum outputs disagree", got, dirty)
		}
	})

	// A tracked artifact modification mixed with an untracked one, which the
	// artifact-only pass must not let the skippable entry mask. (git emits
	// tracked changes ahead of untracked ones, so the tracked entry lands
	// first here regardless of path ordering; TestHasTrackedEntry covers the
	// reverse order directly, without depending on that.)
	t.Run("a tracked pokkum.lock modification alongside an untracked .pokkum still reports dirty", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		writeFile(t, root, "pokkum.lock", "{\"version\":1}\n")
		gitCmd(t, root, "add", "pokkum.lock")
		gitCmd(t, root, "commit", "--quiet", "-m", "commit the lockfile")
		writeFile(t, root, ".pokkum/vendor/node_modules/left-pad/package.json", "{}\n")
		writeFile(t, root, "pokkum.lock", "{\"version\":2}\n")

		// Premise check: both entries must really be present, or this is
		// just the single-entry case again.
		status := gitCmd(t, root, "status", "--porcelain")
		if !strings.Contains(status, "pokkum.lock") || !strings.Contains(status, ".pokkum/") {
			t.Fatalf("[TEST SETUP] expected both a tracked pokkum.lock change and an untracked .pokkum/, got:\n%s", status)
		}

		_, dirty := discoverGitCommit(context.Background(), root)
		if !dirty {
			t.Errorf("dirty = false, want true: the tracked pokkum.lock modification must not be masked by the untracked .pokkum/\ngit status:\n%s", status)
		}
		if got := ociVersionLabelDirty(t, root); got != dirty {
			t.Errorf("OCI version label dirty = %v but SLSA dirty = %v: the two Pokkum outputs disagree", got, dirty)
		}
	})

	// A user who gitignores Pokkum's artifacts never sees them in
	// `git status --porcelain` at all, so nothing needs filtering — but the
	// result must be the same either way, otherwise the provenance depends
	// on the project's .gitignore.
	t.Run("gitignored pokkum artifacts are clean, same as unignored ones", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		writeFile(t, root, ".gitignore", ".pokkum/\npokkum.lock\n")
		gitCmd(t, root, "add", ".gitignore")
		gitCmd(t, root, "commit", "--quiet", "-m", "ignore pokkum artifacts")
		want := gitCmd(t, root, "rev-parse", "HEAD")
		writeFile(t, root, ".pokkum/vendor/node_modules/left-pad/package.json", "{}\n")
		writeFile(t, root, "pokkum.lock", "{\"version\":1}\n")

		if status := gitCmd(t, root, "status", "--porcelain"); status != "" {
			t.Fatalf("precondition failed: gitignored artifacts appeared in git status:\n%s", status)
		}
		commit, dirty := discoverGitCommit(context.Background(), root)
		if commit != want {
			t.Errorf("commit = %q, want %q", commit, want)
		}
		if dirty {
			t.Errorf("dirty = true with gitignored Pokkum artifacts, want false")
		}
	})

	// Monorepo shape: `git status --porcelain` paths are relative to the
	// repository root, not to the project directory, so a project in a
	// subdirectory needs the prefix applied before matching.
	t.Run("project in a repository subdirectory", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		project := filepath.Join(root, "apps", "web")
		writeFile(t, root, "apps/web/package.json", "{}\n")
		gitCmd(t, root, "add", ".")
		gitCmd(t, root, "commit", "--quiet", "-m", "add the web app")
		want := gitCmd(t, root, "rev-parse", "HEAD")

		writeFile(t, root, "apps/web/.pokkum/vendor/node_modules/left-pad/package.json", "{}\n")
		writeFile(t, root, "apps/web/pokkum.lock", "{\"version\":1}\n")

		commit, dirty := discoverGitCommit(context.Background(), project)
		if commit != want {
			t.Errorf("commit = %q, want %q", commit, want)
		}
		if dirty {
			t.Errorf("dirty = true from Pokkum artifacts in a subdirectory project, want false\ngit status:\n%s",
				gitCmd(t, root, "status", "--porcelain"))
		}

		// A .pokkum/ at the *repository* root is not this project's
		// artifact and must not be excused.
		writeFile(t, root, ".pokkum/somebody-elses.txt", "x\n")
		if _, dirty := discoverGitCommit(context.Background(), project); !dirty {
			t.Errorf("dirty = false, want true: a .pokkum/ outside the project directory is not this project's artifact")
		}
	})

	t.Run("untracked source file in a repository subdirectory project", func(t *testing.T) {
		requireGit(t)
		root, _ := newRepo(t)
		project := filepath.Join(root, "apps", "web")
		writeFile(t, root, "apps/web/package.json", "{}\n")
		gitCmd(t, root, "add", ".")
		gitCmd(t, root, "commit", "--quiet", "-m", "add the web app")

		writeFile(t, root, "apps/web/.pokkum/staged.txt", "x\n")
		writeFile(t, root, "apps/web/src/uncommitted.ts", "export const x = 1;\n")

		if _, dirty := discoverGitCommit(context.Background(), project); !dirty {
			t.Errorf("dirty = false, want true for an untracked source file in a subdirectory project")
		}
	})

	t.Run("outside a git repository returns no commit and not dirty", func(t *testing.T) {
		requireGit(t)
		dir := t.TempDir()
		commit, dirty := discoverGitCommit(context.Background(), dir)
		if commit != "" || dirty {
			t.Errorf("discoverGitCommit = (%q, %v), want (\"\", false) outside a repository", commit, dirty)
		}
	})
}

// TestDiscoverGitCommit_UntrackedSourceDivergesFromOCIVersionLabel records,
// rather than asserts away, the one case where Pokkum's two cleanliness
// signals still differ: `git describe --dirty` consults only the index and
// tracked files, so an untracked source file is invisible to the
// org.opencontainers.image.version label while the SLSA attestation
// (correctly) reports it. Closing this gap requires a change in
// cmd/pokkum/git_metadata.go, which this package does not own.
func TestDiscoverGitCommit_UntrackedSourceDivergesFromOCIVersionLabel(t *testing.T) {
	requireGit(t)
	root, _ := newRepo(t)
	writeFile(t, root, "src/routes/+page.svelte", "<h1>never committed</h1>\n")

	_, slsaDirty := discoverGitCommit(context.Background(), root)
	labelDirty := ociVersionLabelDirty(t, root)

	if !slsaDirty {
		t.Errorf("SLSA dirty = false for an untracked source file, want true")
	}
	if labelDirty {
		t.Errorf("git describe --dirty now reports untracked files too; cmd/pokkum/git_metadata.go and this note need revisiting")
	}
}

// TestHasEntry and TestHasTrackedEntry exercise the two status-output
// readers directly, on entry orderings and malformed input that a real git
// will not readily produce — in particular a tracked entry that is not the
// first one, which git's own ordering (tracked changes ahead of untracked)
// keeps out of reach of the repository-level tests above.
func TestHasEntry(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"empty output", "", false},
		{"trailing separator only", "\x00", false},
		{"one untracked entry", "?? src/x.ts\x00", true},
		{"several entries", " M src/a.ts\x00?? src/b.ts\x00", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasEntry(tc.out); got != tc.want {
				t.Errorf("hasEntry(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

func TestHasTrackedEntry(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"empty output", "", false},
		{"only untracked", "?? .pokkum/\x00?? pokkum.lock\x00", false},
		{"tracked first", " M pokkum.lock\x00?? .pokkum/\x00", true},
		{"tracked last, behind two skippable entries", "?? .pokkum/\x00?? .pokkum/vendor/f\x00 M pokkum.lock\x00", true},
		{"staged addition", "A  pokkum.lock\x00", true},
		{"deleted", " D pokkum.lock\x00", true},
		{"unmerged", "UU pokkum.lock\x00", true},
		{"rename carries a trailing original-path field", "R  pokkum.lock\x00old.lock\x00", true},
		{"malformed short entry counts as tracked", "?\x00", true},
		{"entry with no path counts as tracked", "?? \x00", true},
		{"untracked path containing a quote", "?? .pokkum/a \"b\".json\x00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasTrackedEntry(tc.out); got != tc.want {
				t.Errorf("hasTrackedEntry(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}
