//go:build !linux

package bunexec

import "os/exec"

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
