package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findMsg returns the first ValidationError whose ID matches id and whose
// Msg contains substr, or nil if none matched.
func findMsg(errs []ValidationError, id, substr string) *ValidationError {
	for i := range errs {
		if errs[i].ID == id && strings.Contains(errs[i].Msg, substr) {
			return &errs[i]
		}
	}
	return nil
}

func TestValidate_ValidItemsProduceNoErrors(t *testing.T) {
	dir := t.TempDir()
	implPath := "real/file.go"
	mustTouch(t, dir, implPath)

	areas := []Area{
		{
			Name: "Test Area",
			Items: []Item{
				{ID: "a", Title: "A", Status: "open", Summary: "sums a", SourceFile: "a.yaml", Impl: []string{implPath}},
				{ID: "b", Title: "B", Status: "shipped", Summary: "sums b", SourceFile: "a.yaml", Related: []string{"a"}},
			},
		},
	}
	errs := Validate(areas, dir, map[int]bool{})
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidate_DuplicateIDAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	areas := []Area{
		{Name: "Area 1", Items: []Item{{ID: "dupe", Title: "One", Status: "open", Summary: "s", SourceFile: "one.yaml"}}},
		{Name: "Area 2", Items: []Item{{ID: "dupe", Title: "Two", Status: "open", Summary: "s", SourceFile: "two.yaml"}}},
	}
	errs := Validate(areas, dir, map[int]bool{})
	e := findMsg(errs, "dupe", "duplicate id")
	if e == nil {
		t.Fatalf("expected a duplicate-id error for %q, got %v", "dupe", errs)
	}
	if !strings.Contains(e.Msg, "one.yaml") || !strings.Contains(e.Msg, "two.yaml") {
		t.Errorf("duplicate-id error should name both files, got: %s", e.Msg)
	}
}

func TestValidate_UnknownStatus(t *testing.T) {
	dir := t.TempDir()
	areas := []Area{
		{Name: "Area", Items: []Item{{ID: "x", Title: "X", Status: "not-a-real-status", Summary: "s", SourceFile: "x.yaml"}}},
	}
	errs := Validate(areas, dir, map[int]bool{})
	if findMsg(errs, "x", "unknown status") == nil {
		t.Fatalf("expected an unknown-status error, got %v", errs)
	}
}

func TestValidate_UnknownKindTierStage(t *testing.T) {
	dir := t.TempDir()
	areas := []Area{
		{Name: "Area", Items: []Item{{
			ID: "x", Title: "X", Status: "open", Summary: "s", SourceFile: "x.yaml",
			Kind: "not-a-kind", Tier: "not-a-tier", Stage: "not-a-stage",
		}}},
	}
	errs := Validate(areas, dir, map[int]bool{})
	if findMsg(errs, "x", "unknown kind") == nil {
		t.Errorf("expected an unknown-kind error, got %v", errs)
	}
	if findMsg(errs, "x", "unknown tier") == nil {
		t.Errorf("expected an unknown-tier error, got %v", errs)
	}
	if findMsg(errs, "x", "unknown stage") == nil {
		t.Errorf("expected an unknown-stage error, got %v", errs)
	}
}

func TestValidate_MissingRequiredFields(t *testing.T) {
	dir := t.TempDir()
	areas := []Area{
		{Name: "Area", Items: []Item{{ID: "x", SourceFile: "x.yaml"}}}, // no title, status, summary
	}
	errs := Validate(areas, dir, map[int]bool{})
	for _, want := range []string{"missing required \"title\"", "missing required \"status\"", "missing required \"summary\""} {
		if findMsg(errs, "x", want) == nil {
			t.Errorf("expected error containing %q, got %v", want, errs)
		}
	}
}

func TestValidate_ImplPathDoesNotExist(t *testing.T) {
	dir := t.TempDir()
	areas := []Area{
		{Name: "Area", Items: []Item{{
			ID: "x", Title: "X", Status: "open", Summary: "s", SourceFile: "x.yaml",
			Impl: []string{"does/not/exist.go"},
		}}},
	}
	errs := Validate(areas, dir, map[int]bool{})
	if findMsg(errs, "x", "impl path does not exist") == nil {
		t.Fatalf("expected an impl-path error, got %v", errs)
	}
}

func TestValidate_RelatedReferencesUnknownID(t *testing.T) {
	dir := t.TempDir()
	areas := []Area{
		{Name: "Area", Items: []Item{{
			ID: "x", Title: "X", Status: "open", Summary: "s", SourceFile: "x.yaml",
			Related: []string{"does-not-exist"},
		}}},
	}
	errs := Validate(areas, dir, map[int]bool{})
	if findMsg(errs, "x", "related references unknown item id") == nil {
		t.Fatalf("expected a related-reference error, got %v", errs)
	}
}

func TestValidate_FindingsReferencesUnknownEntry(t *testing.T) {
	dir := t.TempDir()
	areas := []Area{
		{Name: "Area", Items: []Item{{
			ID: "x", Title: "X", Status: "open", Summary: "s", SourceFile: "x.yaml",
			Evidence: Evidence{Findings: []int{1, 999}},
		}}},
	}
	// Only entry #1 exists.
	errs := Validate(areas, dir, map[int]bool{1: true})
	e := findMsg(errs, "x", "entry #999")
	if e == nil {
		t.Fatalf("expected an error for the nonexistent finding #999, got %v", errs)
	}
	if findMsg(errs, "x", "entry #1") != nil {
		t.Errorf("finding #1 exists and should not have produced an error, got %v", errs)
	}
}

// TestValidate_MultiItemCollection_NonFirstItemError exercises
// mem:self_review_checklist rows 3/4: a multi-item fixture where the error
// is injected on the SECOND item, not the first, so a validator that
// short-circuits after the first item's checks or that was only ever
// tested against a single-item fixture would silently miss it.
func TestValidate_MultiItemCollection_NonFirstItemError(t *testing.T) {
	dir := t.TempDir()
	areas := []Area{
		{
			Name: "Area",
			Items: []Item{
				{ID: "first", Title: "First", Status: "open", Summary: "s", SourceFile: "x.yaml"},
				{ID: "second", Title: "Second", Status: "bogus-status", Summary: "s", SourceFile: "x.yaml"},
				{ID: "third", Title: "Third", Status: "shipped", Summary: "s", SourceFile: "x.yaml"},
			},
		},
	}
	errs := Validate(areas, dir, map[int]bool{})
	if findMsg(errs, "first", "") != nil {
		t.Errorf("first item is valid and should not have produced an error, got %v", errs)
	}
	if findMsg(errs, "second", "unknown status") == nil {
		t.Fatalf("expected the second item's bad status to be caught, got %v", errs)
	}
	if findMsg(errs, "third", "") != nil {
		t.Errorf("third item is valid and should not have produced an error, got %v", errs)
	}
}

func TestValidate_MissingAreaName(t *testing.T) {
	if _, err := loadAreaFile(writeTempYAML(t, "items:\n  - id: x\n    title: X\n    status: open\n    summary: s\n")); err == nil {
		t.Fatal("expected an error for a missing top-level area name, got nil")
	}
}

func TestLoadAreaFile_RejectsUnknownField(t *testing.T) {
	path := writeTempYAML(t, "area: Test\nitems:\n  - id: x\n    title: X\n    status: open\n    summary: s\n    totally_unknown_field: oops\n")
	if _, err := loadAreaFile(path); err == nil {
		t.Fatal("expected strict decoding to reject an unknown field, got nil error")
	}
}

func TestLoadAreaFile_RejectsUnknownNestedField(t *testing.T) {
	path := writeTempYAML(t, "area: Test\nitems:\n  - id: x\n    title: X\n    status: open\n    summary: s\n    evidence:\n      commits: [abc123]\n      bogus: true\n")
	if _, err := loadAreaFile(path); err == nil {
		t.Fatal("expected strict decoding to reject an unknown nested field, got nil error")
	}
}

func mustTouch(t *testing.T, root, relPath string) {
	t.Helper()
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "area.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
