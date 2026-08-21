package slsa

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// gitCommandTimeout bounds every git subprocess this file invokes. Mirrors
// cmd/pokkum/git_metadata.go's execGit: discovering source provenance is
// best-effort and must never hang (or meaningfully slow) a build waiting on
// a slow, wedged, or network-backed git invocation.
const gitCommandTimeout = 2 * time.Second

// discoverGitCommit resolves the current commit SHA for the project at dir,
// and reports whether the working tree had uncommitted changes at that
// moment — excluding the artifacts Pokkum's own build writes into the
// project directory, which are not a modification of the source. See
// workingTreeDirty for exactly what is and is not excused.
//
// GITHUB_SHA is checked first, mirroring cmd/pokkum/git_metadata.go's
// getGitRevision — so a CI build's SLSA statement agrees with the same
// build's org.opencontainers.image.revision label, both derived from the
// same environment signal. A GitHub Actions checkout is always clean at that
// SHA, so dirty is unconditionally false on that path; the working-tree
// check below only runs when falling back to `git rev-parse HEAD`.
//
// Returns ("", false) on any failure — outside a git checkout, in a
// repository with no commits yet, if the git binary is missing, or if dir
// is empty — never an error: a build must not fail because this
// best-effort source attribution could not be computed. Callers that
// already have an explicit commit (a caller-supplied
// SLSAGeneratorRequest.GitCommit) must not call this at all; it exists
// only to fill in what the caller didn't supply.
func discoverGitCommit(ctx context.Context, dir string) (commit string, dirty bool) {
	if dir == "" {
		return "", false
	}
	if sha := os.Getenv("GITHUB_SHA"); sha != "" {
		return sha, false
	}
	out, err := runGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", false
	}
	commit = strings.TrimSpace(out)
	if commit == "" {
		return "", false
	}

	return commit, workingTreeDirty(ctx, dir)
}

// pokkumGeneratedNames are the paths, relative to the project directory,
// that Pokkum itself writes during a build. They are the only paths whose
// presence as *untracked* entries is excused when judging whether the source
// tree was modified — see workingTreeDirty.
//
// ".pokkum" is the build sandbox (ports.CompileRequest documents the staged
// node_modules tree it now holds); pokkum.lock is the base-image lockfile
// (ports.PokkumLockfileName), which a build writes on first run and on
// --update-base.
//
// These are git pathspecs, resolved relative to the project directory the
// build ran in — so a monorepo project at apps/web excuses
// apps/web/.pokkum but not a .pokkum elsewhere in the same repository,
// which is not its artifact. ".pokkum" matches that path and everything
// beneath it; it does not match the sibling ".pokkum.yaml"
// (ports.ConfigFilename), which is user-authored configuration.
var pokkumGeneratedNames = []string{".pokkum", ports.PokkumLockfileName}

// workingTreeDirty reports whether the git working tree at dir carries
// changes that would stop a rebuild from the recorded commit reproducing the
// artifact.
//
// A bare commit hash asserts "the source tree matched this commit exactly."
// That is false when the working tree has uncommitted changes, so a rebuild
// verifying against the recorded commit could never actually reproduce the
// artifact that was built — the "-dirty" suffix the caller appends makes
// that honest rather than silently omitted. What it must not do is call a
// tree modified because Pokkum's own build wrote its own artifacts into it,
// which made every build after the first report a dirty source digest while
// the same build's OCI version label reported clean.
//
// What is deliberately NOT excused:
//
//   - Untracked files anywhere other than Pokkum's own artifact paths. An
//     untracked source file is a genuine reproducibility hazard — a rebuild
//     from the recorded commit would not contain it — so it must keep
//     reporting dirty. That is why this filters two named paths instead of
//     suppressing untracked files wholesale, which would discard the signal
//     for every project.
//   - Tracked modifications to Pokkum's own artifacts. A project that
//     commits pokkum.lock and then has Pokkum rewrite it really does diverge
//     from its commit, and `git describe --dirty` — the source of the
//     org.opencontainers.image.version OCI label, see
//     cmd/pokkum/git_metadata.go's getGitVersion — reports it too.
//     Suppressing it here would recreate the very label-vs-attestation
//     disagreement this filter exists to remove, with the signs flipped.
//     This is what the second git call below is for: a path-based exclusion
//     alone cannot tell a generated untracked artifact from a committed file
//     the build overwrote.
//   - Anything else in the repository, including outside the project
//     directory. A modified shared package in a monorepo is consumed by the
//     build and belongs in the verdict.
//
// A user who has gitignored ".pokkum"/pokkum.lock needs no special handling:
// git omits ignored paths from --porcelain output entirely, so there is
// simply nothing to exclude and the verdict is identical either way. The
// provenance must not depend on the project's .gitignore.
// WorkingTreeDirty reports whether dir's git working tree carries changes that
// would stop a rebuild from reproducing this build, ignoring the artifacts
// Pokkum itself generates. See workingTreeDirty for the full contract.
//
// Exported so `pokkum repro doctor` can report the same verdict this package
// records in provenance. Its own check used to be a constant: gitClean was
// initialised true and the only assignment inside the .git stat set it true
// again, so "No dirty uncommitted working tree modifications detected" was
// printed unconditionally and could never fail — a check that agrees with
// everything is not evidence of anything, and it was cited as corroboration in
// a field report precisely because it looked like a second opinion. Two Pokkum
// outputs disagreeing about one tree is the confusion this filter removed; two
// agreeing by construction is the point.
func WorkingTreeDirty(ctx context.Context, dir string) bool {
	return workingTreeDirty(ctx, dir)
}

func workingTreeDirty(ctx context.Context, dir string) bool {
	// Pass 1 — everything that is not a Pokkum artifact, tracked or not.
	//
	// The exclusion is done by git rather than by matching its output in Go:
	// --porcelain paths are relative to the repository root while the
	// artifacts are relative to the project directory, and git already knows
	// how to bridge the two. It also means git never walks the large staged
	// node_modules tree under .pokkum at all.
	//
	// -z makes git emit NUL-terminated entries with literal, unquoted paths,
	// so a filename containing a quote, backslash, or newline cannot be
	// misparsed. Nothing here parses paths anyway: any surviving entry means
	// dirty, so no parse failure can produce a false "clean".
	args := []string{"status", "--porcelain", "-z", "--"}
	for _, name := range pokkumGeneratedNames {
		args = append(args, ":(exclude)"+name)
	}
	out, err := runGit(ctx, dir, args...)
	if err != nil {
		// The pathspec magic above needs a git that understands
		// negative-only pathspecs. Rather than let an old git resolve to a
		// silent "clean" — the strongest claim this function can make, from
		// the least evidence — fall back to the unfiltered status, which
		// over-reports Pokkum's own artifacts as dirty but never
		// under-reports.
		out, err = runGit(ctx, dir, "status", "--porcelain", "-z")
		if err != nil {
			return false
		}
	}
	if hasEntry(out) {
		return true
	}

	// Pass 2 — the Pokkum artifact paths on their own, to catch the case
	// pass 1 deliberately cannot see: an artifact that is *tracked* and has
	// been modified or staged. A pathspec matching nothing is not an error
	// for git status, so a project with neither artifact present simply
	// yields no entries here.
	args = append([]string{"status", "--porcelain", "-z", "--"}, pokkumGeneratedNames...)
	out, err = runGit(ctx, dir, args...)
	if err != nil {
		return false
	}
	return hasTrackedEntry(out)
}

// hasEntry reports whether NUL-separated `git status --porcelain -z` output
// contains at least one entry.
func hasEntry(statusOut string) bool {
	for _, entry := range strings.Split(statusOut, "\x00") {
		if entry != "" {
			return true
		}
	}
	return false
}

// hasTrackedEntry reports whether NUL-separated `git status --porcelain -z`
// output contains an entry that is anything other than a plain untracked
// ("??") one — that is, a change git knows about because the path is
// tracked, staged, renamed, or in conflict.
//
// An entry too short to classify counts as tracked: a supply-chain claim of
// "clean" must never be reachable through a parse failure.
func hasTrackedEntry(statusOut string) bool {
	for _, entry := range strings.Split(statusOut, "\x00") {
		if entry == "" {
			continue
		}
		// Entry layout is "XY<space><path>", so the shortest legal entry
		// names a single-character path. Only the status characters are read
		// here; a rename's trailing original-path field is never reached,
		// because a rename is not "??" and returns on the spot.
		if len(entry) < 4 {
			return true
		}
		if entry[0] != '?' || entry[1] != '?' {
			return true
		}
	}
	return false
}

// discoverGitSource resolves the source repository URL for the project at
// dir. GITHUB_SERVER_URL/GITHUB_REPOSITORY are checked first, for the same
// consistency reason as discoverGitCommit's GITHUB_SHA check, falling back
// to the "origin" remote's URL. Returns "" on any failure, including
// outside a git checkout or when no "origin" remote is configured — never
// an error, for the same reason as discoverGitCommit.
func discoverGitSource(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	server := os.Getenv("GITHUB_SERVER_URL")
	repo := os.Getenv("GITHUB_REPOSITORY")
	if server != "" && repo != "" {
		return strings.TrimRight(server, "/") + "/" + strings.TrimLeft(repo, "/")
	}
	out, err := runGit(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// runGit runs `git <args...>` with its working directory set to dir,
// bounded by gitCommandTimeout.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}
