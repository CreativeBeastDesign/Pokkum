package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestCompositionRootWiresTheStaticViabilityGate closes the one hole in the
// static-build preflight.
//
// internal/core skips the gate when Deps.StaticViabilityAnalyzer is nil, which
// is a fail-open. That nil exists for the ~16 test callers that construct
// core.Deps directly and have no interest in this check; it must never be what
// a user's build gets. Nothing in core can assert that — core cannot see its
// own composition root — so the assertion lives here, where buildDeps is.
//
// Without this, deleting one line from buildDeps silently disables the gate
// for every real build while every test in internal/core still passes: they
// supply their own analyzer, so they would not notice, and the ones that pass
// nil are asserting the skip works. That is a check that can be turned off
// without any check noticing (mem:self_review_checklist row 47).
func TestCompositionRootWiresTheStaticViabilityGate(t *testing.T) {
	deps := buildDeps(slog.New(slog.NewTextHandler(io.Discard, nil)), io.Discard)

	if deps.StaticViabilityAnalyzer == nil {
		t.Fatal("buildDeps left StaticViabilityAnalyzer nil — core skips the static-build " +
			"preflight on nil, so every `pokkum build --strategy=static` would silently lose it")
	}

	// The sibling optional analysis ports, asserted together for the same
	// reason: each is skipped on nil, so each can be disabled by deleting one
	// line, and none of internal/core's tests would notice.
	if deps.EnvBakeDetector == nil {
		t.Error("buildDeps left EnvBakeDetector nil — $env/static/* detection would silently stop running")
	}
	if deps.SecretGuard == nil {
		t.Error("buildDeps left SecretGuard nil — the secret scan would silently stop running")
	}
	if deps.RouteFilter == nil {
		t.Error("buildDeps left RouteFilter nil — --exclude-route would silently stop filtering")
	}
}

// TestStaticGateEndToEndThroughTheRealAdapter drives the composition root's own
// Deps — the real sveltekitutils-backed analyzer, not a stub — through
// core.Build, against real project trees on disk.
//
// Lessons.md 2026-08-17 is precisely why this exists rather than only the
// stubbed tests in internal/core: a well-tested fix to one function in a call
// chain does not prove the chain works, and that entry is a Preflight check
// that blocked every real project while its own unit tests passed. The unit
// tests here assert the gate's logic; this asserts that a real project meets
// the real analyzer through the real wiring and gets the right answer.
func TestStaticGateEndToEndThroughTheRealAdapter(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	staticReq := func(dir string) core.BuildRequest {
		req := core.BuildRequest{
			ProjectDir: dir,
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			Tags:       []string{"v1.0.0"},
			Output:     core.OutputOptions{Mode: core.OutputLocal},
		}
		req.Compile.Strategy = core.StrategyStatic
		return req
	}

	t.Run("a project with a POST endpoint is refused, naming the file", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"package.json":              `{"dependencies":{"@sveltejs/kit":"^2.31.0"}}`,
			"src/routes/+page.svelte":   "<h1>hi</h1>\n",
			"src/routes/api/+server.ts": "export const POST = async ({ request }) => new Response(await request.text());\n",
		})

		_, err := core.Build(context.Background(), buildDeps(logger, io.Discard), staticReq(dir), core.BuildOptions{})
		if err == nil {
			t.Fatal("the build was not refused")
		}
		if !strings.Contains(err.Error(), "src/routes/api/+server.ts") {
			t.Errorf("error does not name the offending file: %v", err)
		}
		if !strings.Contains(err.Error(), "POST") {
			t.Errorf("error does not say what about the file is the problem: %v", err)
		}
	})

	t.Run("the repo's own sveltekit-basic fixture is NOT refused", func(t *testing.T) {
		// The over-rejection guard at the end of the real chain.
		// tests/integration/static_e2e_test.go builds this exact fixture with
		// StrategyStatic; its /api/health endpoint is GET-only, which
		// SvelteKit prerenders. If this gate ever refuses it, that E2E breaks
		// and so does every real project with a read-only endpoint.
		dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
		if err != nil {
			t.Fatal(err)
		}
		_, buildErr := core.Build(context.Background(), buildDeps(logger, io.Discard), staticReq(dir), core.BuildOptions{})

		// The build fails later for unrelated reasons (no toolchain here), so
		// this asserts the absence of THIS refusal rather than success.
		if buildErr != nil && strings.Contains(buildErr.Error(), "cannot be prerendered") {
			t.Fatalf("the static gate refused a fixture that builds statically today: %v", buildErr)
		}
	})

	t.Run("a project the scan cannot read is NOT refused", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"package.json": `{"name":"no-routes"}`})

		_, err := core.Build(context.Background(), buildDeps(logger, io.Discard), staticReq(dir), core.BuildOptions{})
		if err != nil && strings.Contains(err.Error(), "cannot be prerendered") {
			t.Fatalf("an unscannable project was refused by the static gate: %v", err)
		}
	})
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAllowServerCodeInStatic_FlagAndConfigBothReachTheRequest covers the two
// independent ways the override is set, because they are two independent places
// it can be dropped (mem:self_review_checklist row 10: a new config field is
// silently invisible unless BOTH the flag path and the profile-merge path
// carry it).
func TestAllowServerCodeInStatic_FlagAndConfigBothReachTheRequest(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name   string
		flag   bool
		config *bool
		want   bool
	}{
		{name: "unset by default", want: false},
		{name: "flag alone", flag: true, want: true},
		{name: "config key alone", config: boolPtr(true), want: true},
		{name: "config false, flag true", flag: true, config: boolPtr(false), want: true},
		{name: "config explicitly false", config: boolPtr(false), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{
				"package.json":            `{"dependencies":{"@sveltejs/kit":"^2.31.0"}}`,
				"src/routes/+page.svelte": "<h1>hi</h1>\n",
			})
			projCfg := &ports.ProjectConfig{
				Version: ports.ConfigSchemaVersion,
				Build:   ports.BuildConfig{AllowServerCodeInStatic: tc.config},
			}
			mgr, err := config.New(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			flags := &buildFlags{
				allowServerCodeInStatic: tc.flag,
				strategy:                string(ports.StrategyStatic),
			}

			req, err := buildRequestFromResolvedConfig(context.Background(),
				slog.New(slog.NewTextHandler(io.Discard, nil)), flags, dir, mgr, projCfg, "")
			if err != nil {
				t.Fatalf("buildRequestFromResolvedConfig: %v", err)
			}
			if req.AllowServerCodeInStatic != tc.want {
				t.Errorf("AllowServerCodeInStatic = %v, want %v (flag=%v config=%v)",
					req.AllowServerCodeInStatic, tc.want, tc.flag, tc.config)
			}
		})
	}
}

// TestApplyProfile_CarriesAllowServerCodeInStatic: a profile must be able to
// turn the override back off, which is the reason the config field is a
// *bool rather than a bool.
func TestApplyProfile_CarriesAllowServerCodeInStatic(t *testing.T) {
	on, off := true, false
	base := &ports.ProjectConfig{
		Version: ports.ConfigSchemaVersion,
		Build:   ports.BuildConfig{AllowServerCodeInStatic: &on},
		Profiles: map[string]ports.BuildProfile{
			"prod": {Build: ports.BuildConfig{AllowServerCodeInStatic: &off}},
			"noop": {},
		},
	}
	mgr, err := config.New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	merged, err := mgr.ApplyProfile(base, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if merged.Build.AllowServerCodeInStatic == nil || *merged.Build.AllowServerCodeInStatic {
		t.Errorf("profile did not turn the override back off: %v", merged.Build.AllowServerCodeInStatic)
	}

	// A profile that says nothing must inherit, not reset.
	inherited, err := mgr.ApplyProfile(base, "noop")
	if err != nil {
		t.Fatal(err)
	}
	if inherited.Build.AllowServerCodeInStatic == nil || !*inherited.Build.AllowServerCodeInStatic {
		t.Errorf("a profile that sets nothing dropped the base value: %v", inherited.Build.AllowServerCodeInStatic)
	}

	// Pointer IDENTITY, not just value.
	//
	// deepCopyProjectConfig starts with `dst := *src`, which copies the
	// pointer itself — so the inherited value is already correct without the
	// explicit clone, and a value-only assertion passes whether or not the
	// clone exists (verified: deleting that branch left this test green).
	// What the clone actually buys is that merged and base do not share the
	// bool, so a later write through one cannot reach the other. Identity is
	// the observable that distinguishes them (mem:self_review_checklist row 45).
	if inherited.Build.AllowServerCodeInStatic == base.Build.AllowServerCodeInStatic {
		t.Error("the merged config shares its AllowServerCodeInStatic pointer with the base config; " +
			"deepCopyProjectConfig must clone it, or a write through one silently mutates the other")
	}
}
