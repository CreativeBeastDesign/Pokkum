# Targeted Scanner vs Monolithic Syft

## 1. The Optimization Concept
Pokkum currently relies on Anchore Syft (`github.com/anchore/syft`) to scan container images and tarballs for OS packages and application dependencies. Syft is a fantastic, comprehensive tool, but it carries immense weight because it includes parsers for nearly every package manager in existence (Rust, Java, Python, Go, Ruby, etc.) and various cloud SDKs.

Since Pokkum is explicitly and exclusively designed for SvelteKit applications running on specific Linux bases (Debian, Alpine, Chainguard), dragging the Syft monolith into the CLI adds ~50MB of unnecessary bloat.

**The Solution:**
Replace Syft with a custom, lightweight, zero-dependency parser that exclusively understands:
1. `dpkg` status files (`/var/lib/dpkg/status`) for Debian bases.
2. `apk` index files (`/lib/apk/db/installed`) for Alpine/Chainguard bases.
3. `package.json` and `package-lock.json` / `pnpm-lock.yaml` for SvelteKit Node.js dependencies.

## 2. Potential Pitfalls & Risks
Taking ownership of package parsing means taking ownership of specification changes. If the underlying formats change and the parser fails, Pokkum will silently report zero vulnerabilities, defeating the entire purpose of the CVE build gate.

**Specific Risks:**
- **OS Package Database Changes:** While highly unlikely, if Alpine changes the structure of `apk/db/installed`, the parser will break.
- **Node Ecosystem Evolution:** NPM, PNPM, and Yarn frequently release new lockfile versions (e.g., PNPM lockfile v5 to v6). If a user uses a newer lockfile format, the parser might fail to extract the dependency tree.
- **Incomplete SBOM:** Moving away from Syft means Pokkum must manually generate its own valid SPDX/CycloneDX SBOM outputs if users expect them.

## 3. The Automation Tripwire (How to not bugger up)
To prevent silent failures without manual monitoring, you must implement automated integration tests (tripwires) that continuously validate the parser against live, evolving upstream targets.

### CI Tripwire Implementation
Create a weekly GitHub Actions cron job that tests the parser against the absolute latest ecosystem artifacts:

1. **OS Parsing Test:**
   - Pull `gcr.io/distroless/cc-debian12:latest`.
   - Run the custom `dpkg` parser on it.
   - **Assertion:** `len(packages) > 20`. If the format changes, this will return 0 and fail the CI.
   - Repeat for `cgr.dev/chainguard/node:latest` (testing the `apk` parser).

2. **Ecosystem Parsing Test:**
   - Clone 2-3 popular open-source SvelteKit repositories that use different package managers (npm, pnpm, yarn).
   - Run the lockfile parser on them.
   - **Assertion:** `len(dependencies) > 50` and it successfully extracts known packages like `svelte`.

If any of these tests fail, the GitHub Action alerts the repository maintainers, indicating that an upstream format has changed and the parser needs an update *before* users encounter the issue.
