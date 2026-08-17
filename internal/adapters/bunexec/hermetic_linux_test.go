//go:build linux

package bunexec

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestApplyHermeticSandbox_SetsCloneFlags is a pure struct-level check: no
// process is started, so it needs no environment capability and never skips.
// It also proves applyHermeticSandbox composes with setNewProcessGroup
// (Prepare calls both, in that order, on the same *exec.Cmd) rather than
// clobbering its SysProcAttr.
func TestApplyHermeticSandbox_SetsCloneFlags(t *testing.T) {
	cmd := exec.Command("true")
	setNewProcessGroup(cmd)
	if !cmd.SysProcAttr.Setpgid {
		t.Fatalf("test premise broken: setNewProcessGroup did not set Setpgid")
	}

	applyHermeticSandbox(cmd)

	if !cmd.SysProcAttr.Setpgid {
		t.Errorf("applyHermeticSandbox must not clear Setpgid set by setNewProcessGroup")
	}
	if cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWNET == 0 {
		t.Errorf("expected CLONE_NEWNET set, got Cloneflags=%#x", cmd.SysProcAttr.Cloneflags)
	}
	if cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWUSER == 0 {
		t.Errorf("expected CLONE_NEWUSER set, got Cloneflags=%#x", cmd.SysProcAttr.Cloneflags)
	}
	wantUID, wantGID := os.Getuid(), os.Getgid()
	if len(cmd.SysProcAttr.UidMappings) != 1 || cmd.SysProcAttr.UidMappings[0].HostID != wantUID || cmd.SysProcAttr.UidMappings[0].ContainerID != wantUID || cmd.SysProcAttr.UidMappings[0].Size != 1 {
		t.Errorf("unexpected UidMappings: %+v (want a single 1:1 mapping for uid %d)", cmd.SysProcAttr.UidMappings, wantUID)
	}
	if len(cmd.SysProcAttr.GidMappings) != 1 || cmd.SysProcAttr.GidMappings[0].HostID != wantGID || cmd.SysProcAttr.GidMappings[0].ContainerID != wantGID || cmd.SysProcAttr.GidMappings[0].Size != 1 {
		t.Errorf("unexpected GidMappings: %+v (want a single 1:1 mapping for gid %d)", cmd.SysProcAttr.GidMappings, wantGID)
	}
}

// TestApplyHermeticSandbox_IsolatesLoopback empirically proves the real
// kernel enforcement, not just that the right struct fields are set (see
// mem:self_review_checklist row 17: packaged/configured behavior must
// actually execute, not just look right). It starts a real TCP listener on
// the host's loopback interface, confirms a normal (non-sandboxed) child can
// reach it as a control, then confirms a child with applyHermeticSandbox
// cannot — deterministic and self-contained (no dependency on external
// network reachability or CI firewall policy, unlike probing a real internet
// host), since a fresh, unshared network namespace's loopback device has no
// route to the host's namespace at all.
//
// Skips (does not fail) when the environment can't support what's being
// tested — no bash, or unprivileged user namespaces disabled
// (kernel.unprivileged_userns_clone=0, some hardened distributions) — since
// that is an environment capability gap, not a defect in this code; Prepare
// itself surfaces a clear, actionable error in that case (see compiler.go).
func TestApplyHermeticSandbox_IsolatesLoopback(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; this probe relies on bash's /dev/tcp pseudo-device")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start loopback listener: %v", err)
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
	port := ln.Addr().(*net.TCPAddr).Port

	probe := fmt.Sprintf(`timeout 2 bash -c 'exec 3<>/dev/tcp/127.0.0.1/%d' 2>/dev/null && echo CONNECTED || echo BLOCKED`, port)

	control := exec.Command("bash", "-c", probe)
	controlOut, err := control.CombinedOutput()
	if err != nil {
		t.Skipf("control probe failed to run: %v, output: %s", err, controlOut)
	}
	if got := strings.TrimSpace(string(controlOut)); got != "CONNECTED" {
		t.Skipf("control (unsandboxed) probe could not reach its own loopback listener (got %q) — test environment issue, not a sandbox defect", got)
	}

	sandboxed := exec.Command("bash", "-c", probe)
	applyHermeticSandbox(sandboxed)
	sandboxedOut, err := sandboxed.CombinedOutput()
	if err != nil {
		t.Skipf("sandboxed process failed to start (unprivileged user namespaces unavailable in this environment?): %v, output: %s", err, sandboxedOut)
	}
	if got := strings.TrimSpace(string(sandboxedOut)); got != "BLOCKED" {
		t.Fatalf("expected the hermetic sandbox to isolate the child from the host's loopback listener, got %q", got)
	}
}

// TestPrepare_HermeticModeBlocksRealNetworkEgress is the end-to-end version
// of TestApplyHermeticSandbox_IsolatesLoopback: rather than calling
// applyHermeticSandbox directly, it drives Compiler.Prepare exactly as
// core.Build does (req.Hermetic: true) with a fake "bun" on PATH that
// attempts a real network connection and reports what it saw — proving
// hermetic enforcement actually reaches the real `bun run build` subprocess
// through Prepare's normal call path (mem:self_review_checklist row 17: the
// wiring must actually execute, not just the isolated helper function).
func TestPrepare_HermeticModeBlocksRealNetworkEgress(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; this probe relies on bash's /dev/tcp pseudo-device")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start loopback listener: %v", err)
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
	port := ln.Addr().(*net.TCPAddr).Port

	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	fakeBun := fmt.Sprintf(`
if bash -c 'timeout 2 bash -c "exec 3<>/dev/tcp/127.0.0.1/%d"' 2>/dev/null; then
	echo "NETWORK_REACHABLE" 1>&2
else
	echo "NETWORK_BLOCKED" 1>&2
fi
exit 1
`, port)
	putFakeBunOnPath(t, fakeBun)
	c := NewCompiler(discardLogger())

	// StrategyExe matches validSvelteConfig's configured adapter, so Prepare
	// reaches the bun invocation this test is about (same premise as
	// TestPrepare_BunExitNonZero).
	_, err = c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir, Strategy: ports.StrategyExe, SourceDateEpoch: time.Unix(0, 0), Hermetic: true,
	})
	if err == nil {
		t.Fatal("expected Prepare to fail — the fake bun script always exits 1")
	}
	if strings.Contains(err.Error(), "NETWORK_REACHABLE") {
		t.Fatalf("hermetic mode failed to block real network egress from bun run build's subprocess: %v", err)
	}
	if strings.Contains(err.Error(), "failed to start inside the hermetic network sandbox") {
		t.Skipf("hermetic sandbox unavailable in this environment (unprivileged user namespaces disabled?): %v", err)
	}
	if !strings.Contains(err.Error(), "NETWORK_BLOCKED") {
		t.Fatalf("expected the error to carry the fake bun script's network probe result, got: %v", err)
	}
}

// TestCompile_HermeticModeBlocksRealNetworkEgress is TestPrepare_HermeticModeBlocksRealNetworkEgress's
// counterpart for Compile — the stage a real security review (this session)
// found was NOT sandboxed at all in the first cut of this feature: `bun
// build --compile` bundles the entrypoint Prepare's third-party adapter
// generated and executes bunfig.toml preload plugins / `with { type: "macro" }`
// imports at bundle time, so it needed the same enforcement, not just
// Prepare's `bun run build`. This proves Compile's real call path blocks
// network egress too, closing that gap rather than trusting the fix by
// inspection alone.
func TestCompile_HermeticModeBlocksRealNetworkEgress(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; this probe relies on bash's /dev/tcp pseudo-device")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start loopback listener: %v", err)
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
	port := ln.Addr().(*net.TCPAddr).Port

	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	fakeBun := fmt.Sprintf(`
if bash -c 'timeout 2 bash -c "exec 3<>/dev/tcp/127.0.0.1/%d"' 2>/dev/null; then
	echo "NETWORK_REACHABLE" 1>&2
else
	echo "NETWORK_BLOCKED" 1>&2
fi
exit 1
`, port)
	putFakeBunOnPath(t, fakeBun)
	c := NewCompiler(discardLogger())

	_, err = c.Compile(context.Background(), ports.CompileRequest{
		ProjectDir:      dir,
		EntrypointPath:  filepath.Join(dir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts"),
		Platform:        ports.LinuxAMD64,
		OutputPath:      filepath.Join(t.TempDir(), "app-linux-amd64"),
		SourceDateEpoch: time.Unix(0, 0),
		Hermetic:        true,
	})
	if err == nil {
		t.Fatal("expected Compile to fail — the fake bun script always exits 1")
	}
	if strings.Contains(err.Error(), "NETWORK_REACHABLE") {
		t.Fatalf("hermetic mode failed to block real network egress from bun build --compile's subprocess: %v", err)
	}
	if strings.Contains(err.Error(), "failed to start inside the hermetic network sandbox") {
		t.Skipf("hermetic sandbox unavailable in this environment (unprivileged user namespaces disabled?): %v", err)
	}
	if !strings.Contains(err.Error(), "NETWORK_BLOCKED") {
		t.Fatalf("expected the error to carry the fake bun script's network probe result, got: %v", err)
	}
}
