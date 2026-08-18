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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
)

type mockFetcher struct {
	responses map[string][]byte
}

func (m *mockFetcher) Get(_ context.Context, url string, _ int64) ([]byte, error) {
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

// --- Size cap tests (defect 1: unbounded reads before verification) ---

// TestDefaultHTTPFetcher_Get_RejectsOversizedBody confirms an oversized
// response body is rejected outright, not silently truncated and handed
// back as if it were the real (smaller) artifact -- these bytes are
// completely untrusted at fetch time (this happens before any
// signature/checksum check), so accepting a truncated body would be worse
// than accepting nothing.
func TestDownloadFetcher_Get_RejectsOversizedBody(t *testing.T) {
	const cap = 1024
	oversized := bytes.Repeat([]byte("a"), cap+500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oversized)
	}))
	defer srv.Close()

	f := &defaultHTTPFetcher{client: srv.Client()}
	body, err := f.Get(context.Background(), srv.URL, cap)
	if err == nil {
		t.Fatalf("expected error for oversized body, got %d bytes with no error", len(body))
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("expected error to mention the byte limit, got: %v", err)
	}
	if body != nil {
		t.Errorf("expected a rejected body to be nil, not truncated-and-accepted; got %d bytes", len(body))
	}
}

// TestDefaultHTTPFetcher_Get_AcceptsBodyAtExactCap confirms the LimitReader
// boundary itself is correct: a body landing exactly on the cap must be
// accepted in full, not off-by-one rejected.
func TestDownloadFetcher_Get_AcceptsBodyAtExactCap(t *testing.T) {
	const cap = 1024
	exact := bytes.Repeat([]byte("b"), cap)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(exact)
	}))
	defer srv.Close()

	f := &defaultHTTPFetcher{client: srv.Client()}
	body, err := f.Get(context.Background(), srv.URL, cap)
	if err != nil {
		t.Fatalf("unexpected error for a body exactly at the cap: %v", err)
	}
	if len(body) != cap {
		t.Errorf("body length = %d, want %d", len(body), cap)
	}
}

// TestExtractBinaryFromTarGzCapped_RejectsOversizedEntry covers the
// post-verification decompression-bomb gap: a tar entry larger than the
// cap must be rejected, not truncated-and-accepted. Uses
// extractBinaryFromTarGzCapped (the parameterized variant) with a small
// cap so the test doesn't need to construct a real 512MB+ entry.
func TestExtractBinaryFromTarGzCapped_RejectsOversizedEntry(t *testing.T) {
	const capBytes = 2048
	content := bytes.Repeat([]byte("x"), capBytes+100)
	tarGzBytes := createMockTarGz(t, content)

	data, err := extractBinaryFromTarGzCapped(tarGzBytes, "pokkum", capBytes)
	if err == nil {
		t.Fatalf("expected error for oversized tar entry, got %d bytes with no error", len(data))
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("expected error to mention the byte limit, got: %v", err)
	}
	if data != nil {
		t.Errorf("expected a rejected entry to be nil, not truncated-and-accepted; got %d bytes", len(data))
	}
}

// TestExtractBinaryFromTarGzCapped_AcceptsEntryAtExactCap mirrors the HTTP
// fetcher's exact-cap test for the tar-entry cap's LimitReader boundary.
func TestExtractBinaryFromTarGzCapped_AcceptsEntryAtExactCap(t *testing.T) {
	const capBytes = 2048
	content := bytes.Repeat([]byte("y"), capBytes)
	tarGzBytes := createMockTarGz(t, content)

	data, err := extractBinaryFromTarGzCapped(tarGzBytes, "pokkum", capBytes)
	if err != nil {
		t.Fatalf("unexpected error for an entry exactly at the cap: %v", err)
	}
	if len(data) != capBytes {
		t.Errorf("extracted length = %d, want %d", len(data), capBytes)
	}
}

// --- replaceBinary tests (defect 2: no-restore-path self-destruct) ---

// TestReplaceBinary_RenameFailureRestoresWorkingBinary is the direct
// regression test for defect 2: a simulated failure on the actual
// new-binary-into-place rename must leave targetPath holding the
// ORIGINAL, working binary -- never neither file. It injects the failure
// via the renameFile package var (this package's existing
// exitFunc/execSelfPath-style testability seam), matching the exact
// non-retriable-failure shape described in replaceBinary's doc comment
// (e.g. EXDEV, a locked file on Windows, a read-only remount) without
// needing to fabricate any of those real OS conditions.
func TestReplaceBinary_RenameFailureRestoresWorkingBinary(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "pokkum")
	newPath := filepath.Join(dir, "pokkum-new")

	oldContent := []byte("old working binary")
	newContent := []byte("new binary that will fail to install")

	if err := os.WriteFile(targetPath, oldContent, 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.WriteFile(newPath, newContent, 0o755); err != nil {
		t.Fatalf("write new binary: %v", err)
	}

	simulatedErr := errors.New("simulated rename failure: permission denied")
	origRename := renameFile
	defer func() { renameFile = origRename }()
	renameFile = func(oldpath, newpath string) error {
		if oldpath == newPath && newpath == targetPath {
			return simulatedErr
		}
		return os.Rename(oldpath, newpath)
	}

	err := replaceBinary(newPath, targetPath)
	if err == nil {
		t.Fatal("expected an error from replaceBinary when the replace rename fails, got nil")
	}
	if !errors.Is(err, simulatedErr) && !strings.Contains(err.Error(), simulatedErr.Error()) {
		t.Errorf("expected error to wrap the simulated failure, got: %v", err)
	}

	// The critical assertion this test exists for: targetPath must still
	// have a WORKING binary -- the original content -- never be missing
	// entirely (the exact self-destruct bug this fix closes).
	got, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("targetPath missing after a failed replace (self-destruct bug reintroduced): %v", readErr)
	}
	if string(got) != string(oldContent) {
		t.Errorf("targetPath content = %q, want original %q restored", got, oldContent)
	}

	// The restore should have cleaned up its own backup file.
	if _, statErr := os.Stat(targetPath + backupSuffix); !os.IsNotExist(statErr) {
		t.Errorf("expected backup path removed after a successful restore, stat err = %v", statErr)
	}
}

// TestReplaceBinary_CrossDeviceFallbackCopiesAndPreservesMode covers the
// EXDEV branch specifically: rename failing with EXDEV must fall back to a
// real copy (not error out, and not retry the identical doomed rename),
// and the copy must land the new binary's bytes at targetPath while
// preserving its file mode.
func TestReplaceBinary_CrossDeviceFallbackCopiesAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "pokkum")
	newPath := filepath.Join(dir, "pokkum-new")

	oldContent := []byte("old binary")
	newContent := []byte("new binary installed via cross-device copy fallback")
	const newMode = 0o700

	if err := os.WriteFile(targetPath, oldContent, 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.WriteFile(newPath, newContent, newMode); err != nil {
		t.Fatalf("write new binary: %v", err)
	}

	origRename := renameFile
	defer func() { renameFile = origRename }()
	renameFile = func(oldpath, newpath string) error {
		if oldpath == newPath && newpath == targetPath {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		return os.Rename(oldpath, newpath)
	}

	if err := replaceBinary(newPath, targetPath); err != nil {
		t.Fatalf("replaceBinary with simulated EXDEV: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(newContent) {
		t.Errorf("target content = %q, want %q", got, newContent)
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Mode().Perm() != newMode {
		t.Errorf("target mode = %o, want %o (preserved from the copied source)", info.Mode().Perm(), newMode)
	}

	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("expected source path removed after a successful copy fallback, stat err = %v", err)
	}
	if _, err := os.Stat(targetPath + backupSuffix); !os.IsNotExist(err) {
		t.Errorf("expected backup path cleaned up after success, stat err = %v", err)
	}
}

// TestReplaceBinary_FreshInstallNoExistingTarget confirms replaceBinary
// still works when targetPath does not exist yet (nothing to back up or
// restore) -- the fresh-install case introduced by defect 2's fix
// shouldn't regress the no-prior-binary path.
func TestReplaceBinary_FreshInstallNoExistingTarget(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "pokkum")
	newPath := filepath.Join(dir, "pokkum-new")
	content := []byte("fresh binary")

	if err := os.WriteFile(newPath, content, 0o755); err != nil {
		t.Fatalf("write new binary: %v", err)
	}

	if err := replaceBinary(newPath, targetPath); err != nil {
		t.Fatalf("replaceBinary fresh install: %v", err)
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("target content = %q, want %q", got, content)
	}
}

// TestUpgradeCommand_ApplyUpgrade_PreservesExistingMode is the end-to-end
// regression test for defect 3: a real runUpgrade apply must not widen (or
// narrow) an existing installation's file mode. Mirrors
// TestUpgradeCommand_ApplyUpgrade but installs the dummy target with a
// distinctive non-default mode (0700, not the old hardcoded 0755) and
// asserts it survives the upgrade unchanged.
func TestUpgradeCommand_ApplyUpgrade_PreservesExistingMode(t *testing.T) {
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

	tempTargetDir, err := os.MkdirTemp("", "pokkum-target-*")
	if err != nil {
		t.Fatalf("create temp target dir: %v", err)
	}
	defer os.RemoveAll(tempTargetDir)

	const originalMode = 0o700
	dummyExecPath := filepath.Join(tempTargetDir, "pokkum")
	if err := os.WriteFile(dummyExecPath, []byte("old binary"), originalMode); err != nil {
		t.Fatalf("write dummy exec: %v", err)
	}

	targetVer := "1.3.0"
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

	newContent, err := os.ReadFile(dummyExecPath)
	if err != nil {
		t.Fatalf("read upgraded executable: %v", err)
	}
	if string(newContent) != string(mockBinary) {
		t.Errorf("binary content mismatch: expected %q, got %q", string(mockBinary), string(newContent))
	}

	info, err := os.Stat(dummyExecPath)
	if err != nil {
		t.Fatalf("stat upgraded executable: %v", err)
	}
	if info.Mode().Perm() != originalMode {
		t.Errorf("mode after upgrade = %o, want %o preserved from the pre-upgrade binary", info.Mode().Perm(), originalMode)
	}
}
