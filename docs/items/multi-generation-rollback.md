<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: multi-generation-rollback)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum rollback

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Rolls back image references in Kubernetes manifests using `pokkum.dev/image-history` annotations, with generation depth selection.

## Flags

- `-g`
- `--generation`
- `--list`
- `--to`

## Implementation

- [cmd/pokkum/rollback.go](../../cmd/pokkum/rollback.go)

## Known Limitations

- History accumulation depends on the annotation surviving across independent CLI runs — a static, untouched manifest template with no live cluster query has no other source for it. `pokkum apply`'s pre-flight cluster inspection closes this for the deploy path; a bare `pokkum resolve` run does not.

## Related

- [pokkum apply](k8s-apply.md)

