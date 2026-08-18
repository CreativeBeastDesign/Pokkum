//go:build linux

package bunexec

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// HermeticReexecEnvVar carries the real target command (path, args, dir,
// env) as JSON when a hermetic build subprocess is re-invoked as
// `pokkum __hermetic-reexec` inside its own freshly-unshared CLONE_NEWNS
// mount namespace. Exported so cmd/pokkum's hidden reexec subcommand
// (the only other caller of RunHermeticReexec) doesn't need to duplicate
// the env var name.
const HermeticReexecEnvVar = "POKKUM_HERMETIC_REEXEC_TARGET"

// hermeticMaskPaths are the filesystem-path Unix domain sockets masked by
// --hermetic-mount-isolation. Bind-mounting an empty regular file over an
// existing path at each of these makes it unreachable as a socket (a
// connect() against a regular file fails, cleanly and immediately) — this
// is a named-path allowlist, not exhaustive protection against every
// conceivable bind-mounted socket; see Roadmap.md for the honest scope.
// A path that doesn't exist in the build's mount table is silently
// skipped, never created — masking never turns an absent path into a
// present one, only ever makes a present one unreachable.
var hermeticMaskPaths = []string{
	"/var/run/docker.sock",
	"/run/docker.sock",
}

// hermeticMaskPathsTestOverrideEnvVar is a comma-separated test-only
// override for hermeticMaskPaths — see RunHermeticReexec's doc comment at
// its point of use for why this is safe (unreachable by the sandboxed build
// subprocess) and why it exists (the real default paths may be a live
// Docker daemon socket on the test-running machine).
const hermeticMaskPathsTestOverrideEnvVar = "POKKUM_HERMETIC_REEXEC_MASK_PATHS_TEST_OVERRIDE"

// execSelfPath resolves the path of the currently running pokkum binary,
// for retargeting cmd.Path in applyHermeticMountIsolation. A package-level
// function var (not a direct os.Executable() call) purely so tests can
// substitute it: inside `go test`, os.Executable() resolves to the compiled
// TEST binary, not a real pokkum binary that actually has the
// __hermetic-reexec subcommand wired up — tests that need genuine end-to-end
// coverage of the reexec mechanism build a real pokkum binary once and point
// this at it. Production code never overrides this; the default is exactly
// os.Executable(), unchanged in behavior.
var execSelfPath = os.Executable

// hermeticReexecTarget is the JSON payload carried in HermeticReexecEnvVar:
// the real command applyHermeticMountIsolation() would otherwise have run
// directly, captured before retargeting cmd to the reexec subcommand.
type hermeticReexecTarget struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
}

// applyHermeticMountIsolation adds CLONE_NEWNS to cmd's existing Cloneflags
// (cmd must already have gone through applyHermeticSandbox, which set
// CLONE_NEWNET|CLONE_NEWUSER — this function does not set those itself) and
// retargets cmd to re-invoke the current pokkum binary as the hidden
// `__hermetic-reexec` subcommand instead of running the real target
// directly.
//
// Why a reexec is necessary at all (researched and documented the same day
// this was deferred, see Lessons.md's "--hermetic's pathname-Unix-socket
// gap" entry): Go's syscall.SysProcAttr has no hook to run arbitrary code
// (a mount/umount2 call to mask a specific path) between the kernel
// unsharing the new namespaces and execve() replacing the process image —
// only a single Chroot path. An empty CLONE_NEWNS unshare by itself buys
// nothing either: a new mount namespace starts as a COPY of the parent's
// mount table, not an empty one, so docker.sock (if bind-mounted into
// Pokkum's own runtime) is still visible in it until something actively
// masks it. The only way to run that masking code is a separate process
// step — re-invoke Pokkum itself, already unshared into the new
// namespaces, to do the masking, then syscall.Exec into the real target —
// the same pattern runc/Docker use for their own namespace setup.
//
// cmd's Path/Args/Dir/Env at the time this is called are the REAL target
// (e.g. the actual `bun run build` invocation) — captured into a
// hermeticReexecTarget and passed through HermeticReexecEnvVar, since
// nothing else can smuggle that information across the exec() boundary.
// After this returns, cmd.Path/Args point at the reexec subcommand instead;
// cmd.Env is reduced to just the one payload variable (the real env is data
// carried in the payload, not this wrapper process's own environment — it
// is applied at the final syscall.Exec, not inherited).
func applyHermeticMountIsolation(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		return fmt.Errorf("bunexec: internal error: applyHermeticMountIsolation called before applyHermeticSandbox (SysProcAttr is nil)")
	}
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNS

	selfPath, err := execSelfPath()
	if err != nil {
		return fmt.Errorf("bunexec: resolve self executable for hermetic mount isolation reexec: %w", err)
	}

	realPath := cmd.Path
	// exec.Cmd.Args[0] is conventionally the program name/path itself;
	// the real argv to pass through is everything after it.
	var realArgs []string
	if len(cmd.Args) > 1 {
		realArgs = append([]string(nil), cmd.Args[1:]...)
	}

	target := hermeticReexecTarget{
		Path: realPath,
		Args: realArgs,
		Dir:  cmd.Dir,
		Env:  cmd.Env,
	}
	payload, err := json.Marshal(target)
	if err != nil {
		return fmt.Errorf("bunexec: encode hermetic reexec target: %w", err)
	}

	cmd.Path = selfPath
	cmd.Args = []string{selfPath, hermeticReexecSubcommand}
	cmd.Env = []string{HermeticReexecEnvVar + "=" + string(payload)}
	if hermeticMaskPathsTestOverride != "" {
		cmd.Env = append(cmd.Env, hermeticMaskPathsTestOverrideEnvVar+"="+hermeticMaskPathsTestOverride)
	}
	return nil
}

// hermeticMaskPathsTestOverride is applyHermeticMountIsolation's test-only
// hook for propagating hermeticMaskPathsTestOverrideEnvVar into the
// reexec'd process's environment, for tests exercising the full
// Compiler.Prepare/Compile call path where the test has no direct access to
// the constructed *exec.Cmd to append env vars itself (unlike a test that
// calls applyHermeticMountIsolation directly). Empty in production —
// production code never sets this.
var hermeticMaskPathsTestOverride string

// verifyHermeticMountIsolationApplied mirrors verifyHermeticSandboxApplied's
// fail-closed backstop, for CLONE_NEWNS specifically: refuses to let cmd
// start if mount isolation was requested but the flag isn't actually set.
func verifyHermeticMountIsolationApplied(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWNS != syscall.CLONE_NEWNS {
		return fmt.Errorf("bunexec: internal error: hermetic mount isolation was requested but CLONE_NEWNS is not set on the subprocess about to run — refusing to start it unsandboxed")
	}
	return nil
}

// hermeticReexecSubcommand is the hidden cobra subcommand name cmd/pokkum
// registers (see cmd/pokkum/hermetic_reexec.go) — a single shared constant
// so the two sides of this exec() boundary cannot drift apart.
const hermeticReexecSubcommand = "__hermetic-reexec"

// RunHermeticReexec is the hidden `pokkum __hermetic-reexec` subcommand's
// entire implementation. It must be run already inside the fresh
// CLONE_NEWNS/CLONE_NEWNET/CLONE_NEWUSER namespaces
// applyHermeticMountIsolation's Cloneflags produced (i.e. as the direct
// child cmd.Start() launched) — invoking it any other way has no sandbox to
// mask paths inside and will mask nothing.
//
// Never returns on success: the final step is syscall.Exec, which replaces
// this process's image entirely (same PID, so the original cmd.Wait() in
// Prepare/Compile still tracks it correctly) — there is no code after it to
// return from. Only returns (with a non-nil error) if something failed
// before that point.
func RunHermeticReexec() error {
	payload := os.Getenv(HermeticReexecEnvVar)
	if payload == "" {
		return fmt.Errorf("bunexec: %s is not set — %s is an internal implementation detail of --hermetic-mount-isolation, not meant to be invoked directly", HermeticReexecEnvVar, hermeticReexecSubcommand)
	}

	var target hermeticReexecTarget
	if err := json.Unmarshal([]byte(payload), &target); err != nil {
		return fmt.Errorf("bunexec: decode %s: %w", HermeticReexecEnvVar, err)
	}
	if target.Path == "" {
		return fmt.Errorf("bunexec: %s decoded to an empty target path", HermeticReexecEnvVar)
	}

	maskPaths := hermeticMaskPaths
	// Test-only override, deliberately NOT reachable from the sandboxed
	// build subprocess: applyHermeticMountIsolation constructs this
	// process's entire environment from scratch (cmd.Env is replaced
	// outright, never merged with os.Environ()), so nothing the sandboxed
	// dependency does can set this — only pokkum's own Go code
	// (applyHermeticMountIsolation itself, gated on the package-level
	// hermeticMaskPathsTestOverride var tests set) ever adds this variable
	// to that fresh environment. Exists because the real default paths
	// (/var/run/docker.sock) may be a live Docker daemon socket on the
	// machine running the test; overriding lets tests exercise this exact,
	// real, separately-compiled binary's masking logic against a
	// disposable test path instead. Same precedent as
	// bunruntime.Resolver.ReleaseKeyArmored (a production-safe seam for
	// exactly this class of otherwise-untestable path).
	if override := os.Getenv(hermeticMaskPathsTestOverrideEnvVar); override != "" {
		maskPaths = strings.Split(override, ",")
	}
	if err := maskHermeticSensitivePaths(maskPaths); err != nil {
		return fmt.Errorf("bunexec: mask hermetic-sensitive paths: %w", err)
	}

	if target.Dir != "" {
		if err := os.Chdir(target.Dir); err != nil {
			return fmt.Errorf("bunexec: chdir %s: %w", target.Dir, err)
		}
	}

	argv := append([]string{target.Path}, target.Args...)
	if err := syscall.Exec(target.Path, argv, target.Env); err != nil {
		return fmt.Errorf("bunexec: exec %s: %w", target.Path, err)
	}
	return nil // unreachable: syscall.Exec only returns on error
}

// maskHermeticSensitivePaths bind-mounts an empty regular file over every
// path in paths that actually exists in this process's (already-private,
// already-CLONE_NEWNS'd) mount table. A path that does not exist is
// silently skipped — masking never creates a path that wasn't already
// there, only ever makes an existing one unreachable.
func maskHermeticSensitivePaths(paths []string) error {
	for _, path := range paths {
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		if err := maskOnePath(path); err != nil {
			return fmt.Errorf("mask %s: %w", path, err)
		}
	}
	return nil
}

// maskOnePath bind-mounts a freshly-created empty regular file over target,
// so that any subsequent open()/connect() against target's path resolves to
// an empty regular file instead of whatever was there before (typically a
// Unix domain socket) — a connect() against a regular file fails
// immediately and unambiguously, which is exactly "unreachable."
//
// The bind mount does not require its source and target to be the same
// file type — Linux resolves target's path through the new mount entry
// regardless of what type of file originally lived there or what type the
// source is, so a regular-file source safely masks a socket target. The
// source file is removed immediately after a successful mount: this is
// safe (the mount table holds its own reference to the underlying inode,
// independent of the directory entry that created it) and prevents this
// process's temp file from littering the build sandbox after
// syscall.Exec replaces it.
func maskOnePath(target string) error {
	empty, err := os.CreateTemp("", "pokkum-hermetic-mask-*")
	if err != nil {
		return fmt.Errorf("create mask source file: %w", err)
	}
	source := empty.Name()
	if closeErr := empty.Close(); closeErr != nil {
		_ = os.Remove(source)
		return fmt.Errorf("close mask source file: %w", closeErr)
	}
	defer os.Remove(source)

	if err := syscall.Mount(source, target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind-mount %s over %s: %w", source, target, err)
	}
	return nil
}
