package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// writeConfig writes a .pokkum.yaml into a fresh temp dir and returns the dir.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ports.ConfigFilename), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return dir
}

// TestDeployConfigFieldsSurviveApplyProfile is the row-10 guard.
//
// config.Manager.ApplyProfile is hand-written per field, not reflection-based,
// so a new DeployConfig field parses fine, validates fine, and is then
// silently discarded by `--profile <name>` unless a matching merge line
// exists. This walks EVERY exported field of ports.DeployConfig via reflection
// and fails naming any field the profile did not override, so adding a field
// later without a merge line fails here rather than in production.
func TestDeployConfigFieldsSurviveApplyProfile(t *testing.T) {
	dir := writeConfig(t, `
version: 1
docker:
  repo: ghcr.io/me/app
deploy:
  target: swiftwave
  method: webhook
  endpoint: https://base.example.com/webhook/redeploy-app/base/tok
  endpoint_env: BASE_ENDPOINT_ENV
  application: base-app
  token_env: BASE_TOKEN
  auto: false
  update_image: false
  registry_url: base.registry.example.com
  registry_username_env: BASE_USER
  registry_password_env: BASE_PASS
  timeout: 10s
profiles:
  production:
    deploy:
      target: dokploy
      method: api
      endpoint: https://prod.example.com
      endpoint_env: PROD_ENDPOINT_ENV
      application: prod-app
      token_env: PROD_TOKEN
      auto: true
      update_image: true
      registry_url: prod.registry.example.com
      registry_username_env: PROD_USER
      registry_password_env: PROD_PASS
      timeout: 90s
`)

	_, cfg, activeProfile, err := resolveProjectConfig(discardLogger(), dir, "production", false)
	if err != nil {
		t.Fatalf("resolveProjectConfig: %v", err)
	}
	if activeProfile != "production" {
		t.Fatalf("activeProfile = %q, want production", activeProfile)
	}

	merged := reflect.ValueOf(cfg.Deploy)
	typ := merged.Type()
	if typ.NumField() == 0 {
		t.Fatal("ports.DeployConfig has no fields; this test would pass vacuously")
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		got := merged.Field(i).Interface()

		// Every profile value above is deliberately different from its base
		// counterpart, so "still equal to the base value" means the merge line
		// for this field is missing.
		switch v := got.(type) {
		case string:
			if strings.HasPrefix(v, "base") || strings.HasPrefix(v, "BASE") ||
				v == "swiftwave" || v == "webhook" || v == "10s" ||
				strings.Contains(v, "base.example.com") {
				t.Errorf("ApplyProfile did not merge DeployConfig.%s: got the base value %q. Add an override line to config.Manager.ApplyProfile.", field.Name, v)
			}
		case *bool:
			if v == nil {
				t.Errorf("ApplyProfile did not merge DeployConfig.%s: got nil, want the profile's explicit value. Add an override line to config.Manager.ApplyProfile.", field.Name)
			} else if !*v {
				t.Errorf("ApplyProfile did not merge DeployConfig.%s: got the base value false. Add an override line to config.Manager.ApplyProfile.", field.Name)
			}
		default:
			t.Errorf("DeployConfig.%s has unhandled type %T; extend this test so the new field is actually checked", field.Name, got)
		}
	}
}

// TestApplyProfileDeepCopiesDeployPointers checks the other half of the merge:
// the *bool fields must be copied, not aliased, or mutating a merged config
// would reach back into the base one.
func TestApplyProfileDeepCopiesDeployPointers(t *testing.T) {
	dir := writeConfig(t, `
version: 1
deploy:
  target: dokploy
  endpoint: https://panel.example.com
  application: app
  auto: true
  update_image: true
profiles:
  staging:
    base: chainguard
`)
	mgr, err := config.New(dir, discardLogger())
	if err != nil {
		t.Fatalf("config.New: %v", err)
	}
	base, err := mgr.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	merged, err := mgr.ApplyProfile(base, "staging")
	if err != nil {
		t.Fatalf("ApplyProfile: %v", err)
	}

	if merged.Deploy.Auto == base.Deploy.Auto {
		t.Error("Deploy.Auto is aliased between the base and merged configs, not deep-copied")
	}
	if merged.Deploy.UpdateImage == base.Deploy.UpdateImage {
		t.Error("Deploy.UpdateImage is aliased between the base and merged configs, not deep-copied")
	}
	// Values must still be carried across by the copy.
	if merged.Deploy.Auto == nil || !*merged.Deploy.Auto {
		t.Error("Deploy.Auto lost its value in the deep copy")
	}
}

// TestConfigValidateChecksDeployInEveryProfile is the other half of row 10: a
// broken deploy block inside a named profile must fail `pokkum config
// validate`, not only one at the top level.
func TestConfigValidateChecksDeployInEveryProfile(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		contains string
	}{
		{
			name: "invalid target at the top level",
			body: `
version: 1
deploy:
  target: heroku
  endpoint: https://example.com
`,
			contains: "invalid deploy.target",
		},
		{
			name: "invalid target inside a profile",
			body: `
version: 1
profiles:
  production:
    deploy:
      target: heroku
      endpoint: https://example.com
`,
			contains: `profile "production"`,
		},
		{
			name: "dokploy webhook combination inside a profile",
			body: `
version: 1
profiles:
  production:
    deploy:
      target: dokploy
      method: webhook
      endpoint: https://example.com
      application: app
`,
			contains: "does not support method",
		},
		{
			name: "api without an application inside a profile",
			body: `
version: 1
profiles:
  staging:
    deploy:
      target: dokploy
      endpoint: https://example.com
`,
			contains: "deploy.application is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeConfig(t, tc.body)
			err := runConfigValidate(discardLogger(), &configValidateOptions{dir: dir})
			if err == nil {
				t.Fatal("config validate accepted a broken deploy block")
			}
		})
	}
}

// TestConfigValidateAcceptsValidDeployBlocks proves the checks above are not
// passing merely because the validator rejects every deploy block.
func TestConfigValidateAcceptsValidDeployBlocks(t *testing.T) {
	dir := writeConfig(t, `
version: 1
docker:
  repo: ghcr.io/me/app
deploy:
  target: swiftwave
  endpoint_env: SW_WEBHOOK
profiles:
  production:
    deploy:
      target: dokploy
      endpoint: https://panel.example.com
      application: prod-app
      update_image: true
      registry_url: ghcr.io
      registry_username_env: GHCR_USER
      registry_password_env: GHCR_TOKEN
      timeout: 2m
`)
	if err := runConfigValidate(discardLogger(), &configValidateOptions{dir: dir}); err != nil {
		t.Fatalf("config validate rejected a valid deploy configuration: %v", err)
	}
}

// TestPrimaryTaggedRef covers the reference SwiftWave's webhook matching
// depends on, including the cases where there is nothing to build one from.
func TestPrimaryTaggedRef(t *testing.T) {
	tests := []struct {
		repo string
		tags []string
		want string
	}{
		{"ghcr.io/me/app", []string{"v1.2.3", "latest"}, "ghcr.io/me/app:v1.2.3"},
		{"ghcr.io/me/app", nil, ""},
		{"", []string{"latest"}, ""},
		{"ghcr.io/me/app", []string{""}, ""},
	}
	for _, tc := range tests {
		if got := primaryTaggedRef(tc.repo, tc.tags); got != tc.want {
			t.Errorf("primaryTaggedRef(%q, %v) = %q, want %q", tc.repo, tc.tags, got, tc.want)
		}
	}
}

// TestConfigValidateChecksTheMergedDeployBlock is the regression guard for a
// bug an end-to-end run found that every unit test had missed: `pokkum config
// validate` reported this configuration valid, and `pokkum deploy -P sw` then
// refused it.
//
// A deploy block's validity is cross-field — the target decides whether
// update_image is legal — and those fields inherit from the base config. The
// validator checked each profile's RAW block, so a base setting Dokploy with
// update_image and a profile switching the target to SwiftWave (which cannot
// honour it) passed validation and failed at deploy time. A validator that
// accepts what its consumer refuses is the class already logged in Lessons.md
// for `pokkum init`.
func TestConfigValidateChecksTheMergedDeployBlock(t *testing.T) {
	dir := writeConfig(t, `
version: 1
deploy:
  target: dokploy
  endpoint: http://127.0.0.1:1/panel
  application: app-42
  update_image: true
profiles:
  sw:
    deploy:
      target: swiftwave
      endpoint: http://127.0.0.1:1/webhook/redeploy-app/app-7/tok
`)
	// The base block on its own is valid, so a failure here can only come from
	// the merged profile — which is the thing under test.
	if problems := core.ValidateDeployConfig(ports.DeployConfig{
		Target: "dokploy", Endpoint: "http://127.0.0.1:1/panel",
		Application: "app-42", UpdateImage: boolPtr(true),
	}); len(problems) != 0 {
		t.Fatalf("the base deploy block is itself invalid (%v); this test would pass for the wrong reason", problems)
	}

	if err := runConfigValidate(discardLogger(), &configValidateOptions{dir: dir}); err == nil {
		t.Fatal("config validate accepted a profile whose MERGED deploy block the deploy path refuses: " +
			"update_image inherited from the base config is not supported for swiftwave")
	}
}

// boolPtr is a local helper for the pointer-valued config fields.
func boolPtr(b bool) *bool { return &b }
