//go:build linux

package main

import "syscall"

// childSysProcAttr describes how the supervised child is created on the only
// platform that actually runs it.
//
// Setpgid makes the child a process group leader, so its pgid equals its pid
// and one kill(2) reaches the child and every descendant that has not
// deliberately left the group. Without it the child would share the
// supervisor's group and every relayed signal would come straight back to us.
//
// Pdeathsig closes the other half of the problem: if the supervisor dies
// abruptly (SIGKILL from an OOM kill, a bug, a runtime that decides PID 1 is
// unresponsive) the kernel sends SIGKILL to the child instead of leaving a
// SvelteKit server running in a container nobody is watching. It is a Linux
// facility with no portable equivalent, which is why this file is split.
//
// Pdeathsig has one sharp edge: the kernel fires it when the *thread* that
// called clone(2) exits, not the process. Run therefore pins itself to its OS
// thread for the whole life of the child; see supervisor.go.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

// signalGroup sends sig to every member of the process group led by pgid.
// The negative pid is what makes kill(2) address a group rather than a process.
func signalGroup(pgid int, sig syscall.Signal) error {
	return syscall.Kill(-pgid, sig)
}

// reapOne performs one non-blocking wait for any exited child. It returns
// (0, nil) when children exist but none have exited, and syscall.ECHILD when
// there are no children left at all.
func reapOne(ws *syscall.WaitStatus) (int, error) {
	return syscall.Wait4(-1, ws, syscall.WNOHANG, nil)
}
