package registryutils_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/docker/cli/cli/config"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestResolve_StaticAuth(t *testing.T) {
	jsonConfig := `{
		"auths": {
			"registry.example.com": {
				"username": "testuser",
				"password": "secretpassword"
			}
		}
	}`
	cf, err := config.LoadFromReader(strings.NewReader(jsonConfig))
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	kc := registryutils.NewCustomConfigFileKeychain(cf)
	reg, err := name.NewRegistry("registry.example.com")
	if err != nil {
		t.Fatalf("failed to parse registry: %v", err)
	}

	auth, err := kc.Resolve(reg)
	if err != nil {
		t.Fatalf("unexpected error resolving auth: %v", err)
	}

	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatalf("unexpected error getting auth config: %v", err)
	}

	if cfg.Username != "testuser" || cfg.Password != "secretpassword" {
		t.Errorf("expected testuser/secretpassword, got %s/%s", cfg.Username, cfg.Password)
	}
}

func TestResolve_AnonymousFallback(t *testing.T) {
	jsonConfig := `{"auths": {}}`
	cf, err := config.LoadFromReader(strings.NewReader(jsonConfig))
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	kc := registryutils.NewCustomConfigFileKeychain(cf)
	reg, err := name.NewRegistry("unknown.registry.io")
	if err != nil {
		t.Fatalf("failed to parse registry: %v", err)
	}

	auth, err := kc.Resolve(reg)
	if err != nil {
		t.Fatalf("unexpected error resolving auth: %v", err)
	}

	if auth != authn.Anonymous {
		t.Errorf("expected authn.Anonymous, got %v", auth)
	}
}

func TestResolve_CredHelpers_MissingBinary(t *testing.T) {
	jsonConfig := `{
		"credHelpers": {
			"123456789.dkr.ecr.us-east-1.amazonaws.com": "nonexistent-helper-xyz"
		},
		"auths": {
			"123456789.dkr.ecr.us-east-1.amazonaws.com": {
				"username": "fallbackuser",
				"password": "fallbackpassword"
			}
		}
	}`
	cf, err := config.LoadFromReader(strings.NewReader(jsonConfig))
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	var warnBuf bytes.Buffer
	kc := registryutils.NewCustomConfigFileKeychain(cf)
	kc.SetStderr(&warnBuf)

	reg, err := name.NewRegistry("123456789.dkr.ecr.us-east-1.amazonaws.com")
	if err != nil {
		t.Fatalf("failed to parse registry: %v", err)
	}

	auth, err := kc.Resolve(reg)
	if err != nil {
		t.Fatalf("unexpected error resolving auth: %v", err)
	}

	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatalf("unexpected error getting auth config: %v", err)
	}

	if cfg.Username != "fallbackuser" || cfg.Password != "fallbackpassword" {
		t.Errorf("expected fallback credentials, got %s/%s", cfg.Username, cfg.Password)
	}

	if !strings.Contains(warnBuf.String(), "not found on PATH") {
		t.Errorf("expected warning about missing binary on PATH, got: %s", warnBuf.String())
	}
}

func TestResolve_CredHelpers_MockExecutionAndCaching(t *testing.T) {
	tmpDir := t.TempDir()
	mockHelperPath := filepath.Join(tmpDir, "docker-credential-mockprovider")

	// Create mock credential helper script that returns JSON credentials
	script := `#!/bin/sh
cat << 'EOF'
{"ServerURL":"mock.registry.io","Username":"mockuser","Secret":"mocksecret"}
EOF
`
	if err := os.WriteFile(mockHelperPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to create mock helper script: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s:%s", tmpDir, origPath))

	jsonConfig := `{
		"credHelpers": {
			"mock.registry.io": "mockprovider"
		}
	}`
	cf, err := config.LoadFromReader(strings.NewReader(jsonConfig))
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	kc := registryutils.NewCustomConfigFileKeychain(cf)
	reg, err := name.NewRegistry("mock.registry.io")
	if err != nil {
		t.Fatalf("failed to parse registry: %v", err)
	}

	auth, err := kc.Resolve(reg)
	if err != nil {
		t.Fatalf("unexpected error resolving auth: %v", err)
	}

	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatalf("unexpected error getting auth config: %v", err)
	}

	if cfg.Username != "mockuser" || cfg.Password != "mocksecret" {
		t.Errorf("expected mockuser/mocksecret, got %s/%s", cfg.Username, cfg.Password)
	}

	// Delete the mock helper to verify that subsequent Resolve calls hit the in-memory cache
	if err := os.Remove(mockHelperPath); err != nil {
		t.Fatalf("failed to remove mock helper: %v", err)
	}

	cachedAuth, err := kc.Resolve(reg)
	if err != nil {
		t.Fatalf("unexpected error resolving cached auth: %v", err)
	}
	cachedCfg, err := cachedAuth.Authorization()
	if err != nil {
		t.Fatalf("unexpected error getting cached auth config: %v", err)
	}
	if cachedCfg.Username != "mockuser" || cachedCfg.Password != "mocksecret" {
		t.Errorf("expected cached mockuser/mocksecret, got %s/%s", cachedCfg.Username, cachedCfg.Password)
	}
}

func TestResolveKeychain_Errors(t *testing.T) {
	// 1. Empty string returns DefaultKeychain
	kc, err := registryutils.ResolveKeychain("")
	if err != nil {
		t.Fatalf("expected no error for empty config path, got %v", err)
	}
	if kc == nil {
		t.Fatal("expected non-nil keychain")
	}

	// 2. Non-existent file returns core.ErrRegistryAuth wrapped
	_, err = registryutils.ResolveKeychain("/non/existent/path/config.json")
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
	if !errors.Is(err, core.ErrRegistryAuth) {
		t.Errorf("expected error to wrap core.ErrRegistryAuth, got %v", err)
	}
}
