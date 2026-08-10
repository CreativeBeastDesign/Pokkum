//go:build !linux

package main

import "syscall"

// This file exists so the package builds, tests and vets on the macOS
// development host. pokkum-init only ever ships for Linux; everything here is
// the portable subset of proc_linux.go with the Linux-only parts removed.
//
// It is tagged !linux rather than darwin because the useful boundary is "the
// platform we ship to" versus "a developer's machine". Wait4 and process groups
// exist on every Unix; Windows and plan9 have neither, and a supervisor for
// them would be a different program, not a build tag.

// childSysProcAttr matches proc_linux.go apart from Pdeathsig, which has no
// equivalent outside Linux. A supervisor killed with SIGKILL on macOS therefore
// orphans its child, which is acceptable: the production behaviour is the Linux
// one, and tests that care assert on the group, not on Pdeathsig.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// signalGroup sends sig to every member of the process group led by pgid.
func signalGroup(pgid int, sig syscall.Signal) error {
	return syscall.Kill(-pgid, sig)
}

// reapOne performs one non-blocking wait for any exited child. It returns
// (0, nil) when children exist but none have exited, and syscall.ECHILD when
// there are no children left at all.
func reapOne(ws *syscall.WaitStatus) (int, error) {
	return syscall.Wait4(-1, ws, syscall.WNOHANG, nil)
}
