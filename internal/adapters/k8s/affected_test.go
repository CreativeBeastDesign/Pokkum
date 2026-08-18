package k8s_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// TestResolve_Since_DetectorErrorOnNonFirstPath_NoOrphanedBuildGoroutine is
// the regression test for the goroutine-leak bug in Resolve's
// affected-detection fan-out (see Lessons.md and Serena mem:core's
// "Monorepo Affected-Detection" entry, and mem:self_review_checklist rows 1
// and 4, whose Origin note describes this exact function): an
// AffectedDetector error on a non-first path used to return before g.Wait(),
// stranding any build goroutine already dispatched for an earlier path —
// each one a full image build (compile + package + registry push) whose
// error is silently discarded and which may still be pushing when the
// process exits.
//
// TestResolve_Since_DetectorErrorFailsClosed (above) is exactly the
// coverage gap this fills: it uses a single-project manifest, and with
// only one path there is no earlier build to ever orphan. This test uses
// two distinct paths and, because Go map iteration order is unspecified,
// injects the detector failure on the SECOND call made — by call order,
// not by path identity — so "non-first" holds regardless of which of the
// two paths Resolve happens to process first.
func TestResolve_Since_DetectorErrorOnNonFirstPath_NoOrphanedBuildGoroutine(t *testing.T) {
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
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api-app
spec:
  template:
    spec:
      containers:
      - name: api
        image: pokkum://./apps/api
`),
	}

	var detectorCalls atomic.Int32
	var buildStarted atomic.Int32
	buildStartedSignal := make(chan string, 2)
	release := make(chan struct{})
	buildFinished := make(chan struct{}, 2)

	_, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: []ports.Document{doc},
		Since:     "abc123",
		AffectedDetector: func(_ context.Context, _, _ string) (bool, error) {
			// The first call made (whichever path it lands on) reports
			// "affected" so that path needs a build. The SECOND call --
			// necessarily landing on the other, non-first path -- fails.
			if detectorCalls.Add(1) == 1 {
				return true, nil
			}
			return false, core.ErrInvalidRequest
		},
		Build: func(_ context.Context, path string) (string, error) {
			buildStarted.Add(1)
			buildStartedSignal <- path
			<-release
			buildFinished <- struct{}{}
			return testDigest("d"), nil
		},
	})

	// Always release and drain, so a leaked goroutine from a genuinely
	// buggy implementation cannot hang this test process past its own
	// completion.
	defer func() {
		close(release)
		for i := int32(0); i < buildStarted.Load(); i++ {
			select {
			case <-buildFinished:
			case <-time.After(2 * time.Second):
			}
		}
	}()

	if !errors.Is(err, core.ErrManifestUnresolved) {
		t.Fatalf("expected ErrManifestUnresolved on non-first-path detector failure, got %v", err)
	}

	// The structural fix runs every path's AffectedDetector check to
	// completion (pass 1) before dispatching any build goroutine (pass 2).
	// A failure on the second call must therefore prevent the FIRST,
	// already-decided-affected path's build from ever starting at all --
	// not merely prevent Resolve from waiting on it. Poll briefly rather
	// than checking immediately: on the pre-fix code a dispatched build
	// goroutine may not have run yet at the exact instant Resolve returns,
	// so an instantaneous check could false-negative on the very bug this
	// test exists to catch.
	select {
	case path := <-buildStartedSignal:
		t.Fatalf("build goroutine was dispatched for %q despite a non-first-path detector "+
			"error -- orphaned build goroutine (goroutine leak)", path)
	case <-time.After(300 * time.Millisecond):
		// No build was ever dispatched -- correct.
	}
	if got := buildStarted.Load(); got != 0 {
		t.Fatalf("expected zero build dispatches when detector errors on a non-first path, got %d", got)
	}
}
