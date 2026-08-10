package k8s_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
}
