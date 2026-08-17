// Package vexutils builds a real, spec-shaped OpenVEX document
// (https://github.com/openvex/spec) from a build's active VEX exemptions.
// It is a pure data transform — no I/O, no port interface — hence the
// "utils" suffix rather than living under internal/adapters as a concrete
// adapter (see CLAUDE.md's Shared Utility Package Convention).
package vexutils

import (
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// IsUtilityPackage declares that vexutils is a helper module and not a
// direct port adapter.
const IsUtilityPackage = true

// openVEXContext is OpenVEX's own namespace URI, spec version 0.2.0.
const openVEXContext = "https://openvex.dev/ns/v0.2.0"

// statusNotAffected is the only OpenVEX status this package ever emits: a
// build-time --fail-on-cve exemption is definitionally a "not_affected"
// assertion (the other three statuses — affected/fixed/under_investigation
// — describe states an exemption mechanism doesn't represent).
const statusNotAffected = "not_affected"

// Document is a real OpenVEX document's top-level shape (the fields OpenVEX
// actually defines: @context, @id, author, timestamp, version, statements).
type Document struct {
	Context    string      `json:"@context"`
	ID         string      `json:"@id"`
	Author     string      `json:"author"`
	Timestamp  string      `json:"timestamp"`
	Version    int         `json:"version"`
	Statements []Statement `json:"statements"`
}

// Statement is a single OpenVEX statement, restricted to the fields a
// not_affected exemption statement uses: vulnerability, products, status,
// justification (mandatory for not_affected per the OpenVEX spec), and the
// optional status_notes.
type Statement struct {
	Vulnerability VulnerabilityID `json:"vulnerability"`
	Products      []Product       `json:"products"`
	Status        string          `json:"status"`
	Justification string          `json:"justification"`
	StatusNotes   string          `json:"status_notes,omitempty"`
}

// VulnerabilityID identifies the vulnerability a statement is about.
type VulnerabilityID struct {
	Name string `json:"name"`
}

// Product identifies the artifact a statement is about, per OpenVEX's
// @id-keyed product identifier convention.
type Product struct {
	ID string `json:"@id"`
}

// BuildDocument converts exemptions into a real OpenVEX document: one
// statement per non-expired exemption. now is the real current time (see
// ports.VEXExemption.Expired's doc comment for why); an already-expired
// exemption makes no statement at all — it no longer describes reality.
// id is the document's own @id, a URI Pokkum synthesizes to be unique per
// build; product is the OCI image reference every statement is about.
func BuildDocument(exemptions []ports.VEXExemption, now time.Time, id, product string) Document {
	doc := Document{
		Context:   openVEXContext,
		ID:        id,
		Author:    "Pokkum",
		Timestamp: now.UTC().Format(time.RFC3339),
		Version:   1,
	}
	for _, ex := range exemptions {
		if ex.Expired(now) {
			continue
		}
		doc.Statements = append(doc.Statements, Statement{
			Vulnerability: VulnerabilityID{Name: ex.CVE},
			Products:      []Product{{ID: product}},
			Status:        statusNotAffected,
			Justification: string(ex.Justification),
			StatusNotes:   ex.StatusNotes,
		})
	}
	return doc
}
