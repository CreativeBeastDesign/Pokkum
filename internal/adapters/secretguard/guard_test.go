package secretguard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestSecretGuard_CleanDirectoryPasses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.ts"), `console.log("hello world");`)
	writeFile(t, filepath.Join(dir, ".env.example"), `API_KEY=your_key_here`)

	adapter := NewAdapter()
	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if !res.Passed {
		t.Errorf("expected clean directory to pass, got matches: %+v", res.Matches)
	}
}

func TestSecretGuard_DetectsRSAPrivateKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "key.pem"), "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0...")

	adapter := NewAdapter()
	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if res.Passed {
		t.Errorf("expected private key detection to fail scan")
	}
	if len(res.Matches) != 1 || res.Matches[0].RuleName != "RSA Private Key" {
		t.Errorf("unexpected matches: %+v", res.Matches)
	}
}

func TestSecretGuard_AllowPatternBypassesMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "test.js"), `const token = "ghp_1234567890abcdefghijklmnopqrstuvwxyz"; // mock test token`)

	adapter := NewAdapter()

	// Without allow pattern -> fails
	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if res.Passed {
		t.Errorf("expected scan to fail without allow pattern")
	}

	// With allow pattern -> passes
	res2, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{
		ProjectDir:    dir,
		AllowPatterns: []string{`mock test token`},
	})
	if err != nil {
		t.Fatalf("ScanDirectory with allow pattern: %v", err)
	}
	if !res2.Passed {
		t.Errorf("expected scan to pass with allow pattern, got matches: %+v", res2.Matches)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}
