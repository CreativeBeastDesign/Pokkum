<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Roadmap

## Needs decision

_None._

## v1.1

### Build & Packaging

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Tarball output silently drops every OCI annotation](items/tarball-output-drops-annotations.md) | pokkum build --output=tarball writes the legacy docker-save format, which has no annotations field, so every annotation Pokkum stamps is lost without warning. | fix | open |

### Developer Experience

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [pokkum dev --cluster](items/cluster-dev-loop.md) | Watch, rebuild, and sync app server and client output directly into a running pod via the Kubernetes API, without an image build or registry round-trip. | dx | open |

### Supply Chain & Attestation

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Build-environment capture (Go + SvelteKit versions)](items/build-environment-capture.md) | Round out toolchain provenance by also recording the SvelteKit version used, alongside Go and Bun which are already captured. | feature | open |
| [The generic secret rule misses camelCase and suffixed key names](items/generic-secret-rule-key-coverage.md) | password/secret/api_key/token are word-boundary anchored, so apiKey, dbPassword and accessToken are not matched at all. | hardening | open |

## v1.2

### Build & Packaging

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Cache-Control contract, tested for every strategy](items/cache-control-contract.md) | The /_app/immutable, version.json, service-worker.js and prerendered-HTML header contract is a tested invariant only for --strategy=static today; layered/exe rely on adapter-node's bundled sirv defaults with no Pokkum-side test. | infra | open |
| [ECR repository auto-create](items/ecr-repo-autocreate.md) | ECR requires the target repository to exist before the first push; --create-repository would close that first-push failure every ECR user hits once. | feature | open |
| [Shared vendor cache across a monorepo invocation](items/monorepo-vendor-cache.md) | --since skips builds between unaffected projects but does nothing within a build when many packages in one invocation share dependencies; extending the layer cache into a content-addressable vendor-layer cache would close that. | feature | open |
| [Resumable chunked layer upload](items/resumable-chunked-upload.md) | Back off and retry on 429/5xx during a large layer push instead of failing the whole push on one transient registry hiccup. | hardening | open |
| [Supervisor cgroup awareness](items/supervisor-cgroup-awareness.md) | JSC (Bun's engine) doesn't read cgroup limits, so a Bun app in a 512Mi container OOMKills in ways that look random; read /sys/fs/cgroup/memory.max, export it, and warn below a sane floor. | feature | open |

### Supply Chain & Attestation

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [CI OIDC identity for provenance](items/ci-oidc-identity.md) | Bind SLSA provenance to an issuer-attested CI OIDC identity instead of a self-asserted one from a developer laptop. | feature | open |
| [KMS-backed signing](items/kms-signing.md) | Sign builds using a cloud KMS key instead of a local static key file. | feature | open |

## backlog

### Build & Packaging

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Build-time test gate (--test)](items/build-time-test-gate.md) | Condition image creation on the project's own test suite passing, once its interaction with --hermetic (a test suite needing network access conflicts with hermetic-by-default) has an explicit answer. | feature | open |
| [pokkum doctor drift check](items/doctor-drift-check.md) | Nothing currently validates that a .pokkum.yaml's configured adapter, base image, and telemetry settings are still coherent after a SvelteKit or Bun upgrade. | dx | open |
| [Registry-specific error surfacing](items/registry-error-surfacing.md) | Translate GAR/Harbor project-path errors and Docker Hub anonymous-pull rate limits into a readable, specific message instead of a generic push/pull failure. | dx | open |
| [Source maps as an OCI referrer](items/source-maps-oci-referrer.md) | Strip source maps from the shipped image and attach them as a digest-keyed OCI referrer artifact, so Sentry release tagging works without shipping maps to production. | feature | open |

### Developer Experience

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [pokkum config view value provenance](items/config-view-provenance.md) | Show where each resolved `.pokkum.yaml` setting actually came from — flag, profile, env, or default — not just its final value. | dx | open |
| [Documented CLI exit-code table](items/exit-code-reference.md) | Publish a stable reference table for the CLI's exit codes — 125 and 126 already carry specific, undocumented meanings. | dx | open |
| [Pre/post-build shell hooks](items/hooks-system.md) | Deferred: pre/post-build shell hooks would defuse plugin-system demand cheaply, but add new maintenance surface for something CI pipelines already provide natively. | dx | open |
| [--to-oci-layout for daemonless cluster loading](items/oci-layout-dev-output.md) | Emit an OCI layout on disk and load it directly into kind/k3d/minikube, for contributors and CI environments with no Docker/Podman daemon at all. | dx | open |
| [JSON Schema for .pokkum.yaml](items/pokkum-yaml-json-schema.md) | Publish a JSON Schema for `.pokkum.yaml` so editors can offer inline validation and completion instead of only failing at `pokkum config validate` time. | dx | open |
| [Stable Go library API](items/stable-go-library-api.md) | Expose Pokkum's build pipeline as a stable, embeddable Go library API, for a future Skaffold/Tilt-style integration. | dx | open |

### Kubernetes & Operations

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Helm post-renderer + Kustomize KRM function](items/helm-kustomize-integration.md) | Deferred: pokkum resolve only handles raw-YAML pokkum:// refs today, which most teams — who template with Helm or Kustomize — never reach at all. | feature | open |
| [Multi-environment management](items/multi-env-management.md) | Deferred: staging/production config templating and secret-manager integration are real needs, but a large surface most teams already solve at the CI/CD layer. | dx | open |
| [Policy as code (pokkum policy check)](items/policy-as-code.md) | Deferred: embedding OPA/Rego policy checking would add real CLI size and maintenance for something CI-level tools (Kyverno, Conftest) already cover well. | infra | open |

### Observability

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Supervisor-side /metrics endpoint](items/supervisor-metrics-endpoint.md) | Give pokkum-init a real /metrics HTTP handler exporting Bun runtime and cgroup metrics, which is the capability the removed pokkum metrics command only claimed to provide. | feature | open |

### Supply Chain & Attestation

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Dedicated chainguard-static base image preset](items/chainguard-static-preset.md) | Give --strategy=static its own base-image preset so it stops sharing a pokkum.lock slot with an explicit chainguard glibc-dynamic --base build. | hardening | open |
| [Lifecycle-script execution provenance](items/lifecycle-script-provenance.md) | Record which packages actually ran install-time lifecycle scripts during the build as a provenance field. | feature | open |
| [Native addon (.node binary) provenance](items/native-addon-provenance.md) | Hash and record prebuilt native addon binaries in provenance instead of leaving them outside the SBOM/SLSA story entirely. | feature | open |
| [Node-core CVE lookup for --runtime=node](items/node-cve-lookup.md) | Decide whether to add an OSV Node-core ecosystem query path, or document the gap and leave base-image CVE posture to the operator. | feature | open |
| [Verify base image on cache hit](items/verify-base-on-cache-hit.md) | Opt-in flag to re-verify the base image's signature even on a confirmed remote-cache hit, for audit environments that need a uniform verified-base guarantee. | hardening | open |

### Testing & Infrastructure

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [make verify's five steps don't cover supervisor/ or the integration/golden test suites](items/verify-suite-scope-gaps.md) | supervisor/ (pokkum-init, pokkum-static) shares the root go.mod but needs its own explicit go build/go test, and tests/integration's golden-manifest and runtime-smoke suites also sit outside make verify's canonical five steps. | infra | open |

## Unscheduled

### Build & Packaging

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Exclude routes from the production build](items/route-exclusion-filter.md) | Dev-only routes are bundle entry points, so tree-shaking cannot remove them; a build-time filter would keep them out of the image entirely. | feature | open |

### Testing & Infrastructure

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [Helper-delegated walk callbacks are outside G122's reach](items/walk-callback-helper-delegation-toctou.md) | 12 of the repo's 15 Walk/WalkDir callbacks pass the walked path to a helper that opens it internally, so the symlink-TOCTOU class survives in a form gosec G122 structurally cannot see. | infra | open |

## Non-goals

Deliberate decisions, not gaps. Each item page states the reasoning.

| Title | Summary | Kind | Status |
| --- | --- | --- | --- |
| [npm-distributed plugin system](items/plugin-system.md) | Will not be built: an npm-based extension model would undercut the exact supply-chain hardening story Pokkum exists to provide. | dx | wont-do |
| [Progressive deployment strategies](items/progressive-deployment-strategies.md) | Will not be built: canary, blue-green, and auto-rollback are Argo Rollouts/Flagger's turf, with Kubernetes-native primitives Pokkum has no reason to reimplement. | infra | wont-do |
| [Service mesh integration](items/service-mesh-integration.md) | Will not be built: Istio/Linkerd sidecar config generation is real but narrow demand against real, ongoing API churn that dedicated mesh tooling already tracks. | infra | wont-do |

