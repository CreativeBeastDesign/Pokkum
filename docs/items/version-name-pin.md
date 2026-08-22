<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: version-name-pin)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Pin kit.version.name so correctly-configured projects build reproducibly

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | fix |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

The reproducibility pin lived inside the adapter-injection path, so it only ran for misconfigured projects — a correctly configured one got no pin and a different image on every build.

## Problem

SvelteKit defaults `kit.version.name` to `Date.now()`. That value lands in
`_app/version.json` and participates in client chunk naming, so two builds of an
unchanged tree produce different filenames and different layer digests — the exact
property Pokkum sells.

Pokkum did pin it, but only as a side effect of staging a Vite config while injecting a
corrected adapter. A project whose adapter was already right never entered that path,
so it got no injection, no pin, and non-reproducible images. The behaviour was
inverted: misconfiguring your project was the only way to get a reproducible build.

Measured on a real component library: two consecutive builds of an unchanged tree
emitted `{"version":"1787392160265"}` and `{"version":"1787392373500"}`, renaming every
hashed chunk and differing two OCI layers.

## Decision

Shipped 2026-08-22. The pin now runs as its own guard, independent of whether an adapter
injection happened, and covers both outcomes explicitly:

Where the project's build script is exactly `vite build`, Pokkum stages a Vite config
solely to pin the version and runs that. Where it does more (`vite build && bun run
prepack`), taking the build over would silently skip the rest, so Pokkum leaves it alone
and warns that the image is not bit-for-bit reproducible, naming the one-line fix
(`kit.version.name = process.env.SOURCE_DATE_EPOCH`, which Pokkum exports into the build
environment).

Verified end to end against a real project: pinning the version name made all 288
emitted client filenames identical between builds, and the warning fires on the real
multi-command build script that cannot be taken over.

## Implementation

- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)
- [internal/adapters/sveltekitutils/injector.go](../../internal/adapters/sveltekitutils/injector.go)

## Known Limitations

- The staged-config path is covered by unit tests driving `Prepare`, but has not been exercised end to end against a real single-command project — the project it was verified against is on the warn path.
- Pinning the version name is necessary but not sufficient: prerendered HTML containing non-deterministic component IDs (`Math.random()` rather than Svelte's `$props.id()`) still differs between builds. That is a property of the user's source, not of Pokkum.

