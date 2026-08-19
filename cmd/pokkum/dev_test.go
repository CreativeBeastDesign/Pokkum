package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewDevCommand_Flags(t *testing.T) {
	cmd := newDevCommand(context.Background(), discardLogger())

	if cmd.Use != "dev [dir]" {
		t.Errorf("cmd.Use = %q, want 'dev [dir]'", cmd.Use)
	}

	debugFlag := cmd.Flags().Lookup("debug")
	if debugFlag == nil {
		t.Fatalf("missing --debug flag")
	}

	portFlag := cmd.Flags().Lookup("port")
	if portFlag == nil || portFlag.DefValue != "3000:3000" {
		t.Fatalf("--port flag default = %v, want '3000:3000'", portFlag)
	}

	watchFlag := cmd.Flags().Lookup("watch")
	if watchFlag == nil || watchFlag.DefValue != "true" {
		t.Fatalf("--watch flag default = %v, want 'true'", watchFlag)
	}

	noContainerFlag := cmd.Flags().Lookup("no-container")
	if noContainerFlag == nil || noContainerFlag.DefValue != "false" {
		t.Fatalf("--no-container flag default = %v, want 'false'", noContainerFlag)
	}
}

func TestGetLatestModTime(t *testing.T) {
	dir := t.TempDir()
	mod1 := getLatestModTime(dir)
	if mod1.IsZero() {
		t.Errorf("expected non-zero mod time for temp dir")
	}

	testFile := filepath.Join(dir, "test.txt")
	_ = os.WriteFile(testFile, []byte("hello"), 0644)
	mod2 := getLatestModTime(dir)
	if !mod2.After(mod1) && mod2 != mod1 {
		t.Errorf("mod2 should be >= mod1, got mod1=%v, mod2=%v", mod1, mod2)
	}
}
