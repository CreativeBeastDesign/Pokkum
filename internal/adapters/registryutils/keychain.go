package registryutils

import (
	"context"
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

	// Cache the negative result too. Without this, a registry that has a
	// credsStore configured but no stored credential for it re-executed the
	// helper subprocess on every single registry operation: the two caching
	// writes above are on the success paths only, so the "no credential
	// anywhere" answer — the common case for public registries on a machine
	// with `"credsStore": "desktop"` — was the one answer that never stuck.
	k.mu.Lock()
	k.cache[reg] = authn.Anonymous
	k.mu.Unlock()
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

// memoKeychain wraps a Keychain with a per-target credential cache.
//
// It exists because both keychains this package hands out re-derive
// credentials from scratch on every Resolve, and go-containerregistry calls
// Resolve once per fetcher/writer it builds — roughly once per registry
// operation. authn.DefaultKeychain in particular re-reads
// $DOCKER_CONFIG/config.json and, through docker/cli's GetAuthConfig,
// re-executes the configured credential helper subprocess (docker-credential-
// desktop / -ecr-login / -gcloud, 100-500ms each) every time.
//
// The cache key is target.String() — the *full* resource, e.g.
// "ghcr.io/acme/app", not just "ghcr.io". That is deliberate and is the
// security-relevant property of this type: authn.DefaultKeychain itself looks
// up target.String() before falling back to target.RegistryStr(), so
// credentials may legitimately be scoped to a single repository. Keying the
// memo on the finer of the two identifiers means a cached entry can only ever
// be returned for the exact resource it was resolved for; it can never hand
// one repository's — or one registry's — credential to another. Coarsening
// this key would be a credential-confusion bug, not an optimisation.
//
// Errors are never cached: a transient helper failure must not poison the
// rest of the build.
//
// The tradeoff is that a credential change made *during* a single pokkum
// invocation (a concurrent `docker login`, a helper whose token rotates) is
// not observed until the process restarts. That is the same tradeoff
// CustomConfigFileKeychain's own per-registry cache already made; the only
// thing that changes here is that the cache now survives longer than one
// registry operation, which is what makes it worth having at all.
type memoKeychain struct {
	inner authn.Keychain

	mu    sync.Mutex
	cache map[string]authn.Authenticator
}

func newMemoKeychain(inner authn.Keychain) *memoKeychain {
	return &memoKeychain{inner: inner, cache: make(map[string]authn.Authenticator)}
}

// Resolve implements authn.Keychain.
func (m *memoKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	return m.ResolveContext(context.Background(), target)
}

// ResolveContext implements authn.ContextKeychain, so that authn.Resolve keeps
// threading the caller's context through to the wrapped keychain instead of
// silently downgrading to context.Background().
func (m *memoKeychain) ResolveContext(ctx context.Context, target authn.Resource) (authn.Authenticator, error) {
	key := target.String()

	m.mu.Lock()
	if cached, ok := m.cache[key]; ok {
		m.mu.Unlock()
		return cached, nil
	}
	m.mu.Unlock()

	auth, err := authn.Resolve(ctx, m.inner, target)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.cache[key] = auth
	m.mu.Unlock()
	return auth, nil
}

// defaultKeychain is the process-wide memoised view of authn.DefaultKeychain.
// Every ResolveKeychain result reaches DefaultKeychain through this, so the
// Docker config file is parsed — and any credential helper executed — at most
// once per distinct registry/repository for the life of the process instead of
// once per registry operation.
var defaultKeychain = newMemoKeychain(authn.DefaultKeychain)

// keychainEntry memoises one ResolveKeychain result.
type keychainEntry struct {
	once sync.Once
	kc   authn.Keychain
	err  error
}

// keychainCache maps a config-file identity (path + mtime + size) to the
// keychain built from it. Memoising here is what makes
// CustomConfigFileKeychain's per-registry cache useful: before, every call
// built a brand-new keychain whose cache started empty, so the cache never
// survived a single registry operation and the credential helper was
// re-executed ~26 times per signed two-platform push.
var keychainCache sync.Map // string -> *keychainEntry

// ResolveKeychain returns an authn.Keychain configured with the specified custom Docker config.json file,
// or the memoised default keychain if configPath is empty.
//
// Results are memoised per config-file identity, so repeated calls within one
// build share a single keychain — and therefore a single credential-helper
// execution per registry. The identity includes the file's modification time
// and size, so rewriting the config file still yields a freshly parsed
// keychain rather than a stale one.
//
// Memoisation never widens credential selection: credentials are still
// resolved per target by the same keychains as before (see memoKeychain's
// comment on why its cache key is target.String()).
func ResolveKeychain(configPath string) (authn.Keychain, error) {
	if configPath == "" {
		return defaultKeychain, nil
	}

	key := configPath
	if fi, err := os.Stat(configPath); err == nil {
		key = fmt.Sprintf("%s\x00%d\x00%d", configPath, fi.ModTime().UnixNano(), fi.Size())
	}

	v, _ := keychainCache.LoadOrStore(key, &keychainEntry{})
	entry := v.(*keychainEntry)
	entry.once.Do(func() {
		entry.kc, entry.err = loadKeychain(configPath)
	})
	if entry.err != nil {
		// Never let a failure stick: a config file that was missing or
		// unparseable at this instant may be present and valid a moment later
		// (a helper writing it, a test fixture materialising it), and a
		// memoised error would keep reporting the old failure forever.
		keychainCache.Delete(key)
		return nil, entry.err
	}
	return entry.kc, nil
}

// loadKeychain parses configPath and builds the keychain for it. It is the
// pre-memoisation body of ResolveKeychain and must stay free of caching so
// that the memo above owns that concern entirely.
func loadKeychain(configPath string) (authn.Keychain, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("registry auth config %s: %w: %w", configPath, err, core.ErrRegistryAuth)
	}
	defer f.Close()

	cf, err := config.LoadFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("registry auth config %s: %w: %w", configPath, err, core.ErrRegistryAuth)
	}

	return authn.NewMultiKeychain(NewCustomConfigFileKeychain(cf), defaultKeychain), nil
}
