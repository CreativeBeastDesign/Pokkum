# Open Decisions

One row per decision waiting on the maintainer. Structured for easy
options-with-tradeoffs generation, per the maintainer's stated preference.
Status is one of: `decided-not-implemented` (maintainer picked an option;
someone still needs to build it) or `open` (no recommendation locked in yet).

| # | Decision | Options | Recommendation | Status |
|---|---|---|---|---|
| 3 | `--runtime=node` + `--telemetry` (currently rejected outright, `internal/core/model.go:1139`) | (A) leave rejected — no Bun `--preload` equivalent under Node, and building a parallel Node-native OTel bootstrap is real net-new work. (B) build a Node-native bootstrap mirroring the layered mechanism (likely `--require`/`NODE_OPTIONS`-based instead of `--preload`) | not yet recommended | Open. |
| 4 | Node-core CVE lookup (distroless ships Node outside `dpkg`, invisible to both `scannerutils`'s OS-package scanner and the zero-dependency toolchain scanner, which has no Node-core ecosystem entry) | (A) add an OSV Node-core ecosystem query path. (B) accept and document the gap — the base image's own CVE posture is the operator's responsibility for now | not yet recommended | Open. |
| 5 | `--strategy=exe` secret-scanning parity (`secretguard` scans build **output directories**; exe's output is a single compiled binary, not a directory of source-like files) | Needs verification of current coverage first, then a choice between (A) scan the compiled binary directly (harder — binary, not text) or (B) scan exe's pre-compile intermediate output the same way layered/static are scanned | needs verification before a recommendation — see `mem:state`'s Secret scanning entry | Open. |
| 6 | Asset-overlay `verify` gap — `pokkum verify`'s default rebuild-and-compare path is not `--asset-overlay`-aware and reports a false-positive digest mismatch on an overlay image | (A) teach `verify` to accept the same predecessor refs / `--asset-overlay-from` syntax before rebuilding. (B) read `pokkum.dev/asset-overlay-sources` directly off the image under verification instead of requiring the flag again | not yet recommended | Open, tracked as `docs/items/asset-overlay-verify-gap.md`. |
| 7 | Remote-cache verify key doesn't inherit `req.Signing.PublicKeyPEM` — a build signed via `--signing-key` alone doesn't automatically make its own cache entries verifiable | (A) auto-populate the cache-verify key chain from the signing key when no `POKKUM_CACHE_PUBKEY`/`POKKUM_SIGNING_PUBKEY` is set. (B) leave separate and clearly document the operator burden (two keys to configure for full acceleration) | not yet recommended | Open — see `mem:state`'s Caching entry. |
| 8 | `--hermetic-mount-isolation`'s residual `CAP_SYS_ADMIN` retention (the sandboxed process keeps the capability that created its own `docker.sock` mount mask, so it could in principle self-`umount()`) | (A) `capset(2)` to drop `CAP_SYS_ADMIN` immediately before the final `exec`, closing the self-undo path. (B) accept as documented residual risk — network isolation (the higher-value half) is not subject to this class of self-undo | not yet recommended | Open, tracked as a Roadmap follow-up. |

## Maintenance rule
Add a row the moment a decision needs the maintainer's input and can't be
resolved by an agent alone. Mark `decided-not-implemented` the moment André
picks an option — don't wait for the implementation to land before recording
the decision. Delete a row only once its implementation has shipped (move any
lasting fact it established into `mem:state`, not here).
