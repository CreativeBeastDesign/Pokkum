package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// A static build must default to a libc-free base.
//
// The defaulting lived inside `if flags.static`, so it applied to the --static
// flag but not to --strategy=static — the documented spelling, and the one the
// strategy flag exists for. A real project built with --strategy=static
// therefore landed on distroless/cc-debian12 and shipped libssl, libstdc++ and
// libgomp that pokkum-static (built CGO_ENABLED=0) cannot even load: 44.3MB, of
// which roughly 34MB was base the server never touches — for the one strategy
// whose entire pitch is not shipping a runtime.
//
// Both spellings must reach the same base. Asserting only the --static path
// would have passed before the fix.
func TestStaticStrategyDefaultsToLibcFreeBase(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags buildFlags
	}{
		{"--strategy=static", buildFlags{strategy: "static", strategyExplicit: true}},
		{"--static flag", buildFlags{strategy: "layered", static: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := requestForFlags(t, tc.flags)
			if req.BaseImage.Ref != core.StaticBaseRef {
				t.Errorf("base ref = %q, want %q — a static build must not inherit the dynamically-linked default",
					req.BaseImage.Ref, core.StaticBaseRef)
			}
		})
	}
}

// TestNonStaticStrategyKeepsTheDynamicBase pins the other direction: layered and
// exe payloads ARE dynamically linked against glibc, so widening the static
// default to every strategy would produce images that cannot execute at all.
func TestNonStaticStrategyKeepsTheDynamicBase(t *testing.T) {
	req := requestForFlags(t, buildFlags{strategy: "layered"})
	if req.BaseImage.Ref == core.StaticBaseRef {
		t.Errorf("a layered build was given the libc-free base %q; Bun is dynamically linked and would not start", req.BaseImage.Ref)
	}
}

// TestExplicitBaseStillWinsForStatic guards the escape hatch: a user who pins
// --base must keep it, static or not.
func TestExplicitBaseStillWinsForStatic(t *testing.T) {
	f := buildFlags{strategy: "static", strategyExplicit: true, base: "chainguard", baseExplicit: true}
	req := requestForFlags(t, f)
	if req.BaseImage.Ref == core.StaticBaseRef {
		t.Errorf("an explicitly pinned --base was overridden by the static default")
	}
}

// requestForFlags builds a BuildRequest against a throwaway project directory.
func requestForFlags(t *testing.T, flags buildFlags) *core.BuildRequest {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// --local avoids needing a destination repository for a request-shaping test.
	flags.local = true
	req, err := buildRequestFromConfigAndFlags(context.Background(), discardLogger(), &flags, dir)
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
	}
	return req
}
