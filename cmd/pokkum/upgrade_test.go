package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
)

type mockFetcher struct {
	responses map[string][]byte
}

func (m *mockFetcher) Get(_ context.Context, url string) ([]byte, error) {
	if data, ok := m.responses[url]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("mock missing response for %s", url)
}

func generateTestKeyPair(t *testing.T) (privKey *ecdsa.PrivateKey, pubPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	return priv, pubPEM
}

func createMockTarGz(t *testing.T, binaryContent []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "pokkum",
		Mode: 0755,
		Size: int64(len(binaryContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(binaryContent); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

func TestUpgradeCommand_Offline(t *testing.T) {
	flags := &upgradeFlags{
		check:   true,
		output:  "text",
		offline: true,
	}

	err := runUpgrade(context.Background(), discardLogger(), nil, &mockFetcher{}, flags, "pokkum")
	if err != nil {
		t.Fatalf("runUpgrade check offline failed: %v", err)
	}

	// Apply mode offline should fail
	flags.check = false
	err = runUpgrade(context.Background(), discardLogger(), nil, &mockFetcher{}, flags, "pokkum")
	if err == nil {
		t.Fatal("expected error running upgrade apply in offline mode, got nil")
	}
}

func TestUpgradeCommand_Check_OnlineVerified(t *testing.T) {
	privKey, pubPEM := generateTestKeyPair(t)

	keyFile, err := os.CreateTemp("", "pokkum-key-*.pem")
	if err != nil {
		t.Fatalf("create temp key file: %v", err)
	}
	defer os.Remove(keyFile.Name())
	if _, err := keyFile.Write(pubPEM); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	keyFile.Close()

	targetVer := "1.1.0"
	archiveName := fmt.Sprintf("pokkum_%s_%s_%s.tar.gz", targetVer, runtime.GOOS, runtime.GOARCH)

	mockBinary := []byte("#!/bin/sh\necho pokkum 1.1.0\n")
	tarGzBytes := createMockTarGz(t, mockBinary)
	tarHash := fmt.Sprintf("%x", sha256.Sum256(tarGzBytes))

	checksumsContent := []byte(fmt.Sprintf("%s  %s\n", tarHash, archiveName))

	h := sha256.Sum256(checksumsContent)
	sigBytes, err := ecdsa.SignASN1(rand.Reader, privKey, h[:])
	if err != nil {
		t.Fatalf("sign checksums: %v", err)
	}

	baseURL := fmt.Sprintf("https://github.com/CreativeBeastDesign/pokkum/releases/download/v%s", targetVer)
	fetcher := &mockFetcher{
		responses: map[string][]byte{
			"https://api.github.com/repos/CreativeBeastDesign/pokkum/releases/latest": []byte(fmt.Sprintf(`{"tag_name":"v%s"}`, targetVer)),
			baseURL + "/checksums.txt":     checksumsContent,
			baseURL + "/checksums.txt.sig": sigBytes,
			baseURL + "/" + archiveName:    tarGzBytes,
		},
	}

	signer := cosign.NewSigner(discardLogger())

	flags := &upgradeFlags{
		check:  true,
		output: "json",
		key:    keyFile.Name(),
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runUpgrade(context.Background(), discardLogger(), signer, fetcher, flags, "pokkum")
	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runUpgrade failed: %v", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	outStr := buf.String()

	if !strings.Contains(outStr, `"verified": true`) {
		t.Errorf("expected json output to contain verified: true, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, `"update_available": true`) {
		t.Errorf("expected update_available: true, got:\n%s", outStr)
	}
}

func TestUpgradeCommand_Check_InvalidSignature(t *testing.T) {
	_, pubPEM := generateTestKeyPair(t)

	keyFile, err := os.CreateTemp("", "pokkum-key-*.pem")
	if err != nil {
		t.Fatalf("create temp key file: %v", err)
	}
	defer os.Remove(keyFile.Name())
	if _, err := keyFile.Write(pubPEM); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	keyFile.Close()

	targetVer := "1.1.0"
	archiveName := fmt.Sprintf("pokkum_%s_%s_%s.tar.gz", targetVer, runtime.GOOS, runtime.GOARCH)

	checksumsContent := []byte(fmt.Sprintf("1111111111111111111111111111111111111111111111111111111111111111  %s\n", archiveName))
	badSigBytes := []byte("badsigdata")

	baseURL := fmt.Sprintf("https://github.com/CreativeBeastDesign/pokkum/releases/download/v%s", targetVer)
	fetcher := &mockFetcher{
		responses: map[string][]byte{
			"https://api.github.com/repos/CreativeBeastDesign/pokkum/releases/latest": []byte(fmt.Sprintf(`{"tag_name":"v%s"}`, targetVer)),
			baseURL + "/checksums.txt":     checksumsContent,
			baseURL + "/checksums.txt.sig": badSigBytes,
		},
	}

	signer := cosign.NewSigner(discardLogger())

	flags := &upgradeFlags{
		check:  true,
		output: "json",
		key:    keyFile.Name(),
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runUpgrade(context.Background(), discardLogger(), signer, fetcher, flags, "pokkum")
	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runUpgrade failed: %v", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	outStr := buf.String()

	if !strings.Contains(outStr, `"verified": false`) {
		t.Errorf("expected json output to contain verified: false on bad signature, got:\n%s", outStr)
	}
}

func TestUpgradeCommand_ApplyUpgrade(t *testing.T) {
	privKey, pubPEM := generateTestKeyPair(t)

	keyFile, err := os.CreateTemp("", "pokkum-key-*.pem")
	if err != nil {
		t.Fatalf("create temp key file: %v", err)
	}
	defer os.Remove(keyFile.Name())
	if _, err := keyFile.Write(pubPEM); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	keyFile.Close()

	// Create temp target binary path
	tempTargetDir, err := os.MkdirTemp("", "pokkum-target-*")
	if err != nil {
		t.Fatalf("create temp target dir: %v", err)
	}
	defer os.RemoveAll(tempTargetDir)

	dummyExecPath := filepath.Join(tempTargetDir, "pokkum")
	if err := os.WriteFile(dummyExecPath, []byte("old binary"), 0755); err != nil {
		t.Fatalf("write dummy exec: %v", err)
	}

	targetVer := "1.2.0"
	archiveName := fmt.Sprintf("pokkum_%s_%s_%s.tar.gz", targetVer, runtime.GOOS, runtime.GOARCH)

	mockBinary := []byte("new upgraded binary content")
	tarGzBytes := createMockTarGz(t, mockBinary)
	tarHash := fmt.Sprintf("%x", sha256.Sum256(tarGzBytes))

	checksumsContent := []byte(fmt.Sprintf("%s  %s\n", tarHash, archiveName))

	h := sha256.Sum256(checksumsContent)
	sigBytes, err := ecdsa.SignASN1(rand.Reader, privKey, h[:])
	if err != nil {
		t.Fatalf("sign checksums: %v", err)
	}

	baseURL := fmt.Sprintf("https://github.com/CreativeBeastDesign/pokkum/releases/download/v%s", targetVer)
	fetcher := &mockFetcher{
		responses: map[string][]byte{
			baseURL + "/checksums.txt":     checksumsContent,
			baseURL + "/checksums.txt.sig": sigBytes,
			baseURL + "/" + archiveName:    tarGzBytes,
		},
	}

	signer := cosign.NewSigner(discardLogger())

	flags := &upgradeFlags{
		check:  false,
		target: targetVer,
		output: "json",
		key:    keyFile.Name(),
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runUpgrade(context.Background(), discardLogger(), signer, fetcher, flags, dummyExecPath)
	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runUpgrade apply failed: %v", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	outStr := buf.String()

	if !strings.Contains(outStr, `"applied": true`) {
		t.Errorf("expected json output to contain applied: true, got:\n%s", outStr)
	}

	// Verify binary was actually replaced
	newContent, err := os.ReadFile(dummyExecPath)
	if err != nil {
		t.Fatalf("read upgraded executable: %v", err)
	}
	if string(newContent) != string(mockBinary) {
		t.Errorf("binary content mismatch: expected %q, got %q", string(mockBinary), string(newContent))
	}
}

// TestUpgradeCommand_NilVerifier_ApplyFailsClosed guards against the exact
// shape of the original bug: a nil verifier must never let apply mode fall
// through to downloading and installing a binary with Verified silently
// true. It must error before any network call.
func TestUpgradeCommand_NilVerifier_ApplyFailsClosed(t *testing.T) {
	flags := &upgradeFlags{
		check:  false,
		target: "9.9.9",
		output: "json",
	}

	err := runUpgrade(context.Background(), discardLogger(), nil, &mockFetcher{}, flags, "pokkum")
	if err == nil {
		t.Fatal("expected error when applying an upgrade with no verifier configured, got nil")
	}
}

// TestUpgradeCommand_NilVerifier_CheckReportsUnverified confirms --check
// with no verifier configured degrades to an honest verified=false report
// instead of erroring — nothing gets installed in check mode, so this is
// safe, unlike the apply-mode case above.
func TestUpgradeCommand_NilVerifier_CheckReportsUnverified(t *testing.T) {
	targetVer := "1.1.0"
	archiveName := fmt.Sprintf("pokkum_%s_%s_%s.tar.gz", targetVer, runtime.GOOS, runtime.GOARCH)
	checksumsContent := []byte(fmt.Sprintf("1111111111111111111111111111111111111111111111111111111111111111  %s\n", archiveName))

	baseURL := fmt.Sprintf("https://github.com/CreativeBeastDesign/pokkum/releases/download/v%s", targetVer)
	fetcher := &mockFetcher{
		responses: map[string][]byte{
			baseURL + "/checksums.txt":     checksumsContent,
			baseURL + "/checksums.txt.sig": []byte("irrelevant-without-a-verifier"),
		},
	}

	flags := &upgradeFlags{
		check:  true,
		target: targetVer,
		output: "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runUpgrade(context.Background(), discardLogger(), nil, fetcher, flags, "pokkum")
	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runUpgrade check with nil verifier failed: %v", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	if !strings.Contains(buf.String(), `"verified": false`) {
		t.Errorf("expected json output to contain verified: false with no verifier configured, got:\n%s", buf.String())
	}
}
