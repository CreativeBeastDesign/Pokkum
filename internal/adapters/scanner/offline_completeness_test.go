package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// An --offline scan must not look like a clean bill of health.
//
// Found by an adversarial field test: on a project with six real CVEs,
// `pokkum scan --offline` returned status success, passed true, incomplete
// unset, zero vulnerabilities and exit 0. The dependency lookup is skipped
// entirely when offline — which is the flag's purpose — but nothing recorded
// that it had been skipped, so the output was byte-indistinguishable from
// "scanned everything and found nothing". An air-gapped CI job got an
// unqualified green light.
//
// Contrast with the genuinely-unreachable-database path, which already behaved
// correctly: it sets Incomplete, emits warnings and fails closed.
//
// This asserts the honesty of the result, not the policy. Not failing closed on
// --offline stays deliberate, because air-gapped scanning is supported; what
// changed is that the caller can now tell the two situations apart.
func TestScan_OfflineMarksResultIncomplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","dependencies":{"svelte":"5.0.0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := NewAdapter(nil).Scan(context.Background(), ports.ScanRequest{
		Target:  dir,
		Offline: true,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if !res.Incomplete {
		t.Error("an --offline scan reported Incomplete=false; with the dependency lookup skipped, that claims a coverage it does not have")
	}
	if len(res.Warnings) == 0 {
		t.Error("an --offline scan produced no warnings; nothing tells the operator the dependency scan never ran")
	}
	var mentionsSkip bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "offline") || strings.Contains(w, "skipped") {
			mentionsSkip = true
		}
	}
	if !mentionsSkip {
		t.Errorf("no warning names the skipped dependency scan: %v", res.Warnings)
	}
}

// TestScan_OfflineStillDoesNotFailClosed pins the other half, so a later change
// cannot quietly turn air-gapped scanning into a hard failure while thinking it
// is tightening security.
func TestScan_OfflineStillDoesNotFailClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewAdapter(nil).Scan(context.Background(), ports.ScanRequest{
		Target:  dir,
		Offline: true,
	}); err != nil {
		t.Fatalf("an --offline scan must not fail closed on its own reduced coverage: %v", err)
	}
}
