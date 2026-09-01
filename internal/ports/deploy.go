package ports

import (
	"context"
	"time"
)

// DeployTarget names a PaaS control plane Pokkum can hand a freshly published
// image to.
//
// A target is deliberately NOT a generic "webhook" escape hatch. Each one is
// implemented against that platform's own documented contract, because the
// interesting part of a deploy integration is not sending the request — it is
// knowing which responses actually mean the deployment happened. Swiftwave's
// redeploy webhook, for instance, answers HTTP 200 with the body
// "OK - No rebuild" when it decides the request does not concern the
// application's current image, which a naive status-code check reports as
// success. See the swiftwave adapter for the full contract.
type DeployTarget string

const (
	// DeployDokploy is Dokploy (https://dokploy.com), driven through its
	// OpenAPI/tRPC HTTP API with an x-api-key header.
	DeployDokploy DeployTarget = "dokploy"

	// DeploySwiftwave is SwiftWave (https://swiftwave.org), driven either
	// through its per-application redeploy webhook or its GraphQL API.
	DeploySwiftwave DeployTarget = "swiftwave"
)

// String returns the target's wire/config spelling.
func (t DeployTarget) String() string { return string(t) }

// DeployMethod selects which transport a target is driven through. Not every
// method is valid for every target; core.ValidateDeployMethod owns that matrix
// rather than each adapter rejecting it independently.
type DeployMethod string

const (
	// DeployMethodAPI uses the platform's authenticated management API.
	// For Dokploy that is application.saveDockerProvider + application.deploy;
	// for SwiftWave it is the rebuildApplication GraphQL mutation.
	DeployMethodAPI DeployMethod = "api"

	// DeployMethodWebhook posts to a per-application redeploy webhook whose
	// URL already carries its own secret. SwiftWave only.
	DeployMethodWebhook DeployMethod = "webhook"
)

// String returns the method's wire/config spelling.
func (m DeployMethod) String() string { return string(m) }

// DefaultDeployTimeout bounds a single deploy request. A deploy is a control
// plane call that returns as soon as the platform has *queued* the rollout, so
// it should complete in seconds; the generous default exists for a loaded
// self-hosted panel, not for waiting on the rollout itself.
const DefaultDeployTimeout = 60 * time.Second

// DeployRequest is everything an adapter needs to trigger one deployment.
//
// Token is a live credential. It is resolved from the environment by
// core.ResolveDeployRequest and never read from, or written to, .pokkum.yaml —
// the config file holds only the NAME of the variable to read. Adapters must
// never log it, echo it into an error message, or place it in a URL query
// string.
type DeployRequest struct {
	// Target selects the adapter. Required.
	Target DeployTarget

	// Method selects the transport within that target. Required.
	Method DeployMethod

	// Endpoint is the platform's base URL for DeployMethodAPI (e.g.
	// "https://panel.example.com"), or the complete webhook URL for
	// DeployMethodWebhook. Required.
	Endpoint string

	// Token authenticates the call: a Dokploy API key, or a SwiftWave JWT.
	// Empty for DeployMethodWebhook, whose secret is already inside Endpoint.
	Token string

	// Application identifies the application on the platform. Required for
	// DeployMethodAPI; unused for DeployMethodWebhook, where the id is part of
	// the URL.
	Application string

	// ImageRef is the reference Pokkum just published — the digest-pinned
	// "repo@sha256:…" form when the platform can be repointed at an exact
	// image, since that is the whole reason to deploy from a build rather than
	// from a tag.
	ImageRef string

	// TaggedRef is the same image addressed by its primary mutable tag, or
	// empty when the build produced none. Some platforms can only redeploy
	// whatever reference they were already configured with, and a digest they
	// cannot be moved to is useless to them; see the swiftwave adapter.
	TaggedRef string

	// UpdateImage asks the adapter to repoint the application at ImageRef
	// before triggering the rollout, rather than redeploying whatever
	// reference the platform already holds. Only DeployMethodAPI against a
	// target that supports it can honour this.
	UpdateImage bool

	// RegistryURL, RegistryUsername and RegistryPassword are the pull
	// credentials to write alongside ImageRef when UpdateImage is set, for
	// platforms whose image-update call rewrites them wholesale (Dokploy's
	// does). All three empty means "public image": the adapter sends explicit
	// nulls and must report, in DeployResult.Detail, that it cleared whatever
	// the platform previously stored.
	//
	// RegistryPassword is a live credential and carries the same handling
	// rules as Token.
	RegistryURL      string
	RegistryUsername string
	RegistryPassword string

	// Timeout bounds the whole exchange. Zero means DefaultDeployTimeout.
	Timeout time.Duration
}

// DeployResult reports what a deploy actually did.
//
// ImageUpdated and Triggered are separate booleans on purpose: "the platform
// accepted the call" and "the platform started a rollout" are different facts,
// and at least one supported platform reports the first while declining the
// second. A result with Triggered false is never returned alongside a nil
// error — adapters must fail instead — but the field is carried so callers can
// report the distinction rather than infer it.
type DeployResult struct {
	// Target and Method echo what ran.
	Target DeployTarget
	Method DeployMethod

	// Application is the platform-side application the deploy addressed. For
	// a webhook this is parsed back out of the URL, so a log line names
	// something more useful than the secret-bearing URL.
	Application string

	// ImageRef is the reference the application was pointed at, when the
	// adapter was able to set one. Empty when the platform redeployed its own
	// existing reference.
	ImageRef string

	// ImageUpdated reports whether the application's image reference was
	// actually repointed by this call.
	ImageUpdated bool

	// Triggered reports whether the platform confirmed it started a rollout.
	Triggered bool

	// Detail is a short human-readable summary from the platform, safe to log
	// (adapters must not put credentials in it).
	Detail string
}

// Deployer hands a published image to one PaaS control plane.
//
// Implementations must honour ctx cancellation, must be safe for concurrent
// use, and must treat any response they cannot positively identify as a
// started rollout as an error — never as success. Unlike the build-pipeline
// ports, a Deployer is explicitly NOT required to be deterministic or
// clock-free: it performs a side effect against a live system and is invoked
// after the reproducible part of the build has finished.
type Deployer interface {
	// Target reports which platform this implementation drives.
	Target() DeployTarget

	// Deploy triggers one deployment and reports what happened.
	Deploy(ctx context.Context, req DeployRequest) (DeployResult, error)
}
