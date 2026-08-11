package jsonutils_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestFormatSuccess(t *testing.T) {
	data := map[string]string{"foo": "bar"}
	res, err := jsonutils.FormatSuccess("test-cmd", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var env ports.JSONEnvelope
	if err := json.Unmarshal(res, &env); err != nil {
		t.Fatalf("failed to unmarshal JSON envelope: %v", err)
	}

	if env.SchemaVersion != jsonutils.CurrentSchemaVersion {
		t.Errorf("expected schema version %s, got %s", jsonutils.CurrentSchemaVersion, env.SchemaVersion)
	}
	if env.Command != "test-cmd" {
		t.Errorf("expected command test-cmd, got %s", env.Command)
	}
	if env.Status != "success" {
		t.Errorf("expected status success, got %s", env.Status)
	}
}

func TestWriteError(t *testing.T) {
	var buf bytes.Buffer
	err := jsonutils.WriteError(&buf, "test-cmd", "ERR_TEST", "something failed", "extra info")
	if err != nil {
		t.Fatalf("unexpected error writing error envelope: %v", err)
	}

	var env ports.JSONEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("failed to unmarshal JSON error envelope: %v", err)
	}

	if env.Status != "error" {
		t.Errorf("expected status error, got %s", env.Status)
	}
	if env.Error == nil {
		t.Fatal("expected Error field in envelope, got nil")
	}
	if env.Error.Code != "ERR_TEST" {
		t.Errorf("expected error code ERR_TEST, got %s", env.Error.Code)
	}
}
