package ports_test

import (
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestPlatform_Methods(t *testing.T) {
	t.Run("String formatting", func(t *testing.T) {
		if ports.LinuxAMD64.String() != "linux/amd64" {
			t.Errorf("expected linux/amd64, got %q", ports.LinuxAMD64.String())
		}
		if ports.LinuxARM64.String() != "linux/arm64" {
			t.Errorf("expected linux/arm64, got %q", ports.LinuxARM64.String())
		}
		var zero ports.Platform
		if zero.String() != "" {
			t.Errorf("expected empty string for zero Platform, got %q", zero.String())
		}
		withVar := ports.Platform{OS: "linux", Arch: "arm", Variant: "v7"}
		if withVar.String() != "linux/arm/v7" {
			t.Errorf("expected linux/arm/v7, got %q", withVar.String())
		}
	})

	t.Run("Supported check", func(t *testing.T) {
		if !ports.LinuxAMD64.Supported() {
			t.Errorf("expected LinuxAMD64 to be supported")
		}
		if !ports.LinuxARM64.Supported() {
			t.Errorf("expected LinuxARM64 to be supported")
		}
		invalid := ports.Platform{OS: "windows", Arch: "amd64"}
		if invalid.Supported() {
			t.Errorf("expected windows/amd64 to be unsupported")
		}
	})

	t.Run("IsZero", func(t *testing.T) {
		var zero ports.Platform
		if !zero.IsZero() {
			t.Errorf("expected zero.IsZero() = true")
		}
		if ports.LinuxAMD64.IsZero() {
			t.Errorf("expected LinuxAMD64.IsZero() = false")
		}
	})

	t.Run("ToV1 and PlatformFromV1 roundtrip", func(t *testing.T) {
		v1Plat := ports.LinuxAMD64.ToV1()
		if v1Plat.OS != "linux" || v1Plat.Architecture != "amd64" {
			t.Errorf("unexpected v1 platform: %+v", v1Plat)
		}
		p, ok := ports.PlatformFromV1(v1Plat)
		if !ok || p != ports.LinuxAMD64 {
			t.Errorf("expected PlatformFromV1 roundtrip to equal LinuxAMD64, got p=%+v ok=%t", p, ok)
		}

		_, okNil := ports.PlatformFromV1(nil)
		if okNil {
			t.Errorf("expected PlatformFromV1(nil) = false")
		}
		_, okEmpty := ports.PlatformFromV1(&v1.Platform{})
		if okEmpty {
			t.Errorf("expected PlatformFromV1(&v1.Platform{}) = false")
		}
	})

	t.Run("BunTarget", func(t *testing.T) {
		bunAMD64, okAMD := ports.LinuxAMD64.BunTarget()
		if !okAMD || bunAMD64 != "bun-linux-x64" {
			t.Errorf("expected bun-linux-x64, got %q (ok=%t)", bunAMD64, okAMD)
		}
		bunARM64, okARM := ports.LinuxARM64.BunTarget()
		if !okARM || bunARM64 != "bun-linux-arm64" {
			t.Errorf("expected bun-linux-arm64, got %q (ok=%t)", bunARM64, okARM)
		}
		invalid := ports.Platform{OS: "darwin", Arch: "arm64"}
		_, okInv := invalid.BunTarget()
		if okInv {
			t.Errorf("expected BunTarget for darwin/arm64 to be false")
		}
	})
}

func TestSeverity_Valid(t *testing.T) {
	validSeverities := []ports.Severity{
		ports.SeverityLow,
		ports.SeverityMedium,
		ports.SeverityHigh,
		ports.SeverityCritical,
	}
	for _, s := range validSeverities {
		if !s.Valid() {
			t.Errorf("expected severity %q to be valid", s)
		}
	}

	invalid := ports.Severity("UNKNOWN")
	if invalid.Valid() {
		t.Errorf("expected severity %q to be invalid", invalid)
	}
}

func TestJSONEnvelope(t *testing.T) {
	env := ports.JSONEnvelope{
		SchemaVersion: "1.0",
		Command:       "build",
		Status:        "success",
	}
	if env.SchemaVersion != "1.0" || env.Status != "success" {
		t.Errorf("unexpected JSONEnvelope values: %+v", env)
	}
}
