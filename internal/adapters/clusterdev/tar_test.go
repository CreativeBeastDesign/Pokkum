package clusterdev

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// readTar returns the archive's entry names mapped to their contents, with
// directory entries recorded as an empty string under their trailing-slash
// name.
func readTar(t *testing.T, data []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(body)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestWriteTar_MapsTwoDirsAndExcludesTopLevelChildren covers the shape a real
// sync sends: two source directories landing at two different in-pod
// destinations, with the /app/server tree's client/vendor/native/prerendered
// children excluded exactly as the packager excludes them when it builds that
// layer.
//
// Two directories, not one, on purpose: a single-directory fixture cannot
// catch a prefix that leaks from one destination into the other.
func TestWriteTar_MapsTwoDirsAndExcludesTopLevelChildren(t *testing.T) {
	base := t.TempDir()
	out := filepath.Join(base, "output")
	client := filepath.Join(out, "client")

	writeFile(t, filepath.Join(out, "index.js"), "server-entry")
	writeFile(t, filepath.Join(out, "server", "chunk.js"), "server-chunk")
	writeFile(t, filepath.Join(out, "vendor", "junk.js"), "vendor")
	writeFile(t, filepath.Join(out, "native", "n.node"), "native")
	writeFile(t, filepath.Join(out, "prerendered", "index.html"), "prerendered")
	writeFile(t, client, "")
	if err := os.Remove(client); err != nil {
		t.Fatalf("remove placeholder: %v", err)
	}
	writeFile(t, filepath.Join(client, "_app", "x.css"), "css")
	// A nested directory that happens to share an excluded name. Only the
	// TOP-LEVEL client/vendor/native/prerendered are the packager's
	// exclusions; a bundle directory named "client" three levels down is
	// ordinary server code and must ship.
	writeFile(t, filepath.Join(out, "server", "client", "nested.js"), "nested")

	var buf bytes.Buffer
	stats, err := WriteTar(&buf, []ports.ClusterSyncDir{
		{LocalDir: client, RemoteDir: ports.AppClientDirPrefix},
		{LocalDir: out, RemoteDir: ports.AppServerDirPrefix, ExcludeDirs: []string{"client", "vendor", "native", "prerendered"}},
	})
	if err != nil {
		t.Fatalf("WriteTar: %v", err)
	}

	entries := readTar(t, buf.Bytes())

	want := map[string]string{
		"app/client/_app/x.css":              "css",
		"app/server/index.js":                "server-entry",
		"app/server/server/chunk.js":         "server-chunk",
		"app/server/server/client/nested.js": "nested",
	}
	for name, body := range want {
		got, ok := entries[name]
		if !ok {
			t.Errorf("entry %q missing from the archive", name)
			continue
		}
		if got != body {
			t.Errorf("entry %q = %q, want %q", name, got, body)
		}
	}

	for _, name := range []string{
		"app/server/client/_app/x.css",
		"app/server/vendor/junk.js",
		"app/server/native/n.node",
		"app/server/prerendered/index.html",
	} {
		if _, ok := entries[name]; ok {
			t.Errorf("entry %q was sent, but its top-level directory is excluded", name)
		}
	}

	if stats.Files != len(want) {
		var got []string
		for k := range entries {
			got = append(got, k)
		}
		sort.Strings(got)
		t.Errorf("Files = %d, want %d; archive contained %v", stats.Files, len(want), got)
	}
	var wantBytes int64
	for _, b := range want {
		wantBytes += int64(len(b))
	}
	if stats.Bytes != wantBytes {
		t.Errorf("Bytes = %d, want %d", stats.Bytes, wantBytes)
	}
	if len(stats.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", stats.Skipped)
	}
}

// TestWriteTar_ReportsSkippedIrregularEntries: a symlink in the build output
// is not sent, and the fact that it was not sent has to be reported rather
// than swallowed — the pod's tree silently diverging from the local one is
// exactly the failure this loop cannot afford.
func TestWriteTar_ReportsSkippedIrregularEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevation on Windows")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "real.js"), "real")
	if err := os.Symlink(filepath.Join(dir, "real.js"), filepath.Join(dir, "link.js")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var buf bytes.Buffer
	stats, err := WriteTar(&buf, []ports.ClusterSyncDir{{LocalDir: dir, RemoteDir: "/app/server"}})
	if err != nil {
		t.Fatalf("WriteTar: %v", err)
	}
	if stats.Files != 1 {
		t.Errorf("Files = %d, want 1 (the symlink must not be counted)", stats.Files)
	}
	if len(stats.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want exactly the symlink", stats.Skipped)
	}
	if filepath.Base(stats.Skipped[0]) != "link.js" {
		t.Errorf("Skipped[0] = %q, want the symlink", stats.Skipped[0])
	}
	if _, ok := readTar(t, buf.Bytes())["app/server/link.js"]; ok {
		t.Error("the symlink was written into the archive")
	}
}

// TestWriteTar_RejectsBadInputs pins the failure cases as errors rather than
// empty archives: a sync that silently sent nothing is the failure mode this
// whole loop is least able to notice.
func TestWriteTar_RejectsBadInputs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notadir")
	writeFile(t, file, "x")

	cases := map[string]ports.ClusterSyncDir{
		"missing local dir":   {LocalDir: filepath.Join(dir, "nope"), RemoteDir: "/app/server"},
		"local dir is a file": {LocalDir: file, RemoteDir: "/app/server"},
		"remote dir is root":  {LocalDir: dir, RemoteDir: "/"},
		"remote dir is empty": {LocalDir: dir, RemoteDir: ""},
		"remote dir is dot":   {LocalDir: dir, RemoteDir: "."},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if _, err := WriteTar(&buf, []ports.ClusterSyncDir{d}); err == nil {
				t.Fatalf("WriteTar accepted %+v", d)
			}
		})
	}
}
