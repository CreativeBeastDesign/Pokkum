package sveltekitutils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProject materialises files into a temp project. Keys are
// slash-separated project-relative paths.
func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

func blockerFiles(r StaticReport) []string {
	out := make([]string, 0, len(r.Blockers))
	for _, b := range r.Blockers {
		out = append(out, b.File)
	}
	return out
}

// The route sources below are the real shapes SvelteKit's own docs and `sv
// create` scaffolds produce, not shapes invented to satisfy this
// implementation (mem:self_review_checklist rows 12 and 50).

const realPageServerLoad = `import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ locals }) => {
	return { user: locals.user };
};
`

const realFormActions = `import { fail } from '@sveltejs/kit';
import type { Actions } from './$types';

export const actions: Actions = {
	default: async ({ request }) => {
		const data = await request.formData();
		if (!data.get('email')) return fail(400);
		return { success: true };
	}
};
`

const realServerEndpoint = `import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';

export const GET: RequestHandler = async () => {
	return json({ ok: true });
};
`

const realRemoteQuery = `import { query } from '$app/server';
import * as db from '$lib/server/database';

export const getPosts = query(async () => {
	return await db.sql` + "`SELECT * FROM post`" + `;
});
`

const realRemotePrerender = `import { prerender } from '$app/server';

export const getStaticPosts = prerender(async () => {
	return [{ slug: 'hello' }];
});
`

func TestAnalyzeStaticViability_CleanPrerenderableProject(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+layout.ts":     "export const prerender = true;\n",
		"src/routes/+page.svelte":   "<h1>hi</h1>\n",
		"src/routes/about/+page.ts": "export const load = async () => ({ title: 'About' });\n",
		"src/lib/util.ts":           "export const add = (a: number, b: number) => a + b;\n",
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticViable {
		t.Errorf("Verdict = %q, want %q (blockers: %v)", got.Verdict, StaticViable, got.Blockers)
	}
	if !got.RootPrerenderDeclared {
		t.Error("RootPrerenderDeclared = false, want true — +layout.ts declares prerender = true")
	}
	// Row 47: a scan that read nothing must not look like a clean project.
	if got.FilesScanned == 0 {
		t.Error("FilesScanned = 0 — a viable verdict from zero files read is indistinguishable from a broken walk")
	}
}

func TestAnalyzeStaticViability_BlockersByKind(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantFile     string
		wantReason   string
		wantOverride bool
	}{
		{
			name: "server endpoint",
			files: map[string]string{
				"src/routes/api/+server.ts": realServerEndpoint,
			},
			wantFile:     "src/routes/api/+server.ts",
			wantReason:   "server endpoint",
			wantOverride: true,
		},
		{
			name: "server load function",
			files: map[string]string{
				"src/routes/profile/+page.server.ts": realPageServerLoad,
			},
			wantFile:     "src/routes/profile/+page.server.ts",
			wantReason:   "server-side load function",
			wantOverride: true,
		},
		{
			name: "form actions are unconditional",
			files: map[string]string{
				"src/routes/signup/+page.server.ts": realFormActions,
			},
			wantFile:     "src/routes/signup/+page.server.ts",
			wantReason:   "form actions",
			wantOverride: false,
		},
		{
			name: "layout server load",
			files: map[string]string{
				"src/routes/+layout.server.ts": realPageServerLoad,
			},
			wantFile:     "src/routes/+layout.server.ts",
			wantReason:   "server-side load function",
			wantOverride: true,
		},
		{
			name: "remote query function outside routes",
			files: map[string]string{
				"src/routes/+page.svelte": "<h1>x</h1>\n",
				"src/lib/posts.remote.ts": realRemoteQuery,
			},
			wantFile:   "src/lib/posts.remote.ts",
			wantReason: "remote query()",
		},
		{
			name: "explicit prerender opt-out",
			files: map[string]string{
				"src/routes/live/+page.ts": "export const prerender = false;\n",
			},
			wantFile:   "src/routes/live/+page.ts",
			wantReason: "prerender = false",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.files["src/routes/+page.svelte"]; !ok {
				tc.files["src/routes/+page.svelte"] = "<h1>root</h1>\n"
			}
			got := AnalyzeStaticViability(writeProject(t, tc.files))

			if got.Verdict != StaticBlocked {
				t.Fatalf("Verdict = %q, want %q (report: %+v)", got.Verdict, StaticBlocked, got)
			}
			var found *StaticFinding
			for i := range got.Blockers {
				if got.Blockers[i].File == tc.wantFile {
					found = &got.Blockers[i]
				}
			}
			if found == nil {
				t.Fatalf("no blocker for %s; got %v", tc.wantFile, blockerFiles(got))
			}
			if !strings.Contains(found.Reason, tc.wantReason) {
				t.Errorf("blocker reason = %q, want it to mention %q", found.Reason, tc.wantReason)
			}
			if hasOverride := found.Override != ""; hasOverride != tc.wantOverride {
				t.Errorf("Override presence = %v, want %v (Override=%q). A finding a prerender flag "+
					"cannot retire must not offer one, and vice versa", hasOverride, tc.wantOverride, found.Override)
			}
		})
	}
}

// TestAnalyzeStaticViability_PrerenderTrueRetiresServerFinding is the override
// half: the same server file that blocks above must stop blocking once it opts
// in, because that is what actually makes it build.
func TestAnalyzeStaticViability_PrerenderTrueRetiresServerFinding(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+page.svelte":   "<h1>x</h1>\n",
		"src/routes/api/+server.ts": "export const prerender = true;\n" + realServerEndpoint,
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticViable {
		t.Errorf("Verdict = %q, want %q — `export const prerender = true` makes a +server.ts build-time; blockers: %v",
			got.Verdict, StaticViable, got.Blockers)
	}
}

// TestAnalyzeStaticViability_FormActionsSurviveP rerenderTrue: actions cannot be
// prerendered at all, so a prerender = true alongside them must NOT silence the
// finding. Ordering bug bait: the actions check has to run before the override.
func TestAnalyzeStaticViability_FormActionsSurvivePrerenderTrue(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+page.svelte":           "<h1>x</h1>\n",
		"src/routes/signup/+page.server.ts": "export const prerender = true;\n" + realFormActions,
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticBlocked {
		t.Fatalf("Verdict = %q, want %q — form actions handle POSTs and cannot prerender, "+
			"so a prerender = true in the same file must not retire the finding", got.Verdict, StaticBlocked)
	}
}

// TestAnalyzeStaticViability_RemotePrerenderIsNotABlocker: a remote prerender()
// resolves at build time and ships as data.
func TestAnalyzeStaticViability_RemotePrerenderIsNotABlocker(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+page.svelte": "<h1>x</h1>\n",
		"src/lib/posts.remote.ts": realRemotePrerender,
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticViable {
		t.Errorf("Verdict = %q, want %q — a remote prerender() is build-time data; blockers: %v",
			got.Verdict, StaticViable, got.Blockers)
	}
}

// TestAnalyzeStaticViability_UnrecognisedRemoteModuleFailsClosed: the whole
// point of a .remote.ts is server-side execution, so an unparseable one must
// not return the reassuring answer (rows 52/53).
func TestAnalyzeStaticViability_UnrecognisedRemoteModuleFailsClosed(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+page.svelte": "<h1>x</h1>\n",
		"src/lib/odd.remote.ts":   "export const something = makeItUp();\n",
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticBlocked {
		t.Errorf("Verdict = %q, want %q — an unrecognised remote module must fail closed, "+
			"not be waved through", got.Verdict, StaticBlocked)
	}
}

// TestAnalyzeStaticViability_CommentsAndStringsAreNotCode is the direct
// regression guard for the 2026-08-16 (whole-file regex) and 2026-08-17
// (substring match) incidents, in both directions at once.
func TestAnalyzeStaticViability_CommentsAndStringsAreNotCode(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+layout.ts": `
// export const prerender = false;   <- commented out, must NOT count
/* export const actions = {}; */
const doc = "export const prerender = false";
const help = ` + "`" + `add export const prerender = false to opt out` + "`" + `;
export const prerender = true;
`,
		"src/routes/+page.svelte": "<h1>x</h1>\n",
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticViable {
		t.Errorf("Verdict = %q, want %q — `prerender = false` in comments and string literals "+
			"is not live code; blockers: %v", got.Verdict, StaticViable, got.Blockers)
	}
	if !got.RootPrerenderDeclared {
		t.Error("RootPrerenderDeclared = false — the live `export const prerender = true` was missed")
	}
}

// TestAnalyzeStaticViability_MissingRoutesIsUnknownNotViable is the fail-open
// guard: no evidence must never render as the reassuring verdict.
func TestAnalyzeStaticViability_MissingRoutesIsUnknownNotViable(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"package.json": `{"name":"not-a-sveltekit-project"}`,
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticUnknown {
		t.Fatalf("Verdict = %q, want %q — a project with no routes directory is unknown, not viable", got.Verdict, StaticUnknown)
	}
	if got.UndecidedWhy == "" {
		t.Error("UndecidedWhy is empty — an unknown verdict must say what stopped it")
	}
}

// TestAnalyzeStaticViability_EmptyRoutesDirIsUnknown: a routes directory that
// exists but yields no readable sources gives no basis for a recommendation.
func TestAnalyzeStaticViability_EmptyRoutesDirIsUnknown(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "routes"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticUnknown {
		t.Errorf("Verdict = %q, want %q — zero files scanned is not evidence of a clean project", got.Verdict, StaticUnknown)
	}
}

// TestAnalyzeStaticViability_HonoursCustomRoutesDir proves the scan follows
// kit.files.routes rather than assuming src/routes.
func TestAnalyzeStaticViability_HonoursCustomRoutesDir(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"svelte.config.js":         "export default { kit: { files: { routes: 'src/pages' } } };\n",
		"src/pages/+page.svelte":   "<h1>x</h1>\n",
		"src/pages/api/+server.ts": realServerEndpoint,
	})

	got := AnalyzeStaticViability(dir)

	if got.RoutesDir != "src/pages" {
		t.Errorf("RoutesDir = %q, want %q", got.RoutesDir, "src/pages")
	}
	if got.Verdict != StaticBlocked {
		t.Fatalf("Verdict = %q, want %q — the +server.ts under the custom routes dir must be seen", got.Verdict, StaticBlocked)
	}
}

// TestAnalyzeStaticViability_ReportsEveryBlockerNotJustTheFirst guards the
// multi-item class: an early return would report one of four.
func TestAnalyzeStaticViability_ReportsEveryBlockerNotJustTheFirst(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+page.svelte":           "<h1>x</h1>\n",
		"src/routes/api/a/+server.ts":       realServerEndpoint,
		"src/routes/api/b/+server.ts":       realServerEndpoint,
		"src/routes/signup/+page.server.ts": realFormActions,
		"src/lib/posts.remote.ts":           realRemoteQuery,
	})

	got := AnalyzeStaticViability(dir)

	want := []string{
		"src/lib/posts.remote.ts",
		"src/routes/api/a/+server.ts",
		"src/routes/api/b/+server.ts",
		"src/routes/signup/+page.server.ts",
	}
	gotFiles := blockerFiles(got)
	if len(gotFiles) != len(want) {
		t.Fatalf("got %d blockers %v, want %d %v", len(gotFiles), gotFiles, len(want), want)
	}
	for i := range want {
		if gotFiles[i] != want[i] {
			t.Errorf("blocker[%d] = %q, want %q (findings must be sorted for stable output)", i, gotFiles[i], want[i])
		}
	}
}

// TestAnalyzeStaticViability_SkipsBuildAndDependencyTrees: node_modules is full
// of +server.ts-shaped files in package fixtures, and build/ is the project's
// own prior output.
func TestAnalyzeStaticViability_SkipsBuildAndDependencyTrees(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+layout.ts":                       "export const prerender = true;\n",
		"src/routes/+page.svelte":                     "<h1>x</h1>\n",
		"node_modules/some-pkg/src/routes/+server.ts": realServerEndpoint,
		"node_modules/some-pkg/lib/x.remote.ts":       realRemoteQuery,
		"build/server/+page.server.js":                realFormActions,
		".svelte-kit/output/+server.js":               realServerEndpoint,
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticViable {
		t.Errorf("Verdict = %q, want %q — dependency and build trees are not user source; blockers: %v",
			got.Verdict, StaticViable, got.Blockers)
	}
}

// TestAnalyzeStaticViability_ServerHooksAreACaveatNotABlocker: hooks.server.ts
// runs during prerendering, so a static build is still possible.
func TestAnalyzeStaticViability_ServerHooksAreACaveatNotABlocker(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+layout.ts":   "export const prerender = true;\n",
		"src/routes/+page.svelte": "<h1>x</h1>\n",
		"src/hooks.server.ts":     "export const handle = async ({ event, resolve }) => resolve(event);\n",
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticViable {
		t.Fatalf("Verdict = %q, want %q — server hooks run at prerender time and do not rule static out; blockers: %v",
			got.Verdict, StaticViable, got.Blockers)
	}
	if len(got.Caveats) != 1 || got.Caveats[0].File != "src/hooks.server.ts" {
		t.Errorf("Caveats = %v, want exactly one for src/hooks.server.ts", got.Caveats)
	}
}

// TestAnalyzeStaticViability_ViableWithoutRootPrerender is the inverse-flag
// case: nothing rules static out, but the project has not opted in, so the
// caller has something actionable to say.
func TestAnalyzeStaticViability_ViableWithoutRootPrerender(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"src/routes/+page.svelte":   "<h1>x</h1>\n",
		"src/routes/about/+page.ts": "export const load = async () => ({});\n",
	})

	got := AnalyzeStaticViability(dir)

	if got.Verdict != StaticViable {
		t.Fatalf("Verdict = %q, want %q; blockers: %v", got.Verdict, StaticViable, got.Blockers)
	}
	if got.RootPrerenderDeclared {
		t.Error("RootPrerenderDeclared = true, want false — no +layout declares it")
	}
}

func TestResolveRoutesDir(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "default",
			files: map[string]string{"package.json": "{}"},
			want:  filepath.Join("src", "routes"),
		},
		{
			name:  "svelte.config.js override",
			files: map[string]string{"svelte.config.js": "export default { kit: { files: { routes: 'src/pages' } } };"},
			want:  filepath.Join("src", "pages"),
		},
		{
			name:  "vite.config.ts override",
			files: map[string]string{"vite.config.ts": "sveltekit({ files: { routes: 'app/routes' } })"},
			want:  filepath.Join("app", "routes"),
		},
		{
			// The behaviour bunexec's private copy got wrong.
			name:  "commented-out override is ignored",
			files: map[string]string{"svelte.config.js": "// files: { routes: 'src/old' }\nexport default {};"},
			want:  filepath.Join("src", "routes"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeProject(t, tc.files)
			got := ResolveRoutesDir(dir)
			if want := filepath.Join(dir, tc.want); got != want {
				t.Errorf("ResolveRoutesDir = %q, want %q", got, want)
			}
		})
	}
}

// TestAnalyzeStaticViability_SymlinkEscapingTheProjectIsNotSilentlyRead proves
// the os.Root containment is load-bearing rather than decorative: a route
// source that is really a symlink out of the project must not be read as if it
// were project content, and the resulting reduced coverage must surface as
// "unknown" rather than as a clean project.
func TestAnalyzeStaticViability_SymlinkEscapingTheProjectIsNotSilentlyRead(t *testing.T) {
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.remote.ts")
	if err := os.WriteFile(outsideFile, []byte(realRemoteQuery), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := writeProject(t, map[string]string{
		"src/routes/+layout.ts":   "export const prerender = true;\n",
		"src/routes/+page.svelte": "<h1>x</h1>\n",
	})
	link := filepath.Join(dir, "src", "lib", "data.remote.ts")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	got := AnalyzeStaticViability(dir)

	// StaticUnknown specifically, not merely "not viable".
	//
	// The first version of this assertion read `!= StaticViable` and passed
	// with the os.Root conversion reverted: plain os.ReadFile FOLLOWS the
	// symlink, reads the outside file, finds its query() and returns
	// StaticBlocked — which also satisfies "not viable", for entirely the
	// wrong reason. Only the contained read refuses the escape and degrades to
	// "could not check", so that is the observable that separates them
	// (mem:self_review_checklist row 45).
	if got.Verdict != StaticUnknown {
		t.Errorf("Verdict = %q, want %q — a symlink out of the project must be refused, "+
			"not followed and read as project content; UndecidedWhy=%q",
			got.Verdict, StaticUnknown, got.UndecidedWhy)
	}
}
