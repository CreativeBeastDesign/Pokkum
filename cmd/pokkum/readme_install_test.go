package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
// Every check but one resolves against this repository's own files and git refs,
// so it is deterministic and cannot flake on network or rate limits. The
// exception is deliberate: a claim about an EXTERNAL registry cannot be proven
// from this repo, and that is exactly what broke with @pokkum/cli. So there is
// one network-reaching test, TestReadmeNpmPackagesArePublished, and it is built
// so that only an authoritative answer (a 404 from the registry) can fail it —
// every transport failure, rate limit or offline sandbox skips instead. An
// install path that points at an external registry needs a check of that shape;
// one that only reads this repo belongs with the offline tests above it.

const readmeRepoSlug = "CreativeBeastDesign/pokkum"

// releasedVersionRefRe matches the only ref shape this repo publishes: a full
// version tag. Deliberately rejects a bare major (`v1`), a branch name, and
// `latest`.
var releasedVersionRefRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

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

	tags, heads := gitRefNames(t)

	for _, m := range matches {
		path, ref := m[1], m[2]
		if path != "" {
			if _, err := os.Stat(repoRootPath(path)); err != nil {
				t.Errorf("README documents `uses: %s/%s@%s` but %q does not exist in the repo.",
					readmeRepoSlug, path, ref, path)
			}
		}
		// The ENFORCED property is the ref's shape, not its presence in this
		// clone. That is a deliberate narrowing, and it cost two blocked releases
		// to arrive at.
		//
		// The presence check cannot be trusted, because how many tags a checkout
		// has is a property of the checkout, not of the repository. CI's
		// actions/checkout fetches none, so an earlier version of this test
		// skipped there and looked fine. The release job uses fetch-depth: 0 and
		// gets a PARTIAL tag set — non-empty, so the old "skip only when empty"
		// heuristic treated it as authoritative and failed on @v1.0.1, a tag that
		// genuinely exists. A partial ref set is worse than an absent one: it
		// looks complete. And this test blocked the release pipeline, which is
		// the most expensive place to raise a false alarm.
		//
		// Shape is checkable anywhere and catches the bug this guard exists for:
		// the README documented setup-pokkum@v1, and no `v1` ref has ever existed
		// because only full versions are tagged. @main, @latest and @v2 fail here
		// too. What shape cannot catch is a well-formed version that was never
		// tagged (@v9.9.9) — accepted, because the alternative is a check that
		// fails on correct input in half the environments it runs in.
		if !releasedVersionRefRe.MatchString(ref) {
			t.Errorf("README documents `uses: %s/%s@%s`, but %q is not a released-version tag.\n"+
				"\tThis repo publishes only full versions (v1.2.3) and no moving major tag, so a ref like `v1`, `main` or `latest` cannot resolve.\n"+
				"\tPin a released version, and re-point it when you release.",
				readmeRepoSlug, path, ref, ref)
			continue
		}
		// Presence is reported, never enforced, for the reason above.
		if len(tags) > 0 && !tags[ref] && !heads[ref] {
			t.Logf("note: %q is not among the %d tag(s) in this checkout. That is expected when the clone "+
				"has a partial tag set (CI fetches few or none); verify manually if you did not just bump it.", ref, len(tags))
		}
	}
}

// gitRefNames returns local tag names and branch names separately.
//
// They must stay separate: a shallow CI checkout has branches but no tags, and
// conflating the two turns "we cannot check tags here" into "this tag does not
// exist" — a false failure on a correct README. Either map may be empty.
func gitRefNames(t *testing.T) (tags, heads map[string]bool) {
	t.Helper()
	read := func(namespace string) map[string]bool {
		out, err := exec.Command("git", "-C", repoRootPath(), "for-each-ref",
			"--format=%(refname:short)", namespace).Output()
		if err != nil {
			t.Logf("git %s unavailable (%v)", namespace, err)
			return nil
		}
		refs := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" {
				refs[line] = true
			}
		}
		return refs
	}
	tags, heads = read("refs/tags"), read("refs/heads")
	if len(tags) == 0 {
		t.Logf("no tags present (a CI checkout fetches none by default); ref-resolution half of this check is skipped, path checks still ran")
	}
	return tags, heads
}

// @pokkum/cli and its four platform packages published with v1.0.6, so the
// README no longer needs the "not on npm yet" caveat the earlier guard here
// enforced. That guard is replaced, not deleted — it stood in for two stronger
// properties, and each is now checked directly:
//
//   - the package names the README tells people to install are the names this
//     project's release pipeline actually publishes (offline, always runs);
//   - those names, and any version the README pins, really exist on npm
//     (network, and skips rather than fails when it cannot get an answer).

// readmeNpmCommandPkgRe matches a scoped package named in a runnable npm/npx
// command. It deliberately does NOT hard-code our own scope: a command reading
// `npx @pokkumm/cli` is exactly the kind of typo this guard exists to catch, and
// a check keyed to the right scope would look straight past it.
var readmeNpmCommandPkgRe = regexp.MustCompile(`(?m)^\s*(?:npx|npm (?:install|i|add))\b[^\n]*?(@[A-Za-z0-9._-]+/[A-Za-z0-9._{},-]+)`)

// readmeNpmPinRe matches a version-pinned mention, e.g. `npx @pokkum/cli@1.0.6`.
//
// The README deliberately carries no such pin today — it shows the shape
// (`@pokkum/cli@<version>`) and links to the releases page, so there is no
// literal to go stale on every release. This matches nothing, and is kept for
// when a concrete version comes back: only a numeric version matches, so a
// placeholder cannot be mistaken for a real pin. Nothing here reports on pins
// that do not exist, so do not read a green run as "the pins were checked".
var readmeNpmPinRe = regexp.MustCompile(`(@[A-Za-z0-9._-]+/[A-Za-z0-9._-]+)@(\d+\.\d+\.\d+[A-Za-z0-9.-]*)`)

// expandBraces expands `{a,b}` alternation, so the README's shorthand for the
// platform packages (`@pokkum/pokkum_{linux,darwin}_{amd64,arm64}`) yields the
// four real names npm would be asked for. A string with no braces expands to
// itself.
func expandBraces(s string) []string {
	braceRe := regexp.MustCompile(`\{([^{}]*)\}`)
	out := []string{s}
	for {
		var next []string
		expanded := false
		for _, cur := range out {
			m := braceRe.FindStringSubmatchIndex(cur)
			if m == nil {
				next = append(next, cur)
				continue
			}
			expanded = true
			for _, alt := range strings.Split(cur[m[2]:m[3]], ",") {
				next = append(next, cur[:m[0]]+alt+cur[m[1]:])
			}
		}
		out = next
		if !expanded {
			return out
		}
	}
}

// readmeNpmPackages returns every npm package name the README documents, from
// two passes that catch different mistakes:
//
//	a runnable npm/npx line, at any scope — catches a typo'd scope in the one
//	place a reader will copy-paste from;
//	any mention of OUR scope, anywhere — catches the platform packages, which
//	appear in prose rather than in a command.
//
// Brace shorthand is expanded and duplicates collapsed, in first-seen order.
func readmeNpmPackages(t *testing.T, scope string) []string {
	t.Helper()
	body := readmeBody(t)

	var raw []string
	for _, m := range readmeNpmCommandPkgRe.FindAllStringSubmatch(body, -1) {
		raw = append(raw, m[1])
	}
	raw = append(raw, regexp.MustCompile(regexp.QuoteMeta(scope)+`/[A-Za-z0-9._{},-]+`).FindAllString(body, -1)...)

	seen := map[string]bool{}
	var names []string
	for _, r := range raw {
		for _, name := range expandBraces(r) {
			name = strings.TrimRight(name, ".,")
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}

// publishedNpmPackages derives the scope and the set of package names a release
// actually publishes, from the two files that decide it rather than from a
// literal duplicated here.
//
// The names are prefix + binary + platform suffix: release.yml passes
// `--prefix @pokkum` to goreleaser-npm-publisher and then patches the launcher's
// name, and .goreleaser.yaml decides the binary name and the platform matrix.
// Reading both means a renamed scope, a renamed launcher or a dropped platform
// shows up here as a README documenting something no longer published — rather
// than as a check that quietly matches nothing.
func publishedNpmPackages(t *testing.T) (scope string, published map[string]bool) {
	t.Helper()

	releaseYML, err := os.ReadFile(repoRootPath(".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading release.yml: %v", err)
	}
	scopeMatch := regexp.MustCompile(`--prefix\s+(@[A-Za-z0-9._-]+)\s`).FindSubmatch(releaseYML)
	if scopeMatch == nil {
		t.Fatal("[TEST SETUP] release.yml has no `--prefix @scope` for the npm build; this test can no longer derive package names")
	}
	launcher := regexp.MustCompile(`npm pkg set name=(@[A-Za-z0-9._/-]+)`).FindSubmatch(releaseYML)
	if launcher == nil {
		t.Fatal("[TEST SETUP] release.yml no longer patches the launcher package name; this test can no longer derive it")
	}

	goreleaser, err := os.ReadFile(repoRootPath(".goreleaser.yaml"))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading .goreleaser.yaml: %v", err)
	}
	var cfg struct {
		Builds []struct {
			Binary string   `yaml:"binary"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(goreleaser, &cfg); err != nil {
		t.Fatalf("[TEST SETUP] parsing .goreleaser.yaml: %v", err)
	}
	if len(cfg.Builds) == 0 || cfg.Builds[0].Binary == "" || len(cfg.Builds[0].Goos) == 0 || len(cfg.Builds[0].Goarch) == 0 {
		t.Fatal("[TEST SETUP] .goreleaser.yaml declares no build with a binary name and a platform matrix")
	}

	scope = string(scopeMatch[1])
	build := cfg.Builds[0]
	published = map[string]bool{string(launcher[1]): true}
	for _, goos := range build.Goos {
		for _, goarch := range build.Goarch {
			published[fmt.Sprintf("%s/%s_%s_%s", scope, build.Binary, goos, goarch)] = true
		}
	}
	return scope, published
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestReadmeNpmPackageNamesMatchWhatReleasePublishes is the offline half. It
// cannot prove a package is on npm, but it catches the cheaper mistake: a README
// naming a package this project never publishes under that name — which is
// exactly how @pokkum/cli went wrong the first time, when the release job's
// phantom flags meant the published launcher was really @pokkum/pokkum.
func TestReadmeNpmPackageNamesMatchWhatReleasePublishes(t *testing.T) {
	scope, published := publishedNpmPackages(t)

	documented := readmeNpmPackages(t, scope)
	if len(documented) == 0 {
		t.Skip("README documents no npm package; nothing to check")
	}

	for _, name := range documented {
		if !published[name] {
			t.Errorf("README tells readers to install %q, but the release pipeline publishes no such package.\n"+
				"\tPublished names are derived from release.yml's `--prefix`/`npm pkg set name=` and .goreleaser.yaml's binary and platform matrix: %s.\n"+
				"\tEither fix the README or change what the release publishes — do not leave them disagreeing.",
				name, strings.Join(sortedKeys(published), ", "))
		}
	}
}

// registryVerdict is deliberately three-valued. "Not published" and "could not
// find out" are different answers and must never share a representation: the
// first is a bug in the README, the second is a fact about the network.
type registryVerdict int

const (
	registryUnknown   registryVerdict = iota // no usable answer — skip, never fail
	registryPublished                        // the registry serves this package
	registryMissing                          // the registry is authoritative that it does not exist
)

// queryNpmRegistry asks a registry about one package.
//
// versions is nil when the package's version list could not be read, which is
// distinct from an empty map; callers must not treat nil as "no versions".
// detail always explains the verdict and is safe to put in a skip or failure
// message.
func queryNpmRegistry(ctx context.Context, client *http.Client, base, name string) (verdict registryVerdict, versions map[string]struct{}, detail string) {
	// The scope separator must be percent-encoded: registry.npmjs.org serves
	// /@scope%2Fname, not /@scope/name.
	url := strings.TrimSuffix(base, "/") + "/" + strings.Replace(name, "/", "%2F", 1)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return registryUnknown, nil, fmt.Sprintf("could not build a request for %s: %v", url, err)
	}
	// The abbreviated document carries the same version list for far less
	// payload than the full packument.
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")

	resp, err := client.Do(req)
	if err != nil {
		return registryUnknown, nil, fmt.Sprintf("registry unreachable (%v)", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return registryMissing, nil, "the registry returns 404 for it"
	case http.StatusOK:
	default:
		return registryUnknown, nil, fmt.Sprintf("the registry answered %s, which says nothing about whether the package exists", resp.Status)
	}

	var doc struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&doc); err != nil {
		return registryPublished, nil, fmt.Sprintf("package exists, but its registry document did not decode (%v)", err)
	}
	versions = make(map[string]struct{}, len(doc.Versions))
	for v := range doc.Versions {
		versions[v] = struct{}{}
	}
	return registryPublished, versions, fmt.Sprintf("package exists with %d published version(s)", len(versions))
}

// npmRegistryBase is the real registry, overridden only by
// TestNpmRegistryVerdicts against a local httptest server.
const npmRegistryBase = "https://registry.npmjs.org"

// TestReadmeNpmPackagesArePublished is the real registry check the guard it
// replaces asked for by name. It is the only test in this file that touches the
// network, and it is deliberately asymmetric about what it will fail on:
//
//	404           -> failure. The registry is authoritative that the name is free.
//	200           -> pass, and any README-pinned version must be in `versions`.
//	anything else -> skip. A timeout, a 429, a proxy, an offline sandbox and a
//	                 registry outage are all "no answer", not "not published".
//
// That asymmetry is the whole design. The one thing this file must never become
// is a test that blocks a release because npm was slow: the ref-presence check
// above already cost two blocked releases by treating a missing answer as a
// negative one, and the lesson applies with more force to a third-party service.
// The predicate itself is pinned in both directions by TestNpmRegistryVerdicts,
// which needs no network. Skipped in -short.
func TestReadmeNpmPackagesArePublished(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: skipping the network-reaching npm registry check")
	}

	scope, _ := publishedNpmPackages(t)
	documented := readmeNpmPackages(t, scope)
	if len(documented) == 0 {
		t.Skip("README documents no npm package; nothing to check")
	}

	pins := map[string][]string{}
	for _, m := range readmeNpmPinRe.FindAllStringSubmatch(readmeBody(t), -1) {
		pins[m[1]] = append(pins[m[1]], m[2])
	}

	client := &http.Client{Timeout: 15 * time.Second}
	for _, name := range documented {
		t.Run(name, func(t *testing.T) {
			verdict, versions, detail := queryNpmRegistry(t.Context(), client, npmRegistryBase, name)
			switch verdict {
			case registryMissing:
				t.Fatalf("README tells readers to install %s, but %s — every reader who copies that command hits a failure.\n"+
					"\tEither publish the package or remove the instruction; do not document an install path that cannot work.", name, detail)
			case registryUnknown:
				t.Skipf("cannot confirm %s either way: %s", name, detail)
			}
			if versions == nil {
				if len(pins[name]) > 0 {
					t.Skipf("%s: existence confirmed, pinned version(s) unchecked — %s", name, detail)
				}
				return
			}
			for _, version := range pins[name] {
				if _, ok := versions[version]; !ok {
					t.Errorf("README pins %s@%s, but that version is not published (%d version(s) exist).\n"+
						"\tA pin is a copy-pasteable command like any other — bump it when you release, or drop the pin.",
						name, version, len(versions))
				}
			}
		})
	}
}

// TestNpmRegistryVerdicts pins the skip-vs-fail predicate in BOTH directions,
// offline. Without it, the only tested direction would be the one the live
// registry happens to produce today — and the failure that matters (a transient
// error read as "unpublished", blocking a release) would never be exercised.
func TestNpmRegistryVerdicts(t *testing.T) {
	const pkg = "@scope/thing"

	cases := []struct {
		name         string
		handler      http.HandlerFunc
		closed       bool // serve nothing: reproduces an offline sandbox
		wantVerdict  registryVerdict
		wantVersions []string
		wantNoList   bool
	}{
		{
			name: "200 lists versions",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if got, want := r.URL.EscapedPath(), "/@scope%2Fthing"; got != want {
					t.Errorf("registry asked for %q, want the scope separator percent-encoded as %q", got, want)
				}
				_, _ = w.Write([]byte(`{"versions":{"1.0.0":{},"1.0.6":{}}}`))
			},
			wantVerdict:  registryPublished,
			wantVersions: []string{"1.0.0", "1.0.6"},
		},
		{
			name:        "404 is authoritative",
			handler:     func(w http.ResponseWriter, r *http.Request) { http.Error(w, "not found", http.StatusNotFound) },
			wantVerdict: registryMissing,
		},
		{
			name:        "500 is not an answer",
			handler:     func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
			wantVerdict: registryUnknown,
		},
		{
			name:        "429 rate limit is not an answer",
			handler:     func(w http.ResponseWriter, r *http.Request) { http.Error(w, "slow down", http.StatusTooManyRequests) },
			wantVerdict: registryUnknown,
		},
		{
			name:        "unreachable is not an answer",
			closed:      true,
			wantVerdict: registryUnknown,
		},
		{
			// Existence is still known; the version list is not. The two must
			// not collapse into "no versions published".
			name:        "200 with an undecodable body",
			handler:     func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"versions":`)) },
			wantVerdict: registryPublished,
			wantNoList:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.handler
			if handler == nil {
				handler = func(http.ResponseWriter, *http.Request) {}
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if tc.closed {
				srv.Close() // nothing is listening on that port any more
			}

			verdict, versions, detail := queryNpmRegistry(t.Context(), srv.Client(), srv.URL, pkg)
			if verdict != tc.wantVerdict {
				t.Fatalf("verdict = %v, want %v (detail: %s)", verdict, tc.wantVerdict, detail)
			}
			if detail == "" {
				t.Error("detail is empty; every verdict must explain itself, since it is printed in the skip or failure message")
			}
			if tc.wantNoList {
				if versions != nil {
					t.Errorf("versions = %v, want nil so callers cannot mistake an unreadable list for an empty one", versions)
				}
				return
			}
			if tc.wantVerdict != registryPublished {
				return
			}
			for _, want := range tc.wantVersions {
				if _, ok := versions[want]; !ok {
					t.Errorf("version %q missing from %v", want, versions)
				}
			}
			if len(versions) != len(tc.wantVersions) {
				t.Errorf("got %d version(s), want %d", len(versions), len(tc.wantVersions))
			}
		})
	}
}
