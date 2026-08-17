package ports_test

import (
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func validExemption() ports.VEXExemption {
	return ports.VEXExemption{
		CVE:           "CVE-2024-12345",
		Justification: ports.VEXComponentNotPresent,
		Expires:       time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		Owner:         "security-team@example.com",
	}
}

func TestVEXExemption_Valid(t *testing.T) {
	t.Run("valid exemption has no error", func(t *testing.T) {
		if reason := validExemption().Valid(); reason != "" {
			t.Errorf("expected no validation error, got %q", reason)
		}
	})

	t.Run("missing cve", func(t *testing.T) {
		ex := validExemption()
		ex.CVE = ""
		if reason := ex.Valid(); reason == "" {
			t.Error("expected a validation error for missing cve")
		}
	})

	t.Run("invalid justification", func(t *testing.T) {
		ex := validExemption()
		ex.Justification = "not_a_real_openvex_code"
		if reason := ex.Valid(); reason == "" {
			t.Error("expected a validation error for an invalid justification code")
		}
	})

	t.Run("missing expires", func(t *testing.T) {
		ex := validExemption()
		ex.Expires = time.Time{}
		if reason := ex.Valid(); reason == "" {
			t.Error("expected a validation error for missing expires")
		}
	})

	t.Run("missing owner", func(t *testing.T) {
		ex := validExemption()
		ex.Owner = ""
		if reason := ex.Valid(); reason == "" {
			t.Error("expected a validation error for missing owner")
		}
	})
}

func TestVEXJustification_Valid(t *testing.T) {
	valid := []ports.VEXJustification{
		ports.VEXComponentNotPresent,
		ports.VEXVulnerableCodeNotPresent,
		ports.VEXVulnerableCodeNotInExecutePath,
		ports.VEXVulnerableCodeCannotBeControlledByAdversary,
		ports.VEXInlineMitigationsAlreadyExist,
	}
	for _, j := range valid {
		if !j.Valid() {
			t.Errorf("expected %q to be a valid OpenVEX justification code", j)
		}
	}
	if ports.VEXJustification("bogus").Valid() {
		t.Error("expected an invented justification code to be invalid")
	}
}

func TestVEXExemption_Expired(t *testing.T) {
	ex := validExemption() // expires 2030-01-01
	notYet := time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)
	if ex.Expired(notYet) {
		t.Error("expected exemption not to be expired before its expiry date")
	}
	past := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	if !ex.Expired(past) {
		t.Error("expected exemption to be expired after its expiry date")
	}
	// The boundary instant itself counts as expired — Expires.After(now) is
	// false when now == Expires, matching "no longer in the future".
	if !ex.Expired(ex.Expires) {
		t.Error("expected exemption to be expired exactly at its expiry instant")
	}
}

func TestVEXExemption_Matches(t *testing.T) {
	ex := validExemption()
	ex.CVE = "CVE-2024-12345"

	cases := []struct {
		name string
		v    ports.Vulnerability
		want bool
	}{
		{"same id", ports.Vulnerability{ID: "CVE-2024-12345"}, true},
		{"case-insensitive id", ports.Vulnerability{ID: "cve-2024-12345"}, true},
		{"different id", ports.Vulnerability{ID: "CVE-2024-99999"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ex.Matches(c.v); got != c.want {
				t.Errorf("Matches(%+v) = %v, want %v", c.v, got, c.want)
			}
		})
	}

	t.Run("package-scoped exemption only matches that package", func(t *testing.T) {
		scoped := ex
		scoped.Package = "openssl"
		if !scoped.Matches(ports.Vulnerability{ID: "CVE-2024-12345", Package: "openssl"}) {
			t.Error("expected match on same CVE and same package")
		}
		if scoped.Matches(ports.Vulnerability{ID: "CVE-2024-12345", Package: "curl"}) {
			t.Error("expected no match on same CVE but different package")
		}
	})
}
