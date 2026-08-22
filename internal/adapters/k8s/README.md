# Kubernetes Manifest Adapter (`internal/adapters/k8s`)

The `k8s` adapter implements the [`ports.Resolver`](../../ports/k8s.go) interface. It parses Kubernetes manifests (YAML documents), finds `pokkum://` image references, invokes the provided image builder callback to compile and publish container images, and rewrites the manifest fields to immutable digest references.

This mechanism works like Google Cloud / CNCF `ko`'s `ko://` scheme, tailored for SvelteKit projects processed by Pokkum.

---

## Key Features

- **Schema-Agnostic YAML Traversal**: Operates strictly on generic YAML AST (`gopkg.in/yaml.v3`), searching for string values under any `"image"` key at any depth. It requires no Kubernetes API dependencies (`k8s.io/api`), making it compatible with Deployments, StatefulSets, CronJobs, Argo Rollouts, Helm template outputs, and custom CRDs.
- **Concurrent & Deduplicated Builds**: Deduplicates distinct project paths across all input documents and invokes `ImageBuilder` concurrently using `errgroup`.
- **Byte-Identical Preservation**: Documents containing no `pokkum://` image references are returned byte-identical. Comments, whitespace, key ordering, and `---` document separators are preserved for modified documents.
- **Strict Typo Validation**: Optional `Strict: true` mode rejects malformed scheme typos (such as `pokkum:/` or `pokkum:`) with a `core.ErrManifestUnresolved` error.
- **Digest Reference Enforcement**: Rejects any build result that does not produce an immutable digest reference (`@sha256:...`).

---

## URI Scheme & Example Transformation

In raw Kubernetes manifests, SvelteKit application images are referenced using the `pokkum://` scheme followed by a path relative to the manifest directory:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
spec:
  template:
    spec:
      containers:
      - name: app
        image: pokkum://./src/app
```

After resolving via `k8s.NewResolver().Resolve(...)`, the reference is rewritten to its immutable digest reference:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
spec:
  template:
    spec:
      containers:
      - name: app
        image: ghcr.io/acme/app@sha256:0f1e2d3c4b5a697887654321fedcba0987654321fedcba0987654321fedcba0
```

---

## How It Works

1. **AST Decoding**: Reads raw document byte slices and decodes them into `*yaml.Node` document streams.
2. **Reference Discovery**: Recursively traverses `yaml.Node` structures (Mappings, Sequences, Aliases) to locate scalar values under keys named `"image"`.
3. **Typo Checking (Strict Mode)**: If `Strict` is set to `true`, string values under `"image"` keys that resemble scheme typos (e.g. `pokkum:`, `pokkum:/`) are flagged with `core.ErrManifestUnresolved`.
4. **Concurrent Image Build**: Unique project paths are built concurrently by calling `req.Build(ctx, path)`.
5. **AST Mutation & Re-encoding**: For documents with references, target scalar nodes are updated in-place with resolved digest strings and re-encoded. Documents without `pokkum://` references are returned byte-identical.

---

## Go Usage Example

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/k8s"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func main() {
	resolver := k8s.NewResolver()

	docs := []ports.Document{
		{
			Name:    "deployment.yaml",
			Content: []byte("apiVersion: apps/v1\nkind: Deployment\n...\n  image: pokkum://./src/frontend"),
		},
	}

	result, err := resolver.Resolve(context.Background(), ports.ResolveRequest{
		Documents: docs,
		Build: func(ctx context.Context, path string) (string, error) {
			// Trigger image compilation and registry push
			return "ghcr.io/org/frontend@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", nil
		},
		Strict: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolution failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(result.Documents[0].Content))
}
```

---

## Error Sentinels

All errors returned across the port boundary follow Pokkum's error wrapping conventions defined in `internal/core/errors.go`:

- `core.ErrManifestInvalid`: Returned when a document contains invalid/unparseable YAML syntax.
- `core.ErrManifestUnresolved`: Returned when an image build fails, when a build returns a tag instead of a digest reference, or when `Strict: true` catches a scheme typo.
- `core.ErrInvalidRequest`: Returned when `req.Build` is `nil` or `req.Documents` is empty.
