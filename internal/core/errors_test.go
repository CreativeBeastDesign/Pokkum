package core_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{"ErrInvalidRequest", core.ErrInvalidRequest},
		{"ErrUnsupportedPlatform", core.ErrUnsupportedPlatform},
		{"ErrProjectNotFound", core.ErrProjectNotFound},
		{"ErrBunNotFound", core.ErrBunNotFound},
		{"ErrBunTooOld", core.ErrBunTooOld},
		{"ErrAdapterMissing", core.ErrAdapterMissing},
		{"ErrPrepareFailed", core.ErrPrepareFailed},
		{"ErrCompileFailed", core.ErrCompileFailed},
		{"ErrSupervisorUnavailable", core.ErrSupervisorUnavailable},
		{"ErrInvalidBaseImage", core.ErrInvalidBaseImage},
		{"ErrBaseImageIncompatible", core.ErrBaseImageIncompatible},
		{"ErrPackageFailed", core.ErrPackageFailed},
		{"ErrNoDockerRepo", core.ErrNoDockerRepo},
		{"ErrRegistryAuth", core.ErrRegistryAuth},
		{"ErrPushFailed", core.ErrPushFailed},
		{"ErrDaemonUnavailable", core.ErrDaemonUnavailable},
		{"ErrTarballFailed", core.ErrTarballFailed},
		{"ErrInvalidSBOMFormat", core.ErrInvalidSBOMFormat},
		{"ErrSBOMFailed", core.ErrSBOMFailed},
		{"ErrManifestInvalid", core.ErrManifestInvalid},
		{"ErrManifestUnresolved", core.ErrManifestUnresolved},
		{"ErrInvalidOutputMode", core.ErrInvalidOutputMode},
		{"ErrInvalidRuntimeConfig", core.ErrInvalidRuntimeConfig},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.sentinel == nil {
				t.Fatalf("sentinel error %s is nil", tt.name)
			}

			// Single wrap test
			wrapped := fmt.Errorf("adapter: operation failed: %w", tt.sentinel)
			if !errors.Is(wrapped, tt.sentinel) {
				t.Errorf("errors.Is failed to match wrapped sentinel for %s", tt.name)
			}

			// Double wrap test (idiomatic pattern described in errors.go)
			underlying := errors.New("underlying detail")
			doubleWrapped := fmt.Errorf("adapter: op: %w: %w", underlying, tt.sentinel)
			if !errors.Is(doubleWrapped, tt.sentinel) {
				t.Errorf("errors.Is failed to match double-wrapped sentinel for %s", tt.name)
			}
			if !errors.Is(doubleWrapped, underlying) {
				t.Errorf("errors.Is failed to match underlying error for %s", tt.name)
			}
		})
	}
}

func TestIsUnsupportedPlatform(t *testing.T) {
	errMatch := fmt.Errorf("platform linux/riscv64: %w", core.ErrUnsupportedPlatform)
	if !core.IsUnsupportedPlatform(errMatch) {
		t.Errorf("IsUnsupportedPlatform returned false for wrapped ErrUnsupportedPlatform")
	}

	errOther := fmt.Errorf("some other error: %w", core.ErrInvalidRequest)
	if core.IsUnsupportedPlatform(errOther) {
		t.Errorf("IsUnsupportedPlatform returned true for ErrInvalidRequest")
	}

	if core.IsUnsupportedPlatform(nil) {
		t.Errorf("IsUnsupportedPlatform returned true for nil error")
	}
}
