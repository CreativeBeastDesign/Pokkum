package bunexec

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// buildEnv merges the inherited process environment with extra KEY=VALUE
// entries, extra winning on collision. It never mutates os.Environ() or
// extra; it returns a freshly built, sorted slice so that the resulting
// subprocess environment (and therefore anything that hashes or logs it) is
// deterministic across runs with the same inputs.
func buildEnv(extra []string) []string {
	m := make(map[string]string, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return sortedEnvSlice(m)
}

// buildEnvWithEpoch is buildEnv plus SOURCE_DATE_EPOCH set from
// sourceDateEpoch, always taking precedence over any same-named entry in
// extra. Prepare and Compile use this; Preflight (which has no
// SourceDateEpoch to export) uses buildEnv directly.
func buildEnvWithEpoch(extra []string, sourceDateEpoch time.Time) []string {
	m := make(map[string]string, len(os.Environ())+len(extra)+1)
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	m["SOURCE_DATE_EPOCH"] = strconv.FormatInt(sourceDateEpoch.Unix(), 10)
	return sortedEnvSlice(m)
}

func sortedEnvSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// hermeticStrippedEnvVars names environment variables that point at a local
// IPC channel a hermetic build's subprocess has no legitimate reason to use,
// stripped from its environment when req.Hermetic is true. This is a
// deliberately narrow, honestly-scoped mitigation for one specific residual
// gap in the netns sandbox (applyHermeticSandbox in hermetic_linux.go): that
// sandbox correctly isolates IP sockets and Linux abstract-namespace Unix
// sockets via CLONE_NEWNET, but filesystem-*path* Unix domain sockets are
// namespaced by the mount namespace, not the network namespace, and
// CLONE_NEWNS is deliberately not set (closing that fully needs a
// /proc/self/exe reexec helper doing selective mount masking — real, but
// materially riskier engineering than this fix; see Roadmap.md).
//
// What this actually closes, precisely: SSH_AUTH_SOCK is the *only* way
// ssh/git tooling locates the agent socket — with the env var gone, nothing
// in the sandboxed subprocess can find it, full stop. GPG_AGENT_INFO is the
// equivalent, older convention for gpg-agent. DBUS_SESSION_BUS_ADDRESS is
// the equivalent for the D-Bus session bus, occasionally present on
// desktop-Linux dev machines.
//
// What this does NOT close: DOCKER_HOST is included for the non-default
// case (a script that only knows to look at this env var's value, e.g. a
// non-standard or rootless docker socket path), but stripping it provides no
// protection against the common case — /var/run/docker.sock is a filesystem
// convention a malicious script can hardcode or probe directly
// (`test -S /var/run/docker.sock`) with zero reliance on any environment
// variable. That vector remains genuinely open; see Roadmap.md.
var hermeticStrippedEnvVars = []string{
	"SSH_AUTH_SOCK",
	"SSH_AGENT_PID",
	"GPG_AGENT_INFO",
	"DBUS_SESSION_BUS_ADDRESS",
	"DOCKER_HOST",
}

// stripHermeticSensitiveEnv removes hermeticStrippedEnvVars from env,
// returning a new slice — env itself is never mutated. Called on the
// already-built subprocess environment for Prepare/Compile when
// req.Hermetic is true, after buildEnv/buildEnvWithEpoch has merged
// os.Environ() in, so a socket-bearing var the parent process has set is
// never handed to the sandboxed child regardless of where in the merge it
// came from.
func stripHermeticSensitiveEnv(env []string) []string {
	strip := make(map[string]bool, len(hermeticStrippedEnvVars))
	for _, k := range hermeticStrippedEnvVars {
		strip[k] = true
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if strip[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
