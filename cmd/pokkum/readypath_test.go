package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

func projectWithReadyRoute(t *testing.T, withRoute bool) string {
	t.Helper()
	dir := t.TempDir()
	if !withRoute {
		return dir
	}
	route := filepath.Join(dir, "src", "routes", "api", "ready")
	if err := os.MkdirAll(route, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(route, "+server.ts"), []byte("export const GET = () => new Response('ok');\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveReadyPath(t *testing.T) {
	for _, tc := range []struct {
		name          string
		route         bool
		strategy      string
		flag, config  string
		want          string
		wantInvalidRq bool
	}{
		// The default the user asked to keep: no route, nothing configured,
		// nothing baked, readiness stays a TCP connect.
		{name: "no route, nothing set", strategy: "layered", want: ""},
		{name: "route auto-detected", route: true, strategy: "layered", want: "/api/ready"},
		{name: "route auto-detected for exe", route: true, strategy: "exe", want: "/api/ready"},
		{name: "config wins over detection", route: true, strategy: "layered", config: "/healthcheck/deep", want: "/healthcheck/deep"},
		{name: "flag wins over config", route: true, strategy: "layered", flag: "/flag", config: "/config", want: "/flag"},
		{name: "off disables detection", route: true, strategy: "layered", config: "off", want: ""},
		{name: "flag off beats config path", strategy: "layered", flag: "off", config: "/config", want: ""},
		{name: "explicit path without a route is honoured", strategy: "layered", flag: "/custom", want: "/custom"},
		{name: "static never gets one", route: true, strategy: "static", want: ""},
		{name: "static ignores an explicit one", strategy: "static", flag: "/api/ready", want: ""},
		{name: "invalid flag", strategy: "layered", flag: "api/ready", wantInvalidRq: true},
		{name: "protocol-relative config", strategy: "layered", config: "//evil.example/ready", wantInvalidRq: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveReadyPath(discardLogger(), projectWithReadyRoute(t, tc.route), tc.strategy, tc.flag, tc.config)
			if tc.wantInvalidRq {
				if !errors.Is(err, core.ErrInvalidRequest) {
					t.Fatalf("err = %v, want ErrInvalidRequest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveReadyPath: %v", err)
			}
			if got != tc.want {
				t.Errorf("ready path = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildRequest_ReadyPathReachesRuntime drives the real flag-and-config
// builder, so the resolver is proven wired into the request the packager
// reads rather than merely existing.
func TestBuildRequest_ReadyPathReachesRuntime(t *testing.T) {
	dir := projectWithReadyRoute(t, true)
	flags := defaultBuildFlagsForTest(t)
	req, err := buildRequestFromConfigAndFlags(t.Context(), discardLogger(), flags, dir)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.Runtime.ReadyPath != "/api/ready" {
		t.Errorf("Runtime.ReadyPath = %q, want /api/ready", req.Runtime.ReadyPath)
	}

	plain := projectWithReadyRoute(t, false)
	req, err = buildRequestFromConfigAndFlags(t.Context(), discardLogger(), defaultBuildFlagsForTest(t), plain)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.Runtime.ReadyPath != "" {
		t.Errorf("project without a ready route got Runtime.ReadyPath = %q, want empty", req.Runtime.ReadyPath)
	}
}

// defaultBuildFlagsForTest mirrors the flag defaults a real `pokkum build`
// starts from, the same set build_profile_test.go uses.
func defaultBuildFlagsForTest(t *testing.T) *buildFlags {
	t.Helper()
	return &buildFlags{
		dryRun:          true,
		platforms:       []string{"linux/amd64"},
		strategy:        "layered",
		sbom:            "spdx-json",
		sbomAttach:      "referrer",
		compression:     "gzip",
		bunVariant:      "standard",
		traceSampleRate: 1.0,
		inject:          true,
	}
}
