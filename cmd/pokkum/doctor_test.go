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

// TestDoctorCommand_JSONOutput_FailureIncludesChecks guards against
// `doctor --output json` losing per-check structure on the failure path —
// exactly backwards from what a machine consumer needs, since the failing
// checks (with their Remediation) are precisely the ones that matter.
//
// tmpDir deliberately has no package.json, which makes the "SvelteKit
// Workspace" check fail deterministically regardless of the host
// environment (unlike, say, the Bun Runtime or Registry Authentication
// checks, whose outcome depends on what's installed/configured on the
// machine running the test).
func TestDoctorCommand_JSONOutput_FailureIncludesChecks(t *testing.T) {
	tmpDir := t.TempDir()

	opts := &doctorOptions{
		dir:    tmpDir,
		output: "json",
	}

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
		t.Fatalf("doctor --output=json emitted invalid JSON on a failing run: %v, raw: %s", err, outBuf.String())
	}

	if env.Status != "error" {
		t.Errorf("expected top-level status \"error\" for a failing doctor run, got %q", env.Status)
	}

	dataBytes, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("failed to re-marshal envelope Data: %v", err)
	}
	var payload ports.DoctorOutput
	if err := json.Unmarshal(dataBytes, &payload); err != nil {
		t.Fatalf("failed to unmarshal envelope Data into DoctorOutput: %v", err)
	}

	if payload.Passed {
		t.Errorf("expected DoctorOutput.Passed=false for a failing run")
	}

	// Assertion #1: the failure path must still carry the per-check array —
	// this is the core regression. Before the fix, a failing run went
	// through jsonutils.WriteError, which carries only a summary string and
	// a code: Data (and therefore every check) was entirely absent.
	if len(payload.Checks) == 0 {
		t.Fatalf("expected a failing doctor JSON run to still carry the per-check array, got zero checks (raw: %s)", outBuf.String())
	}

	var workspaceCheck *ports.DoctorCheck
	for i := range payload.Checks {
		if payload.Checks[i].Name == "SvelteKit Workspace" {
			workspaceCheck = &payload.Checks[i]
			break
		}
	}
	if workspaceCheck == nil {
		t.Fatalf("expected a %q check in the JSON failure output, checks were: %+v", "SvelteKit Workspace", payload.Checks)
	}

	// Assertion #2: the envelope must name *which* check failed, not merely
	// that some check did, and must preserve that check's Remediation
	// string — the field the SvelteKit Adapter check (and this one) puts
	// its entire actionable value into.
	if workspaceCheck.Passed {
		t.Errorf("expected %q check to be marked failed (Passed=true)", workspaceCheck.Name)
	}
	if workspaceCheck.Remediation == "" {
		t.Errorf("expected %q check's Remediation to survive into the JSON failure envelope, got empty string", workspaceCheck.Name)
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
