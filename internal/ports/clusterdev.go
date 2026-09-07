package ports

import "context"

// EnvDevMode is read by pokkum-init as a boolean opt-in for the in-pod
// development loop (`pokkum dev --cluster`). It is deliberately OFF by
// default and is never set by the packager: an image only has it because an
// operator put it on the workload, which is the point — enabling it lets
// anyone who can already `kubectl exec` into the pod overwrite the
// application tree under /app and restart the server process, and that is a
// capability a production image must not carry just because it happens to
// be a Pokkum image.
//
// Two behaviours are gated on it, both in pokkum-init:
//
//   - the hidden `pokkum-init __dev-sync` subcommand, which extracts a tar
//     stream from stdin into the live /app tree, refuses to run at all;
//   - SIGHUP keeps its ordinary meaning (relay to the child) instead of
//     meaning "stop the child and start a fresh one in its place".
//
// The literal is mirrored in supervisor/cmd/pokkum-init/config.go — see the
// note there on why the supervisor duplicates these constants rather than
// importing this package.
const EnvDevMode = "POKKUM_DEV_MODE"

// DevSyncSubcommand is the argv[1] that puts pokkum-init into extract-a-tar
// mode instead of supervise-a-child mode. The double underscore prefix
// matches `pokkum __hermetic-reexec`'s convention for a subcommand that
// exists for one internal caller and is not part of the user-facing surface.
const DevSyncSubcommand = "__dev-sync"

// ClusterTarget identifies the single running container that a cluster dev
// loop syncs into, together with the two facts about it that decide whether
// syncing is safe to attempt at all.
type ClusterTarget struct {
	// Namespace, Pod and Container name the container. All three are
	// non-empty in a successfully resolved target: `kubectl exec` needs an
	// explicit container whenever the pod has more than one, and defaulting
	// silently to "the first one" in a pod that also runs an OTEL sidecar
	// would sync the application into the collector.
	Namespace string
	Pod       string
	Container string

	// DevModeEnabled reports whether the container's own spec sets
	// EnvDevMode to a true value. It is reported rather than acted on here
	// so that the command layer owns the policy, but there is only one
	// defensible policy: refuse. A sync into a container whose pokkum-init
	// will reject `__dev-sync` fails anyway, and it fails after the tar has
	// been built and streamed rather than before.
	DevModeEnabled bool

	// AttestationEnabled reports whether the container's spec sets
	// EnvAttestationDigest. It is not fatal — the digest is verified once,
	// at supervisor startup, and a child restart does not re-verify — but it
	// means the pod is one eviction, node drain or OOM kill away from
	// crash-looping on exit code 125, because the /app tree it would then
	// re-attest is the one this dev loop has been overwriting. The caller
	// must warn.
	AttestationEnabled bool
}

// ClusterTargetQuery selects the container to sync into.
type ClusterTargetQuery struct {
	// Namespace is the Kubernetes namespace to search. Required — an empty
	// namespace would mean "whatever the current kubeconfig context happens
	// to point at", and a dev loop that hot-patches a pod is not a command
	// that should ever guess which cluster namespace it is hot-patching.
	Namespace string

	// Selector is a label selector in `kubectl -l` syntax
	// (e.g. "app=storefront"). Required, for the same reason: the set of
	// candidate pods must be named, not defaulted.
	Selector string

	// Container names the container within the matched pod. Optional when
	// the pod runs exactly one container; required otherwise.
	Container string
}

// ClusterSyncDir is one directory to mirror into the running container.
type ClusterSyncDir struct {
	// LocalDir is the absolute host directory whose contents are sent.
	LocalDir string

	// RemoteDir is the absolute in-pod directory the contents land in
	// (e.g. AppServerDirPrefix). It must already exist in the container:
	// creating it would mean this loop is syncing into an image that was
	// never laid out by the layered packager, and silently materialising a
	// tree there would produce a pod that looks synced and serves nothing.
	RemoteDir string

	// ExcludeDirs are child directory names, relative to LocalDir's root,
	// that are not sent. The layered packager excludes exactly
	// client/vendor/native/prerendered when it builds the /app/server layer
	// out of the whole SvelteKit output directory; a sync that did not
	// mirror that exclusion would push the client tree into /app/server as
	// well as /app/client.
	ExcludeDirs []string
}

// ClusterSyncRequest asks for one round of the dev loop.
type ClusterSyncRequest struct {
	// Target is a ClusterTarget previously returned by ResolveTarget.
	Target ClusterTarget

	// Dirs are the directories to mirror, applied in the given order. Order
	// is preserved rather than sorted so the caller can put the tree whose
	// contents the restart re-reads last.
	Dirs []ClusterSyncDir

	// Restart asks pokkum-init to stop the application process and start a
	// fresh one once extraction has succeeded. Static assets under
	// /app/client are re-read per request and need no restart; server code
	// under /app/server is loaded once at process start and needs one.
	Restart bool
}

// ClusterSyncResult reports what a sync actually moved.
type ClusterSyncResult struct {
	// Files and Bytes count the regular files written, and their total
	// uncompressed size. They are the loop's only proof that it did
	// anything: a sync that silently matched zero files looks exactly like
	// a successful one from the exit code alone.
	Files int
	Bytes int64

	// Restarted reports whether a restart was requested AND acknowledged by
	// the in-pod extractor. It is never true for a request with Restart
	// false, and never true when the extractor failed to signal.
	Restarted bool
}

// ClusterDevSyncer copies freshly built application output straight into a
// running pod, with no image build, registry push or pod restart in between.
//
// It is the one adapter in this codebase whose entire job is a side effect
// against a live system, so — exactly like the PaaS Deployer — the
// determinism and zero-clock rules that bind the build pipeline do not apply
// to it. It is never called from core.Build.
//
// Implementations must be safe for concurrent use.
type ClusterDevSyncer interface {
	// ResolveTarget picks the container to sync into. It returns an error
	// rather than an empty target when no pod matches, when several pods
	// match but none is running, or when the matched pod has several
	// containers and the query did not name one.
	ResolveTarget(ctx context.Context, q ClusterTargetQuery) (ClusterTarget, error)

	// Sync mirrors req.Dirs into the target container and, when requested,
	// restarts the application process. It is all-or-nothing per call from
	// the caller's point of view: a partially extracted tree is reported as
	// an error, never as a success with fewer files.
	Sync(ctx context.Context, req ClusterSyncRequest) (ClusterSyncResult, error)
}
