<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: supervisor-cgroup-awareness)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Supervisor cgroup awareness

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | feature |
| Tier | polish |
| Area | Build & Packaging |

## Summary

JSC (Bun's engine) doesn't read cgroup limits, so a Bun app in a 512Mi container OOMKills in ways that look random; read /sys/fs/cgroup/memory.max, export it, and warn below a sane floor.

