package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// --- validateDevOutputFlag ---------------------------------------------
//
// dev never registers its own --output flag (it only reads the persistent
// one main.go registers on the root command), so this is tested against a
// bare *pflag.FlagSet rather than a *cobra.Command with a real parent chain
// -- exactly the same shape validateDevClusterFlags already uses fs for.

func TestValidateDevOutputFlag(t *testing.T) {
	tests := []struct {
		name    string
		output  string // "" means the flag is never Set, i.e. left at its default
		wantErr bool
	}{
		{name: "default text is accepted", output: "", wantErr: false},
		{name: "explicit text is accepted", output: "text", wantErr: false},
		{name: "json is rejected", output: "json", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := pflag.NewFlagSet("dev-output-test", pflag.ContinueOnError)
			fs.String("output", "text", "")
			if tc.output != "" {
				must(t, fs.Set("output", tc.output))
			}

			err := validateDevOutputFlag(fs)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(err, core.ErrInvalidRequest) {
				t.Fatalf("expected error to wrap core.ErrInvalidRequest, got: %v", err)
			}
			if !strings.Contains(err.Error(), "--output=json") {
				t.Fatalf("error %q does not mention --output=json", err.Error())
			}
		})
	}
}

// TestValidateDevOutputFlag_NoOutputFlagRegistered guards the "read lazily"
// half of the design: a bare FlagSet that never registered "output" at all
// (the situation every test building a standalone *cobra.Command via
// newDevCommand actually has, since --output only exists once the command is
// attached under the real root) must not be treated as a rejection -- an
// unregistered flag and an explicitly-set "text" must behave identically.
func TestValidateDevOutputFlag_NoOutputFlagRegistered(t *testing.T) {
	fs := pflag.NewFlagSet("dev-output-test-bare", pflag.ContinueOnError)
	if err := validateDevOutputFlag(fs); err != nil {
		t.Fatalf("unexpected error with no --output flag registered at all: %v", err)
	}
}

// --- validateDevFlags wiring --------------------------------------------
//
// Confirms validateDevOutputFlag is actually reached from validateDevFlags
// (the function newDevCommand's RunE calls), and that it fires before -- not
// instead of -- --cluster's own required-flag checks, so a caller combining
// --output=json with an incomplete --cluster invocation gets the clearer,
// more specific error rather than "missing --namespace and --selector".

func TestValidateDevFlags_RejectsOutputJSON(t *testing.T) {
	cmd := newDevCommand(context.Background(), discardLogger())
	// Simulates the persistent --output flag main.go registers on the root
	// command: newDevCommand's returned command has no parent in this test,
	// so nothing provides --output unless the test adds it itself.
	cmd.Flags().String("output", "text", "")
	must(t, cmd.Flags().Set("output", "json"))

	err := validateDevFlags(cmd)
	if err == nil {
		t.Fatal("expected --output=json to be rejected")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Fatalf("expected error to wrap core.ErrInvalidRequest, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--output=json") {
		t.Fatalf("error %q does not mention --output=json", err.Error())
	}
}

func TestValidateDevFlags_OutputJSONRejectedBeforeClusterFlagChecks(t *testing.T) {
	cmd := newDevCommand(context.Background(), discardLogger())
	cmd.Flags().String("output", "text", "")
	must(t, cmd.Flags().Set("output", "json"))
	// --cluster with neither --namespace nor --selector would normally fail
	// with its own "requires --namespace and --selector" error first if
	// validateDevOutputFlag were not checked ahead of it.
	must(t, cmd.Flags().Set("cluster", "true"))

	err := validateDevFlags(cmd)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "--namespace") {
		t.Fatalf("expected the --output=json rejection to take priority over --cluster's own flag checks, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--output=json") {
		t.Fatalf("error %q does not mention --output=json", err.Error())
	}
}

// TestValidateDevFlags_TextOutputUnaffected is the regression guard for the
// task's "human-readable output must be completely unchanged" requirement,
// at the validation layer: every existing validateDevFlags behavior (e.g.
// --no-container's own rejections) must be entirely unaffected by the new
// --output check when --output is left at its text default.
func TestValidateDevFlags_TextOutputUnaffected(t *testing.T) {
	cmd := newDevCommand(context.Background(), discardLogger())
	cmd.Flags().String("output", "text", "")
	must(t, cmd.Flags().Set("no-container", "true"))
	must(t, cmd.Flags().Set("debug", "true"))

	err := validateDevFlags(cmd)
	if err == nil {
		t.Fatal("expected --no-container's own --debug rejection to still fire")
	}
	if !strings.Contains(err.Error(), "--debug") {
		t.Fatalf("expected the pre-existing --no-container/--debug rejection, got: %v", err)
	}
}
