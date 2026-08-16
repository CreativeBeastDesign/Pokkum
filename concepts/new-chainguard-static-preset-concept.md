# Concept: A Dedicated `chainguard-static` Base Image Preset

## 1. Problem Statement & Motivation

`--strategy=static`'s default base image is `cgr.dev/chainguard/static:latest` (`ports.StaticBaseRef`), but as shipped (2026-08-16 fix), it reuses the existing `BaseImageChainguard` preset value to get there — `cmd/pokkum/build.go` sets `Preset = BaseImageChainguard` and overrides `Ref` to `StaticBaseRef`. This was a deliberate, minimal fix for a confirmed bug (see §2), not the most correct design.

### Why the current fix is minimal, not ideal

`BaseImagePreset` is a dispatch key consulted independently in at least three places:

1. **Signature identity** (`DefaultKeylessIdentity()`): which Fulcio issuer/SAN to verify against.
2. **`pokkum.lock` cache key** (`internal/adapters/baseimage/resolver.go`): `lockKey := string(req.Preset)`.
3. **The libc-compatibility safety gate** (`staticBaseReason`, ref-pattern-based, not preset-based — unaffected by this concept either way).

Reusing `BaseImageChainguard` for `--static`'s default correctly fixes (1) — this codebase's own doc comment (`internal/ports/baseimage.go`) already cross-validated that Chainguard's Fulcio SAN is identical across `chainguard/glibc-dynamic` and `chainguard/static`, three years of certs, byte-identical — so verifying a `chainguard/static` pull against the `BaseImageChainguard` identity is not a shortcut, it's provably correct.

But it leaves a **residual, narrower collision** in (2): `pokkum build --base cgr.dev/chainguard/glibc-dynamic` (explicit layered, dynamic linking) and `pokkum build --static` (static, libc-free) in the same project now share one `pokkum.lock` slot — both are `Preset = "chainguard"` — for two base images that are not interchangeable. This is a much narrower version of the bug that motivated the original fix (which was between `distroless` and `chainguard/static`, a total mismatch); the residual case only bites a project that deliberately uses *both* an explicit Chainguard dynamic base *and* `--static`, which is a real but uncommon combination.

---

## 2. Original Bug (context, already fixed)

Before the 2026-08-16 fix, `--static`'s default wiring set `Preset = BaseImageDistroless` (unchanged) while overriding `Ref` to `chainguard/static` — so signature verification checked the pulled Chainguard-signed image against Distroless's Google-service-account Fulcio identity, failing on every default `--static` build out of the box. Fixed by switching `Preset` to `BaseImageChainguard`. This document proposes going one step further.

---

## 3. Proposed Design: `BaseImageChainguardStatic`

Add a fourth preset value, fully distinct from `BaseImageChainguard`:

```go
// internal/ports/baseimage.go

const (
    BaseImageDistroless       BaseImagePreset = "distroless"
    BaseImageChainguard       BaseImagePreset = "chainguard"
    BaseImageChainguardStatic BaseImagePreset = "chainguard-static" // NEW
    BaseImageCustom           BaseImagePreset = "custom"
)

const ChainguardStaticBaseRef = StaticBaseRef // alias for naming consistency; same underlying value

func (p BaseImagePreset) Valid() bool {
    switch p {
    case BaseImageDistroless, BaseImageChainguard, BaseImageChainguardStatic, BaseImageCustom:
        return true
    default:
        return false
    }
}

func (p BaseImagePreset) DefaultRef() (string, bool) {
    switch p {
    case BaseImageDistroless:
        return DistrolessBaseRef, true
    case BaseImageChainguard:
        return ChainguardBaseRef, true
    case BaseImageChainguardStatic:
        return StaticBaseRef, true
    default:
        return "", false
    }
}

func (p BaseImagePreset) DefaultVerifyMode() BaseImageVerifyMode {
    switch p {
    case BaseImageDistroless, BaseImageChainguard, BaseImageChainguardStatic:
        return BaseImageVerifyKeyless
    default:
        return BaseImageVerifyStaticKey
    }
}

func (p BaseImagePreset) DefaultKeylessIdentity() (KeylessIdentity, bool) {
    switch p {
    case BaseImageDistroless:
        return KeylessIdentity{Issuer: DistrolessKeylessIssuer, SAN: DistrolessKeylessSAN}, true
    case BaseImageChainguard, BaseImageChainguardStatic: // same identity — see §1
        return KeylessIdentity{Issuer: ChainguardKeylessIssuer, SAN: ChainguardKeylessSAN}, true
    default:
        return KeylessIdentity{}, false
    }
}
```

`cmd/pokkum/build.go`'s `--static` default wiring changes from:

```go
req.BaseImage.Preset = core.BaseImageChainguard
req.BaseImage.Ref = core.StaticBaseRef
```

to:

```go
req.BaseImage.Preset = core.BaseImageChainguardStatic
req.BaseImage.Ref = core.StaticBaseRef // still explicit, in case DefaultRef's use site ever changes
```

This gives `--static` its own `pokkum.lock` key (`"chainguard-static"`), fully disjoint from both `"distroless"` and `"chainguard"` — eliminating the residual collision in §1 entirely, not just narrowing it.

---

## 4. Backward Compatibility

A `pokkum.lock` file already containing a `"chainguard"` entry created by a `--static` build under the *current* (2026-08-16) fix will **not** be found under the new `"chainguard-static"` key once this ships — the next `--static` build silently re-resolves against the registry and writes a new `"chainguard-static"` entry, leaving the stale `"chainguard"` entry orphaned in the lockfile (harmless, but dead weight, and confusing in a `git diff` of `pokkum.lock`).

Options:
- **Do nothing** (simplest): document the one-time re-resolve in a changelog/release note. Low blast radius — re-resolving a base image tag is exactly what `pokkum.lock` already does gracefully on a cache miss.
- **One-time migration**: on lockfile load, if `"chainguard-static"` is absent but `"chainguard"`'s `entry.Ref` equals `StaticBaseRef` (i.e., it was almost certainly written by a prior `--static` build, not a real `--base chainguard` layered build), copy it to the new key. Adds a real edge case (what if a project genuinely used *both* and the `"chainguard"` entry is ambiguous?) for a one-time convenience — probably not worth the complexity.

**Recommendation: do nothing beyond a changelog note.** The lockfile is designed to self-heal on a miss; this is precisely that mechanism doing its job.

---

## 5. Trade-offs vs. the Shipped Fix

| | Reuse `BaseImageChainguard` (shipped) | New `BaseImageChainguardStatic` (this doc) |
|---|---|---|
| Signature identity | Correct (proven identical SAN) | Correct |
| `pokkum.lock` collision | Residual: only vs. explicit `--base chainguard` layered builds in the same project | None |
| Surface area | Zero new CLI-visible concepts | New preset name to document (`Vocabulary.md`, `--base` help text, `pokkum base update --preset`) |
| Migration cost | None | One orphaned lockfile entry per existing `--static` user, self-healing (§4) |
| Symmetry | N/A | Raises the question of whether `distroless` needs an equivalent (`BaseImageDistrolessStatic`) for consistency, even though nothing currently uses `distroless/static` as a real base (it's explicitly the libc-free trap `staticBaseReason` exists to reject for non-static strategies) |

The residual collision the shipped fix leaves open is narrow enough (requires deliberately combining `--base cgr.dev/chainguard/glibc-dynamic` *and* `--static` in one project) that this is a **low-priority** correctness polish, not an open bug — see `Roadmap.md`'s backlog framing.

---

## 6. Open Questions

- Preset value naming: `"chainguard-static"` vs `"static-chainguard"` vs something that doesn't couple the vendor name to "static" at all (e.g. a generic `"static"` preset, since `chainguard/static` is currently the *only* sensible libc-free choice — but that forecloses ever offering a second static-base vendor without a breaking rename).
- Should `--base-verify-mode`/`--base-keyless-identity`/`--base-keyless-issuer` CLI help text enumerate the new preset alongside `distroless`/`chainguard`, or fold it into `chainguard`'s existing description with a note?
- Worth doing at all before a second real-world report of the residual collision, per the same "don't build for a hypothetical" reasoning as `strategy-dispatch-refactor-concept.md` §6 — or is base-image identity correctness enough of a supply-chain-security concern to justify doing it proactively regardless of how narrow the trigger is? (Unlike the strategy-dispatch refactor, this one touches signature verification — arguably a lower bar for "worth doing early.")
