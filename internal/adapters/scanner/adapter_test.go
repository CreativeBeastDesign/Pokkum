package scanner

import (
	"context"
	"errors"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestScanner_EmbeddedAdvisories(t *testing.T) {
	adapter := NewAdapter(nil)

	res, err := adapter.Scan(context.Background(), ports.ScanRequest{
		FailOn:  ports.SeverityCritical,
		Offline: true,
	})
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if !res.Passed {
		t.Errorf("expected Scan with fail-on=critical to pass when max severity is high/medium")
	}
}

func TestScanner_FailsWhenThresholdExceeded(t *testing.T) {
	adapter := NewAdapter(nil)

	_, err := adapter.Scan(context.Background(), ports.ScanRequest{
		FailOn:  ports.SeverityLow,
		Offline: true,
	})
	if err == nil {
		t.Fatalf("expected Scan with fail-on=low to fail when advisories exist")
	}
	if !errors.Is(err, core.ErrVulnerabilityThresholdExceeded) {
		t.Errorf("expected ErrVulnerabilityThresholdExceeded, got %v", err)
	}
}
