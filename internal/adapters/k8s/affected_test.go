package k8s_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/k8s"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// testDigest is a deterministic digest reference helper.
func testDigest(hex string) string {
	return "ghcr.io/acme/app@sha256:" + strings.Repeat(hex, 64/len(hex))
}

func affectedDetector(unaffected []string) ports.AffectedDetector {
	unaffectedSet := make(map[string]bool, len(unaffected))
	for _, p := range unaffected {
		unaffectedSet[p] = true
	}
	return func(_ context.Context, projectPath, sinceRef string) (bool, error) {
		// Returns "affected" (true) unless the path is in the unaffected set.
		return !unaffectedSet[projectPath], nil
	}
}

// TestResolve_Since_SkipsUnaffectedWithSeededAnnotation builds only the affected
// app and reuses the unaffected app's prior current-image digest from the
// manifest annotation.
func TestResolve_Since_SkipsUnaffectedWithSeededAnnotation(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
  annotations:
    pokkum.dev/current-image: ` + testDigest("a") + `
spec:
  template:
    metadata:
      annotations:
        pokkum.dev/current-image: ` + testDigest("a") + `
    spec:
      containers:
      - name: web
        image: pokkum://./apps/web
`),
	}

	var built atomic.Int32
	buildDigest := testDigest("b")
	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		Since:            "abc123",
		AffectedDetector: affectedDetector([]string{"./apps/web"}),
		Build: func(_ context.Context, _ string) (string, error) {
			built.Add(1)
			return buildDigest, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if built.Load() != 0 {
		t.Errorf("expected build to be skipped for unaffected app, but Build was called %d times", built.Load())
	}
	if len(res.References) != 1 || res.References[0].Skipped != true {
		t.Fatalf("expected 1 skipped reference, got %+v", res.References)
	}
	if got := res.References[0].Resolved; got != testDigest("a") {
		t.Errorf("expected reused digest %s, got %s", testDigest("a"), got)
	}
	out := string(res.Documents[0].Content)
	if !strings.Contains(out, "image: "+testDigest("a")) {
		t.Errorf("expected manifest to reuse prior digest, got:\n%s", out)
	}
}

// TestResolve_Since_BuildsWhenUnaffectedButNoKnownDigest: with no seeded
// annotation and no cluster state, an unaffected app must still be built so the
// manifest carries a real digest reference.
func TestResolve_Since_BuildsWhenUnaffectedButNoKnownDigest(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
spec:
  template:
    spec:
      containers:
      - name: web
        image: pokkum://./apps/web
`),
	}

	var built atomic.Int32
	buildDigest := testDigest("b")
	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		Since:            "abc123",
		AffectedDetector: affectedDetector([]string{"./apps/web"}),
		Build: func(_ context.Context, _ string) (string, error) {
			built.Add(1)
			return buildDigest, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if built.Load() != 1 {
		t.Errorf("expected a build when no prior digest is known, got %d builds", built.Load())
	}
	if len(res.References) != 1 || res.References[0].Skipped {
		t.Fatalf("expected 1 non-skipped reference, got %+v", res.References)
	}
	if got := res.References[0].Resolved; got != buildDigest {
		t.Errorf("expected built digest %s, got %s", buildDigest, got)
	}
}

// TestResolve_Since_BuildsAffectedApp changes only one app in a multi-app
// manifest: the changed app is rebuilt, the unchanged app is reused.
func TestResolve_Since_BuildsAffectedApp(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
  annotations:
    pokkum.dev/current-image: ` + testDigest("a") + `
spec:
  template:
    metadata:
      annotations:
        pokkum.dev/current-image: ` + testDigest("a") + `
    spec:
      containers:
      - name: web
        image: pokkum://./apps/web
`),
	}

	// apps/web is AFFECTED -> must rebuild despite the seeded annotation.
	var built atomic.Int32
	buildDigest := testDigest("b")
	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		Since:            "abc123",
		AffectedDetector: affectedDetector(nil), // nothing unaffected
		Build: func(_ context.Context, _ string) (string, error) {
			built.Add(1)
			return buildDigest, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if built.Load() != 1 {
		t.Errorf("expected affected app to rebuild, got %d builds", built.Load())
	}
	if len(res.References) != 1 || res.References[0].Skipped {
		t.Fatalf("expected 1 non-skipped reference, got %+v", res.References)
	}
	if got := res.References[0].Resolved; got != buildDigest {
		t.Errorf("expected rebuilt digest %s, got %s", buildDigest, got)
	}
}

// TestResolve_NoSince_buildsAllEvenWithAnnotations: without --since there is no
// detector and every referenced project is built.
func TestResolve_NoSince_buildsAllEvenWithAnnotations(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
  annotations:
    pokkum.dev/current-image: ` + testDigest("a") + `
spec:
  template:
    metadata:
      annotations:
        pokkum.dev/current-image: ` + testDigest("a") + `
    spec:
      containers:
      - name: web
        image: pokkum://./apps/web
`),
	}

	var built atomic.Int32
	buildDigest := testDigest("b")
	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: []ports.Document{doc},
		// Since and AffectedDetector intentionally empty.
		Build: func(_ context.Context, _ string) (string, error) {
			built.Add(1)
			return buildDigest, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if built.Load() != 1 {
		t.Errorf("expected build without --since, got %d builds", built.Load())
	}
	if got := res.References[0].Resolved; got != buildDigest {
		t.Errorf("expected built digest %s, got %s", buildDigest, got)
	}
}

// TestResolve_Since_UsesClusterSeededDigest: an unaffected app with no local
// annotation reuses the live cluster's current-image digest.
func TestResolve_Since_UsesClusterSeededDigest(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: web
        image: pokkum://./apps/web
`),
	}

	clusterImage := testDigest("c")
	inspector := func(_ context.Context, kind, name, ns string) (ports.ClusterWorkloadState, error) {
		return ports.ClusterWorkloadState{
			Annotations: map[string]string{
				ports.AnnotationCurrentImage: clusterImage,
			},
		}, nil
	}

	var built atomic.Int32
	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		Since:            "abc123",
		AffectedDetector: affectedDetector([]string{"./apps/web"}),
		ClusterInspector: inspector,
		Build: func(_ context.Context, _ string) (string, error) {
			built.Add(1)
			return testDigest("b"), nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if built.Load() != 0 {
		t.Errorf("expected skip to reuse cluster digest without building, got %d builds", built.Load())
	}
	if len(res.References) != 1 || res.References[0].Skipped != true {
		t.Fatalf("expected 1 skipped reference, got %+v", res.References)
	}
	if got := res.References[0].Resolved; got != clusterImage {
		t.Errorf("expected reused cluster digest %s, got %s", clusterImage, got)
	}
}

// TestResolve_Since_DetectorErrorFailsClosed: a detector error must fail the
// resolve rather than silently building or silently skipping.
func TestResolve_Since_DetectorErrorFailsClosed(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
spec:
  template:
    spec:
      containers:
      - name: web
        image: pokkum://./apps/web
`),
	}

	_, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: []ports.Document{doc},
		Since:     "abc123",
		AffectedDetector: func(_ context.Context, _, _ string) (bool, error) {
			return false, core.ErrInvalidRequest
		},
		Build: func(_ context.Context, _ string) (string, error) {
			return testDigest("b"), nil
		},
	})
	if !errors.Is(err, core.ErrManifestUnresolved) {
		t.Fatalf("expected ErrManifestUnresolved on detector failure, got %v", err)
	}
}

// TestResolve_Since_MultiAppOnlyAffectedBuilt builds only the changed app and
// reuses the unchanged app's seeded digest across two workload documents.
func TestResolve_Since_MultiAppOnlyAffectedBuilt(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
  annotations:
    pokkum.dev/current-image: ` + testDigest("a") + `
spec:
  template:
    metadata:
      annotations:
        pokkum.dev/current-image: ` + testDigest("a") + `
    spec:
      containers:
      - name: web
        image: pokkum://./apps/web
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api-app
  annotations:
    pokkum.dev/current-image: ` + testDigest("c") + `
spec:
  template:
    metadata:
      annotations:
        pokkum.dev/current-image: ` + testDigest("c") + `
    spec:
      containers:
      - name: api
        image: pokkum://./apps/api
`),
	}

	var webBuilds, apiBuilds atomic.Int32
	buildDigest := testDigest("d")
	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		Since:            "abc123",
		AffectedDetector: affectedDetector([]string{"./apps/api"}),
		Build: func(_ context.Context, path string) (string, error) {
			if path == "./apps/api" {
				apiBuilds.Add(1)
				return testDigest("c"), nil
			}
			webBuilds.Add(1)
			return buildDigest, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if apiBuilds.Load() != 0 {
		t.Errorf("expected apps/api to be skipped and reused, got %d builds", apiBuilds.Load())
	}
	if webBuilds.Load() != 1 {
		t.Errorf("expected apps/web (affected) to build exactly once, got %d builds", webBuilds.Load())
	}

	byPath := map[string]ports.Reference{}
	for _, ref := range res.References {
		byPath[ref.Path] = ref
	}
	if ref := byPath["./apps/api"]; !ref.Skipped || ref.Resolved != testDigest("c") {
		t.Errorf("expected apps/api reused+skipped, got %+v", ref)
	}
	if ref := byPath["./apps/web"]; ref.Skipped || ref.Resolved != buildDigest {
		t.Errorf("expected apps/web built+not-skipped, got %+v", ref)
	}
}
