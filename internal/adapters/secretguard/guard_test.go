package secretguard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestSecretGuard_DetectsSecretInOversizedMinifiedLine is the row-14-style
// "starts in the broken state" regression test for this feature's core bug:
// a real minified/bundled JS file is routinely ONE line far longer than
// bufio.Scanner's default 64KB token limit. The previous implementation's
// scanFile used bufio.NewScanner(f) with no larger buffer, so Scan() would
// return false with scanner.Err() == bufio.ErrTooLong on such a line — and
// ScanDirectory's caller treated ANY scanFile error as "skip this file"
// (`if err != nil { return nil }`), discarding every match already found
// and reporting the whole directory as Passed. A secret sitting past the
// 64KB mark on a giant minified line — exactly what $env/static/* baking or
// a Vite `define` replacement produces in real build output — passed
// silently. This test starts with exactly that broken shape (one line over
// 100KB, secret near the end) and asserts it is caught, not silently passed.
func TestSecretGuard_DetectsSecretInOversizedMinifiedLine(t *testing.T) {
	dir := t.TempDir()

	// Build one minified-looking line comfortably past bufio.Scanner's
	// 64KB default token limit, with the secret placed near the very end
	// — if the file were truncated or partially scanned at 64KB, this
	// secret would never be seen.
	var b strings.Builder
	b.WriteString("var app=(function(){")
	for i := 0; i < 6000; i++ {
		b.WriteString(`console.log("minified filler token number `)
		b.WriteString(strings.Repeat("x", 8))
		b.WriteString(`");`)
	}
	b.WriteString(`var apiKey="AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY";`)
	b.WriteString("})();")
	line := b.String()
	if len(line) < 65536 {
		t.Fatalf("test setup bug: line is only %d bytes, need > 64KB to exercise the old bufio.ErrTooLong path", len(line))
	}
	writeFile(t, filepath.Join(dir, "bundle.js"), line)

	adapter := NewAdapter()
	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if res.Passed {
		t.Fatalf("expected a secret past the 64KB mark of a single minified line to be caught, got Passed=true (matches=%+v, skipped=%+v)", res.Matches, res.Skipped)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("file is well under the size ceiling; it should be fully scanned, not skipped: %+v", res.Skipped)
	}
	found := false
	for _, m := range res.Matches {
		if m.RuleName == "Google API Key" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a Google API Key match past the 64KB mark, got matches: %+v", res.Matches)
	}
}

// TestSecretGuard_MultipleSecretsOnSameLineAllReported guards the companion
// fix to the above: the old code used FindStringIndex + break, reporting at
// most ONE match per line. For a minified file where "line" and "file" are
// nearly synonymous, that silently hid every secret after the first one
// inlined into a given bundle.
func TestSecretGuard_MultipleSecretsOnSameLineAllReported(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bundle.js"),
		`var a=1;var apiKey="AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY";var b=2;var token="ghp_abcdefghijklmnopqrstuvwxyz0123456789";var c=3;`)

	adapter := NewAdapter()
	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if res.Passed {
		t.Fatalf("expected two distinct secrets on one line to fail the scan, got Passed=true")
	}
	if len(res.Matches) < 2 {
		t.Errorf("expected at least 2 matches (one per secret on the same line), got %d: %+v", len(res.Matches), res.Matches)
	}
}

// TestSecretGuard_OversizedTextFileFailsClosed covers the other half of the
// 2MB-skip limitation: a file that genuinely cannot be scanned (because it
// exceeds the configured size ceiling) must be reported as a Skip that
// forces Passed=false, never silently treated as clean.
func TestSecretGuard_OversizedTextFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("a", 2048) + "\n"
	writeFile(t, filepath.Join(dir, "huge.js"), content)

	adapter := NewAdapter()
	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{
		ProjectDir:       dir,
		MaxFileSizeBytes: 1024, // smaller than the file, forces a skip
	})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if res.Passed {
		t.Fatalf("expected an oversized, unscanned file to fail closed (Passed=false), got Passed=true")
	}
	if len(res.Skipped) != 1 || res.Skipped[0].FilePath != "huge.js" {
		t.Errorf("expected huge.js to be recorded as skipped, got: %+v", res.Skipped)
	}
	if len(res.Matches) != 0 {
		t.Errorf("a skipped file must not also report matches (it was never read): %+v", res.Matches)
	}
}

// TestSecretGuard_BinaryContentSkippedSilentlyNotFailedClosed distinguishes
// "recognized non-text content" (never a coverage gap; skipped silently,
// scan still passes) from an oversized TEXT file (a real coverage gap; see
// TestSecretGuard_OversizedTextFileFailsClosed above). A NUL byte anywhere
// in the file's first bytes is git/grep's own binary heuristic.
func TestSecretGuard_BinaryContentSkippedSilentlyNotFailedClosed(t *testing.T) {
	dir := t.TempDir()
	// A NUL byte followed by bytes that would otherwise match a secret rule
	// if this were treated as text — proving the whole file is skipped, not
	// scanned-and-clean.
	content := append([]byte{0x00, 0x01, 0x02}, []byte(`api_key="AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY"`)...)
	if err := os.WriteFile(filepath.Join(dir, "native.node"), content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	adapter := NewAdapter()
	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if !res.Passed {
		t.Errorf("expected binary content to be skipped silently (scan still clean), got matches=%+v skipped=%+v", res.Matches, res.Skipped)
	}
}

// TestSecretGuard_PrecompressedSidecarsSkipped covers the .gz/.br/.zst
// sidecars internal/adapters/precompressutils generates alongside a static
// asset: these are binary-encoded duplicates of already-covered content and
// must not be flagged as an unscannable coverage gap.
func TestSecretGuard_PrecompressedSidecarsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.js.gz"), "not really gzip, just needs the extension")

	adapter := NewAdapter()
	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if !res.Passed || len(res.Skipped) != 0 {
		t.Errorf("expected .gz sidecar to be skipped outright (no match, no skip record), got matches=%+v skipped=%+v", res.Matches, res.Skipped)
	}
}

// TestSecretGuard_SourcemapsExcludedByDefaultButScannableWhenRequested
// covers the *.map exclusion in ignoreutils.DefaultPatterns(): pokkum's
// packager strips sourcemaps by default, so excluding them from the scan by
// default avoids noise/wasted work — but a build with --sourcemap set
// genuinely ships them, and a sourcemap can embed the ORIGINAL,
// un-minified source (including anything baked in via $env/static/*),
// so ScanSourcemaps must re-enable coverage for that case.
func TestSecretGuard_SourcemapsExcludedByDefaultButScannableWhenRequested(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.js.map"), `{"sourcesContent":["const apiKey = 'AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY';"]}`)

	adapter := NewAdapter()

	res, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if !res.Passed {
		t.Errorf("expected *.map to be excluded by default, got matches=%+v", res.Matches)
	}

	res2, err := adapter.ScanDirectory(context.Background(), ports.SecretScanRequest{ProjectDir: dir, ScanSourcemaps: true})
	if err != nil {
		t.Fatalf("ScanDirectory with ScanSourcemaps: %v", err)
	}
	if res2.Passed {
		t.Errorf("expected ScanSourcemaps=true to cover *.map content and catch the embedded secret, got Passed=true")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}
