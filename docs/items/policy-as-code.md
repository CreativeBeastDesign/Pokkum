<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: policy-as-code)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Policy as code (pokkum policy check)

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | infra |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Deferred: embedding OPA/Rego policy checking would add real CLI size and maintenance for something CI-level tools (Kyverno, Conftest) already cover well.

## Problem

`pokkum policy check` with built-in policies for common compliance frameworks (PCI-DSS, SOC2)
and custom Rego policies ("no images running as root", "must include SBOM") would add roughly
15MB to the CLI for an embedded OPA/Rego runtime, plus an ongoing policy-maintenance burden.
CI-level policy gates already own this space well. Revisit only if a real adopting team
specifically asks.

