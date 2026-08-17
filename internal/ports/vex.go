package ports

import (
	"fmt"
	"strings"
	"time"
)

// VEXJustification is one of OpenVEX's five defined justification codes for
// a "not_affected" status statement (https://github.com/openvex/spec) — the
// only values a Pokkum exemption may declare, so every exemption this tool
// evaluates corresponds to a real, spec-valid OpenVEX statement rather than
// an invented one.
type VEXJustification string

const (
	VEXComponentNotPresent                         VEXJustification = "component_not_present"
	VEXVulnerableCodeNotPresent                    VEXJustification = "vulnerable_code_not_present"
	VEXVulnerableCodeNotInExecutePath              VEXJustification = "vulnerable_code_not_in_execute_path"
	VEXVulnerableCodeCannotBeControlledByAdversary VEXJustification = "vulnerable_code_cannot_be_controlled_by_adversary"
	VEXInlineMitigationsAlreadyExist               VEXJustification = "inline_mitigations_already_exist"
)

// Valid reports whether j is one of OpenVEX's five defined justification codes.
func (j VEXJustification) Valid() bool {
	switch j {
	case VEXComponentNotPresent, VEXVulnerableCodeNotPresent, VEXVulnerableCodeNotInExecutePath, VEXVulnerableCodeCannotBeControlledByAdversary, VEXInlineMitigationsAlreadyExist:
		return true
	default:
		return false
	}
}

// VEXExemption records why a specific CVE is allowed to pass
// --fail-on-cve's threshold gate, as a "not_affected" OpenVEX statement (see
// internal/adapters/vex for the real OpenVEX document this becomes).
//
// Expires and Owner are Pokkum-specific requirements layered on top of the
// OpenVEX standard — OpenVEX itself defines no per-statement expiry or
// owner field. They exist so a one-off exemption cannot silently become a
// permanent, unreviewed hole: past its expiry it stops applying and the
// gate re-applies to that CVE, until someone deliberately re-authors the
// exemption with a fresh expiry and takes ownership of that decision again.
type VEXExemption struct {
	// CVE is the vulnerability identifier this exemption covers (e.g.
	// "CVE-2024-12345"). Matched case-insensitively against
	// Vulnerability.ID.
	CVE string

	// Package optionally scopes the exemption to one affected package
	// (Vulnerability.Package). Empty matches the CVE regardless of package.
	Package string

	// Justification is mandatory and must be one of OpenVEX's five defined
	// codes — see VEXJustification's doc comment.
	Justification VEXJustification

	// StatusNotes is optional free-text human context, mapped to OpenVEX's
	// own optional status_notes field.
	StatusNotes string

	// Expires is mandatory: once passed, this exemption no longer applies
	// and the underlying CVE counts toward --fail-on-cve's threshold again.
	Expires time.Time

	// Owner is mandatory: who is accountable for this exemption (a name,
	// email, or team) — not a real OpenVEX field, a Pokkum addition so a
	// human, not just a timestamp, is attached to the decision.
	Owner string
}

// Valid reports the first reason ex is not usable, or "" if it is usable.
// All four of CVE/Justification/Expires/Owner are mandatory.
func (ex VEXExemption) Valid() string {
	switch {
	case strings.TrimSpace(ex.CVE) == "":
		return "cve is required"
	case !ex.Justification.Valid():
		return fmt.Sprintf("justification %q is not one of OpenVEX's defined justification codes", ex.Justification)
	case ex.Expires.IsZero():
		return "expires is required"
	case strings.TrimSpace(ex.Owner) == "":
		return "owner is required"
	default:
		return ""
	}
}

// Expired reports whether ex's expiry has passed as of now. now is an
// explicit parameter, not time.Now(), so callers control it (real wall-clock
// time at the one real call site, a fixed instant in tests) — this is about
// unit-testability, not the bit-for-bit OCI reproducibility invariant: an
// exemption's expiry is real-world calendar time by definition (when its
// owner must re-review it), never image layer content, so it is correctly
// evaluated against actual current time rather than SOURCE_DATE_EPOCH — a
// rebuild of an old commit today must detect a since-expired exemption using
// today's date, not silently honor it because the commit predates expiry.
func (ex VEXExemption) Expired(now time.Time) bool {
	return !ex.Expires.After(now)
}

// Matches reports whether ex covers vulnerability v: the same CVE
// (case-insensitive), and, if ex.Package is set, the same package too.
func (ex VEXExemption) Matches(v Vulnerability) bool {
	if !strings.EqualFold(ex.CVE, v.ID) {
		return false
	}
	if ex.Package != "" && ex.Package != v.Package {
		return false
	}
	return true
}
