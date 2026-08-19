# Pokkum Core — Durable Invariants

Pokkum is a Go-based zero-dependency OCI container image compiler for
SvelteKit applications. Start at `mem:index` for routing; read `mem:state` for
current implementation reality. This memory holds only invariants meant to
survive every future feature — if a fact would become false the moment the
next feature ships, it belongs in `mem:state`, not here.

## Hexagonal architecture
- `internal/ports`: leaf dependency node. Imports stdlib +
  `go-containerregistry/pkg/v1` only. NEVER imports `internal/core` or any
  adapter.
- `internal/core`: imports `internal/ports` + stdlib only. Re-exports port
  vocabulary as type aliases (`core.Platform = ports.Platform`).
- `internal/adapters/*`: import `internal/ports` and `internal/core` (sentinel
  errors only). Adapter→adapter imports are forbidden — `internal/architecture_test.go`
  AST-scans and enforces an allowlist, currently empty. `cmd/pokkum` is the
  sole composition root permitted to construct verifiers/signers; it injects
  them as options at every real call site.
- Every concrete adapter declares a compile-time port assertion
  (`var _ ports.X = (*Y)(nil)`).
- Purity enforced by `internal/architecture_test.go`, run via
  `go test ./internal/...` or `make check-arch`.
- A utility package consumed from `internal/core` still goes through a
  `ports` interface (e.g. `ports.EnvBakeDetector` wrapping `envbake`) — the
  hexagonal boundary applies to utility packages too, not only concrete
  adapters.

## Bit-for-bit OCI reproducibility
- Zero clock access: adapters never call `time.Now()`. All timestamps and tar
  headers derive from `req.SourceDateEpoch` — with one structural exception:
  immutable, content-invariant binary layers (Bun, `pokkum-init`,
  `pokkum-static`) use a fixed pinned-epoch constant instead, since their
  bytes don't vary with source and a fixed timestamp is strictly more
  deterministic than a source-derived one. A second, deliberate wall-clock
  exception exists for VEX exemption expiry (calendar time, not image bytes).
- Directory traversals, tar headers, and map keys are explicitly sorted
  before archiving or hashing.
- Build-time process metadata must never leak into content-addressed artifact
  bytes. This is a named class after two separate incidents in the same
  layers (`SOURCE_DATE_EPOCH` timestamp leakage, then Go's `-buildvcs`
  default) — any new embedded or compiled artifact meant to be
  content-determined needs its non-determinism sources identified and pinned
  the same way.

## Zero-mutation build sandbox
- Code injection (adapter config rewriting, telemetry bootstrap) happens only
  under `.pokkum/` inside the project directory, never in the user's real
  source tree.
- A failed or disabled injection falls back to fail-fast validation
  (`checkEffectiveAdapter`), never a silent skip that could build the wrong
  thing quietly.

## Fail-closed verification (a structural rule, not a point-in-time fact)
- Any verification path (signature, attestation, base-image trust, cache-hit
  legitimacy) must refuse when its dependency — a key, a verifier, a trust
  root — is missing or unconstructed. It must never silently report
  `false`/unsigned/unverified alongside a `nil` error. A nil-tolerant
  condition that reads as a defensive guard is a fail-open the moment
  whatever made that branch unreachable changes. This exact shape has
  recurred three separate times in this codebase — treat it as the default
  hypothesis whenever reviewing a verification branch. See
  `mem:self_review_checklist` rows 30/32.
- No shared, unattributed, or placeholder trust anchor may ever be a default
  fallback for a verification key. A trust anchor nobody owns is worse than
  refusing to verify.

## Naming conventions
- Any shared/internal helper package that does not implement a concrete
  Hexagonal port adapter gets a `utils` suffix (`sveltekitutils`,
  `ignoreutils`, `poolutils`, ...) and SHOULD declare
  `const IsUtilityPackage = true`.

## Toolchain version stability
- `go.mod`'s `go` directive and every `go-version:` pin in
  `.github/workflows/*.yml` only ever hold steady or rise — never regress.
  A tool/editor/dependency conflict is a signal to fix the real
  incompatibility or ask the user, not to lower the pin.
