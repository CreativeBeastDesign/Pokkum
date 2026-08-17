//go:build linux

package bunexec

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// hermeticSandboxSupported is true on Linux: applyHermeticSandbox provides
// real, kernel-enforced network isolation there, not merely an advisory env
// var a compromised build script can ignore.
const hermeticSandboxSupported = true

// applyHermeticSandbox configures cmd to run inside a fresh, unshared Linux
// network namespace (CLONE_NEWNET) containing only a loopback interface that
// is down and has no addresses configured — there is no route out of it, so
// any outbound connection attempt made by cmd or anything it forks (a
// compromised dependency's build-time plugin, a malicious postinstall
// equivalent hook) fails at the kernel level regardless of what
// BUN_OFFLINE/NODE_ENV or any other environment variable claims.
//
// The network namespace is paired with a new user namespace (CLONE_NEWUSER)
// mapping the current uid/gid 1:1 so that creating it does not require
// CAP_SYS_ADMIN or root — this is the standard unprivileged-network-namespace
// pattern, and works wherever the kernel allows unprivileged user namespaces
// (the default on most modern Linux distributions, including GitHub Actions'
// standard runners; some hardened distributions disable it via
// kernel.unprivileged_userns_clone=0, in which case cmd.Start() fails and the
// build fails closed with that error rather than silently running
// unisolated — see Prepare's error handling).
//
// cmd.SysProcAttr may already carry Setpgid (see setNewProcessGroup, called
// before this) — Cloneflags/UidMappings/GidMappings are additive fields on
// the same struct and do not conflict with it.
func applyHermeticSandbox(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNET | syscall.CLONE_NEWUSER
	cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
		{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1},
	}
	cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
		{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1},
	}
}

// verifyHermeticSandboxApplied returns an error if cmd is about to run a
// hermetic build without the network-isolating Cloneflags actually set —
// the last-resort backstop against a future call-order mistake (e.g. some
// other helper replacing cmd.SysProcAttr wholesale after applyHermeticSandbox
// ran, silently downgrading an "active" sandbox to no sandbox at all while
// the caller's own log line still claims enforcement). Called immediately
// before cmd.Run() in both Prepare and Compile.
func verifyHermeticSandboxApplied(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Cloneflags&(syscall.CLONE_NEWNET|syscall.CLONE_NEWUSER) != syscall.CLONE_NEWNET|syscall.CLONE_NEWUSER {
		return fmt.Errorf("bunexec: internal error: hermetic sandbox was requested but CLONE_NEWNET|CLONE_NEWUSER are not set on the subprocess about to run — refusing to start it unsandboxed")
	}
	return nil
}
