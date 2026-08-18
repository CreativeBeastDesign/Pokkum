package ports

// AppRuntime names the JavaScript runtime that executes the application
// inside the built image. It is the second dimension — alongside
// BuildStrategy — of what a `pokkum build` actually produces, and it must be
// treated exactly like BunVersion everywhere an identity of the embedded
// runtime matters: the composite remote-cache input hash, pokkum.lock keying
// (via the base-image preset for RuntimeNode), the toolchain CVE lookup, and
// SLSA provenance. A cache or lock key that ignores this dimension is a
// correctness bug: a Bun-built image would satisfy a Node-requested build.
//
// The two runtimes have deliberately different acquisition models:
//
//   - RuntimeBun: the runtime binary is downloaded, GPG/checksum-verified and
//     embedded as its own image layer (see BunRuntimeResolver and
//     BunBinaryPath). The base image ships no JS runtime of its own.
//   - RuntimeNode: the runtime comes from the base image itself — the
//     default base for RuntimeNode is BaseImageDistrolessNode
//     (gcr.io/distroless/nodejsNN-debian12:nonroot), which ships Node at
//     NodeBinaryPath and is keyless-signed by the same distroless identity
//     the existing distroless preset verifies. Pokkum embeds nothing; CVE
//     response for the runtime is `pokkum base update` re-pinning the base.
//     This route was chosen deliberately over replicating bunruntime's
//     download/verify/pin machinery for Node: Node's release-integrity story
//     (SHASUMS256.txt + a rotating multi-person GPG release-key set) is a
//     materially larger surface than Bun's single release key, and shipping
//     a weaker downloaded-runtime integrity path than Bun's would be worse
//     than not shipping one. The tradeoff is that the Node version is
//     whatever the pinned base digest carries, not independently selectable.
//
// Note this selects the runtime of the IMAGE, not of the build: the SvelteKit
// build itself (vite via `bun x vite build`) always runs under the host's
// Bun toolchain regardless of AppRuntime — Bun is Pokkum's build tool the
// way esbuild is Vite's, and the layered strategy's build output is
// @sveltejs/adapter-node's standard Node-compatible ESM either way.
type AppRuntime string

const (
	// RuntimeBun runs the application under the embedded Bun runtime
	// (BunBinaryPath). The default, and the only runtime the exe and static
	// strategies interact with (exe compiles via `bun build --compile`;
	// static ships no JS runtime at all).
	RuntimeBun AppRuntime = "bun"

	// RuntimeNode runs the application under the base image's own Node.js
	// (NodeBinaryPath). Supported for StrategyLayered only: the layered
	// strategy already targets @sveltejs/adapter-node, whose output is
	// written for Node — the runtime swap is substitution, not redesign.
	RuntimeNode AppRuntime = "node"

	// DefaultAppRuntime is the runtime used when a BuildRequest leaves
	// AppRuntime at its zero value.
	DefaultAppRuntime = RuntimeBun
)

// Valid reports whether r is a known runtime. The zero value ("") is NOT
// valid; core normalises it to DefaultAppRuntime before validating.
func (r AppRuntime) Valid() bool {
	switch r {
	case RuntimeBun, RuntimeNode:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (r AppRuntime) String() string { return string(r) }
