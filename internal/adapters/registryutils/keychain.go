package registryutils

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	"github.com/docker/docker-credential-helpers/client"
	"github.com/docker/docker-credential-helpers/credentials"
	"github.com/google/go-containerregistry/pkg/authn"
)

// IsUtilityPackage marks this package as a shared utility package.
const IsUtilityPackage = true

// CustomConfigFileKeychain is an authn.Keychain backed by a Docker config.json file.
// It supports static auth credentials in the auths block as well as dynamic credential
// helper execution (e.g., credHelpers and credsStore) with in-memory caching.
type CustomConfigFileKeychain struct {
	cf     *configfile.ConfigFile
	mu     sync.Mutex
	cache  map[string]authn.Authenticator
	warnMu sync.Mutex
	warned map[string]bool
	stderr io.Writer
}

// NewCustomConfigFileKeychain creates a new CustomConfigFileKeychain from a parsed configfile.ConfigFile.
func NewCustomConfigFileKeychain(cf *configfile.ConfigFile) *CustomConfigFileKeychain {
	return &CustomConfigFileKeychain{
		cf:     cf,
		cache:  make(map[string]authn.Authenticator),
		warned: make(map[string]bool),
		stderr: os.Stderr,
	}
}

// SetStderr sets the error/warning writer for testing.
func (k *CustomConfigFileKeychain) SetStderr(w io.Writer) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.stderr = w
}

func (k *CustomConfigFileKeychain) warnOnce(key, msg string) {
	k.warnMu.Lock()
	defer k.warnMu.Unlock()
	if !k.warned[key] {
		k.warned[key] = true
		if k.stderr != nil {
			fmt.Fprintln(k.stderr, msg)
		}
	}
}

// Resolve looks up credentials for the given resource target.
func (k *CustomConfigFileKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	if k.cf == nil {
		return authn.Anonymous, nil
	}

	reg := target.RegistryStr()

	k.mu.Lock()
	if cached, ok := k.cache[reg]; ok {
		k.mu.Unlock()
		return cached, nil
	}
	k.mu.Unlock()

	// 1. Check if a credential helper is configured
	if helperName := k.findHelper(reg); helperName != "" {
		program := client.NewShellProgramFunc("docker-credential-" + helperName)
		creds, err := client.Get(program, reg)
		if err != nil && !strings.HasPrefix(reg, "https://") {
			// Retry with URL scheme in case the helper indexes keys by full URL
			if c2, err2 := client.Get(program, "https://"+reg); err2 == nil && (c2.Username != "" || c2.Secret != "") {
				creds = c2
				err = nil
			}
		}

		if err == nil && creds != nil && (creds.Username != "" || creds.Secret != "") {
			auth := authn.FromConfig(authn.AuthConfig{
				Username: creds.Username,
				Password: creds.Secret,
			})
			k.mu.Lock()
			k.cache[reg] = auth
			k.mu.Unlock()
			return auth, nil
		}

		if err != nil {
			if _, isExecErr := err.(*exec.Error); isExecErr || strings.Contains(err.Error(), "executable file not found") {
				k.warnOnce("notfound:"+helperName, fmt.Sprintf("pokkum: warning: credential helper 'docker-credential-%s' not found on PATH; falling back to static auth", helperName))
			} else if !credentials.IsErrCredentialsNotFound(err) {
				k.warnOnce("err:"+helperName+":"+reg, fmt.Sprintf("pokkum: warning: credential helper 'docker-credential-%s' failed for %s: %v; falling back to static auth", helperName, reg, err))
			}
		}
	}

	// 2. Fall back to static auths block
	if cfg, ok := k.getStaticAuthConfig(reg); ok {
		auth := authn.FromConfig(authn.AuthConfig{
			Username:      cfg.Username,
			Password:      cfg.Password,
			Auth:          cfg.Auth,
			IdentityToken: cfg.IdentityToken,
			RegistryToken: cfg.RegistryToken,
		})
		k.mu.Lock()
		k.cache[reg] = auth
		k.mu.Unlock()
		return auth, nil
	}

	return authn.Anonymous, nil
}

func (k *CustomConfigFileKeychain) getStaticAuthConfig(reg string) (types.AuthConfig, bool) {
	if k.cf == nil {
		return types.AuthConfig{}, false
	}
	if k.cf.AuthConfigs != nil {
		if cfg, ok := k.cf.AuthConfigs[reg]; ok && (cfg.Username != "" || cfg.Password != "" || cfg.Auth != "" || cfg.IdentityToken != "" || cfg.RegistryToken != "") {
			return cfg, true
		}
		if cfg, ok := k.cf.AuthConfigs["https://"+reg]; ok && (cfg.Username != "" || cfg.Password != "" || cfg.Auth != "" || cfg.IdentityToken != "" || cfg.RegistryToken != "") {
			return cfg, true
		}
		if strings.HasPrefix(reg, "https://") {
			trimmed := strings.TrimPrefix(reg, "https://")
			if cfg, ok := k.cf.AuthConfigs[trimmed]; ok && (cfg.Username != "" || cfg.Password != "" || cfg.Auth != "" || cfg.IdentityToken != "" || cfg.RegistryToken != "") {
				return cfg, true
			}
		}
	}
	if cfg, err := k.cf.GetAuthConfig(reg); err == nil && (cfg.Username != "" || cfg.Password != "" || cfg.Auth != "" || cfg.IdentityToken != "" || cfg.RegistryToken != "") {
		return cfg, true
	}
	return types.AuthConfig{}, false
}

func (k *CustomConfigFileKeychain) findHelper(reg string) string {
	if k.cf == nil {
		return ""
	}

	// Check direct matching in credHelpers
	if h, ok := k.cf.CredentialHelpers[reg]; ok && h != "" {
		return h
	}

	// Check with https:// prefix
	if h, ok := k.cf.CredentialHelpers["https://"+reg]; ok && h != "" {
		return h
	}

	// Check without https:// prefix
	if strings.HasPrefix(reg, "https://") {
		trimmed := strings.TrimPrefix(reg, "https://")
		if h, ok := k.cf.CredentialHelpers[trimmed]; ok && h != "" {
			return h
		}
	}

	// Check global credsStore
	if k.cf.CredentialsStore != "" {
		return k.cf.CredentialsStore
	}

	return ""
}

// ResolveKeychain returns an authn.Keychain configured with the specified custom Docker config.json file,
// or authn.DefaultKeychain if configPath is empty.
func ResolveKeychain(configPath string) (authn.Keychain, error) {
	if configPath == "" {
		return authn.DefaultKeychain, nil
	}

	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("registry auth config %s: %w: %w", configPath, err, core.ErrRegistryAuth)
	}
	defer f.Close()

	cf, err := config.LoadFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("registry auth config %s: %w: %w", configPath, err, core.ErrRegistryAuth)
	}

	return authn.NewMultiKeychain(NewCustomConfigFileKeychain(cf), authn.DefaultKeychain), nil
}
