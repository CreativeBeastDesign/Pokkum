<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: performance-benchmark-harness)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# A benchmark harness, and the optimisation pass it made possible

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
| Kind | dx |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

Nothing in this repo measured speed, so every performance claim was an argument from reading the code; adding benchmarks over realistic inputs turned that into numbers, and the numbers then found two bugs that correctness tests structurally could not.

## Decision

Shipped 2026-09-05. Before this there were zero Benchmark functions in the module, the
pipeline recorded only a single whole-build duration, and benchmarks/three-way measured
image size, package count and CVEs but had no wall-clock row. `make bench` now covers the
build hot paths and both PID-1 binaries, with GOTOOLCHAIN pinned the way the supervisor
targets pin it, since numbers taken under whatever Go happens to be installed are not
comparable to CI's.

Every benchmark carries a fixture floor that fails the run if it would otherwise measure
the wrong path — that pruning actually pruned, that the cache entry was present on the hit
path, that the scanner scanned rather than skipped. Several of those floors were shown to
go red before being kept.

The optimisation pass that followed is measured against that baseline. Largest results:
the layer-cache hit path went from 24.3ms to 33us and the Bun custom-file layer hit from
157ms to 37us (both were fully decompressing and re-hashing a blob on every hit); secret
scanning went from 4.5 to 92.6 MB/s over a directory and 4.2 to 180.7 MB/s over a large
bundle; IsJunk went from 4535ns to 108ns per path; the ignore matcher from 1611ns to 242ns
at zero allocations; precompression memory from 16.3GB to 1.8GB; per-container-start
attestation from 648ms to 387ms with 89% less memory; and static serving improved on five
of six request shapes, with the 404 path 14x faster. Registry auth sessions for a signed
two-platform push dropped from roughly 26 to 8, and directory-tree layers are now built
once per build rather than once per platform, so multi-platform layer work is flat in
platform count instead of linear.

## Implementation

- [Makefile](../../Makefile)
- [internal/adapters/packager/bench_test.go](../../internal/adapters/packager/bench_test.go)
- [internal/adapters/secretguard/bench_test.go](../../internal/adapters/secretguard/bench_test.go)
- [supervisor/cmd/pokkum-init/attest_bench_test.go](../../supervisor/cmd/pokkum-init/attest_bench_test.go)
- [supervisor/cmd/pokkum-static/server_bench_test.go](../../supervisor/cmd/pokkum-static/server_bench_test.go)

## Known Limitations

- Two bugs were found only because something finally counted: the remote build cache could never hit on any project since it shipped (pokkum.lock was hashed as source while the build stamps time.Now() into it beforehand), and a credential cache stored only its successes so every registry without a stored credential re-spawned the helper subprocess forever. Both are invisible to correctness tests — a cache that never hits behaves exactly like one that correctly misses. See Lessons.md 2026-09-05 and mem:self_review_checklist rows 62 and 63.
- The large-tree layer benchmark is too noisy on a loaded machine to support a timing claim (a 1.9x spread was observed across runs of identical code); allocations are the reliable signal there, and the multi-platform variant is what actually demonstrates the per-build memo.
- One request shape regressed deliberately: a conditional 304 went 23.8us to 27.4us, because the ETag is now taken from the open handle that is actually served, which is what closes the TOCTOU window the old resolve-then-stat-then-open path had. Documented at the benchmark case.
- A typed bun.lock decode was built four ways, measured slower than the map[string]any code it would replace every time, and reverted; the measurements live in a doc comment on ParseBunLock so the next attempt does not repeat them.

## Related

- [make verify's five steps don't cover supervisor/ or the integration/golden test suites](verify-suite-scope-gaps.md)

