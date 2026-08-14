package k8s_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/k8s"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestResolver_References(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	multiDoc := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
      - name: sidecar
        image: nginx:latest
---
apiVersion: batch/v1
kind: Job
metadata:
  name: migration
spec:
  template:
    spec:
      containers:
      - name: db-migrate
        image: pokkum://./src/migrate
`)

	docs := []ports.Document{
		{Name: "manifest.yaml", Content: multiDoc},
	}

	refs, err := r.References(ctx, docs)
	if err != nil {
		t.Fatalf("References failed: %v", err)
	}

	if len(refs) != 2 {
		t.Fatalf("expected 2 references, got %d", len(refs))
	}

	if refs[0].Path != "./src/app" || refs[0].Document != "manifest.yaml" {
		t.Errorf("unexpected ref[0]: %+v", refs[0])
	}
	if refs[1].Path != "./src/migrate" || refs[1].Document != "manifest.yaml" {
		t.Errorf("unexpected ref[1]: %+v", refs[1])
	}
}

func TestResolver_Resolve_Success(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	inputYAML := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
spec:
  template:
    spec:
      initContainers:
      - name: init
        image: pokkum://./src/init
      containers:
      - name: main
        image: pokkum://./src/app
`

	docs := []ports.Document{
		{Name: "deploy.yaml", Content: []byte(inputYAML)},
	}

	mockBuild := func(_ context.Context, path string) (string, error) {
		switch path {
		case "./src/init":
			return "ghcr.io/acme/init@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
		case "./src/app":
			return "ghcr.io/acme/app@sha256:2222222222222222222222222222222222222222222222222222222222222222", nil
		default:
			return "", fmt.Errorf("unknown path %s", path)
		}
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: docs,
		Build:     mockBuild,
		Strict:    true,
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(res.Documents) != 1 {
		t.Fatalf("expected 1 document in result, got %d", len(res.Documents))
	}
	if len(res.References) != 2 {
		t.Fatalf("expected 2 references in result, got %d", len(res.References))
	}

	outStr := string(res.Documents[0].Content)
	if !bytes.Contains(res.Documents[0].Content, []byte("ghcr.io/acme/init@sha256:1111111111111111111111111111111111111111111111111111111111111111")) {
		t.Errorf("output missing init container digest: %s", outStr)
	}
	if !bytes.Contains(res.Documents[0].Content, []byte("ghcr.io/acme/app@sha256:2222222222222222222222222222222222222222222222222222222222222222")) {
		t.Errorf("output missing app container digest: %s", outStr)
	}
}

func TestResolver_ByteIdenticalUnchanged(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	originalContent := []byte(`# Deployment comment preserve check
apiVersion: apps/v1
kind: Deployment
metadata:
  name: regular-app
spec:
  template:
    spec:
      containers:
      - name: web
        image: nginx:1.25.3
`)

	docs := []ports.Document{
		{Name: "regular.yaml", Content: originalContent},
	}

	mockBuild := func(_ context.Context, _ string) (string, error) {
		return "should-not-be-called@sha256:0000000000000000000000000000000000000000000000000000000000000000", nil
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: docs,
		Build:     mockBuild,
		Strict:    true,
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if !bytes.Equal(res.Documents[0].Content, originalContent) {
		t.Errorf("unmodified document was altered!\nExpected:\n%s\nGot:\n%s", string(originalContent), string(res.Documents[0].Content))
	}
}

func TestResolver_StrictTypoDetection(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	typoContent := []byte(`apiVersion: v1
kind: Pod
metadata:
  name: typo-pod
spec:
  containers:
  - name: app
    image: pokkum:/src/app
`)

	docs := []ports.Document{
		{Name: "typo.yaml", Content: typoContent},
	}

	mockBuild := func(_ context.Context, path string) (string, error) {
		return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
	}

	// With Strict: true, should fail with core.ErrManifestUnresolved
	_, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: docs,
		Build:     mockBuild,
		Strict:    true,
	})
	if err == nil {
		t.Fatal("expected error on scheme typo with Strict: true, got nil")
	}
	if !errors.Is(err, core.ErrManifestUnresolved) {
		t.Errorf("expected ErrManifestUnresolved, got %v", err)
	}

	// With Strict: false, typo is ignored
	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: docs,
		Build:     mockBuild,
		Strict:    false,
	})
	if err != nil {
		t.Fatalf("unexpected error with Strict: false: %v", err)
	}
	if len(res.References) != 0 {
		t.Errorf("expected 0 references resolved, got %d", len(res.References))
	}
}

func TestResolver_DeduplicationAndConcurrency(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	yamlDoc := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app1
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/shared
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app2
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/shared
`)

	docs := []ports.Document{
		{Name: "multi.yaml", Content: yamlDoc},
	}

	var buildCount int32
	mockBuild := func(_ context.Context, path string) (string, error) {
		atomic.AddInt32(&buildCount, 1)
		return "ghcr.io/acme/shared@sha256:3333333333333333333333333333333333333333333333333333333333333333", nil
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: docs,
		Build:     mockBuild,
		Strict:    true,
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if atomic.LoadInt32(&buildCount) != 1 {
		t.Errorf("expected build to be called exactly 1 time (deduplicated), called %d times", buildCount)
	}
	if len(res.References) != 2 {
		t.Errorf("expected 2 resolved references, got %d", len(res.References))
	}
}

func TestResolver_SecurityDefaults_InjectedWhenAbsent(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	input := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
`)

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: []ports.Document{{Name: "deploy.yaml", Content: input}},
		Build: func(_ context.Context, _ string) (string, error) {
			return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
		},
		Strict:           true,
		SecurityDefaults: true,
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	out := string(res.Documents[0].Content)

	// Pod-level defaults.
	for _, want := range []string{
		"runAsNonRoot: true",
		"seccompProfile:",
		"type: RuntimeDefault",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected pod-level default %q in output:\n%s", want, out)
		}
	}

	// Container-level defaults.
	for _, want := range []string{
		"allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true",
		"capabilities:",
		"drop:",
		"- ALL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected container-level default %q in output:\n%s", want, out)
		}
	}
}

func TestResolver_SecurityDefaults_ExplicitValuesSurvive(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	cases := []struct {
		name  string
		input string
		want  string
		unwan string
	}{
		{
			name: "pod runAsNonRoot",
			input: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      securityContext:
        runAsNonRoot: false
      containers:
      - name: main
        image: pokkum://./src/app
`,
			want:  "runAsNonRoot: false",
			unwan: "runAsNonRoot: true",
		},
		{
			name: "pod seccompProfile type",
			input: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      securityContext:
        seccompProfile:
          type: Unconfined
      containers:
      - name: main
        image: pokkum://./src/app
`,
			want:  "type: Unconfined",
			unwan: "type: RuntimeDefault",
		},
		{
			name: "container allowPrivilegeEscalation",
			input: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
        securityContext:
          allowPrivilegeEscalation: true
`,
			want:  "allowPrivilegeEscalation: true",
			unwan: "allowPrivilegeEscalation: false",
		},
		{
			name: "container capabilities drop",
			input: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
        securityContext:
          capabilities:
            drop:
            - NET_RAW
`,
			want:  "NET_RAW",
			unwan: "- ALL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := r.Resolve(ctx, ports.ResolveRequest{
				Documents: []ports.Document{{Name: "deploy.yaml", Content: []byte(tc.input)}},
				Build: func(_ context.Context, _ string) (string, error) {
					return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
				},
				Strict:           true,
				SecurityDefaults: true,
			})
			if err != nil {
				t.Fatalf("Resolve failed: %v", err)
			}

			out := string(res.Documents[0].Content)
			if !strings.Contains(out, tc.want) {
				t.Errorf("expected explicit value %q to survive in output:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.unwan) {
				t.Errorf("did not expect injected default %q to appear alongside explicit value in output:\n%s", tc.unwan, out)
			}
		})
	}
}

func TestResolver_SecurityDefaults_SidecarWithoutPokkumRefUntouched(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	input := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
      - name: sidecar
        image: envoyproxy/envoy:v1.28.0
`)

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: []ports.Document{{Name: "deploy.yaml", Content: input}},
		Build: func(_ context.Context, _ string) (string, error) {
			return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
		},
		Strict:           true,
		SecurityDefaults: true,
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	nodes, err := decodeAllDocs(res.Documents[0].Content)
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}

	sidecar := findContainerByName(t, nodes, "sidecar")
	if _, ok := mapValue(sidecar, "securityContext"); ok {
		t.Errorf("sidecar container without a pokkum:// image gained a securityContext:\n%s", res.Documents[0].Content)
	}

	main := findContainerByName(t, nodes, "main")
	if _, ok := mapValue(main, "securityContext"); !ok {
		t.Errorf("pokkum-built container did not gain a securityContext:\n%s", res.Documents[0].Content)
	}

	// The pod is still hardened at the pod level, even though one of its
	// containers is an arbitrary sidecar the resolver knows nothing about.
	if !strings.Contains(string(res.Documents[0].Content), "runAsNonRoot: true") {
		t.Errorf("expected pod-level defaults despite untouched sidecar:\n%s", res.Documents[0].Content)
	}
}

func TestResolver_SecurityDefaults_DisabledSuppressesInjection(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	input := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
`)

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: []ports.Document{{Name: "deploy.yaml", Content: input}},
		Build: func(_ context.Context, _ string) (string, error) {
			return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
		},
		Strict:           true,
		SecurityDefaults: false,
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	out := string(res.Documents[0].Content)
	if strings.Contains(out, "securityContext") {
		t.Errorf("SecurityDefaults: false must suppress all injection, got:\n%s", out)
	}
}

func TestResolver_SecurityDefaults_NoRefsRoundTripsByteIdentical(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	originalContent := []byte(`# Deployment comment preserve check
apiVersion: apps/v1
kind: Deployment
metadata:
  name: regular-app
spec:
  template:
    spec:
      containers:
      - name: web
        image: nginx:1.25.3
`)

	docs := []ports.Document{
		{Name: "regular.yaml", Content: originalContent},
	}

	mockBuild := func(_ context.Context, _ string) (string, error) {
		return "should-not-be-called@sha256:0000000000000000000000000000000000000000000000000000000000000000", nil
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        docs,
		Build:            mockBuild,
		Strict:           true,
		SecurityDefaults: true,
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if !bytes.Equal(res.Documents[0].Content, originalContent) {
		t.Errorf("document with no pokkum:// refs was altered with SecurityDefaults enabled!\nExpected:\n%s\nGot:\n%s", string(originalContent), string(res.Documents[0].Content))
	}
}

// decodeAllDocs parses every YAML document in content into its root mapping
// node (unwrapping the yaml.DocumentNode wrapper), for tests that need to
// walk the structure rather than substring-match the rendered text.
func decodeAllDocs(content []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		docs = append(docs, n.Content[0])
	}
	return docs, nil
}

// mapValue looks up key in a YAML mapping node.
func mapValue(m *yaml.Node, key string) (*yaml.Node, bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1], true
		}
	}
	return nil, false
}

// findContainerByName walks every document looking for a container mapping
// (a member of a "containers" sequence) whose "name" field equals name.
func findContainerByName(t *testing.T, docs []*yaml.Node, name string) *yaml.Node {
	t.Helper()
	var found *yaml.Node
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || found != nil {
			return
		}
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if k.Value == "containers" && v.Kind == yaml.SequenceNode {
					for _, item := range v.Content {
						if nameVal, ok := mapValue(item, "name"); ok && nameVal.Value == name {
							found = item
							return
						}
					}
				}
				walk(v)
			}
		case yaml.SequenceNode:
			for _, item := range n.Content {
				walk(item)
			}
		}
	}
	for _, d := range docs {
		walk(d)
	}
	if found == nil {
		t.Fatalf("container %q not found in decoded document", name)
	}
	return found
}

func TestResolver_Errors(t *testing.T) {
	t.Parallel()

	r := k8s.NewResolver()
	ctx := context.Background()

	t.Run("nil builder", func(t *testing.T) {
		_, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents: []ports.Document{{Name: "doc.yaml", Content: []byte("foo: bar")}},
			Build:     nil,
		})
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for nil builder, got %v", err)
		}
	})

	t.Run("no documents", func(t *testing.T) {
		_, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents: nil,
			Build: func(_ context.Context, _ string) (string, error) {
				return "", nil
			},
		})
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for empty documents, got %v", err)
		}
	})

	t.Run("invalid YAML", func(t *testing.T) {
		_, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents: []ports.Document{{Name: "bad.yaml", Content: []byte("kind: [bad yaml")}},
			Build: func(_ context.Context, _ string) (string, error) {
				return "", nil
			},
		})
		if !errors.Is(err, core.ErrManifestInvalid) {
			t.Errorf("expected ErrManifestInvalid for bad YAML, got %v", err)
		}
	})

	t.Run("build failure", func(t *testing.T) {
		doc := ports.Document{
			Name:    "app.yaml",
			Content: []byte("image: pokkum://./src/fail"),
		}
		_, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents: []ports.Document{doc},
			Build: func(_ context.Context, _ string) (string, error) {
				return "", errors.New("compilation error")
			},
		})
		if !errors.Is(err, core.ErrManifestUnresolved) {
			t.Errorf("expected ErrManifestUnresolved for build failure, got %v", err)
		}
	})

	t.Run("non-digest build return", func(t *testing.T) {
		doc := ports.Document{
			Name:    "app.yaml",
			Content: []byte("image: pokkum://./src/tag"),
		}
		_, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents: []ports.Document{doc},
			Build: func(_ context.Context, _ string) (string, error) {
				return "ghcr.io/acme/app:v1.0.0", nil // Tag, not digest!
			},
		})
		if !errors.Is(err, core.ErrManifestUnresolved) {
			t.Errorf("expected ErrManifestUnresolved for tag return, got %v", err)
		}
	})

	t.Run("with otel sidecar", func(t *testing.T) {
		doc := ports.Document{
			Name: "deploy.yaml",
			Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
`),
		}
		res, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents:       []ports.Document{doc},
			WithOTELSidecar: true,
			Build: func(_ context.Context, _ string) (string, error) {
				return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		output := string(res.Documents[0].Content)
		if !strings.Contains(output, "otel-collector") {
			t.Errorf("expected otel-collector sidecar container in output:\n%s", output)
		}
		if !strings.Contains(output, "4318") {
			t.Errorf("expected OTLP HTTP port 4318 in output:\n%s", output)
		}
	})

	t.Run("with network policy and resource defaults", func(t *testing.T) {
		doc := ports.Document{
			Name: "deploy.yaml",
			Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  selector:
    matchLabels:
      app: app
  template:
    metadata:
      labels:
        app: app
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
`),
		}
		res, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents:        []ports.Document{doc},
			NetworkPolicy:    true,
			ResourceDefaults: true,
			Build: func(_ context.Context, _ string) (string, error) {
				return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Documents) != 3 {
			t.Fatalf("expected 3 documents (deployment + netpol + pdb), got %d", len(res.Documents))
		}
		mainDoc := string(res.Documents[0].Content)
		if !strings.Contains(mainDoc, "resources:") || !strings.Contains(mainDoc, "50m") || !strings.Contains(mainDoc, "256Mi") {
			t.Errorf("expected resources injected into main document:\n%s", mainDoc)
		}
		netPolDoc := string(res.Documents[1].Content)
		if !strings.Contains(netPolDoc, "kind: NetworkPolicy") || !strings.Contains(netPolDoc, "pokkum-network-policy") {
			t.Errorf("expected NetworkPolicy in document 1:\n%s", netPolDoc)
		}
		pdbDoc := string(res.Documents[2].Content)
		if !strings.Contains(pdbDoc, "kind: PodDisruptionBudget") || !strings.Contains(pdbDoc, "pokkum-pdb") {
			t.Errorf("expected PodDisruptionBudget in document 2:\n%s", pdbDoc)
		}
		if !strings.Contains(pdbDoc, `app: "app"`) {
			t.Errorf("expected PodDisruptionBudget selector scoped to workload label app=app, not empty:\n%s", pdbDoc)
		}
		if strings.Contains(pdbDoc, "matchLabels: {}") {
			t.Errorf("PodDisruptionBudget selector must not be the namespace-wide empty selector:\n%s", pdbDoc)
		}
		if !strings.Contains(netPolDoc, `app: "app"`) || strings.Contains(netPolDoc, "podSelector: {}") {
			t.Errorf("expected NetworkPolicy podSelector scoped to workload label app=app, not empty:\n%s", netPolDoc)
		}
	})

	t.Run("resource defaults without extractable labels skips PDB", func(t *testing.T) {
		doc := ports.Document{
			Name: "deploy-nolabels.yaml",
			Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
`),
		}
		res, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents:        []ports.Document{doc},
			ResourceDefaults: true,
			Build: func(_ context.Context, _ string) (string, error) {
				return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(res.Documents) != 1 {
			t.Fatalf("expected PDB generation to be skipped when no workload labels are found, got %d documents", len(res.Documents))
		}
	})
}

func TestResolve_NetworkPolicyDynamicPortsAndRestrictedEgress(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deployment.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://.
        ports:
        - containerPort: 8080
        - containerPort: 9090
`),
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:     []ports.Document{doc},
		NetworkPolicy: true,
		Build: func(_ context.Context, _ string) (string, error) {
			return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.Documents) != 2 {
		t.Fatalf("expected 2 documents (deployment + netpol), got %d", len(res.Documents))
	}

	netPolDoc := string(res.Documents[1].Content)

	// Check dynamic ingress ports 8080 and 9090
	if !strings.Contains(netPolDoc, "port: 8080") || !strings.Contains(netPolDoc, "port: 9090") {
		t.Errorf("expected NetworkPolicy ingress to contain dynamic container ports 8080 and 9090:\n%s", netPolDoc)
	}

	// Check restricted egress (DNS 53, HTTPS 443, OTLP 4317, 4318)
	if !strings.Contains(netPolDoc, "port: 53") || !strings.Contains(netPolDoc, "port: 443") || !strings.Contains(netPolDoc, "port: 4317") {
		t.Errorf("expected NetworkPolicy egress to contain restricted ports 53, 443, 4317:\n%s", netPolDoc)
	}

	// Verify absence of unrestricted wildcard egress "- {}"
	if strings.Contains(netPolDoc, "- {}") {
		t.Errorf("NetworkPolicy must NOT contain unrestricted wildcard egress:\n%s", netPolDoc)
	}
}

// TestResolver_PreviousImageAnnotation_RealRedeploy exercises the real
// writer path for AnnotationCurrentImage/AnnotationPreviousImage/
// AnnotationImageHistory: each resolve call's OUTPUT annotations get
// spliced onto a FRESH copy of the pokkum://-templated source for the next
// call, simulating the one workflow shape that can actually carry this
// state forward — annotations persisted by the caller (e.g. read back from
// the live cluster before an apply, or carried in a tracked deployed-state
// file) — while image: itself stays pokkum:// in the tracked source, per
// Vocabulary.md's documented convention.
//
// A prior version of this test seeded pokkum.dev/previous-image directly
// in the input and only checked it survived round-tripping unchanged —
// that passed even when Resolve's writer was unreachable dead code (see
// AnnotationCurrentImage's doc comment in internal/ports/k8s.go). This
// version proves the writer itself accumulates real history across calls.
//
// IMPORTANT LIMITATION this test also documents: feeding resolve's own
// output (image: now a concrete digest) straight back in as the next
// call's input does NOT work — hasPokkum goes false once image: is no
// longer pokkum://, so the whole document is passed through untouched on
// every subsequent call (see TestResolver_ReResolvingOwnOutputIsANoOp
// below). Resolve alone, run repeatedly against Pokkum's own recommended
// permanent-template workflow with no external state, cannot accumulate
// multi-generation history — only something that separately persists and
// re-supplies annotations (a future apply-time kubectl-get, or a
// caller-managed deployed-state file) can. Tracked as a Roadmap next step.
func TestResolver_PreviousImageAnnotation_RealRedeploy(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	template := func(annotationsBlock string) []byte {
		return []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app` + annotationsBlock + `
spec:
  template:
    metadata:
      labels:
        app: web-app
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
`)
	}

	digests := []string{
		"ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"ghcr.io/acme/app@sha256:2222222222222222222222222222222222222222222222222222222222222222",
		"ghcr.io/acme/app@sha256:3333333333333333333333333333333333333333333333333333333333333333",
	}

	annotationsBlock := ""
	var lastOut string
	for _, digest := range digests {
		d := digest
		res, err := r.Resolve(ctx, ports.ResolveRequest{
			Documents: []ports.Document{{Name: "deploy.yaml", Content: template(annotationsBlock)}},
			Build: func(_ context.Context, _ string) (string, error) {
				return d, nil
			},
		})
		if err != nil {
			t.Fatalf("resolve to %s: unexpected error: %v", d, err)
		}
		lastOut = string(res.Documents[0].Content)

		// Carry only the top-level annotations block forward to the next
		// iteration's source, simulating a caller that persists annotations
		// (e.g. from the live cluster) but keeps image: as pokkum:// in the
		// tracked template.
		ann := regexp.MustCompile(`(?s)\n  annotations:\n(?:    .*\n)+`)
		if m := ann.FindString(lastOut); m != "" {
			annotationsBlock = m[strings.Index(m, "\n"):]
		}
	}

	if !strings.Contains(lastOut, "image: "+digests[2]) {
		t.Fatalf("expected image: to be the most recent digest %s, got:\n%s", digests[2], lastOut)
	}
	if !strings.Contains(lastOut, "pokkum.dev/current-image:") || !strings.Contains(lastOut, digests[2]) {
		t.Errorf("expected pokkum.dev/current-image to record the latest digest, got:\n%s", lastOut)
	}
	if !strings.Contains(lastOut, "pokkum.dev/previous-image:") || !strings.Contains(lastOut, digests[1]) {
		t.Errorf("expected pokkum.dev/previous-image to record %s (the one just displaced), got:\n%s", digests[1], lastOut)
	}
	if !strings.Contains(lastOut, "pokkum.dev/image-history:") || !strings.Contains(lastOut, digests[0]) {
		t.Errorf("expected pokkum.dev/image-history to contain %s (the redeploy before that), got:\n%s", digests[0], lastOut)
	}
}

// TestResolver_ReResolvingOwnOutputIsANoOp documents (see the long comment
// on the test above) that feeding a resolve call's own output — where
// image: is now a concrete digest, not pokkum:// — back in as the next
// call's input is a silent no-op: the document has no pokkum:// refs left,
// so hasPokkum is false and it passes through byte-identical. This is
// existing, correct behavior (Resolve must not touch a document it finds
// no pokkum:// references in), pinned here specifically so it isn't
// mistaken for a bug, and so a future change to make repeated resolves
// self-accumulate history doesn't silently break the invariant it depends on
// today: only a pokkum://-templated image: field is ever rewritten.
func TestResolver_ReResolvingOwnOutputIsANoOp(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{Name: "deploy.yaml", Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
spec:
  template:
    spec:
      containers:
      - name: main
        image: pokkum://./src/app
`)}

	first, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: []ports.Document{doc},
		Build: func(_ context.Context, _ string) (string, error) {
			return "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
		},
	})
	if err != nil {
		t.Fatalf("first resolve: unexpected error: %v", err)
	}

	second, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents: []ports.Document{{Name: "deploy.yaml", Content: first.Documents[0].Content}},
		Build: func(_ context.Context, _ string) (string, error) {
			return "ghcr.io/acme/app@sha256:2222222222222222222222222222222222222222222222222222222222222222", nil
		},
	})
	if err != nil {
		t.Fatalf("second resolve: unexpected error: %v", err)
	}

	if string(second.Documents[0].Content) != string(first.Documents[0].Content) {
		t.Errorf("expected re-resolving an already-concrete-image document to be a no-op, but content changed:\nfirst:\n%s\nsecond:\n%s",
			first.Documents[0].Content, second.Documents[0].Content)
	}
}

func TestResolver_ClusterInspector_SeedsLiveHistory(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	// Clean static template with no annotations
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
        image: pokkum://./src/app
`),
	}

	liveImage1 := "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	liveImage0 := "ghcr.io/acme/app@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	newImage2 := "ghcr.io/acme/app@sha256:2222222222222222222222222222222222222222222222222222222222222222"

	inspectorCalled := false
	inspector := func(_ context.Context, kind, name, ns string) (ports.ClusterWorkloadState, error) {
		inspectorCalled = true
		if kind != "Deployment" || name != "web-app" || ns != "prod" {
			t.Errorf("unexpected inspector args: kind=%s, name=%s, ns=%s", kind, name, ns)
		}
		return ports.ClusterWorkloadState{
			Annotations: map[string]string{
				ports.AnnotationCurrentImage:  liveImage1,
				ports.AnnotationPreviousImage: liveImage0,
				ports.AnnotationImageHistory:  liveImage0,
			},
		}, nil
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		ClusterInspector: inspector,
		Build: func(_ context.Context, _ string) (string, error) {
			return newImage2, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}
	if !inspectorCalled {
		t.Errorf("expected ClusterInspector to be invoked")
	}

	out := string(res.Documents[0].Content)
	if !strings.Contains(out, "image: "+newImage2) {
		t.Errorf("expected image to be %s, got:\n%s", newImage2, out)
	}
	if !strings.Contains(out, "pokkum.dev/current-image: "+newImage2) {
		t.Errorf("expected current-image to be %s, got:\n%s", newImage2, out)
	}
	if !strings.Contains(out, "pokkum.dev/previous-image: "+liveImage1) {
		t.Errorf("expected previous-image to be %s, got:\n%s", liveImage1, out)
	}
	expectedHistory := liveImage1 + "," + liveImage0
	if !strings.Contains(out, "pokkum.dev/image-history: "+expectedHistory) {
		t.Errorf("expected image-history to be %s, got:\n%s", expectedHistory, out)
	}
}

func TestResolver_ClusterInspector_SeedsLiveContainerFallback(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api-service
spec:
  template:
    spec:
      containers:
      - name: api
        image: pokkum://./src/api
`),
	}

	legacyRunningImage := "docker.io/legacy/api:1.0.0"
	newBuiltImage := "ghcr.io/acme/api@sha256:3333333333333333333333333333333333333333333333333333333333333333"

	inspector := func(_ context.Context, _, _, _ string) (ports.ClusterWorkloadState, error) {
		return ports.ClusterWorkloadState{
			Containers: map[string]string{
				"api": legacyRunningImage,
			},
		}, nil
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		ClusterInspector: inspector,
		Build: func(_ context.Context, _ string) (string, error) {
			return newBuiltImage, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	out := string(res.Documents[0].Content)
	if !strings.Contains(out, "pokkum.dev/previous-image: "+legacyRunningImage) {
		t.Errorf("expected fallback previous-image from live container %s, got:\n%s", legacyRunningImage, out)
	}
	if !strings.Contains(out, "pokkum.dev/image-history: "+legacyRunningImage) {
		t.Errorf("expected fallback image-history %s, got:\n%s", legacyRunningImage, out)
	}
}

func TestResolver_ClusterInspector_NotFoundGraceful(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: new-service
spec:
  template:
    spec:
      containers:
      - name: web
        image: pokkum://./src/web
`),
	}

	newBuiltImage := "ghcr.io/acme/web@sha256:4444444444444444444444444444444444444444444444444444444444444444"

	inspector := func(_ context.Context, _, _, _ string) (ports.ClusterWorkloadState, error) {
		return ports.ClusterWorkloadState{}, fmt.Errorf("deployments.apps %q not found", "new-service")
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		ClusterInspector: inspector,
		Build: func(_ context.Context, _ string) (string, error) {
			return newBuiltImage, nil
		},
	})
	if err != nil {
		t.Fatalf("expected resolve to succeed gracefully on not found: %v", err)
	}

	out := string(res.Documents[0].Content)
	if !strings.Contains(out, "pokkum.dev/current-image: "+newBuiltImage) {
		t.Errorf("expected current-image %s, got:\n%s", newBuiltImage, out)
	}
	if strings.Contains(out, "pokkum.dev/previous-image:") {
		t.Errorf("expected no previous-image on first deployment, got:\n%s", out)
	}
}

func TestResolver_ClusterInspector_LocalAnnotationsTakePrecedence(t *testing.T) {
	r := k8s.NewResolver()
	ctx := context.Background()

	localCurrentImage := "ghcr.io/acme/web@sha256:9999999999999999999999999999999999999999999999999999999999999999"
	doc := ports.Document{
		Name: "deploy.yaml",
		Content: []byte(fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
  annotations:
    pokkum.dev/current-image: %s
spec:
  template:
    spec:
      containers:
      - name: web
        image: pokkum://./src/web
`, localCurrentImage)),
	}

	clusterImage := "ghcr.io/acme/web@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newBuiltImage := "ghcr.io/acme/web@sha256:5555555555555555555555555555555555555555555555555555555555555555"

	inspector := func(_ context.Context, _, _, _ string) (ports.ClusterWorkloadState, error) {
		return ports.ClusterWorkloadState{
			Annotations: map[string]string{
				ports.AnnotationCurrentImage: clusterImage,
			},
		}, nil
	}

	res, err := r.Resolve(ctx, ports.ResolveRequest{
		Documents:        []ports.Document{doc},
		ClusterInspector: inspector,
		Build: func(_ context.Context, _ string) (string, error) {
			return newBuiltImage, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	out := string(res.Documents[0].Content)
	if !strings.Contains(out, "pokkum.dev/previous-image: "+localCurrentImage) {
		t.Errorf("expected local explicit annotation %s to take precedence over cluster %s, got:\n%s", localCurrentImage, clusterImage, out)
	}
}
