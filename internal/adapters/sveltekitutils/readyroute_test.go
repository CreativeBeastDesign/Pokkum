package sveltekitutils

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProjectFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectReadyRoute(t *testing.T) {
	const handler = "export const GET = () => new Response('ok');\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		found   bool
		urlPath string
	}{
		{"no routes dir", map[string]string{"package.json": "{}"}, false, ""},
		{"no ready route", map[string]string{"src/routes/+page.svelte": "", "src/routes/api/health/+server.ts": handler}, false, ""},
		{"ts route", map[string]string{"src/routes/api/ready/+server.ts": handler}, true, "/api/ready"},
		{"js route", map[string]string{"src/routes/api/ready/+server.js": handler}, true, "/api/ready"},
		{"inside route groups", map[string]string{"src/routes/(internal)/api/(probes)/ready/+server.ts": handler}, true, "/api/ready"},
		{"page, not endpoint", map[string]string{"src/routes/api/ready/+page.svelte": ""}, false, ""},
		{"deeper route only", map[string]string{"src/routes/api/ready/[id]/+server.ts": handler}, false, ""},
		{"similar name", map[string]string{"src/routes/api/readyz/+server.ts": handler}, false, ""},
		{"custom routes dir", map[string]string{
			"svelte.config.js":                "export default { kit: { files: { routes: 'app/routes' } } };",
			"app/routes/api/ready/+server.ts": handler,
		}, true, "/api/ready"},
		{"literal base", map[string]string{
			"svelte.config.js":                "export default { kit: { paths: { base: '/shop' } } };",
			"src/routes/api/ready/+server.ts": handler,
		}, true, "/shop/api/ready"},
		{"base in a comment is ignored", map[string]string{
			"svelte.config.js":                "// paths: { base: '/old' }\nexport default { kit: {} };",
			"src/routes/api/ready/+server.ts": handler,
		}, true, "/api/ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeProjectFiles(t, tc.files)
			got, err := DetectReadyRoute(dir)
			if err != nil {
				t.Fatalf("DetectReadyRoute: %v", err)
			}
			if got.Found != tc.found || got.URLPath != tc.urlPath {
				t.Errorf("got %+v, want found=%v urlPath=%q", got, tc.found, tc.urlPath)
			}
		})
	}
}

// A base the build cannot evaluate must not be guessed: the route is reported
// found, with no URL and a reason, so the caller keeps readiness at TCP and
// says why instead of polling a path that 404s and never becoming ready.
func TestDetectReadyRoute_UnknownBaseIsNotGuessed(t *testing.T) {
	for _, cfg := range []string{
		"export default { kit: { paths: { base: process.env.BASE_PATH } } };",
		"export default { kit: { paths: { base: `${prefix}/x` } } };",
	} {
		dir := writeProjectFiles(t, map[string]string{
			"svelte.config.js":                cfg,
			"src/routes/api/ready/+server.ts": "export const GET = () => new Response('ok');",
		})
		got, err := DetectReadyRoute(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Found || got.URLPath != "" || got.Reason == "" {
			t.Errorf("%s: got %+v, want found with empty URLPath and a reason", cfg, got)
		}
	}
}
