package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	pokkumregistry "github.com/CreativeBeastDesign/pokkum/pkg/registry"
)

// pushImageWithAnnotations starts an in-memory OCI registry, pushes a real
// image carrying the given OCI manifest annotations, and returns its full
// "host/repo:tag" ref — so history_test.go exercises fetchImageHistory's
// real remote.Get/manifest-read path against a real (if local) registry,
// not the network. Mirrors the pattern already used by
// internal/adapters/baseimage/resolver_test.go and
// internal/adapters/registry/registry_test.go.
func pushImageWithAnnotations(t *testing.T, annotations map[string]string) string {
	t.Helper()

	srv, err := pokkumregistry.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(srv.Close)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	img = mutate.Annotations(img, annotations).(v1.Image)

	ref := srv.Repo("history-test") + ":v1"
	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", ref, err)
	}
	if err := remote.Write(tag, img); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return ref
}

func TestHistoryCommand_RealAnnotations(t *testing.T) {
	ref := pushImageWithAnnotations(t, map[string]string{
		"org.opencontainers.image.revision": "abc1234def5678",
		"org.opencontainers.image.source":   "https://github.com/acme/app",
		"org.opencontainers.image.created":  "2026-01-02T03:04:05Z",
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	flags := &historyFlags{output: "json"}
	err := runHistory(context.Background(), logger, flags, ref)
	_ = w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runHistory failed: %v", err)
	}

	var output bytes.Buffer
	_, _ = io.Copy(&output, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(output.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse JSON envelope: %v, raw: %s", err, output.String())
	}
	if env.Command != "history" || env.Status != "success" {
		t.Fatalf("unexpected envelope: %+v", env)
	}

	dataBytes, _ := json.Marshal(env.Data)
	var res ports.HistoryOutput
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		t.Fatalf("failed to parse HistoryOutput: %v", err)
	}

	// These must be the REAL values pushed with the image, not a hardcoded
	// stub — this is the exact gap the previous version of this test
	// couldn't catch, since it never asserted the values differed per image.
	if res.GitCommit != "abc1234def5678" {
		t.Errorf("expected real GitCommit from the pushed image's annotation, got %q", res.GitCommit)
	}
	if res.GitRepo != "https://github.com/acme/app" {
		t.Errorf("expected real GitRepo from the pushed image's annotation, got %q", res.GitRepo)
	}
	if res.BuildTimestamp != "2026-01-02T03:04:05Z" {
		t.Errorf("expected real BuildTimestamp from the pushed image's annotation, got %q", res.BuildTimestamp)
	}
	if res.ImageDigest == "" {
		t.Error("expected non-empty real image digest")
	}
}

func TestHistoryCommand_NoAnnotations(t *testing.T) {
	ref := pushImageWithAnnotations(t, nil)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	flags := &historyFlags{output: "text"}
	if err := runHistory(context.Background(), logger, flags, ref); err != nil {
		t.Fatalf("expected an image with no annotations to still succeed with empty fields, got: %v", err)
	}
}

func TestHistoryCommand_ImageNotFound(t *testing.T) {
	// A local registry with nothing pushed to this repo/tag — hermetic and
	// fast, unlike depending on a real network round-trip to a public
	// registry to prove the same "not found" error path.
	srv, err := pokkumregistry.NewServer()
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(srv.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	flags := &historyFlags{output: "json"}
	ref := srv.Repo("never-pushed") + ":latest"
	runErr := runHistory(context.Background(), logger, flags, ref)
	_ = w.Close()
	os.Stdout = oldStdout

	var output bytes.Buffer
	_, _ = io.Copy(&output, r)

	// JSON mode reports failure via the envelope's status field rather than
	// a non-nil Go error, matching this repo's existing convention (same
	// shape as adopt.go/doctor.go/init.go/verify.go) — CI is expected to
	// parse status, not rely on the exit code, in --output=json mode.
	if runErr != nil {
		t.Fatalf("unexpected Go error (should report via JSON envelope instead): %v", runErr)
	}
	var env ports.JSONEnvelope
	if err := json.Unmarshal(output.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse JSON envelope: %v, raw: %s", err, output.String())
	}
	if env.Status != "error" {
		t.Fatalf("expected status=error for a nonexistent image, got: %+v", env)
	}
}

func TestHistoryCommand_EmptyImageRef(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newHistoryCommand(context.Background(), logger)
	cmd.SetArgs([]string{""})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an empty image reference, got nil")
	}
}
