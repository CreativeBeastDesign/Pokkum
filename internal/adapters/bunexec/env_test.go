package bunexec

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestBuildEnv_InheritsAndOverrides(t *testing.T) {
	t.Setenv("POKKUM_TEST_INHERITED", "from-os-environ")
	t.Setenv("POKKUM_TEST_OVERRIDDEN", "os-value")

	env := buildEnv([]string{"POKKUM_TEST_OVERRIDDEN=extra-value", "POKKUM_TEST_NEW=new-value"})

	got := envMap(env)
	if got["POKKUM_TEST_INHERITED"] != "from-os-environ" {
		t.Errorf("inherited var = %q, want %q", got["POKKUM_TEST_INHERITED"], "from-os-environ")
	}
	if got["POKKUM_TEST_OVERRIDDEN"] != "extra-value" {
		t.Errorf("overridden var = %q, want extra to win: %q", got["POKKUM_TEST_OVERRIDDEN"], "extra-value")
	}
	if got["POKKUM_TEST_NEW"] != "new-value" {
		t.Errorf("new var = %q, want %q", got["POKKUM_TEST_NEW"], "new-value")
	}
}

func TestBuildEnv_DoesNotMutateOSEnviron(t *testing.T) {
	before := os.Environ()
	_ = buildEnv([]string{"SOME_EXTRA=value"})
	after := os.Environ()
	if len(before) != len(after) {
		t.Fatalf("os.Environ() length changed: before=%d after=%d", len(before), len(after))
	}
}

func TestBuildEnvWithEpoch_SetsSourceDateEpoch(t *testing.T) {
	epoch := time.Unix(1700000000, 0).UTC()
	env := buildEnvWithEpoch(nil, epoch)
	got := envMap(env)
	if got["SOURCE_DATE_EPOCH"] != "1700000000" {
		t.Errorf("SOURCE_DATE_EPOCH = %q, want %q", got["SOURCE_DATE_EPOCH"], "1700000000")
	}
}

func TestBuildEnvWithEpoch_EpochWinsOverExtra(t *testing.T) {
	// core.CompileOptions.Env documents that SOURCE_DATE_EPOCH "must not be
	// set here" by the caller, but bunexec must not silently produce a
	// non-deterministic build if a caller does it anyway - the adapter's own
	// export always wins.
	epoch := time.Unix(1700000000, 0).UTC()
	env := buildEnvWithEpoch([]string{"SOURCE_DATE_EPOCH=0"}, epoch)
	got := envMap(env)
	if got["SOURCE_DATE_EPOCH"] != "1700000000" {
		t.Errorf("SOURCE_DATE_EPOCH = %q, want adapter-set value %q to win", got["SOURCE_DATE_EPOCH"], "1700000000")
	}
}

// TestStripHermeticSensitiveEnv_RemovesSocketBearingVars is PR-2's residual
// gap regression guard: the netns sandbox correctly blocks IP egress and
// abstract-namespace Unix sockets, but filesystem-path Unix sockets are
// namespaced by the mount namespace, which the sandbox deliberately doesn't
// touch (see hermeticStrippedEnvVars's doc comment) — this narrower
// mitigation removes the env vars that are the *only* way some of those
// sockets (chiefly SSH_AUTH_SOCK) can be located at all.
func TestStripHermeticSensitiveEnv_RemovesSocketBearingVars(t *testing.T) {
	in := []string{
		"SSH_AUTH_SOCK=/tmp/ssh-agent.sock",
		"SSH_AGENT_PID=1234",
		"GPG_AGENT_INFO=/tmp/gpg.sock:0:1",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"PATH=/usr/bin",
		"HOME=/home/user",
	}

	got := envMap(stripHermeticSensitiveEnv(in))

	for _, stripped := range hermeticStrippedEnvVars {
		if _, present := got[stripped]; present {
			t.Errorf("expected %s to be stripped, still present: %q", stripped, got[stripped])
		}
	}
	if got["PATH"] != "/usr/bin" {
		t.Errorf("expected PATH to survive stripping, got %q", got["PATH"])
	}
	if got["HOME"] != "/home/user" {
		t.Errorf("expected HOME to survive stripping, got %q", got["HOME"])
	}
}

// TestStripHermeticSensitiveEnv_DoesNotMutateInput mirrors
// TestBuildEnv_DoesNotMutateOSEnviron's discipline for this new function.
func TestStripHermeticSensitiveEnv_DoesNotMutateInput(t *testing.T) {
	in := []string{"SSH_AUTH_SOCK=/tmp/ssh-agent.sock", "PATH=/usr/bin"}
	inCopy := append([]string(nil), in...)

	_ = stripHermeticSensitiveEnv(in)

	if len(in) != len(inCopy) {
		t.Fatalf("input slice length changed: before=%d after=%d", len(inCopy), len(in))
	}
	for i := range in {
		if in[i] != inCopy[i] {
			t.Errorf("input slice mutated at index %d: before=%q after=%q", i, inCopy[i], in[i])
		}
	}
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
