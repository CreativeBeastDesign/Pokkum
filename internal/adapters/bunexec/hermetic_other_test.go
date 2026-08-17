//go:build !linux

package bunexec

import (
	"os/exec"
	"testing"
)

func TestApplyHermeticSandbox_NoopOnNonLinux(t *testing.T) {
	if hermeticSandboxSupported {
		t.Fatalf("hermeticSandboxSupported must be false on non-Linux platforms")
	}

	cmd := exec.Command("true")
	before := cmd.SysProcAttr
	applyHermeticSandbox(cmd)
	if cmd.SysProcAttr != before {
		t.Errorf("applyHermeticSandbox must be a no-op on non-Linux platforms, SysProcAttr changed from %+v to %+v", before, cmd.SysProcAttr)
	}
}
