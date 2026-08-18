//go:build linux

package bunexec

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestApplyHermeticMountIsolation_RetargetsCmd is a pure struct-level check
// (no process started): proves applyHermeticMountIsolation captures the
// REAL target (path/args/dir/env) into the JSON payload before retargeting
// cmd, and that CLONE_NEWNS is added on top of applyHermeticSandbox's
// existing CLONE_NEWNET|CLONE_NEWUSER rather than replacing it.
func TestApplyHermeticMountIsolation_RetargetsCmd(t *testing.T) {
	cmd := exec.Command("/usr/bin/real-bun", "run", "build")
	cmd.Dir = "/real/project/dir"
	cmd.Env = []string{"REAL_VAR=1", "PATH=/usr/bin"}
	applyHermeticSandbox(cmd)

	if err := applyHermeticMountIsolation(cmd); err != nil {
		t.Fatalf("applyHermeticMountIsolation: %v", err)
	}

	if cmd.SysProcAttr.Cloneflags&(syscall.CLONE_NEWNET|syscall.CLONE_NEWUSER) != syscall.CLONE_NEWNET|syscall.CLONE_NEWUSER {
		t.Errorf("expected CLONE_NEWNET|CLONE_NEWUSER to survive from applyHermeticSandbox, got Cloneflags=%#x", cmd.SysProcAttr.Cloneflags)
	}
	if cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWNS == 0 {
		t.Errorf("expected CLONE_NEWNS to be added, got Cloneflags=%#x", cmd.SysProcAttr.Cloneflags)
	}

	wantSelf, err := execSelfPath()
	if err != nil {
		t.Fatalf("execSelfPath: %v", err)
	}
	if cmd.Path != wantSelf {
		t.Errorf("cmd.Path = %q, want %q (retargeted to self)", cmd.Path, wantSelf)
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != wantSelf || cmd.Args[1] != hermeticReexecSubcommand {
		t.Errorf("cmd.Args = %q, want [%q %q]", cmd.Args, wantSelf, hermeticReexecSubcommand)
	}
	if len(cmd.Env) != 1 || !strings.HasPrefix(cmd.Env[0], HermeticReexecEnvVar+"=") {
		t.Fatalf("cmd.Env = %q, want exactly one %s=... entry", cmd.Env, HermeticReexecEnvVar)
	}

	payload := strings.TrimPrefix(cmd.Env[0], HermeticReexecEnvVar+"=")
	var target hermeticReexecTarget
	if err := json.Unmarshal([]byte(payload), &target); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if target.Path != "/usr/bin/real-bun" {
		t.Errorf("target.Path = %q, want %q", target.Path, "/usr/bin/real-bun")
	}
	if got, want := target.Args, []string{"run", "build"}; !stringSlicesEqual(got, want) {
		t.Errorf("target.Args = %q, want %q", got, want)
	}
	if target.Dir != "/real/project/dir" {
		t.Errorf("target.Dir = %q, want %q", target.Dir, "/real/project/dir")
	}
	if got, want := target.Env, []string{"REAL_VAR=1", "PATH=/usr/bin"}; !stringSlicesEqual(got, want) {
		t.Errorf("target.Env = %q, want %q", got, want)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// realPokkumBinary builds the actual pokkum CLI once per test process (not
// per test — tests share it via sync.Once) and returns its path. Needed
// because inside `go test`, os.Executable() resolves to the compiled TEST
// binary, which has no __hermetic-reexec subcommand wired up — only a real
// pokkum binary genuinely exercises the mechanism production actually
// ships. Skips (not fails) if `go build` cannot run here.
var (
	realPokkumBinaryOnce sync.Once
	realPokkumBinaryPath string
	realPokkumBinaryErr  error
)

func realPokkumBinary(t *testing.T) string {
	t.Helper()
	realPokkumBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pokkum-hermetic-reexec-test-")
		if err != nil {
			realPokkumBinaryErr = err
			return
		}
		binPath := filepath.Join(dir, "pokkum")
		buildCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		buildCmd := exec.CommandContext(buildCtx, "go", "build", "-o", binPath, "github.com/CreativeBeastDesign/pokkum/cmd/pokkum")
		if out, err := buildCmd.CombinedOutput(); err != nil {
			realPokkumBinaryErr = fmt.Errorf("go build ./cmd/pokkum: %w\n%s", err, out)
			return
		}
		realPokkumBinaryPath = binPath
	})
	if realPokkumBinaryErr != nil {
		t.Skipf("could not build a real pokkum binary for end-to-end hermetic-mount-isolation testing: %v", realPokkumBinaryErr)
	}
	return realPokkumBinaryPath
}

// TestHermeticMountIsolation_BlocksPathSocketAccess is the core empirical
// proof (mem:self_review_checklist row 17 and row 18): a real Unix domain
// socket listener at a real filesystem path, reachable normally and even
// under applyHermeticSandbox's existing network-namespace isolation alone
// (proving THIS IS the real, previously-open gap — CLONE_NEWNET does
// nothing for path-based sockets), then genuinely unreachable once
// applyHermeticMountIsolation's reexec+mask mechanism runs — using the REAL
// compiled pokkum binary's __hermetic-reexec subcommand, not a stand-in.
//
// hermeticMaskPaths is temporarily overridden to the test's own socket path
// rather than the real /var/run/docker.sock — this test does not require
// root or a pre-existing docker.sock, and proves the identical mechanism
// (maskHermeticSensitivePaths iterates whatever's in the list).
func TestHermeticMountIsolation_BlocksPathSocketAccess(t *testing.T) {
	pokkumBin := realPokkumBinary(t)

	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	probeBin := buildUnixSocketProbe(t)

	t.Run("control_unsandboxed_connects", func(t *testing.T) {
		out, err := exec.Command(probeBin, sockPath).CombinedOutput()
		if err != nil {
			t.Fatalf("probe failed to run: %v\n%s", err, out)
		}
		if got := strings.TrimSpace(string(out)); got != "CONNECTED" {
			t.Fatalf("test premise broken: unsandboxed probe could not reach its own listener, got %q", got)
		}
	})

	t.Run("netns_only_still_connects", func(t *testing.T) {
		// This is the gap this whole feature exists to close, proven
		// directly: applyHermeticSandbox's existing CLONE_NEWNET does
		// nothing for a filesystem-path Unix socket.
		cmd := exec.Command(probeBin, sockPath)
		applyHermeticSandbox(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("sandboxed probe failed to start (unprivileged user namespaces unavailable here?): %v\n%s", err, out)
		}
		if got := strings.TrimSpace(string(out)); got != "CONNECTED" {
			t.Fatalf("expected CLONE_NEWNET alone to NOT block a path-based Unix socket, got %q — if this genuinely changed, the premise behind --hermetic-mount-isolation needs re-examining", got)
		}
	})

	t.Run("mount_isolation_blocks", func(t *testing.T) {
		origSelf := execSelfPath
		execSelfPath = func() (string, error) { return pokkumBin, nil }
		t.Cleanup(func() { execSelfPath = origSelf })

		origMaskOverride := hermeticMaskPathsTestOverride
		hermeticMaskPathsTestOverride = sockPath
		t.Cleanup(func() { hermeticMaskPathsTestOverride = origMaskOverride })

		cmd := exec.Command(probeBin, sockPath)
		applyHermeticSandbox(cmd)
		if err := applyHermeticMountIsolation(cmd); err != nil {
			t.Fatalf("applyHermeticMountIsolation: %v", err)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("mount-isolated probe failed to start (unprivileged user namespaces or CLONE_NEWNS unavailable here?): %v\n%s", err, out)
		}
		if got := strings.TrimSpace(string(out)); got != "BLOCKED" {
			t.Fatalf("expected --hermetic-mount-isolation to block the path-based Unix socket, got %q\nfull output:\n%s", got, out)
		}
	})
}

// buildUnixSocketProbe compiles a tiny standalone Go program that dials a
// Unix domain socket path (given as argv[1]) and prints CONNECTED or
// BLOCKED. A real subprocess is required (not an in-process net.Dial) since
// this test is specifically about PROCESS-level filesystem/namespace
// isolation — the probe must run inside the sandboxed child, not this test
// binary itself.
func buildUnixSocketProbe(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "probe.go")
	code := `package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	conn, err := net.DialTimeout("unix", os.Args[1], 2*time.Second)
	if err != nil {
		fmt.Println("BLOCKED")
		return
	}
	conn.Close()
	fmt.Println("CONNECTED")
}
`
	if err := os.WriteFile(src, []byte(code), 0o644); err != nil {
		t.Fatalf("write probe source: %v", err)
	}
	binPath := filepath.Join(dir, "probe")
	buildCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	buildCmd := exec.CommandContext(buildCtx, "go", "build", "-o", binPath, src)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build unix socket probe: %v\n%s", err, out)
	}
	return binPath
}

// TestPrepare_HermeticMountIsolation_EndToEnd proves the wiring through
// Compiler.Prepare's real call path (not just applyHermeticMountIsolation
// in isolation) actually applies mount isolation — mem:self_review_checklist
// row 13 (a fix's caller chain, not just the fixed function, needs its own
// coverage). A fake "bun" script probes the same test socket directly.
func TestPrepare_HermeticMountIsolation_EndToEnd(t *testing.T) {
	pokkumBin := realPokkumBinary(t)
	origSelf := execSelfPath
	execSelfPath = func() (string, error) { return pokkumBin, nil }
	t.Cleanup(func() { execSelfPath = origSelf })

	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen on %s: %v", sockPath, err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	origMaskOverride := hermeticMaskPathsTestOverride
	hermeticMaskPathsTestOverride = sockPath
	t.Cleanup(func() { hermeticMaskPathsTestOverride = origMaskOverride })

	probeBin := buildUnixSocketProbe(t)

	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	fakeBun := fmt.Sprintf(`
result=$("%s" "%s")
echo "SOCKET_${result}" 1>&2
exit 1
`, probeBin, sockPath)
	putFakeBunOnPath(t, fakeBun)
	c := NewCompiler(discardLogger())

	_, err = c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir, Strategy: ports.StrategyExe, SourceDateEpoch: time.Unix(0, 0),
		Hermetic: true, HermeticMountIsolation: true,
	})
	if err == nil {
		t.Fatal("expected Prepare to fail — the fake bun script always exits 1")
	}
	if strings.Contains(err.Error(), "SOCKET_CONNECTED") {
		t.Fatalf("--hermetic-mount-isolation failed to block the test socket from a real Prepare call: %v", err)
	}
	if strings.Contains(err.Error(), "failed to start inside the hermetic network sandbox") {
		t.Skipf("hermetic sandbox unavailable in this environment: %v", err)
	}
	if !strings.Contains(err.Error(), "SOCKET_BLOCKED") {
		t.Fatalf("expected the error to carry the fake bun script's socket probe result, got: %v", err)
	}
}
