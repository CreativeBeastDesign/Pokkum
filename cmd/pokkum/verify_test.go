package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestVerifyCommand_NoRebuildJSON(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	targetRepo := fmt.Sprintf("%s/app-no-rebuild", host)

	annotations := map[string]string{
		"org.opencontainers.image.source":   "github.com/example/my-app",
		"org.opencontainers.image.revision": "a1b2c3d4e5f678901234567890abcdef12345678",
	}
	img := mutate.Annotations(empty.Image, annotations).(v1.Image)
	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push test image: %v", err)
	}

	ctx := context.Background()
	opts := &verifyOptions{
		noRebuild: true,
		output:    "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runVerify(ctx, nil, opts, targetRepo+":v1.0.0")

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running verify --no-rebuild: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("verify --output=json emitted invalid JSON: %v, raw: %s", err, outBuf.String())
	}

	if env.Command != "verify" {
		t.Errorf("expected command verify, got %s", env.Command)
	}
}

func TestVerifyCommand_FullRebuildWithAgainstTarball(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	targetRepo := fmt.Sprintf("%s/app-verify-rebuild", host)

	// Create test layer
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	hdr := &tar.Header{
		Name: "app/server.js",
		Mode: 0644,
		Size: int64(len("console.log(1);")),
	}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("console.log(1);"))
	_ = tw.Close()

	layer, _ := tarball.LayerFromFile(writeTempTarFile(t, tarBuf.Bytes()))
	img, _ := mutate.AppendLayers(empty.Image, layer)

	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push remote image: %v", err)
	}

	localTarPath := writeTempImageTar(t, img, targetRepo+":v1.0.0")
	defer os.Remove(localTarPath)

	ctx := context.Background()
	opts := &verifyOptions{
		against: localTarPath,
		output:  "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runVerify(ctx, nil, opts, targetRepo+":v1.0.0")

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running verify with --against: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("verify --output=json emitted invalid JSON: %v, raw: %s", err, outBuf.String())
	}

	if env.Command != "verify" {
		t.Errorf("expected command verify, got %s", env.Command)
	}
}

func writeTempTarFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp("", "pokkum-test-layer-*.tar")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()
	_, _ = f.Write(data)
	return f.Name()
}

func writeTempImageTar(t *testing.T, img v1.Image, refStr string) string {
	t.Helper()
	f, err := os.CreateTemp("", "pokkum-test-img-*.tar")
	if err != nil {
		t.Fatalf("create temp tar: %v", err)
	}
	defer f.Close()

	ref, err := name.ParseReference(refStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("parse ref: %v", err)
	}

	if err := tarball.Write(ref, img, f); err != nil {
		t.Fatalf("write tarball: %v", err)
	}

	return f.Name()
}
