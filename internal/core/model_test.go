package core_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestOutputMode(t *testing.T) {
	validModes := []core.OutputMode{
		core.OutputPush,
		core.OutputLocal,
		core.OutputTarball,
	}

	for _, m := range validModes {
		if !m.Valid() {
			t.Errorf("expected mode %q to be valid", m)
		}
		if m.String() != string(m) {
			t.Errorf("expected String() to return %q, got %q", string(m), m.String())
		}
	}

	invalidModes := []core.OutputMode{"", "invalid", "PUSH", "  local  "}
	for _, m := range invalidModes {
		if m.Valid() {
			t.Errorf("expected mode %q to be invalid", m)
		}
	}

	parseTests := []struct {
		input   string
		want    core.OutputMode
		wantErr bool
	}{
		{"push", core.OutputPush, false},
		{" PUSH ", core.OutputPush, false},
		{"Local", core.OutputLocal, false},
		{"tarball", core.OutputTarball, false},
		{"unknown", "", true},
		{"", "", true},
	}

	for _, tt := range parseTests {
		got, err := core.ParseOutputMode(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseOutputMode(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr {
			if got != tt.want {
				t.Errorf("ParseOutputMode(%q) = %v, want %v", tt.input, got, tt.want)
			}
			if err != nil && !errors.Is(err, core.ErrInvalidOutputMode) {
				t.Errorf("ParseOutputMode(%q) error should wrap ErrInvalidOutputMode", tt.input)
			}
		} else if !errors.Is(err, core.ErrInvalidOutputMode) {
			t.Errorf("ParseOutputMode(%q) error = %v, expected ErrInvalidOutputMode sentinel", tt.input, err)
		}
	}
}

func TestParsePlatform(t *testing.T) {
	tests := []struct {
		input   string
		want    core.Platform
		wantErr bool
	}{
		{"linux/amd64", core.LinuxAMD64, false},
		{" linux/arm64 ", core.LinuxARM64, false},
		{"windows/amd64", core.Platform{}, true},
		{"linux/amd64/v1", core.Platform{}, true},
		{"invalid", core.Platform{}, true},
		{"", core.Platform{}, true},
	}

	for _, tt := range tests {
		got, err := core.ParsePlatform(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParsePlatform(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if tt.wantErr {
			if !errors.Is(err, core.ErrUnsupportedPlatform) {
				t.Errorf("ParsePlatform(%q) error = %v, expected ErrUnsupportedPlatform", tt.input, err)
			}
		} else if got != tt.want {
			t.Errorf("ParsePlatform(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParsePlatforms(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []core.Platform
		wantErr error
	}{
		{
			name:    "expand all",
			input:   []string{"all"},
			want:    core.SupportedPlatforms,
			wantErr: nil,
		},
		{
			name:    "expand ALL uppercase trimmed",
			input:   []string{"  ALL  "},
			want:    core.SupportedPlatforms,
			wantErr: nil,
		},
		{
			name:    "explicit list",
			input:   []string{"linux/amd64", "linux/arm64"},
			want:    []core.Platform{core.LinuxAMD64, core.LinuxARM64},
			wantErr: nil,
		},
		{
			name:    "duplicate rejection",
			input:   []string{"linux/amd64", "linux/amd64"},
			want:    nil,
			wantErr: core.ErrInvalidRequest,
		},
		{
			name:    "unsupported platform in list",
			input:   []string{"linux/amd64", "darwin/arm64"},
			want:    nil,
			wantErr: core.ErrUnsupportedPlatform,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.ParsePlatforms(tt.input)
			if tt.wantErr != nil {
				if err == nil || !errors.Is(err, tt.wantErr) {
					t.Errorf("ParsePlatforms(%v) error = %v, want sentinel %v", tt.input, err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Errorf("ParsePlatforms(%v) unexpected error = %v", tt.input, err)
				} else if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("ParsePlatforms(%v) = %v, want %v", tt.input, got, tt.want)
				}
			}
		})
	}
}

func TestPlatformList(t *testing.T) {
	ps := []core.Platform{core.LinuxAMD64, core.LinuxARM64}
	got := core.PlatformList(ps)
	want := "linux/amd64, linux/arm64"
	if got != want {
		t.Errorf("PlatformList(%v) = %q, want %q", ps, got, want)
	}
}

func TestParseSBOMFormat(t *testing.T) {
	tests := []struct {
		input   string
		want    core.SBOMFormat
		wantErr bool
	}{
		{"spdx", core.SBOMFormatSPDXJSON, false},
		{"spdx-json", core.SBOMFormatSPDXJSON, false},
		{"spdxjson", core.SBOMFormatSPDXJSON, false},
		{"cyclonedx", core.SBOMFormatCycloneDXJSON, false},
		{"cyclonedx-json", core.SBOMFormatCycloneDXJSON, false},
		{"cyclonedxjson", core.SBOMFormatCycloneDXJSON, false},
		{"none", core.SBOMFormatNone, false},
		{"off", core.SBOMFormatNone, false},
		{"false", core.SBOMFormatNone, false},
		{"disabled", core.SBOMFormatNone, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		got, err := core.ParseSBOMFormat(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseSBOMFormat(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if tt.wantErr {
			if !errors.Is(err, core.ErrInvalidSBOMFormat) {
				t.Errorf("ParseSBOMFormat(%q) error = %v, expected ErrInvalidSBOMFormat", tt.input, err)
			}
		} else if got != tt.want {
			t.Errorf("ParseSBOMFormat(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseSBOMAttachMode(t *testing.T) {
	tests := []struct {
		input   string
		want    core.SBOMAttachMode
		wantErr bool
	}{
		{"referrer", core.SBOMAttachReferrer, false},
		{"referrers", core.SBOMAttachReferrer, false},
		{"oci1.1", core.SBOMAttachReferrer, false},
		{"tag", core.SBOMAttachTag, false},
		{"tags", core.SBOMAttachTag, false},
		{"auto", core.SBOMAttachAuto, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		got, err := core.ParseSBOMAttachMode(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseSBOMAttachMode(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if tt.wantErr {
			if !errors.Is(err, core.ErrInvalidSBOMAttachMode) {
				t.Errorf("ParseSBOMAttachMode(%q) error = %v, expected ErrInvalidSBOMAttachMode", tt.input, err)
			}
		} else if got != tt.want {
			t.Errorf("ParseSBOMAttachMode(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseVEXExemptions(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("empty input returns nil, no error", func(t *testing.T) {
		got, err := core.ParseVEXExemptions(nil, now)
		if err != nil || got != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("valid entry, plain date", func(t *testing.T) {
		got, err := core.ParseVEXExemptions([]ports.VEXExemptionConfig{{
			CVE:           "CVE-2024-12345",
			Justification: "component_not_present",
			Expires:       "2030-06-15",
			Owner:         "security-team",
		}}, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 exemption, got %d", len(got))
		}
		want := time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)
		if !got[0].Expires.Equal(want) {
			t.Errorf("Expires = %v, want %v", got[0].Expires, want)
		}
	})

	t.Run("valid entry, RFC3339", func(t *testing.T) {
		got, err := core.ParseVEXExemptions([]ports.VEXExemptionConfig{{
			CVE:           "CVE-2024-12345",
			Justification: "component_not_present",
			Expires:       "2030-06-15T12:00:00Z",
			Owner:         "security-team",
		}}, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 exemption, got %d", len(got))
		}
	})

	t.Run("unparseable expires", func(t *testing.T) {
		_, err := core.ParseVEXExemptions([]ports.VEXExemptionConfig{{
			CVE: "CVE-2024-12345", Justification: "component_not_present", Expires: "not-a-date", Owner: "x",
		}}, now)
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for an unparseable expires date, got %v", err)
		}
	})

	t.Run("missing owner rejected", func(t *testing.T) {
		_, err := core.ParseVEXExemptions([]ports.VEXExemptionConfig{{
			CVE: "CVE-2024-12345", Justification: "component_not_present", Expires: "2030-06-15", Owner: "",
		}}, now)
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for a missing owner, got %v", err)
		}
	})

	t.Run("invalid justification code rejected", func(t *testing.T) {
		_, err := core.ParseVEXExemptions([]ports.VEXExemptionConfig{{
			CVE: "CVE-2024-12345", Justification: "made_up_reason", Expires: "2030-06-15", Owner: "x",
		}}, now)
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for an invalid justification code, got %v", err)
		}
	})

	t.Run("already-expired exemption rejected outright", func(t *testing.T) {
		_, err := core.ParseVEXExemptions([]ports.VEXExemptionConfig{{
			CVE: "CVE-2024-12345", Justification: "component_not_present", Expires: "2020-01-01", Owner: "x",
		}}, now)
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for an already-expired exemption, got %v", err)
		}
	})

	t.Run("multi-item: error identifies the offending entry by index and CVE", func(t *testing.T) {
		_, err := core.ParseVEXExemptions([]ports.VEXExemptionConfig{
			{CVE: "CVE-2024-11111", Justification: "component_not_present", Expires: "2030-06-15", Owner: "x"},
			{CVE: "CVE-2024-22222", Justification: "component_not_present", Expires: "2030-06-15", Owner: ""},
		}, now)
		if err == nil {
			t.Fatal("expected an error for the second, invalid entry")
		}
		if !strings.Contains(err.Error(), "CVE-2024-22222") {
			t.Errorf("expected error to name the offending CVE, got: %v", err)
		}
	})
}

func TestParseBaseImagePreset(t *testing.T) {
	tests := []struct {
		input   string
		want    core.BaseImagePreset
		wantErr bool
	}{
		{"distroless", core.BaseImageDistroless, false},
		{"chainguard", core.BaseImageChainguard, false},
		{"custom", core.BaseImageCustom, false},
		{"DISTROLESS", core.BaseImageDistroless, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		got, err := core.ParseBaseImagePreset(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseBaseImagePreset(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if tt.wantErr {
			if !errors.Is(err, core.ErrInvalidBaseImage) {
				t.Errorf("ParseBaseImagePreset(%q) error = %v, expected ErrInvalidBaseImage", tt.input, err)
			}
		} else if got != tt.want {
			t.Errorf("ParseBaseImagePreset(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseSourceDateEpoch(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Time
		wantErr error
	}{
		{"", time.Time{}, nil},
		{"   ", time.Time{}, nil},
		{"0", time.Unix(0, 0).UTC(), nil},
		{"1700000000", time.Unix(1700000000, 0).UTC(), nil},
		{"not-an-int", time.Time{}, core.ErrInvalidRequest},
		{"-100", time.Time{}, core.ErrInvalidRequest},
	}

	for _, tt := range tests {
		got, err := core.ParseSourceDateEpoch(tt.input)
		if tt.wantErr != nil {
			if err == nil || !errors.Is(err, tt.wantErr) {
				t.Errorf("ParseSourceDateEpoch(%q) error = %v, want sentinel %v", tt.input, err, tt.wantErr)
			}
		} else {
			if err != nil {
				t.Errorf("ParseSourceDateEpoch(%q) unexpected error = %v", tt.input, err)
			} else if !got.Equal(tt.want) {
				t.Errorf("ParseSourceDateEpoch(%q) = %v, want %v", tt.input, got, tt.want)
			}
		}
	}
}

func TestBuildRequestNormalize(t *testing.T) {
	req := core.BuildRequest{
		ProjectDir: "./my-app",
	}

	req.Normalize()

	// Check ProjectDir converted to absolute path
	absDir, _ := filepath.Abs("./my-app")
	if req.ProjectDir != absDir {
		t.Errorf("ProjectDir = %q, want %q", req.ProjectDir, absDir)
	}

	// Default platforms
	if !reflect.DeepEqual(req.Platforms, core.SupportedPlatforms) {
		t.Errorf("Platforms = %v, want %v", req.Platforms, core.SupportedPlatforms)
	}

	// Default tags
	if len(req.Tags) != 1 || req.Tags[0] != core.DefaultTag {
		t.Errorf("Tags = %v, want [%q]", req.Tags, core.DefaultTag)
	}

	// Default SourceDateEpoch is epoch 0 UTC
	if !req.SourceDateEpoch.Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("SourceDateEpoch = %v, want Unix epoch UTC", req.SourceDateEpoch)
	}

	// Default BaseImage preset and ref
	if req.BaseImage.Preset != core.DefaultBaseImagePreset {
		t.Errorf("BaseImage.Preset = %q, want %q", req.BaseImage.Preset, core.DefaultBaseImagePreset)
	}
	defaultRef, _ := core.DefaultBaseImagePreset.DefaultRef()
	if req.BaseImage.Ref != defaultRef {
		t.Errorf("BaseImage.Ref = %q, want %q", req.BaseImage.Ref, defaultRef)
	}

	// Default SBOM format
	if req.SBOM.Format != core.DefaultSBOMFormat {
		t.Errorf("SBOM.Format = %q, want %q", req.SBOM.Format, core.DefaultSBOMFormat)
	}

	// Default output mode
	if req.Output.Mode != core.DefaultOutputMode {
		t.Errorf("Output.Mode = %q, want %q", req.Output.Mode, core.DefaultOutputMode)
	}

	// Default concurrency
	if req.Concurrency != len(core.SupportedPlatforms) {
		t.Errorf("Concurrency = %d, want %d", req.Concurrency, len(core.SupportedPlatforms))
	}

	// Test custom base ref without preset infers BaseImageCustom
	reqCustom := core.BuildRequest{
		ProjectDir: "./my-app",
		BaseImage:  core.BaseImageOptions{Ref: "my-custom-image:latest"},
	}
	reqCustom.Normalize()
	if reqCustom.BaseImage.Preset != core.BaseImageCustom {
		t.Errorf("BaseImage.Preset = %q, want %q", reqCustom.BaseImage.Preset, core.BaseImageCustom)
	}

	// Test local output mode without repo generates local repo name
	reqLocal := core.BuildRequest{
		ProjectDir: "./my-app",
		Output:     core.OutputOptions{Mode: core.OutputLocal},
	}
	reqLocal.Normalize()
	wantRepo := "pokkum.local/my-app"
	if reqLocal.Repo != wantRepo {
		t.Errorf("Repo = %q, want %q", reqLocal.Repo, wantRepo)
	}
}

func TestBuildRequestValidate(t *testing.T) {
	validReq := func() core.BuildRequest {
		r := core.BuildRequest{
			ProjectDir: "/abs/path/to/project",
			Repo:       "ghcr.io/example/app",
		}
		r.Normalize()
		return r
	}

	t.Run("valid request", func(t *testing.T) {
		req := validReq()
		if err := req.Validate(); err != nil {
			t.Errorf("Validate() failed on valid request: %v", err)
		}
	})

	tests := []struct {
		name    string
		mutate  func(r *core.BuildRequest)
		wantErr error
	}{
		{
			name: "missing project dir",
			mutate: func(r *core.BuildRequest) {
				r.ProjectDir = ""
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "invalid output mode",
			mutate: func(r *core.BuildRequest) {
				r.Output.Mode = "invalid"
			},
			wantErr: core.ErrInvalidOutputMode,
		},
		{
			name: "missing tarball path",
			mutate: func(r *core.BuildRequest) {
				r.Output.Mode = core.OutputTarball
				r.Output.TarballPath = ""
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "missing repository in push mode",
			mutate: func(r *core.BuildRequest) {
				r.Output.Mode = core.OutputPush
				r.Repo = ""
			},
			wantErr: core.ErrNoDockerRepo,
		},
		{
			name: "repo with whitespace",
			mutate: func(r *core.BuildRequest) {
				r.Repo = "repo with spaces"
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "repo with tag included",
			mutate: func(r *core.BuildRequest) {
				r.Repo = "ghcr.io/example/app:v1.0"
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "repo with digest included",
			mutate: func(r *core.BuildRequest) {
				r.Repo = "ghcr.io/example/app@sha256:1234567890abcdef"
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "empty platforms",
			mutate: func(r *core.BuildRequest) {
				r.Platforms = nil
			},
			wantErr: core.ErrUnsupportedPlatform,
		},
		{
			name: "unsupported platform",
			mutate: func(r *core.BuildRequest) {
				r.Platforms = []core.Platform{{OS: "windows", Arch: "amd64"}}
			},
			wantErr: core.ErrUnsupportedPlatform,
		},
		{
			name: "duplicate platform",
			mutate: func(r *core.BuildRequest) {
				r.Platforms = []core.Platform{core.LinuxAMD64, core.LinuxAMD64}
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "empty tag",
			mutate: func(r *core.BuildRequest) {
				r.Tags = []string{""}
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "invalid tag characters",
			mutate: func(r *core.BuildRequest) {
				r.Tags = []string{"tag:with:colon"}
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "invalid base image preset",
			mutate: func(r *core.BuildRequest) {
				r.BaseImage.Preset = "invalid"
			},
			wantErr: core.ErrInvalidBaseImage,
		},
		{
			name: "empty base image ref",
			mutate: func(r *core.BuildRequest) {
				r.BaseImage.Ref = ""
			},
			wantErr: core.ErrInvalidBaseImage,
		},
		{
			name: "invalid sbom format",
			mutate: func(r *core.BuildRequest) {
				r.SBOM.Format = "invalid"
			},
			wantErr: core.ErrInvalidSBOMFormat,
		},
		{
			name: "zero source date epoch without normalize",
			mutate: func(r *core.BuildRequest) {
				r.SourceDateEpoch = time.Time{}
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "app port out of range",
			mutate: func(r *core.BuildRequest) {
				r.Runtime.Port = 70000
			},
			wantErr: core.ErrInvalidRuntimeConfig,
		},
		{
			name: "probe port out of range",
			mutate: func(r *core.BuildRequest) {
				r.Runtime.ProbePort = 70000
			},
			wantErr: core.ErrInvalidRuntimeConfig,
		},
		{
			name: "port collision",
			mutate: func(r *core.BuildRequest) {
				r.Runtime.Port = 3000
				r.Runtime.ProbePort = 3000
			},
			wantErr: core.ErrInvalidRuntimeConfig,
		},
		{
			name: "negative shutdown timeout",
			mutate: func(r *core.BuildRequest) {
				r.Runtime.ShutdownTimeout = -1 * time.Second
			},
			wantErr: core.ErrInvalidRuntimeConfig,
		},
		{
			name: "negative concurrency",
			mutate: func(r *core.BuildRequest) {
				r.Concurrency = -1
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "negative push concurrency",
			mutate: func(r *core.BuildRequest) {
				r.PushConcurrency = -1
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "negative asset-overlay generations",
			mutate: func(r *core.BuildRequest) {
				r.Compile.AssetOverlayGenerations = -1
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			// Guards against an unbounded make([]string, 0, maxDepth) in
			// assetoverlay.ResolvePredecessorChain — an absurd value here
			// would otherwise attempt a huge allocation before any network
			// call even happens.
			name: "asset-overlay generations far exceeding the sane cap",
			mutate: func(r *core.BuildRequest) {
				r.Compile.AssetOverlayGenerations = 2_000_000_000
			},
			wantErr: core.ErrInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validReq()
			tt.mutate(&req)
			err := req.Validate()
			if err == nil {
				t.Fatalf("Validate() expected error wrapping %v, got nil", tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() error = %v, expected sentinel %v", err, tt.wantErr)
			}
		})
	}
}

// TestBuildRequestPushConcurrency_ZeroAndPositiveValid confirms zero and
// positive PushConcurrency values pass Validate() unchanged, mirroring
// Concurrency's own "zero and positive are fine, only negative is rejected"
// contract on the validation side.
func TestBuildRequestPushConcurrency_ZeroAndPositiveValid(t *testing.T) {
	validReq := func() core.BuildRequest {
		r := core.BuildRequest{
			ProjectDir: "/abs/path/to/project",
			Repo:       "ghcr.io/example/app",
		}
		r.Normalize()
		return r
	}

	for _, tc := range []int{0, 1, 8} {
		t.Run(fmt.Sprintf("PushConcurrency=%d", tc), func(t *testing.T) {
			req := validReq()
			req.PushConcurrency = tc
			if err := req.Validate(); err != nil {
				t.Errorf("Validate() with PushConcurrency=%d: unexpected error: %v", tc, err)
			}
		})
	}
}

// TestBuildRequestNormalize_PushConcurrencyIsNotDefaulted pins the
// deliberate asymmetry between Concurrency and PushConcurrency: Normalize
// defaults a zero Concurrency to len(Platforms), but has no corresponding
// branch for PushConcurrency, whose zero value instead means "let the
// registry adapter pick its own default" (see remoteConfig.Jobs in
// internal/adapters/registry/registry.go). The two fields look alike enough
// that a future edit could accidentally copy the Concurrency defaulting
// pattern onto PushConcurrency; this test fails loudly if that happens.
func TestBuildRequestNormalize_PushConcurrencyIsNotDefaulted(t *testing.T) {
	for _, tc := range []int{0, -1, 3} {
		t.Run(fmt.Sprintf("PushConcurrency=%d", tc), func(t *testing.T) {
			r := core.BuildRequest{
				ProjectDir:      "./my-app",
				PushConcurrency: tc,
			}
			r.Normalize()
			if r.PushConcurrency != tc {
				t.Errorf("Normalize() changed PushConcurrency from %d to %d; PushConcurrency must be left untouched (unlike Concurrency, which defaults from 0)", tc, r.PushConcurrency)
			}
		})
	}

	// Contrast case: Concurrency=0 DOES get defaulted by Normalize, which is
	// exactly the behavior PushConcurrency must NOT exhibit.
	r := core.BuildRequest{
		ProjectDir: "./my-app",
		Platforms:  []core.Platform{core.LinuxAMD64, core.LinuxARM64},
	}
	r.Normalize()
	if r.Concurrency != len(r.Platforms) {
		t.Fatalf("sanity check failed: Concurrency = %d, want %d (len(Platforms)) after Normalize", r.Concurrency, len(r.Platforms))
	}
}

func TestImageResultAndBuildResult(t *testing.T) {
	h := v1.Hash{Algorithm: "sha256", Hex: "abcd1234efgh5678abcd1234efgh5678abcd1234efgh5678abcd1234efgh5678"}
	pr := ports.PublishResult{
		Ref:    "ghcr.io/example/app@sha256:abcd1234",
		Digest: h,
		Tags:   []string{"latest", "v1.0.0"},
		Path:   "",
		Size:   1024,
	}

	imgRes := core.NewImageResult(core.OutputPush, pr, []core.Platform{core.LinuxAMD64}, false)

	if imgRes.Mode != core.OutputPush {
		t.Errorf("Mode = %v, want %v", imgRes.Mode, core.OutputPush)
	}
	if imgRes.Ref != pr.Ref {
		t.Errorf("Ref = %q, want %q", imgRes.Ref, pr.Ref)
	}
	if imgRes.String() != pr.Ref {
		t.Errorf("String() = %q, want %q", imgRes.String(), pr.Ref)
	}
	if imgRes.Size != 1024 {
		t.Errorf("Size = %d, want 1024", imgRes.Size)
	}

	buildRes := core.BuildResult{
		Artifacts: []core.Artifact{
			{Platform: core.LinuxAMD64, Path: "/tmp/app-linux-amd64"},
			{Platform: core.LinuxARM64, Path: "/tmp/app-linux-arm64"},
		},
	}

	artAmd, okAmd := buildRes.ArtifactFor(core.LinuxAMD64)
	if !okAmd || artAmd.Path != "/tmp/app-linux-amd64" {
		t.Errorf("ArtifactFor(LinuxAMD64) = %v, %v", artAmd, okAmd)
	}

	_, okRiscv := buildRes.ArtifactFor(core.Platform{OS: "linux", Arch: "riscv64"})
	if okRiscv {
		t.Errorf("ArtifactFor(riscv64) expected false, got true")
	}
}
