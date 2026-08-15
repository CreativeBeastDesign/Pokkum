package main

import (
	"bytes"
	"encoding/json"
	"io"

	"os"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestDoctorCommand_JSONOutput(t *testing.T) {
	tmpDir := t.TempDir()

	// Write mock package.json
	pkgJSON := `{"dependencies": {"@sveltejs/kit": "2.31.0"}}`
	if err := os.WriteFile(tmpDir+"/package.json", []byte(pkgJSON), 0644); err != nil {
		t.Fatalf("failed to write mock package.json: %v", err)
	}

	opts := &doctorOptions{
		dir:    tmpDir,
		fix:    true,
		output: "json",
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = runDoctor(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("doctor --output=json emitted invalid JSON: %v, raw: %s", err, outBuf.String())
	}

	if env.Command != "doctor" {
		t.Errorf("expected command doctor, got %s", env.Command)
	}
}

func TestDoctor_CheckBaseImageSecurity_CachedAudit(t *testing.T) {
	t.Run("Cached critical vulnerability fails doctor check", func(t *testing.T) {
		tmpDir := t.TempDir()
		lockContent := `{
			"version": 1,
			"updated_at": "2026-08-15T00:00:00Z",
			"bases": {
				"distroless": {
					"ref": "gcr.io/distroless/cc-debian12:nonroot",
					"digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
					"pinned_ref": "gcr.io/distroless/cc-debian12@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					"updated_at": "2026-08-15T00:00:00Z",
					"last_scanned_at": "2026-08-15T00:00:00Z",
					"vulnerabilities_count": 2,
					"max_severity": "CRITICAL"
				}
			}
		}`
		if err := os.WriteFile(tmpDir+"/pokkum.lock", []byte(lockContent), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		check := checkBaseImageSecurity(tmpDir, nil)
		if check.Passed {
			t.Errorf("expected check to fail on cached critical vulnerability")
		}
	})

	t.Run("Cached clean audit passes doctor check", func(t *testing.T) {
		tmpDir := t.TempDir()
		lockContent := `{
			"version": 1,
			"updated_at": "2026-08-15T00:00:00Z",
			"bases": {
				"distroless": {
					"ref": "gcr.io/distroless/cc-debian12:nonroot",
					"digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
					"pinned_ref": "gcr.io/distroless/cc-debian12@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					"updated_at": "2026-08-15T00:00:00Z",
					"last_scanned_at": "2026-08-15T00:00:00Z",
					"vulnerabilities_count": 0,
					"max_severity": ""
				}
			}
		}`
		if err := os.WriteFile(tmpDir+"/pokkum.lock", []byte(lockContent), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		check := checkBaseImageSecurity(tmpDir, nil)
		if !check.Passed {
			t.Errorf("expected clean cached audit to pass, got: %s", check.Message)
		}
	})

	t.Run("Cached audit with malformed severity string is flagged as incomplete", func(t *testing.T) {
		tmpDir := t.TempDir()
		lockContent := `{
			"version": 1,
			"updated_at": "2026-08-15T00:00:00Z",
			"bases": {
				"distroless": {
					"ref": "gcr.io/distroless/cc-debian12:nonroot",
					"digest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
					"pinned_ref": "gcr.io/distroless/cc-debian12@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					"updated_at": "2026-08-15T00:00:00Z",
					"last_scanned_at": "2026-08-15T00:00:00Z",
					"vulnerabilities_count": 1,
					"max_severity": "unknown_bogus"
				}
			}
		}`
		if err := os.WriteFile(tmpDir+"/pokkum.lock", []byte(lockContent), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		check := checkBaseImageSecurity(tmpDir, nil)
		if check.Passed {
			t.Errorf("expected check to fail on malformed cached severity")
		}
	})
}
