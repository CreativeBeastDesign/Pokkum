# Open Decisions

One row per decision waiting on the maintainer. Structured for easy
options-with-tradeoffs generation, per the maintainer's stated preference.
Status is one of: `decided-not-implemented` (maintainer picked an option;
someone still needs to build it) or `open` (no recommendation locked in yet).

| # | Decision | Options | Recommendation | Status |
|---|---|---|---|---|
| 3 | `--runtime=node` + `--telemetry` (currently rejected outright, `internal/core/model.go:1139`) | (A) leave rejected — no Bun `--preload` equivalent under Node, and building a parallel Node-native OTel bootstrap is real net-new work. (B) build a Node-native bootstrap mirroring the layered mechanism (likely `--require`/`NODE_OPTIONS`-based instead of `--preload`) | not yet recommended | Open. |
| 4 | Node-core CVE lookup (distroless ships Node outside `dpkg`, invisible to both `scannerutils`'s OS-package scanner and the zero-dependency toolchain scanner, which has no Node-core ecosystem entry) | (A) add an OSV Node-core ecosystem query path. (B) accept and document the gap — the base image's own CVE posture is the operator's responsibility for now | not yet recommended | Open. |
| 8 | `--hermetic-mount-isolation`'s residual `CAP_SYS_ADMIN` retention (the sandboxed process keeps the capability that created its own `docker.sock` mount mask, so it could in principle self-`umount()`) | (A) `capset(2)` to drop `CAP_SYS_ADMIN` immediately before the final `exec`, closing the self-undo path. (B) accept as documented residual risk — network isolation (the higher-value half) is not subject to this class of self-undo | not yet recommended | Open, tracked as a Roadmap follow-up. |

## Maintenance rule
Add a row the moment a decision needs the maintainer's input and can't be
resolved by an agent alone. Mark `decided-not-implemented` the moment André
picks an option — don't wait for the implementation to land before recording
the decision. Delete a row only once its implementation has shipped (move any
lasting fact it established into `mem:state`, not here).
