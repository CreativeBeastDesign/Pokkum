package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// tarEntry is one entry to put into a synthetic sync archive.
type tarEntry struct {
	name string
	dir  bool
	body string
}

func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Format: tar.FormatPAX}
		if e.dir {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Mode = 0o644
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if !e.dir {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatalf("write body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

// recordingSignaller stands in for syscall.Kill so a restart can be asserted
// without an actual PID 1 anywhere.
type recordingSignaller struct {
	calls []struct {
		pid int
		sig syscall.Signal
	}
	err error
}

func (r *recordingSignaller) send(pid int, sig syscall.Signal) error {
	r.calls = append(r.calls, struct {
		pid int
		sig syscall.Signal
	}{pid, sig})
	return r.err
}

func devModeEnv(on bool) func(string) string {
	return func(k string) string {
		if k == envDevMode && on {
			return "1"
		}
		return ""
	}
}

// TestRunDevSync_ExtractsIntoRootsAndRestarts is the happy path end to end:
// a real tar over a real (0555, image-shaped) destination tree, a real
// extraction, and a recorded restart signal.
func TestRunDevSync_ExtractsIntoRootsAndRestarts(t *testing.T) {
	base := t.TempDir()
	server := filepath.Join(base, "app", "server")
	client := filepath.Join(base, "app", "client")
	for _, d := range []string{server, client} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// The packager ships /app directories with NO write bit for anyone,
	// owner included. Reproducing that here is the whole point: an
	// extractor tested against a 0755 tree would pass while being unable to
	// write a single byte into a real Pokkum image.
	stale := filepath.Join(server, "index.js")
	if err := os.WriteFile(stale, []byte("OLD"), 0o555); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}
	for _, d := range []string{server, client} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatalf("chmod %s: %v", d, err)
		}
	}

	payload := buildTar(t, []tarEntry{
		{name: strings.TrimPrefix(server, "/") + "/index.js", body: "NEW SERVER"},
		{name: strings.TrimPrefix(server, "/") + "/chunks", dir: true},
		{name: strings.TrimPrefix(server, "/") + "/chunks/a.js", body: "chunk"},
		{name: strings.TrimPrefix(client, "/") + "/_app/immutable/x.css", body: "css"},
	})

	sig := &recordingSignaller{}
	var stdout, stderr bytes.Buffer
	err := runDevSync(
		[]string{"--root", server, "--root", client, "--restart", "--signal-pid", "4242"},
		devModeEnv(true), bytes.NewReader(payload), &stdout, &stderr, sig.send)
	if err != nil {
		t.Fatalf("runDevSync: %v (stderr: %s)", err, stderr.String())
	}

	var summary devSyncSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("parse summary %q: %v", stdout.String(), err)
	}
	if summary.Files != 3 {
		t.Errorf("files = %d, want 3", summary.Files)
	}
	wantBytes := int64(len("NEW SERVER") + len("chunk") + len("css"))
	if summary.Bytes != wantBytes {
		t.Errorf("bytes = %d, want %d", summary.Bytes, wantBytes)
	}
	if !summary.Restarted {
		t.Error("summary reports no restart, but --restart was given and the signal succeeded")
	}

	for path, want := range map[string]string{
		filepath.Join(server, "index.js"):                   "NEW SERVER",
		filepath.Join(server, "chunks", "a.js"):             "chunk",
		filepath.Join(client, "_app", "immutable", "x.css"): "css",
	} {
		got, err := os.ReadFile(path) //nolint:gosec // test-controlled path
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}

	if len(sig.calls) != 1 || sig.calls[0].pid != 4242 || sig.calls[0].sig != syscall.SIGHUP {
		t.Errorf("signal calls = %+v, want one SIGHUP to pid 4242", sig.calls)
	}
}

// TestRunDevSync_RefusesWithoutDevMode is the security gate: the subcommand
// must be inert on any container whose operator did not opt in, and must
// refuse BEFORE reading stdin so a rejected sync leaves nothing half-written.
func TestRunDevSync_RefusesWithoutDevMode(t *testing.T) {
	root := t.TempDir()
	payload := buildTar(t, []tarEntry{{name: strings.TrimPrefix(root, "/") + "/x.js", body: "hi"}})

	sig := &recordingSignaller{}
	var stdout, stderr bytes.Buffer
	err := runDevSync([]string{"--root", root, "--restart"}, devModeEnv(false), bytes.NewReader(payload), &stdout, &stderr, sig.send)
	if !errors.Is(err, errDevModeDisabled) {
		t.Fatalf("err = %v, want errDevModeDisabled", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "x.js")); statErr == nil {
		t.Error("a file was written despite dev mode being off")
	}
	if len(sig.calls) != 0 {
		t.Errorf("signalled the supervisor despite refusing: %+v", sig.calls)
	}
	if stdout.Len() != 0 {
		t.Errorf("printed a summary for a refused sync: %q", stdout.String())
	}
}

// TestRunDevSync_RejectsMissingRoot covers syncing into an image that is not
// layered: an exe or static image has no /app/server, and materialising one
// would produce a pod that looks synced and serves nothing.
func TestRunDevSync_RejectsMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "app", "server")
	var stdout, stderr bytes.Buffer
	err := runDevSync([]string{"--root", missing}, devModeEnv(true), bytes.NewReader(nil), &stdout, &stderr, (&recordingSignaller{}).send)
	if err == nil {
		t.Fatal("expected an error for a --root that does not exist")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error should name the missing root, got: %v", err)
	}
}

// TestRunDevSync_RejectsRelativeAndFilesystemRoot pins the two --root values
// that would make containment meaningless.
func TestRunDevSync_RejectsRelativeAndFilesystemRoot(t *testing.T) {
	for _, root := range []string{"app/server", "/"} {
		var stdout, stderr bytes.Buffer
		err := runDevSync([]string{"--root", root}, devModeEnv(true), bytes.NewReader(nil), &stdout, &stderr, (&recordingSignaller{}).send)
		if err == nil {
			t.Errorf("--root %q was accepted", root)
		}
	}
}

// TestExtractTar_RefusesEntriesOutsideRoots is the containment guard. Each
// case is an archive an ordinary prefix check would wave through.
func TestExtractTar_RefusesEntriesOutsideRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "app", "server")
	sibling := filepath.Join(base, "app", "serverevil")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	rootName := strings.TrimPrefix(root, "/")

	cases := []struct {
		name  string
		entry string
	}{
		{"parent traversal", rootName + "/../serverevil/pwned.js"},
		{"absolute escape", strings.TrimPrefix(base, "/") + "/etc/pwned"},
		{"sibling near-miss", strings.TrimPrefix(sibling, "/") + "/pwned.js"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := buildTar(t, []tarEntry{{name: tc.entry, body: "pwned"}})
			summary, err := extractTar(bytes.NewReader(payload), []string{root})
			if err == nil {
				t.Fatalf("entry %q was accepted (summary %+v)", tc.entry, summary)
			}
			if !strings.Contains(err.Error(), "outside every --root") {
				t.Errorf("error should name the containment failure, got: %v", err)
			}
		})
	}

	// Nothing at all may have been written by any of the refused archives.
	entries, err := os.ReadDir(sibling)
	if err != nil {
		t.Fatalf("read sibling dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("refused entries still wrote %d file(s) into %s", len(entries), sibling)
	}
}

// TestExtractTar_RefusesSymlinkEscape is the case a lexical containment check
// cannot see: a symlink already inside the root, pointing out of it, is the
// shape a previous sync could have left behind.
func TestExtractTar_RefusesSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "app", "server")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	payload := buildTar(t, []tarEntry{{name: strings.TrimPrefix(root, "/") + "/escape/pwned.js", body: "pwned"}})
	if _, err := extractTar(bytes.NewReader(payload), []string{root}); err == nil {
		t.Fatal("an entry traversing a symlink out of the root was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.js")); err == nil {
		t.Error("the entry was written outside the root through a symlink")
	}
}

// TestExtractTar_RefusesUnsupportedEntryTypes proves a hard link, symlink or
// device entry in the stream is an error rather than a silent skip: a skipped
// entry is a file the pod is missing with nothing saying so.
func TestExtractTar_RefusesUnsupportedEntryTypes(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     strings.TrimPrefix(root, "/") + "/link",
		Linkname: "/etc/passwd",
		Format:   tar.FormatPAX,
	}); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	if _, err := extractTar(bytes.NewReader(buf.Bytes()), []string{root}); err == nil {
		t.Fatal("a symlink entry was accepted")
	}
}

// TestRunDevSync_RestartFailureIsNotReportedAsSuccess: a sync whose restart
// signal failed must fail the whole call, because a pod with new code on disk
// and an old process still serving it is the most confusing possible outcome.
func TestRunDevSync_RestartFailureIsNotReportedAsSuccess(t *testing.T) {
	root := t.TempDir()
	payload := buildTar(t, []tarEntry{{name: strings.TrimPrefix(root, "/") + "/index.js", body: "x"}})

	sig := &recordingSignaller{err: syscall.EPERM}
	var stdout, stderr bytes.Buffer
	err := runDevSync([]string{"--root", root, "--restart"}, devModeEnv(true), bytes.NewReader(payload), &stdout, &stderr, sig.send)
	if err == nil {
		t.Fatal("expected an error when the restart signal fails")
	}
	if stdout.Len() != 0 {
		t.Errorf("printed a success summary despite the restart failing: %q", stdout.String())
	}
}

// TestRunDevSync_NoRestartLeavesRestartedFalse pins the other half of the
// contract the sending side checks: Restarted is never true unless a restart
// was asked for and actually happened.
func TestRunDevSync_NoRestartLeavesRestartedFalse(t *testing.T) {
	root := t.TempDir()
	payload := buildTar(t, []tarEntry{{name: strings.TrimPrefix(root, "/") + "/index.js", body: "x"}})

	sig := &recordingSignaller{}
	var stdout, stderr bytes.Buffer
	if err := runDevSync([]string{"--root", root}, devModeEnv(true), bytes.NewReader(payload), &stdout, &stderr, sig.send); err != nil {
		t.Fatalf("runDevSync: %v", err)
	}
	var summary devSyncSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if summary.Restarted {
		t.Error("Restarted is true without --restart")
	}
	if len(sig.calls) != 0 {
		t.Errorf("signalled without --restart: %+v", sig.calls)
	}
}

// TestParseBoolEnv_MatchesClusterdevTruthy pins the spelling contract shared
// with internal/adapters/clusterdev's truthy: an operator who writes
// POKKUM_DEV_MODE=true must not get "on" from one half of the system and
// "off" from the other.
func TestParseBoolEnv_MatchesClusterdevTruthy(t *testing.T) {
	cases := map[string]bool{
		"1": true, "t": true, "T": true, "true": true, "TRUE": true, "True": true,
		"0": false, "false": false, "": false, "yes": false, "on": false, "  1  ": true,
	}
	for in, want := range cases {
		if got := parseBoolEnv(in); got != want {
			t.Errorf("parseBoolEnv(%q) = %v, want %v", in, got, want)
		}
	}
}
