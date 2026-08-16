package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestLoadConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Test loading in current directory
	cfg, err := New(".", logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("config is nil")
	}
}

func TestSaveAndLoadProjectConfig(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// 1. Initial Load should return os.ErrNotExist
	_, err = mgr.Load(tmpDir)
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected os.ErrNotExist, got: %v", err)
	}

	// 2. Generate and Save
	bTrue := true
	cfg := &ports.ProjectConfig{
		Version: 1,
		Docker: ports.DockerConfig{
			Repo: "ghcr.io/test/my-app",
		},
		Strategy:  "layered",
		Base:      "distroless",
		Platforms: []string{"linux/amd64", "linux/arm64"},
		Image: ports.ImageConfig{
			Labels: map[string]string{"org.test.vendor": "Acme"},
			Env:    map[string]string{"PORT": "8080"},
			Ports:  []int{8080},
		},
		Security: ports.SecurityConfig{
			FailOnCVE:  "high",
			VerifyBase: &bTrue,
		},
		Profiles: map[string]ports.BuildProfile{
			"local": {
				Output:    "local",
				Platforms: []string{"local"},
				Sourcemap: &bTrue,
			},
		},
	}

	if err := mgr.Save(tmpDir, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	cfgPath := filepath.Join(tmpDir, ConfigFilename)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("expected %s to exist: %v", cfgPath, err)
	}

	// 3. Load saved configuration
	loaded, err := mgr.Load(tmpDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.Version != 1 {
		t.Errorf("expected version 1, got %d", loaded.Version)
	}
	if loaded.Docker.Repo != "ghcr.io/test/my-app" {
		t.Errorf("expected repo ghcr.io/test/my-app, got %q", loaded.Docker.Repo)
	}
	if loaded.Image.Labels["org.test.vendor"] != "Acme" {
		t.Errorf("expected label Acme, got %q", loaded.Image.Labels["org.test.vendor"])
	}
	if loaded.Image.Env["PORT"] != "8080" {
		t.Errorf("expected env PORT 8080, got %q", loaded.Image.Env["PORT"])
	}
	if len(loaded.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(loaded.Profiles))
	}
	localProf := loaded.Profiles["local"]
	if localProf.Output != "local" {
		t.Errorf("expected profile output local, got %q", localProf.Output)
	}
}

func TestApplyProfile(t *testing.T) {
	mgr, err := New(".", nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	bTrue := true
	bFalse := false
	baseCfg := &ports.ProjectConfig{
		Version: 1,
		Docker: ports.DockerConfig{
			Repo: "ghcr.io/test/app",
		},
		Base:      "distroless",
		Strategy:  "layered",
		Platforms: []string{"linux/amd64", "linux/arm64"},
		Image: ports.ImageConfig{
			Labels: map[string]string{"app": "test", "tier": "backend"},
			Env:    map[string]string{"NODE_ENV": "production"},
		},
		Security: ports.SecurityConfig{
			FailOnCVE:  "high",
			VerifyBase: &bTrue,
		},
		Profiles: map[string]ports.BuildProfile{
			"local": {
				Output:    "local",
				Platforms: []string{"local"},
				Base:      "chainguard",
				Image: ports.ImageConfig{
					Labels: map[string]string{"tier": "local-dev"},
					Env:    map[string]string{"DEBUG": "true"},
				},
				Security: ports.SecurityConfig{
					VerifyBase: &bFalse,
				},
			},
		},
	}

	// 1. Empty profile returns unmodified base
	merged, err := mgr.ApplyProfile(baseCfg, "")
	if err != nil {
		t.Fatalf("ApplyProfile with empty name failed: %v", err)
	}
	if merged.Base != "distroless" {
		t.Errorf("expected base distroless, got %q", merged.Base)
	}

	// 2. Apply valid profile
	merged, err = mgr.ApplyProfile(baseCfg, "local")
	if err != nil {
		t.Fatalf("ApplyProfile 'local' failed: %v", err)
	}
	if merged.Base != "chainguard" {
		t.Errorf("expected base overridden to chainguard, got %q", merged.Base)
	}
	if len(merged.Platforms) != 1 || merged.Platforms[0] != "local" {
		t.Errorf("expected platforms [local], got %v", merged.Platforms)
	}
	if merged.Image.Labels["tier"] != "local-dev" {
		t.Errorf("expected tier overridden to local-dev, got %q", merged.Image.Labels["tier"])
	}
	if merged.Image.Labels["app"] != "test" {
		t.Errorf("expected app label preserved as test, got %q", merged.Image.Labels["app"])
	}
	if merged.Image.Env["DEBUG"] != "true" {
		t.Errorf("expected DEBUG env set to true, got %q", merged.Image.Env["DEBUG"])
	}
	if merged.Security.VerifyBase == nil || *merged.Security.VerifyBase != false {
		t.Errorf("expected VerifyBase overridden to false")
	}

	// Ensure baseCfg was not mutated
	if baseCfg.Base != "distroless" {
		t.Errorf("baseCfg was unexpectedly mutated: %q", baseCfg.Base)
	}

	// 3. Non-existent profile returns error
	_, err = mgr.ApplyProfile(baseCfg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent profile, got nil")
	}
}

func TestGenerateDefault(t *testing.T) {
	mgr, err := New(".", nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	opts := ports.InitConfigOptions{
		Repo:               "ghcr.io/myorg/myapp",
		BasePreset:         "chainguard",
		Strategy:           "static",
		EnableLocalProfile: true,
		FailOnCVE:          "critical",
	}

	cfg := mgr.GenerateDefault(opts)
	if cfg.Docker.Repo != "ghcr.io/myorg/myapp" {
		t.Errorf("expected repo ghcr.io/myorg/myapp, got %q", cfg.Docker.Repo)
	}
	if cfg.Base != "chainguard" {
		t.Errorf("expected base chainguard, got %q", cfg.Base)
	}
	if cfg.Strategy != "static" {
		t.Errorf("expected strategy static, got %q", cfg.Strategy)
	}
	if cfg.Security.FailOnCVE != "critical" {
		t.Errorf("expected FailOnCVE critical, got %q", cfg.Security.FailOnCVE)
	}
	if _, ok := cfg.Profiles["local"]; !ok {
		t.Fatal("expected 'local' profile to be generated")
	}
	if cfg.Profiles["local"].Output != "local" {
		t.Errorf("expected local profile output local, got %q", cfg.Profiles["local"].Output)
	}
}

func TestResolveBuildTimestamp(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := New(".", logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Test explicit SOURCE_DATE_EPOCH
	os.Setenv("SOURCE_DATE_EPOCH", "1000000000")
	defer os.Unsetenv("SOURCE_DATE_EPOCH")

	ts, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		t.Fatalf("ResolveBuildTimestamp failed: %v", err)
	}

	expected := time.Unix(1000000000, 0).UTC()
	if ts != expected {
		t.Errorf("expected %v, got %v", expected, ts)
	}

	// Test git commit time when env var is not set
	os.Unsetenv("SOURCE_DATE_EPOCH")

	ts, err = cfg.ResolveBuildTimestamp()
	if err != nil {
		// Error is OK if git is not available in test environment
		t.Logf("ResolveBuildTimestamp returned error (OK if git unavailable): %v", err)
		return
	}

	// Git should have resolved a timestamp
	if ts.IsZero() {
		// Zero is OK - it means git wasn't available but that's tolerated
		t.Logf("ResolveBuildTimestamp returned zero (git likely not available)")
	} else if ts.Before(time.Now().Add(-365 * 24 * time.Hour)) {
		// Reasonable git timestamp
		t.Logf("ResolveBuildTimestamp resolved git timestamp: %v", ts)
	}
}

func TestParseSourceDateEpoch(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		{
			name:    "empty string",
			input:   "",
			want:    time.Time{},
			wantErr: false,
		},
		{
			name:    "valid timestamp",
			input:   "1000000000",
			want:    time.Unix(1000000000, 0).UTC(),
			wantErr: false,
		},
		{
			name:    "invalid timestamp",
			input:   "invalid",
			want:    time.Time{},
			wantErr: true,
		},
		{
			name:    "negative timestamp",
			input:   "-1",
			want:    time.Time{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.ParseSourceDateEpoch(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSourceDateEpoch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseSourceDateEpoch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyProfile_AdversarialDeepCopyIsolation(t *testing.T) {
	mgr, err := New(".", nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	bTrue := true
	baseCfg := &ports.ProjectConfig{
		Version: 1,
		Docker:  ports.DockerConfig{Repo: "ghcr.io/test/base"},
		Image: ports.ImageConfig{
			Labels:      map[string]string{"k1": "v1"},
			Annotations: map[string]string{"a1": "av1"},
			Env:         map[string]string{"ENV1": "val1"},
			Ports:       []int{8080},
			RequireEnv:  []string{"REQ1"},
		},
		Security: ports.SecurityConfig{
			AllowSecretPatterns: []string{"pat1"},
		},
		Profiles: map[string]ports.BuildProfile{
			"dev": {
				Image: ports.ImageConfig{
					Labels:      map[string]string{"k2": "v2"},
					Annotations: map[string]string{"a2": "av2"},
					Env:         map[string]string{"ENV2": "val2"},
					Ports:       []int{9000},
					RequireEnv:  []string{"REQ2"},
				},
				Security: ports.SecurityConfig{
					AllowSecretPatterns: []string{"pat2"},
				},
				Cache: ports.CacheConfig{
					Enabled: &bTrue,
					Pubkey:  "dev-key",
				},
				OTel: ports.OTelConfig{
					Sidecar: &bTrue,
				},
			},
		},
	}

	merged, err := mgr.ApplyProfile(baseCfg, "dev")
	if err != nil {
		t.Fatalf("ApplyProfile failed: %v", err)
	}

	// Mutate merged maps and slices to ensure baseCfg was NOT mutated (true deep copy)
	merged.Image.Labels["mutated"] = "bad"
	merged.Image.Annotations["mutated"] = "bad"
	merged.Image.Env["mutated"] = "bad"
	merged.Image.Ports[0] = 9999
	merged.Image.RequireEnv[0] = "MUTATED"
	merged.Security.AllowSecretPatterns[0] = "MUTATED"

	if baseCfg.Image.Labels["mutated"] != "" {
		t.Errorf("baseCfg.Image.Labels was mutated through merged config")
	}
	if baseCfg.Image.Annotations["mutated"] != "" {
		t.Errorf("baseCfg.Image.Annotations was mutated through merged config")
	}
	if baseCfg.Image.Env["mutated"] != "" {
		t.Errorf("baseCfg.Image.Env was mutated through merged config")
	}
	if baseCfg.Image.Ports[0] != 8080 {
		t.Errorf("baseCfg.Image.Ports was mutated through merged config")
	}
	if baseCfg.Image.RequireEnv[0] != "REQ1" {
		t.Errorf("baseCfg.Image.RequireEnv was mutated through merged config")
	}
	if baseCfg.Security.AllowSecretPatterns[0] != "pat1" {
		t.Errorf("baseCfg.Security.AllowSecretPatterns was mutated through merged config")
	}
}

// TestLoad_KnownFieldsRejectsTypo verifies that Load fails fast on an
// unknown/misspelled top-level key (e.g. "strategey:" instead of
// "strategy:") instead of silently dropping it, matching this repo's
// fail-fast-before-any-network-call convention.
func TestLoad_KnownFieldsRejectsTypo(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	badCfg := `version: 1
docker:
  repo: ghcr.io/example/app
strategey: layered
`
	cfgPath := filepath.Join(tmpDir, ConfigFilename)
	if err := os.WriteFile(cfgPath, []byte(badCfg), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", ConfigFilename, err)
	}

	if _, err := mgr.Load(tmpDir); err == nil {
		t.Fatal("expected Load to reject unknown field 'strategey', got nil error")
	}
}

// TestLoad_KnownFieldsRejectsNestedTypo checks the same enforcement applies
// to a misspelled key nested inside a profile, not just at the top level.
func TestLoad_KnownFieldsRejectsNestedTypo(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	badCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  local:
    securty:
      fail_on_cve: high
`
	cfgPath := filepath.Join(tmpDir, ConfigFilename)
	if err := os.WriteFile(cfgPath, []byte(badCfg), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", ConfigFilename, err)
	}

	if _, err := mgr.Load(tmpDir); err == nil {
		t.Fatal("expected Load to reject unknown nested field 'securty', got nil error")
	}
}

// TestLoad_EmptyFileStillParses ensures the KnownFields switch to
// yaml.Decoder didn't regress the pre-existing behavior of a present but
// empty .pokkum.yaml (Decoder.Decode returns io.EOF on an empty document,
// unlike yaml.Unmarshal, which silently no-ops).
func TestLoad_EmptyFileStillParses(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	cfgPath := filepath.Join(tmpDir, ConfigFilename)
	if err := os.WriteFile(cfgPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", ConfigFilename, err)
	}

	cfg, err := mgr.Load(tmpDir)
	if err != nil {
		t.Fatalf("expected empty config file to parse without error, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil zero-value config for empty file")
	}
}

// TestApplyProfile_DockerRepoOverride verifies a profile can override the
// base config's docker.repo (e.g. a "production" profile pushing to a
// different registry than the default/local profile).
func TestApplyProfile_DockerRepoOverride(t *testing.T) {
	mgr, err := New(".", nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	baseCfg := &ports.ProjectConfig{
		Version: 1,
		Docker:  ports.DockerConfig{Repo: "ghcr.io/test/app-staging"},
		Profiles: map[string]ports.BuildProfile{
			"production": {
				Docker: ports.DockerConfig{Repo: "ghcr.io/test/app-prod"},
			},
		},
	}

	merged, err := mgr.ApplyProfile(baseCfg, "production")
	if err != nil {
		t.Fatalf("ApplyProfile failed: %v", err)
	}
	if merged.Docker.Repo != "ghcr.io/test/app-prod" {
		t.Errorf("expected Docker.Repo overridden to ghcr.io/test/app-prod, got %q", merged.Docker.Repo)
	}
	if baseCfg.Docker.Repo != "ghcr.io/test/app-staging" {
		t.Errorf("baseCfg.Docker.Repo was unexpectedly mutated: %q", baseCfg.Docker.Repo)
	}
}

// TestLoad_ExampleGoldenFixture parses the canonical example config
// (testdata/config/pokkum.yaml.golden) end-to-end through Load, so the
// documented example can never silently drift out of sync with the real
// schema (KnownFields(true) would reject it outright on any typo'd or
// removed field) or with the "local"/"production" profile-merge behavior it
// demonstrates.
func TestLoad_ExampleGoldenFixture(t *testing.T) {
	goldenPath := filepath.Join("..", "..", "..", "testdata", "config", "pokkum.yaml.golden")
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden fixture %s: %v", goldenPath, err)
	}

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ConfigFilename), data, 0644); err != nil {
		t.Fatalf("failed to stage golden fixture as %s: %v", ConfigFilename, err)
	}

	mgr, err := New(tmpDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	cfg, err := mgr.Load(tmpDir)
	if err != nil {
		t.Fatalf("Load failed on golden fixture: %v", err)
	}

	if cfg.Docker.Repo != "ghcr.io/example/my-sveltekit-app" {
		t.Errorf("expected base docker.repo, got %q", cfg.Docker.Repo)
	}
	if len(cfg.Profiles) != 2 {
		t.Fatalf("expected 2 profiles (local, production), got %d", len(cfg.Profiles))
	}

	local, ok := cfg.Profiles["local"]
	if !ok {
		t.Fatal("expected 'local' profile in golden fixture")
	}
	if local.Output != "local" || local.Security.VerifyBase == nil || *local.Security.VerifyBase {
		t.Errorf("unexpected 'local' profile contents: %+v", local)
	}

	prod, ok := cfg.Profiles["production"]
	if !ok {
		t.Fatal("expected 'production' profile in golden fixture")
	}
	if prod.Docker.Repo != "ghcr.io/example/my-sveltekit-app-prod" {
		t.Errorf("expected 'production' profile docker.repo override, got %q", prod.Docker.Repo)
	}

	// Applying the "production" profile must actually change the resolved
	// repo and fail_on_cve relative to the base config, proving the two
	// profiles demonstrate meaningfully different overrides end-to-end.
	merged, err := mgr.ApplyProfile(cfg, "production")
	if err != nil {
		t.Fatalf("ApplyProfile(production) failed: %v", err)
	}
	if merged.Docker.Repo != "ghcr.io/example/my-sveltekit-app-prod" {
		t.Errorf("expected merged repo to reflect production override, got %q", merged.Docker.Repo)
	}
	if merged.Security.FailOnCVE != "critical" {
		t.Errorf("expected merged fail_on_cve overridden to critical, got %q", merged.Security.FailOnCVE)
	}
}
