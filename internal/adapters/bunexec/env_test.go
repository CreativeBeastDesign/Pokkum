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

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
