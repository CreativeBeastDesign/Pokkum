package layerdiffutils_test

import (
	"archive/tar"
	"bytes"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/layerdiffutils"
)

func writeTarEntry(t *testing.T, tw *tar.Writer, name string, body string) {
	t.Helper()
	hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(body))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader(%q): %v", name, err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("Write(%q): %v", name, err)
	}
}

func TestListTarPaths_RegularFile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarEntry(t, tw, "app/server/index.js", "hello")
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := layerdiffutils.ListTarPaths(&buf)
	if err != nil {
		t.Fatalf("ListTarPaths: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Path != "app/server/index.js" || e.Size != 5 {
		t.Errorf("unexpected entry: %+v", e)
	}
	if e.IsWhiteout() || e.IsOpaqueWhiteout() {
		t.Errorf("regular file misclassified as whiteout: %+v", e)
	}
}

func TestListTarPaths_Whiteout(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarEntry(t, tw, "app/vendor/.wh.old-package", "")
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := layerdiffutils.ListTarPaths(&buf)
	if err != nil {
		t.Fatalf("ListTarPaths: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if !e.IsWhiteout() {
		t.Fatalf("expected %+v to be classified as a whiteout", e)
	}
	if e.IsOpaqueWhiteout() {
		t.Errorf("per-file whiteout misclassified as opaque: %+v", e)
	}
	if got, want := e.WhitesOutPath(), "app/vendor/old-package"; got != want {
		t.Errorf("WhitesOutPath() = %q, want %q", got, want)
	}
}

func TestListTarPaths_OpaqueWhiteout(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarEntry(t, tw, "app/vendor/.wh..wh..opq", "")
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := layerdiffutils.ListTarPaths(&buf)
	if err != nil {
		t.Fatalf("ListTarPaths: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if !e.IsOpaqueWhiteout() {
		t.Fatalf("expected %+v to be classified as an opaque whiteout", e)
	}
	if e.IsWhiteout() {
		t.Errorf("opaque whiteout must not also satisfy IsWhiteout(): %+v", e)
	}
	if got, want := e.OpaqueWhiteoutDir(), "app/vendor"; got != want {
		t.Errorf("OpaqueWhiteoutDir() = %q, want %q", got, want)
	}
}

func TestListTarPaths_Empty(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := layerdiffutils.ListTarPaths(&buf)
	if err != nil {
		t.Fatalf("ListTarPaths: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestListTarPaths_Malformed(t *testing.T) {
	// A handful of garbage bytes is not a valid tar header.
	r := bytes.NewReader([]byte("not a tar stream at all, just garbage"))

	if _, err := layerdiffutils.ListTarPaths(r); err == nil {
		t.Fatal("expected an error reading a malformed tar stream, got nil")
	}
}
