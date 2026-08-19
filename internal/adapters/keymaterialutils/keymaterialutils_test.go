package keymaterialutils_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/keymaterialutils"
)

const samplePEM = "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----\n"

func TestResolve_LiteralPEM(t *testing.T) {
	got, err := keymaterialutils.Resolve(samplePEM, "TEST_VAR")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != samplePEM {
		t.Errorf("literal PEM must pass through unchanged, got %q", got)
	}
}

func TestResolve_PathToPEMFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, []byte(samplePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := keymaterialutils.Resolve(path, "TEST_VAR")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != samplePEM {
		t.Errorf("file contents not returned, got %q", got)
	}
}

func TestResolve_EmptyIsNotAnError(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t "} {
		got, err := keymaterialutils.Resolve(in, "TEST_VAR")
		if err != nil {
			t.Errorf("Resolve(%q) errored; nothing-configured is a legitimate state: %v", in, err)
		}
		if got != nil {
			t.Errorf("Resolve(%q) = %q, want nil", in, got)
		}
	}
}

// TestResolve_MistypedPathFailsAsAMissingFile is the behavioural point of this
// package. The previous shape — try os.ReadFile, and on any error treat the
// string itself as PEM — converted a typo'd filename into nonsense key bytes,
// which surfaced much later as "signature verification failed": a message that
// sends the reader hunting for a tampered artifact or a wrong key when the real
// problem is a misspelled path.
func TestResolve_MistypedPathFailsAsAMissingFile(t *testing.T) {
	_, err := keymaterialutils.Resolve("/nonexistent/definitely-not-here/key.pem", "POKKUM_CACHE_PUBKEY")
	if err == nil {
		t.Fatal("a path that cannot be read must be an error, not silently reinterpreted as PEM")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error should wrap fs.ErrNotExist so callers can tell a missing file from bad key material, got %v", err)
	}
	// The failure must name the variable at fault; "read public key file" alone
	// leaves the operator guessing which of four settings they got wrong.
	if !strings.Contains(err.Error(), "POKKUM_CACHE_PUBKEY") {
		t.Errorf("error must name its source, got %v", err)
	}
}

// TestResolve_ReadableFileWithoutPEMIsRejected covers pointing the setting at a
// real file that is not a key — a config file, a private key's passphrase, an
// empty placeholder. Left to the verifier this becomes an opaque parse failure.
func TestResolve_ReadableFileWithoutPEMIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-key.txt")
	if err := os.WriteFile(path, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := keymaterialutils.Resolve(path, "TEST_VAR")
	if err == nil {
		t.Fatal("a readable file with no PEM marker must be rejected here, not passed to the verifier")
	}
	if !strings.Contains(err.Error(), "PEM") {
		t.Errorf("error should say the file is not a PEM public key, got %v", err)
	}
}

func TestResolveFirst_PrecedenceAndSource(t *testing.T) {
	dir := t.TempDir()
	second := filepath.Join(dir, "second.pem")
	if err := os.WriteFile(second, []byte(samplePEM), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("first non-empty wins and its source is reported", func(t *testing.T) {
		got, source, err := keymaterialutils.ResolveFirst(
			keymaterialutils.Candidate{Source: "FIRST", Setting: ""},
			keymaterialutils.Candidate{Source: "SECOND", Setting: second},
			keymaterialutils.Candidate{Source: "THIRD", Setting: samplePEM},
		)
		if err != nil {
			t.Fatalf("ResolveFirst: %v", err)
		}
		if source != "SECOND" {
			t.Errorf("source = %q, want SECOND — precedence must follow argument order", source)
		}
		if string(got) != samplePEM {
			t.Errorf("wrong bytes returned: %q", got)
		}
	})

	t.Run("nothing set is not an error", func(t *testing.T) {
		got, source, err := keymaterialutils.ResolveFirst(
			keymaterialutils.Candidate{Source: "A", Setting: ""},
			keymaterialutils.Candidate{Source: "B", Setting: "  "},
		)
		if err != nil || got != nil || source != "" {
			t.Errorf("empty chain = (%q, %q, %v), want (nil, \"\", nil)", got, source, err)
		}
	})

	// The important one: a set-but-broken earlier candidate must NOT be skipped
	// in favour of a later working one. Falling through would verify against a
	// different key than the operator named — the exact substitution they set
	// the value to prevent — and would do it silently.
	t.Run("a broken earlier candidate errors instead of falling through", func(t *testing.T) {
		_, source, err := keymaterialutils.ResolveFirst(
			keymaterialutils.Candidate{Source: "BROKEN", Setting: "/nonexistent/nope.pem"},
			keymaterialutils.Candidate{Source: "WORKING", Setting: samplePEM},
		)
		if err == nil {
			t.Fatal("a set-but-unresolvable candidate must fail the chain, not be skipped")
		}
		if source != "BROKEN" {
			t.Errorf("source = %q, want BROKEN so the message blames the right setting", source)
		}
	})
}
