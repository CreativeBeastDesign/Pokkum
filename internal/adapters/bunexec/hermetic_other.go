//go:build !linux

package bunexec

import (
	"fmt"
	"os/exec"
)

// hermeticSandboxSupported is false on non-Linux platforms: --hermetic
// remains advisory-only there (BUN_OFFLINE=1, NODE_ENV=production,
// NO_UPDATE_NOTIFIER=1 in Prepare's baseEnv) — a build script that ignores
// those env vars can still make outbound network calls. See Prepare's
// warning log when req.Hermetic is true on a platform where this is false.
const hermeticSandboxSupported = false

// applyHermeticSandbox is a no-op on non-Linux platforms; see
// hermeticSandboxSupported's doc comment for why, and Prepare for the
// warning surfaced to the operator when this path is taken.
func applyHermeticSandbox(_ *exec.Cmd) {}

// verifyHermeticSandboxApplied is a no-op on non-Linux platforms — there is
// nothing to verify, since hermeticSandboxSupported is already false and
// callers only invoke the real Linux verification when it is true.
func verifyHermeticSandboxApplied(_ *exec.Cmd) error { return nil }

// applyHermeticMountIsolation is unreachable on non-Linux platforms: callers
// only invoke it when hermeticSandboxSupported is true, which is always
// false here. Mount namespaces (CLONE_NEWNS) are a Linux-only concept —
// there is no equivalent mechanism to fall back to, unlike Hermetic's own
// advisory-env-var fallback for network isolation.
func applyHermeticMountIsolation(_ *exec.Cmd) error {
	return fmt.Errorf("bunexec: --hermetic-mount-isolation is only supported on Linux")
}

// verifyHermeticMountIsolationApplied is a no-op on non-Linux platforms —
// applyHermeticMountIsolation always errors here, so callers never reach a
// point where they'd call this expecting it to have succeeded.
func verifyHermeticMountIsolationApplied(_ *exec.Cmd) error { return nil }

// RunHermeticReexec is unreachable on non-Linux platforms — the hidden
// `pokkum __hermetic-reexec` subcommand exists in the cobra command tree on
// every platform (a single shared cmd/pokkum registration), but can only
// ever be genuinely invoked as the child of applyHermeticMountIsolation's
// Cloneflags, which is itself Linux-only.
func RunHermeticReexec() error {
	return fmt.Errorf("bunexec: __hermetic-reexec is only meaningful on Linux")
}
