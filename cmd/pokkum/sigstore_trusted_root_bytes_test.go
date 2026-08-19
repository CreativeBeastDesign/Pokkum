package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// TestBuildRequest_TrustedRootFileIsReadOnceForEveryConsumer covers the
// composition-root half of "TrustedRootPath takes bytes, not a path": the CLI
// reads --sigstore-trusted-root itself and hands the same bytes to every
// Sigstore trust-root consumer, so no adapter has to touch the filesystem and
// no two consumers can end up verifying against different bytes.
func TestBuildRequest_TrustedRootFileIsReadOnceForEveryConsumer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	want := []byte(`{"custom":"trusted-root-for-bytes-test"}`)
	path := filepath.Join(t.TempDir(), "trusted-root.json")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("write trusted root: %v", err)
	}

	flags := baseRuntimeTestFlags()
	flags.sigstoreTrustedRoot = path

	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, t.TempDir())
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
	}
	if !bytes.Equal(req.BaseImage.TrustedRootJSON, want) {
		t.Errorf("BaseImage.TrustedRootJSON = %q, want the file's bytes %q", req.BaseImage.TrustedRootJSON, want)
	}
	if !bytes.Equal(req.CacheVerify.TrustedRootJSON, want) {
		t.Errorf("CacheVerify.TrustedRootJSON = %q, want the file's bytes %q", req.CacheVerify.TrustedRootJSON, want)
	}
	// Both consumers must see the same bytes, not merely equal-looking ones
	// read at two different moments: the point of reading once is that the file
	// cannot change between consumers.
	if !bytes.Equal(req.BaseImage.TrustedRootJSON, req.CacheVerify.TrustedRootJSON) {
		t.Error("the base-image and cache-verification consumers were given different trust-root bytes")
	}
}

// TestBuildRequest_TrustedRootAbsentLeavesConsumersUnset pins the "empty means
// use the embedded default" contract: with the flag unset, nothing must invent
// bytes for the field, because an empty document would shadow the embedded
// snapshot rather than defer to it.
func TestBuildRequest_TrustedRootAbsentLeavesConsumersUnset(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, baseRuntimeTestFlags(), t.TempDir())
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
	}
	if len(req.BaseImage.TrustedRootJSON) != 0 {
		t.Errorf("BaseImage.TrustedRootJSON = %q with no --sigstore-trusted-root set, want empty", req.BaseImage.TrustedRootJSON)
	}
}

// TestBuildRequest_UnreadableTrustedRootFailsClosed is the error-semantics half
// of the move. The read used to happen inside the base-image resolver, where a
// failure aborted the build wrapping core.ErrBaseSignatureInvalid; moving it to
// the composition root must not soften that into a warning-and-carry-on, which
// would verify against a different trust root than the operator asked for.
func TestBuildRequest_UnreadableTrustedRootFailsClosed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cases := map[string]func(t *testing.T) string{
		"missing file": func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "does-not-exist.json")
		},
		"a directory": func(t *testing.T) string {
			return t.TempDir()
		},
		"unreadable mode": func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "no-permission.json")
			if err := os.WriteFile(p, []byte(`{}`), 0o000); err != nil {
				t.Fatalf("write: %v", err)
			}
			return p
		},
	}

	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "unreadable mode" && os.Geteuid() == 0 {
				t.Skip("running as root: mode 0000 is still readable, so this case cannot fail closed")
			}
			flags := baseRuntimeTestFlags()
			flags.sigstoreTrustedRoot = mk(t)

			req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, t.TempDir())
			if err == nil {
				t.Fatalf("an unreadable --sigstore-trusted-root produced no error; the build would silently "+
					"fall back to the embedded snapshot (TrustedRootJSON=%q)", req.BaseImage.TrustedRootJSON)
			}
			if !errors.Is(err, core.ErrBaseSignatureInvalid) {
				t.Errorf("error does not wrap core.ErrBaseSignatureInvalid, so callers matching on that sentinel "+
					"stop recognising this failure: %v", err)
			}
			if req != nil {
				t.Error("a BuildRequest was returned alongside the error; a partially populated request must not escape")
			}
		})
	}
}

// TestBuildRequest_UnreadableTrustedRootFailsEvenWithTUFRefreshSet guards the
// documented precedence at the failure edge: --sigstore-trusted-root always
// wins over --sigstore-tuf-refresh, so an unreadable explicit file must fail
// rather than quietly hand the decision to the refresh path.
func TestBuildRequest_UnreadableTrustedRootFailsEvenWithTUFRefreshSet(t *testing.T) {
	hits := installCountingTUFFactory(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	flags := baseRuntimeTestFlags()
	flags.sigstoreTrustedRoot = filepath.Join(t.TempDir(), "missing.json")
	flags.sigstoreTUFRefresh = true

	if _, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, t.TempDir()); err == nil {
		t.Fatal("expected an error for an unreadable explicit trust root")
	} else if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Errorf("error does not wrap core.ErrBaseSignatureInvalid: %v", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("TUF repository contacted %d time(s); an explicit --sigstore-trusted-root must never fall through "+
			"to the refresh path, failing or not", got)
	}
}
