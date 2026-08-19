package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFindingsNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.md")
	content := "# Findings log\n\n## Findings\n\n### 1. First finding\nSome prose about it.\n\n### 14. Fourteenth finding\nMore prose, not a header line.\nline 2\n\n### 2. Second finding\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ParseFindingsNumbers(path)
	if err != nil {
		t.Fatalf("ParseFindingsNumbers: %v", err)
	}
	for _, n := range []int{1, 2, 14} {
		if !got[n] {
			t.Errorf("expected entry #%d to be recognized, got %v", n, got)
		}
	}
	if got[3] {
		t.Errorf("entry #3 does not exist in the fixture and must not be recognized")
	}
}

func TestParseFindingsNumbers_MissingFile(t *testing.T) {
	if _, err := ParseFindingsNumbers(filepath.Join(t.TempDir(), "does-not-exist.md")); err == nil {
		t.Fatal("expected an error for a missing findings file, got nil")
	}
}
