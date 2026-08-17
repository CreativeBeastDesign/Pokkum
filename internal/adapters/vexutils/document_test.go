package vexutils

import (
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestBuildDocument(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	exemptions := []ports.VEXExemption{
		{
			CVE:           "CVE-2024-12345",
			Justification: ports.VEXComponentNotPresent,
			StatusNotes:   "not compiled into this image",
			Expires:       now.AddDate(1, 0, 0),
			Owner:         "security-team",
		},
	}

	doc := BuildDocument(exemptions, now, "https://pokkum.dev/vex/test", "ghcr.io/example/app@sha256:abc123")

	if doc.Context != openVEXContext {
		t.Errorf("Context = %q, want %q", doc.Context, openVEXContext)
	}
	if doc.ID != "https://pokkum.dev/vex/test" {
		t.Errorf("ID = %q", doc.ID)
	}
	if doc.Version != 1 {
		t.Errorf("Version = %d, want 1", doc.Version)
	}
	if doc.Timestamp != "2026-06-15T12:00:00Z" {
		t.Errorf("Timestamp = %q", doc.Timestamp)
	}
	if len(doc.Statements) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(doc.Statements))
	}

	s := doc.Statements[0]
	if s.Vulnerability.Name != "CVE-2024-12345" {
		t.Errorf("Vulnerability.Name = %q", s.Vulnerability.Name)
	}
	if s.Status != statusNotAffected {
		t.Errorf("Status = %q, want %q", s.Status, statusNotAffected)
	}
	if s.Justification != string(ports.VEXComponentNotPresent) {
		t.Errorf("Justification = %q", s.Justification)
	}
	if s.StatusNotes != "not compiled into this image" {
		t.Errorf("StatusNotes = %q", s.StatusNotes)
	}
	if len(s.Products) != 1 || s.Products[0].ID != "ghcr.io/example/app@sha256:abc123" {
		t.Errorf("Products = %+v", s.Products)
	}
}

func TestBuildDocument_ExpiredExemptionProducesNoStatement(t *testing.T) {
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	exemptions := []ports.VEXExemption{
		{
			CVE:           "CVE-2024-12345",
			Justification: ports.VEXComponentNotPresent,
			Expires:       now.AddDate(0, -1, 0), // expired a month ago
			Owner:         "security-team",
		},
	}

	doc := BuildDocument(exemptions, now, "https://pokkum.dev/vex/test", "ghcr.io/example/app")

	if len(doc.Statements) != 0 {
		t.Errorf("expected no statements for an expired exemption, got %+v", doc.Statements)
	}
}

func TestBuildDocument_NoExemptionsProducesEmptyDocument(t *testing.T) {
	doc := BuildDocument(nil, time.Now(), "https://pokkum.dev/vex/test", "ghcr.io/example/app")
	if len(doc.Statements) != 0 {
		t.Errorf("expected no statements, got %+v", doc.Statements)
	}
	// The document envelope itself is still real and well-formed even with
	// zero statements — @context/@id/author/version are not conditional on
	// there being anything to say.
	if doc.Context == "" || doc.ID == "" || doc.Author == "" {
		t.Errorf("expected a well-formed document envelope even with no statements, got %+v", doc)
	}
}
