package sveltekitutils

import (
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestDetectAppRuntime(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		want         ports.AppRuntime
		wantConflict bool
		reasonHas    string
	}{
		{
			name:      "packageManager bun",
			files:     map[string]string{"package.json": `{"packageManager":"bun@1.2.4"}`},
			want:      ports.RuntimeBun,
			reasonHas: "bun@1.2.4",
		},
		{
			name:      "packageManager pnpm implies node",
			files:     map[string]string{"package.json": `{"packageManager":"pnpm@9.1.0"}`},
			want:      ports.RuntimeNode,
			reasonHas: "pnpm@9.1.0",
		},
		{
			name: "packageManager outranks a contradicting lockfile",
			files: map[string]string{
				"package.json":      `{"packageManager":"bun@1.2.4"}`,
				"package-lock.json": `{"lockfileVersion":3}`,
			},
			want:      ports.RuntimeBun,
			reasonHas: "packageManager",
		},
		{
			name:      "bun.lock",
			files:     map[string]string{"package.json": "{}", "bun.lock": "{}"},
			want:      ports.RuntimeBun,
			reasonHas: "bun.lock",
		},
		{
			name:      "package-lock.json",
			files:     map[string]string{"package.json": "{}", "package-lock.json": `{"lockfileVersion":3}`},
			want:      ports.RuntimeNode,
			reasonHas: "package-lock.json",
		},
		{
			name:      "pnpm-lock.yaml",
			files:     map[string]string{"package.json": "{}", "pnpm-lock.yaml": "lockfileVersion: '9.0'\n"},
			want:      ports.RuntimeNode,
			reasonHas: "pnpm-lock.yaml",
		},
		{
			name: "two node lockfiles are not a conflict",
			files: map[string]string{
				"package.json":      "{}",
				"package-lock.json": `{}`,
				"yarn.lock":         "",
			},
			want: ports.RuntimeNode,
		},
		{
			name: "bun and node lockfiles conflict",
			files: map[string]string{
				"package.json":      "{}",
				"bun.lock":          "{}",
				"package-lock.json": `{}`,
			},
			wantConflict: true,
		},
		{
			name:      "engines node, no lockfile",
			files:     map[string]string{"package.json": `{"engines":{"node":">=20"}}`},
			want:      ports.RuntimeNode,
			reasonHas: ">=20",
		},
		{
			name: "lockfile outranks engines",
			files: map[string]string{
				"package.json": `{"engines":{"node":">=20"}}`,
				"bun.lock":     "{}",
			},
			want:      ports.RuntimeBun,
			reasonHas: "bun.lock",
		},
		{
			name:         "engines naming both conflicts",
			files:        map[string]string{"package.json": `{"engines":{"node":">=20","bun":">=1.1"}}`},
			wantConflict: true,
		},
		{
			name:  "no evidence yields no opinion",
			files: map[string]string{"package.json": `{"name":"app"}`},
			want:  "",
		},
		{
			name:  "missing package.json yields no opinion, not a crash",
			files: map[string]string{"README.md": "hi"},
			want:  "",
		},
		{
			name:  "malformed package.json yields no opinion, not a crash",
			files: map[string]string{"package.json": `{"name": `},
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectAppRuntime(writeProject(t, tc.files))

			if tc.wantConflict {
				if got.Conflict == "" {
					t.Fatalf("Conflict = %q, want non-empty (got %+v)", got.Conflict, got)
				}
				// The invariant that makes a conflict safe: no side is picked.
				if got.Runtime != "" {
					t.Errorf("Runtime = %q alongside a conflict, want \"\" — contradictory "+
						"evidence must produce no opinion, never a coin flip", got.Runtime)
				}
				return
			}

			if got.Conflict != "" {
				t.Fatalf("unexpected Conflict = %q", got.Conflict)
			}
			if got.Runtime != tc.want {
				t.Errorf("Runtime = %q, want %q (reason: %q)", got.Runtime, tc.want, got.Reason)
			}
			if tc.want != "" && got.Reason == "" {
				t.Error("Reason is empty — a detected runtime must name the evidence that produced it")
			}
			if tc.reasonHas != "" && !strings.Contains(got.Reason, tc.reasonHas) {
				t.Errorf("Reason = %q, want it to name %q", got.Reason, tc.reasonHas)
			}
		})
	}
}

// TestRecommendedBaseForRuntime pins the pairing that decides whether the image
// can start at all: RuntimeNode takes its Node binary from the base.
func TestRecommendedBaseForRuntime(t *testing.T) {
	if base, why := RecommendedBaseForRuntime(ports.RuntimeNode); base != ports.BaseImageDistrolessNode || why == "" {
		t.Errorf("node -> (%q, %q), want (%q, non-empty)", base, why, ports.BaseImageDistrolessNode)
	}
	if base, why := RecommendedBaseForRuntime(ports.RuntimeBun); base != ports.BaseImageDistroless || why == "" {
		t.Errorf("bun -> (%q, %q), want (%q, non-empty)", base, why, ports.BaseImageDistroless)
	}
	// The zero value must behave as the documented default runtime, not fall
	// through to a base with no runtime for a Node app.
	if base, _ := RecommendedBaseForRuntime(""); base != ports.BaseImageDistroless {
		t.Errorf("empty runtime -> %q, want %q (ports.DefaultAppRuntime is bun)", base, ports.BaseImageDistroless)
	}
}
