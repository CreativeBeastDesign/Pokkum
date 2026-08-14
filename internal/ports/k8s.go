package ports

import "context"

// Scheme is the URI scheme Pokkum recognises inside Kubernetes manifests. A
// manifest written for Pokkum names its image as
//
//	image: pokkum://./src/app
//
// where the part after the scheme is a path to a SvelteKit project, relative to
// the manifest's base directory. Resolving rewrites it to an immutable digest
// reference such as
//
//	image: ghcr.io/acme/app@sha256:0f1e2d…
//
// This is ko's `ko://` convention with a different scheme, and it is deliberate:
// developers keep one manifest that is valid to read, diff and review, and the
// build tool is the only thing that ever knows the digest.
const Scheme = "pokkum://"

// AnnotationPreviousImage is the Kubernetes annotation key used to store
// the displaced image reference prior to an image update or rollback.
const (
	AnnotationPreviousImage = "pokkum.dev/previous-image"
	AnnotationImageHistory  = "pokkum.dev/image-history"

	// AnnotationCurrentImage records the digest Resolve last wrote into this
	// document's image: field, independent of that field's own current
	// value. It exists because a pokkum:// source template's image: field
	// never becomes a concrete ref between runs — Resolve only ever
	// rewrites a pokkum:// value back to a (possibly different) digest, so
	// there is nothing in the field itself to diff a re-resolve against.
	// Comparing this annotation's old value to the freshly-resolved digest
	// is what lets Resolve detect "this redeploy actually changed the
	// image" and push the old digest into AnnotationImageHistory.
	AnnotationCurrentImage = "pokkum.dev/current-image"
)

// ImageBuilder builds and publishes the project at path and returns the
// resulting immutable reference, in "repo@sha256:…" form.
//
// This callback is the hinge of the whole k8s port. Traversing YAML and
// building images are unrelated skills, and putting a builder inside the YAML
// adapter would drag the entire pipeline into it. Instead core supplies a
// closure over its own Build, and the adapter is left owning nothing but
// traversal and substitution — which is the part that is worth unit-testing
// with plain strings.
//
// path is the substring following Scheme, exactly as written in the manifest
// and not cleaned or made absolute; it is core's job to interpret it against
// the base directory. The returned reference must be a digest reference, never
// a tag: a tag in a manifest defeats the purpose of resolving at all.
//
// Implementations are called concurrently for distinct paths. They must be
// idempotent per path within one Resolve call — the adapter may encounter the
// same pokkum:// reference in several documents and will call the builder once
// per distinct path, relying on core to cache.
type ImageBuilder func(ctx context.Context, path string) (string, error)

// Document is a single YAML input and its identity. Content is passed as bytes
// rather than a path so that the adapter never touches the filesystem: reading
// files, expanding directories and honouring --recursive belong to the command
// layer, which already has to do it for other flags, and keeping the adapter
// pure makes multi-document, stdin and templated inputs all the same case.
type Document struct {
	// Name identifies the document for error messages and for writing output
	// back. Conventionally the file path it was read from; "-" or "" for stdin.
	Name string

	// Content is the raw YAML. Required. It may contain multiple documents
	// separated by "---"; the adapter must handle that and must preserve the
	// separators, comments and key order of everything it did not rewrite.
	Content []byte
}

// Reference is one occurrence of a pokkum:// image reference found in the
// input, and what it resolved to.
type Reference struct {
	// Document is the Name of the document it was found in.
	Document string

	// Path is the project path following the scheme, as written.
	Path string

	// Resolved is the digest reference it was rewritten to, or empty in a
	// References call, which does not build anything.
	Resolved string
}

// ResolveRequest asks the resolver to rewrite every pokkum:// reference.
type ResolveRequest struct {
	// Documents are the manifests to process. Required and non-empty. Order is
	// preserved in the result.
	Documents []Document

	// Build is called once per distinct project path to produce its digest
	// reference. Required — a nil Build is a caller bug, not a signal to skip
	// building.
	Build ImageBuilder

	// Strict makes an unresolvable reference a hard error. When false, the
	// resolver still fails the whole call on a build error; Strict additionally
	// rejects a manifest whose image value merely looks like a scheme typo
	// ("pokkum:/", "pokkum:"). Defaults to false.
	Strict bool

	// SecurityDefaults, when true, injects hardened securityContext defaults
	// into every Pod spec and container that a pokkum:// reference was
	// resolved in:
	//
	//   - pod-level: securityContext.runAsNonRoot: true,
	//     securityContext.seccompProfile.type: RuntimeDefault
	//   - container-level (pokkum-built containers only):
	//     securityContext.allowPrivilegeEscalation: false,
	//     securityContext.capabilities.drop: [ALL]
	//
	// A field the manifest already sets — at any nesting level, including a
	// sibling field under an existing securityContext — is never overwritten;
	// only fields that are entirely absent are filled in. Container-level
	// defaults apply only to the container whose image was a pokkum:// ref,
	// never to sidecars the resolver knows nothing about; pod-level defaults
	// necessarily apply to the whole Pod spec, since Kubernetes has no
	// per-container pod securityContext. Defaults to false; the command layer
	// SecurityDefaults, when true, injects hardened securityContext defaults.
	SecurityDefaults bool

	// WithOTELSidecar, when true, injects an OpenTelemetry Collector sidecar container
	// specification into Pod specs where pokkum:// references were resolved.
	WithOTELSidecar bool

	// NetworkPolicy, when true, appends a NetworkPolicy manifest restricting egress/ingress.
	NetworkPolicy bool

	// ResourceDefaults, when true, injects default CPU/memory requests and limits,
	// and appends a PodDisruptionBudget.
	ResourceDefaults bool

	// RegistryConfigPath is the optional custom OCI config.json path for authentication.
	RegistryConfigPath string

	// ClusterInspector queries live cluster state for a workload's annotations and container images.
	// Nil when cluster inspection is disabled (e.g. standalone resolve, dry-run, or air-gapped apply).
	ClusterInspector ClusterInspector
}

// ClusterWorkloadState holds live annotations and container image references queried from a cluster.
type ClusterWorkloadState struct {
	Annotations map[string]string
	Containers  map[string]string // container name -> image ref
}

// ClusterInspector queries a live Kubernetes cluster for a workload's current state.
// It receives the resource kind (e.g. "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Pod"),
// resource name, and namespace.
//
// It returns empty state and a nil error when the workload genuinely does not
// exist in the cluster yet — the expected, unremarkable case for a
// first-ever deployment. Any other failure (cluster unreachable, expired or
// invalid credentials, RBAC denial, malformed kubeconfig, or a response the
// implementation cannot parse) must be returned as a non-nil error rather
// than folded into empty state: callers rely on this distinction to tell
// "there is no history because this is new" apart from "there is no history
// because we could not check", which matters because the latter means
// rollback history may silently regress to empty.
type ClusterInspector func(ctx context.Context, kind, name, namespace string) (ClusterWorkloadState, error)

// ClusterInspectionWarning records a non-nil error returned by
// ClusterInspector for one workload during Resolve. Resolve does not fail
// the whole call on an inspection error — cluster-history enrichment is
// best-effort and must not block `apply` from working when the cluster is
// merely unreachable or the caller's credentials are insufficient — but it
// surfaces every such failure here so the command layer can report it
// loudly (e.g. a Warn-level log line) instead of silently proceeding as if
// the workload were freshly created.
type ClusterInspectionWarning struct {
	Kind      string
	Name      string
	Namespace string
	Err       error
}

// ResolveResult carries the rewritten manifests.
type ResolveResult struct {
	// Documents are the rewritten manifests, in the same order and with the
	// same Names as the input. A document containing no pokkum:// reference is
	// returned byte-identical to its input — the resolver must not reformat,
	// reorder or re-quote YAML it did not need to change, because a diff full
	// of incidental churn is how a team stops trusting a tool that edits their
	// manifests.
	Documents []Document

	// References lists every occurrence that was rewritten, with its resolved
	// digest reference. Used for the build summary and for --dry-run output.
	// Empty when the input contained none, which is not an error.
	References []Reference

	// ClusterInspectionWarnings lists every workload for which
	// ResolveRequest.ClusterInspector returned a non-nil error. Empty when
	// cluster inspection was disabled, or succeeded (or found nothing) for
	// every workload. Never causes Resolve to fail — see ClusterInspector's
	// doc comment — but the command layer should report these, since each
	// one means rollback history for that workload may be incomplete.
	ClusterInspectionWarnings []ClusterInspectionWarning
}

// Resolver rewrites pokkum:// image references in Kubernetes manifests to
// immutable digest references. It is implemented by internal/adapters/k8s.
//
// It knows nothing about Kubernetes API types and must not import any: it
// operates on YAML, looking for string values under an "image" key at any
// depth, so that it works on Deployments, CronJobs, Argo Rollouts, Helm output
// and any CRD that happens to name a container image.
//
// Error expectations: core.ErrManifestUnresolved when a reference cannot be
// built or when Strict rejects a malformed scheme, core.ErrManifestInvalid when
// a document is not parseable YAML. Both must name the offending document and
// the offending value.
//
// Implementations must be safe for concurrent use.
type Resolver interface {
	// Resolve rewrites every reference, calling req.Build for each distinct
	// project path. It returns an error if any build fails; a partially
	// rewritten result is never returned, because applying half-resolved
	// manifests to a cluster is worse than applying none.
	Resolve(ctx context.Context, req ResolveRequest) (ResolveResult, error)

	// References reports every pokkum:// occurrence in the documents without
	// building anything. The Resolved field of each Reference is empty. It
	// backs --dry-run and lets core plan and de-duplicate its builds before
	// committing to any of them.
	References(ctx context.Context, docs []Document) ([]Reference, error)
}
