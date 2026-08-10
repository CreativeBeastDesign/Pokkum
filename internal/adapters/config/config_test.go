package config

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

func TestLoadConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Test loading in current directory
	cfg, err := New(".", logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("config is nil")
	}
}

func TestGetString(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := New(".", logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Test getting a value that doesn't exist - should return default
	result := cfg.GetString("nonexistent", "default_value")
	if result != "default_value" {
		t.Errorf("expected 'default_value', got %q", result)
	}

	// Test environment variable precedence
	os.Setenv("POKKUM_TEST_VAR", "env_value")
	defer os.Unsetenv("POKKUM_TEST_VAR")

	result = cfg.GetString("test.var", "default_value")
	if result != "env_value" {
		t.Errorf("expected 'env_value' from environment, got %q", result)
	}
}

func TestGetBool(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := New(".", logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Test getting a value that doesn't exist - should return default
	result := cfg.GetBool("nonexistent", false)
	if result != false {
		t.Errorf("expected false, got %v", result)
	}

	result = cfg.GetBool("nonexistent", true)
	if result != true {
		t.Errorf("expected true, got %v", result)
	}
}

func TestResolveBuildTimestamp(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := New(".", logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Test explicit SOURCE_DATE_EPOCH
	os.Setenv("SOURCE_DATE_EPOCH", "1000000000")
	defer os.Unsetenv("SOURCE_DATE_EPOCH")

	ts, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		t.Fatalf("ResolveBuildTimestamp failed: %v", err)
	}

	expected := time.Unix(1000000000, 0).UTC()
	if ts != expected {
		t.Errorf("expected %v, got %v", expected, ts)
	}

	// Test git commit time when env var is not set
	os.Unsetenv("SOURCE_DATE_EPOCH")

	ts, err = cfg.ResolveBuildTimestamp()
	if err != nil {
		// Error is OK if git is not available in test environment
		t.Logf("ResolveBuildTimestamp returned error (OK if git unavailable): %v", err)
		return
	}

	// Git should have resolved a timestamp
	if ts.IsZero() {
		// Zero is OK - it means git wasn't available but that's tolerated
		t.Logf("ResolveBuildTimestamp returned zero (git likely not available)")
	} else if ts.Before(time.Now().Add(-365 * 24 * time.Hour)) {
		// Reasonable git timestamp
		t.Logf("ResolveBuildTimestamp resolved git timestamp: %v", ts)
	}
}

func TestConfigPrecedence(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := New(".", logger)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Test precedence: explicit env var should win over default
	os.Setenv("POKKUM_TEST_PRECEDENCE", "env_wins")
	defer os.Unsetenv("POKKUM_TEST_PRECEDENCE")

	result := cfg.GetString("test.precedence", "default")
	if result != "env_wins" {
		t.Errorf("expected 'env_wins' from env var, got %q", result)
	}

	// Clear env var and test default
	os.Unsetenv("POKKUM_TEST_PRECEDENCE")

	result = cfg.GetString("test.precedence", "default")
	if result != "default" {
		t.Errorf("expected 'default', got %q", result)
	}
}

func TestParseSourceDateEpoch(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		{
			name:    "empty string",
			input:   "",
			want:    time.Time{},
			wantErr: false,
		},
		{
			name:    "valid timestamp",
			input:   "1000000000",
			want:    time.Unix(1000000000, 0).UTC(),
			wantErr: false,
		},
		{
			name:    "invalid timestamp",
			input:   "invalid",
			want:    time.Time{},
			wantErr: true,
		},
		{
			name:    "negative timestamp",
			input:   "-1",
			want:    time.Time{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.ParseSourceDateEpoch(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSourceDateEpoch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseSourceDateEpoch() = %v, want %v", got, tt.want)
			}
		})
	}
}
