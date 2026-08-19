<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: ecr-repo-autocreate)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# ECR repository auto-create

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | feature |
| Tier | polish |
| Area | Build & Packaging |

## Summary

ECR requires the target repository to exist before the first push; --create-repository would close that first-push failure every ECR user hits once.

