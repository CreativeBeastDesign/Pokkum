package registryutils_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
)

// writeCountingHelper installs a docker-credential-<name> shim on PATH that
// appends the server URL it was asked about to a log file and answers with a
// credential naming that server. The log is what makes "how many subprocesses
// did this build spawn?" measurable, and the echoed server is what makes
// "did registry A get registry B's credential?" measurable.
func writeCountingHelper(t *testing.T, helperName string) (logPath string) {
	t.Helper()

	binDir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "invocations.log")

	script := fmt.Sprintf(`#!/bin/sh
read -r server
printf '%%s\n' "$server" >> %q
printf '{"ServerURL":"%%s","Username":"user-for-%%s","Secret":"secret-for-%%s"}\n' "$server" "$server" "$server"
`, logPath)

	path := filepath.Join(binDir, "docker-credential-"+helperName)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod helper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func helperInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read helper log: %v", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestResolveKeychain_MemoisesAcrossCalls is the measurement behind the
// credential-helper fix.
//
// ResolveKeychain used to reopen and reparse the config file and build a
// brand-new CustomConfigFileKeychain on every call, whose per-registry cache
// started empty — so the cache never survived a single registry operation and
// a build with a credsStore configured spawned one helper subprocess (100-500ms
// each) per registry operation, roughly two dozen per signed two-platform push.
//
// Here, twenty resolutions of the same registry must cost exactly one helper
// execution.
func TestResolveKeychain_MemoisesAcrossCalls(t *testing.T) {
	logPath := writeCountingHelper(t, "countingmemo")
	cfgPath := writeConfig(t, `{"credHelpers": {"reg.example.com": "countingmemo"}}`)

	reg, err := name.NewRegistry("reg.example.com")
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	const calls = 20
	var first any
	for i := 0; i < calls; i++ {
		kc, err := registryutils.ResolveKeychain(cfgPath)
		if err != nil {
			t.Fatalf("call %d: ResolveKeychain: %v", i, err)
		}
		if i == 0 {
			first = kc
		} else if kc != first {
			t.Fatalf("call %d returned a different keychain instance: ResolveKeychain is not memoised, so its per-registry credential cache starts empty every time", i)
		}
		auth, err := kc.Resolve(reg)
		if err != nil {
			t.Fatalf("call %d: Resolve: %v", i, err)
		}
		cfg, err := auth.Authorization()
		if err != nil {
			t.Fatalf("call %d: Authorization: %v", i, err)
		}
		if cfg.Username != "user-for-reg.example.com" {
			t.Fatalf("call %d: got credential %q, want user-for-reg.example.com", i, cfg.Username)
		}
	}

	invocations := helperInvocations(t, logPath)
	if len(invocations) != 1 {
		t.Fatalf("credential helper was executed %d times for %d registry operations (%v); want exactly 1 — "+
			"the keychain memo, or the per-registry cache inside it, is not surviving across calls",
			len(invocations), calls, invocations)
	}
}

// TestResolveKeychain_MemoDoesNotCrossRegistries is the security guard on the
// memo: sharing a keychain across a build must not let one registry's
// credential be presented to a different registry. The helper echoes back the
// server it was asked about, so a leak is directly visible in the username.
func TestResolveKeychain_MemoDoesNotCrossRegistries(t *testing.T) {
	logPath := writeCountingHelper(t, "countingcross")
	cfgPath := writeConfig(t, `{
		"credHelpers": {
			"first.example.com":  "countingcross",
			"second.example.com": "countingcross",
			"third.example.com":  "countingcross"
		}
	}`)

	registries := []string{"first.example.com", "second.example.com", "third.example.com"}

	// Interleave and repeat, so a memo that returned a previous registry's
	// entry would be caught regardless of ordering.
	for round := 0; round < 3; round++ {
		for _, regStr := range registries {
			kc, err := registryutils.ResolveKeychain(cfgPath)
			if err != nil {
				t.Fatalf("ResolveKeychain: %v", err)
			}
			reg, err := name.NewRegistry(regStr)
			if err != nil {
				t.Fatalf("parse registry %s: %v", regStr, err)
			}
			auth, err := kc.Resolve(reg)
			if err != nil {
				t.Fatalf("resolve %s: %v", regStr, err)
			}
			cfg, err := auth.Authorization()
			if err != nil {
				t.Fatalf("authorization %s: %v", regStr, err)
			}
			if want := "user-for-" + regStr; cfg.Username != want {
				t.Fatalf("round %d: BUG: registry %s was handed the credential %q (want %q) — the memoised keychain is serving one registry's credentials to another",
					round, regStr, cfg.Username, want)
			}
			if want := "secret-for-" + regStr; cfg.Password != want {
				t.Fatalf("round %d: BUG: registry %s was handed the secret %q (want %q)", round, regStr, cfg.Password, want)
			}
		}
	}

	// One helper execution per distinct registry across nine resolutions.
	invocations := helperInvocations(t, logPath)
	if len(invocations) != len(registries) {
		t.Fatalf("credential helper executed %d times (%v); want exactly %d, one per distinct registry",
			len(invocations), invocations, len(registries))
	}
}

// TestResolveKeychain_MemoDoesNotCrossConfigFiles pins the other half of the
// memo key: two different --registry-config files must never share a keychain.
func TestResolveKeychain_MemoDoesNotCrossConfigFiles(t *testing.T) {
	pathA := writeConfig(t, `{"auths": {"shared.example.com": {"username": "alice", "password": "alice-secret"}}}`)
	pathB := writeConfig(t, `{"auths": {"shared.example.com": {"username": "bob", "password": "bob-secret"}}}`)

	reg, err := name.NewRegistry("shared.example.com")
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}

	resolve := func(path string) string {
		kc, err := registryutils.ResolveKeychain(path)
		if err != nil {
			t.Fatalf("ResolveKeychain(%s): %v", path, err)
		}
		auth, err := kc.Resolve(reg)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		cfg, err := auth.Authorization()
		if err != nil {
			t.Fatalf("authorization: %v", err)
		}
		return cfg.Username
	}

	// Interleaved, twice each, so an over-broad memo key shows up whichever
	// config was seen first.
	for round := 0; round < 2; round++ {
		if got := resolve(pathA); got != "alice" {
			t.Fatalf("round %d: config A resolved to %q, want alice — the memo is keyed too coarsely and crossed config files", round, got)
		}
		if got := resolve(pathB); got != "bob" {
			t.Fatalf("round %d: config B resolved to %q, want bob — the memo is keyed too coarsely and crossed config files", round, got)
		}
	}
}

// TestResolveKeychain_ErrorsAreNotMemoised pins that a failed load does not
// stick: a config file that is absent when first asked for and present a
// moment later (a helper writing it, a fixture materialising it) must resolve
// on the second attempt rather than replay the first failure forever.
func TestResolveKeychain_ErrorsAreNotMemoised(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	if _, err := registryutils.ResolveKeychain(cfgPath); err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}

	if err := os.WriteFile(cfgPath, []byte(`{"auths": {"late.example.com": {"username": "carol", "password": "p"}}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	kc, err := registryutils.ResolveKeychain(cfgPath)
	if err != nil {
		t.Fatalf("BUG: the failure was memoised and replayed after the config file appeared: %v", err)
	}
	reg, err := name.NewRegistry("late.example.com")
	if err != nil {
		t.Fatalf("parse registry: %v", err)
	}
	auth, err := kc.Resolve(reg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	if cfg.Username != "carol" {
		t.Fatalf("got %q, want carol", cfg.Username)
	}
}
