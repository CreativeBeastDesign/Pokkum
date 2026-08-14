// Package k8s implements ports.Resolver by parsing Kubernetes manifests
// (YAML) and resolving pokkum:// image references to immutable digest references.
package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var _ ports.Resolver = (*Resolver)(nil)

// Resolver implements ports.Resolver.
type Resolver struct{}

// NewResolver constructs a new Kubernetes manifest resolver.
func NewResolver() *Resolver {
	return &Resolver{}
}

type imageNodeTarget struct {
	keyNode *yaml.Node
	valNode *yaml.Node

	// containerNode is the mapping that directly holds the "image" key — the
	// container object itself (v1.Container / v1.EphemeralContainer), where
	// container-level securityContext defaults are injected as a sibling of
	// "image".
	containerNode *yaml.Node

	// podSpecNode is the nearest ancestor mapping that holds a "containers",
	// "initContainers" or "ephemeralContainers" key whose value is the
	// sequence containing containerNode — i.e. the Pod spec (v1.PodSpec) —
	// where pod-level securityContext defaults are injected. Nil when no such
	// ancestor was found (a container-shaped mapping outside any recognisable
	// Pod spec), in which case pod-level injection is skipped for it.
	podSpecNode *yaml.Node

	// templateNode is the nearest ancestor mapping that owns both a
	// "metadata" and a "spec" key and contains podSpecNode somewhere beneath
	// its "spec" — i.e. the Pod template (v1.PodTemplateSpec) for a
	// Deployment/StatefulSet/DaemonSet/Job, or the Pod object itself for a
	// bare Pod. Its "metadata.labels" are the labels the running Pod will
	// actually carry, which is what NetworkPolicy/PodDisruptionBudget
	// selectors need to scope to this workload instead of the whole
	// namespace. Nil when no such ancestor was found.
	templateNode *yaml.Node
}

// parseYAMLDocuments decodes a byte slice into individual YAML AST document nodes.
func parseYAMLDocuments(content []byte) ([]*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var nodes []*yaml.Node
	for {
		var node yaml.Node
		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, &node)
	}
	return nodes, nil
}

// encodeYAMLNodes serializes YAML AST document nodes back into bytes.
func encodeYAMLNodes(nodes []*yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	for _, n := range nodes {
		if err := encoder.Encode(n); err != nil {
			_ = encoder.Close()
			return nil, err
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// containersKeys names the PodSpec fields that hold a sequence of container
// objects. A mapping node that owns one of these keys is treated as the Pod
// spec for every container nested beneath it — this is how pod-level
// securityContext defaults find their home regardless of whether the
// enclosing kind is a bare Pod, a Deployment's template, a CronJob's
// jobTemplate, or an Argo Rollout.
func isContainersKey(k string) bool {
	switch k {
	case "containers", "initContainers", "ephemeralContainers":
		return true
	default:
		return false
	}
}

// findImageNodes recursively scans a YAML AST node for mapping entries with
// key "image", recording both the enclosing container mapping and the
// nearest ancestor Pod spec mapping for each one.
func findImageNodes(node *yaml.Node) []imageNodeTarget {
	var targets []imageNodeTarget
	var walk func(n *yaml.Node, podSpec *yaml.Node, template *yaml.Node)
	walk = func(n *yaml.Node, podSpec *yaml.Node, template *yaml.Node) {
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.DocumentNode:
			for _, item := range n.Content {
				walk(item, podSpec, template)
			}
		case yaml.MappingNode:
			// A mapping that owns both "metadata" and "spec" is a
			// PodTemplateSpec-shaped object — the Pod template nested under
			// a Deployment/StatefulSet/DaemonSet/(Cron)Job, or a bare Pod
			// itself. Re-evaluated at every level so the innermost match
			// (the actual template, not an outer wrapper like a CronJob's
			// jobTemplate) wins by the time we reach the containers.
			childTemplate := template
			if _, hasMeta := mapGet(n, "metadata"); hasMeta {
				if _, hasSpec := mapGet(n, "spec"); hasSpec {
					childTemplate = n
				}
			}
			for i := 0; i < len(n.Content)-1; i += 2 {
				keyNode := n.Content[i]
				valNode := n.Content[i+1]
				if keyNode.Kind == yaml.ScalarNode && keyNode.Value == "image" {
					targets = append(targets, imageNodeTarget{
						keyNode:       keyNode,
						valNode:       valNode,
						containerNode: n,
						podSpecNode:   podSpec,
						templateNode:  childTemplate,
					})
				}
				childPodSpec := podSpec
				if keyNode.Kind == yaml.ScalarNode && isContainersKey(keyNode.Value) && valNode.Kind == yaml.SequenceNode {
					childPodSpec = n
				}
				walk(valNode, childPodSpec, childTemplate)
			}
		case yaml.SequenceNode:
			for _, item := range n.Content {
				walk(item, podSpec, template)
			}
		case yaml.AliasNode:
			if n.Alias != nil {
				walk(n.Alias, podSpec, template)
			}
		}
	}
	walk(node, nil, nil)
	return targets
}

// parsePokkumRef determines if val is a valid pokkum:// reference or a typo (when strict is true).
func parsePokkumRef(val string, strict bool) (path string, isRef bool, err error) {
	if strings.HasPrefix(val, ports.Scheme) {
		path = strings.TrimPrefix(val, ports.Scheme)
		return path, true, nil
	}
	if strict {
		lower := strings.ToLower(val)
		if strings.HasPrefix(lower, "pokkum:") || strings.HasPrefix(lower, "pokkum/") {
			return "", false, errors.New("malformed pokkum scheme reference")
		}
	}
	return "", false, nil
}

// mapGet looks up key in a YAML mapping node and reports whether it was
// present. It is nil-safe and returns false for anything that is not a
// mapping.
func mapGet(m *yaml.Node, key string) (*yaml.Node, bool) {
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

// mapAppend adds a key/value pair to the end of a mapping node's content,
// after everything the manifest author already wrote. Appending rather than
// inserting is what keeps an injected default from reordering or otherwise
// disturbing the fields the resolver did not need to touch.
func mapAppend(m *yaml.Node, key string, val *yaml.Node) {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	m.Content = append(m.Content, keyNode, val)
}

// ensureChildMapping returns the mapping node at key under parent, creating
// an empty one and appending it if key is entirely absent. If key is present
// but its value is not itself a mapping (an odd but user-written value, e.g.
// "securityContext: null"), it returns nil rather than clobbering whatever
// the user wrote — callers must treat nil as "nothing to fill in here".
func ensureChildMapping(parent *yaml.Node, key string) *yaml.Node {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return nil
	}
	if v, ok := mapGet(parent, key); ok {
		if v.Kind != yaml.MappingNode {
			return nil
		}
		return v
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapAppend(parent, key, child)
	return child
}

// ensureBoolDefault sets key to value only when key is entirely absent from
// parent. An explicit user value — including an explicit false — is never
// overwritten.
func ensureBoolDefault(parent *yaml.Node, key string, value bool) {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return
	}
	if _, ok := mapGet(parent, key); ok {
		return
	}
	mapAppend(parent, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)})
}

// ensureStringDefault sets key to value only when key is entirely absent from
// parent.
func ensureStringDefault(parent *yaml.Node, key, value string) {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return
	}
	if _, ok := mapGet(parent, key); ok {
		return
	}
	mapAppend(parent, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// ensureStringListDefault sets key to a sequence of values only when key is
// entirely absent from parent — so an existing "capabilities.drop: [NET_RAW]"
// is left exactly as written, even though it differs from our default.
func ensureStringListDefault(parent *yaml.Node, key string, values ...string) {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return
	}
	if _, ok := mapGet(parent, key); ok {
		return
	}
	items := make([]*yaml.Node, len(values))
	for i, v := range values {
		items[i] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	}
	mapAppend(parent, key, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items})
}

// setMappingKey sets key in mapping m to val, overwriting if present, or appending if absent.
func setMappingKey(m *yaml.Node, key, val string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].Value = val
			m.Content[i+1].Tag = "!!str"
			return
		}
	}
	mapAppend(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
}

// setAnnotation sets key to val under parent's metadata.annotations mapping.
func setAnnotation(parent *yaml.Node, key, val string) {
	if parent == nil {
		return
	}
	target := parent
	if parent.Kind == yaml.DocumentNode {
		for _, item := range parent.Content {
			if item.Kind == yaml.MappingNode {
				target = item
				break
			}
		}
	}
	if target == nil || target.Kind != yaml.MappingNode {
		return
	}
	meta := ensureChildMapping(target, "metadata")
	if meta == nil {
		return
	}
	ann := ensureChildMapping(meta, "annotations")
	if ann == nil {
		return
	}
	setMappingKey(ann, key, val)
}

// getAnnotation returns the string annotation value under parent's metadata.annotations mapping, or "".
func getAnnotation(parent *yaml.Node, key string) string {
	if parent == nil {
		return ""
	}
	target := parent
	if parent.Kind == yaml.DocumentNode {
		for _, item := range parent.Content {
			if item.Kind == yaml.MappingNode {
				target = item
				break
			}
		}
	}
	if target == nil || target.Kind != yaml.MappingNode {
		return ""
	}
	metaNode, ok := mapGet(target, "metadata")
	if !ok || metaNode.Kind != yaml.MappingNode {
		return ""
	}
	annNode, ok := mapGet(metaNode, "annotations")
	if !ok || annNode.Kind != yaml.MappingNode {
		return ""
	}
	valNode, ok := mapGet(annNode, key)
	if !ok {
		return ""
	}
	return valNode.Value
}

// getDocKindAndMeta extracts the kind, name, and namespace from a root document mapping.
func getDocKindAndMeta(docRoot *yaml.Node) (kind, name, namespace string) {
	if docRoot == nil {
		return "", "", ""
	}
	target := docRoot
	if docRoot.Kind == yaml.DocumentNode {
		for _, item := range docRoot.Content {
			if item.Kind == yaml.MappingNode {
				target = item
				break
			}
		}
	}
	if target == nil || target.Kind != yaml.MappingNode {
		return "", "", ""
	}
	if kNode, ok := mapGet(target, "kind"); ok && kNode.Kind == yaml.ScalarNode {
		kind = kNode.Value
	}
	if metaNode, ok := mapGet(target, "metadata"); ok && metaNode.Kind == yaml.MappingNode {
		if nNode, ok := mapGet(metaNode, "name"); ok && nNode.Kind == yaml.ScalarNode {
			name = nNode.Value
		}
		if nsNode, ok := mapGet(metaNode, "namespace"); ok && nsNode.Kind == yaml.ScalarNode {
			namespace = nsNode.Value
		}
	}
	return kind, name, namespace
}

// updateImageHistory tracks multi-generation previous image history in annotations.
func updateImageHistory(parent *yaml.Node, newDisplaced string) {
	if parent == nil || newDisplaced == "" {
		return
	}
	setAnnotation(parent, ports.AnnotationPreviousImage, newDisplaced)

	existing := getAnnotation(parent, ports.AnnotationImageHistory)
	var history []string
	if existing != "" {
		for _, entry := range strings.Split(existing, ",") {
			e := strings.TrimSpace(entry)
			if e != "" && e != newDisplaced {
				history = append(history, e)
			}
		}
	}
	history = append([]string{newDisplaced}, history...)
	if len(history) > 20 {
		history = history[:20]
	}
	setAnnotation(parent, ports.AnnotationImageHistory, strings.Join(history, ","))
}

// injectPodSecurityDefaults fills in the pod-level hardened defaults on
// podSpec: runAsNonRoot and the RuntimeDefault seccomp profile. It never
// injects runAsUser — Pokkum images already run as UID 65532 baked into the
// image config, and stamping a runAsUser here could conflict with an image
// that legitimately wants a different one.
func injectPodSecurityDefaults(podSpec *yaml.Node) {
	sc := ensureChildMapping(podSpec, "securityContext")
	if sc == nil {
		return
	}
	ensureBoolDefault(sc, "runAsNonRoot", true)
	if sp := ensureChildMapping(sc, "seccompProfile"); sp != nil {
		ensureStringDefault(sp, "type", "RuntimeDefault")
	}
}

// injectContainerSecurityDefaults fills in the container-level hardened
// defaults on container: disabling privilege escalation and dropping all
// Linux capabilities. Callers must only call this for a container whose
// image came from a pokkum:// reference — never for an arbitrary sidecar,
// whose requirements the resolver cannot know.
func injectContainerSecurityDefaults(container *yaml.Node) {
	sc := ensureChildMapping(container, "securityContext")
	if sc == nil {
		return
	}
	ensureBoolDefault(sc, "allowPrivilegeEscalation", false)
	ensureBoolDefault(sc, "readOnlyRootFilesystem", true)
	if caps := ensureChildMapping(sc, "capabilities"); caps != nil {
		ensureStringListDefault(caps, "drop", "ALL")
	}
}

// References scans documents and reports every pokkum:// occurrence without building anything.
func (r *Resolver) References(_ context.Context, docs []ports.Document) ([]ports.Reference, error) {
	var refs []ports.Reference
	for _, doc := range docs {
		nodes, err := parseYAMLDocuments(doc.Content)
		if err != nil {
			return nil, fmt.Errorf("k8s: document %s: parse yaml: %w: %w", doc.Name, err, core.ErrManifestInvalid)
		}
		for _, node := range nodes {
			targets := findImageNodes(node)
			for _, target := range targets {
				if target.valNode.Kind != yaml.ScalarNode {
					continue
				}
				val := target.valNode.Value
				if strings.HasPrefix(val, ports.Scheme) {
					path := strings.TrimPrefix(val, ports.Scheme)
					refs = append(refs, ports.Reference{
						Document: doc.Name,
						Path:     path,
						Resolved: "",
					})
				}
			}
		}
	}
	return refs, nil
}

// Resolve rewrites every pokkum:// reference in req.Documents to a concrete digest reference.
func (r *Resolver) Resolve(ctx context.Context, req ports.ResolveRequest) (ports.ResolveResult, error) {
	if req.Build == nil {
		return ports.ResolveResult{}, fmt.Errorf("k8s: nil image builder: %w", core.ErrInvalidRequest)
	}
	if len(req.Documents) == 0 {
		return ports.ResolveResult{}, fmt.Errorf("k8s: no documents provided: %w", core.ErrInvalidRequest)
	}

	type docParseResult struct {
		doc       ports.Document
		nodes     []*yaml.Node
		targets   []imageNodeTarget
		hasPokkum bool
	}

	parsedDocs := make([]docParseResult, len(req.Documents))
	distinctPathsMap := make(map[string]struct{})

	for i, doc := range req.Documents {
		nodes, err := parseYAMLDocuments(doc.Content)
		if err != nil {
			return ports.ResolveResult{}, fmt.Errorf("k8s: document %s: parse yaml: %w: %w", doc.Name, err, core.ErrManifestInvalid)
		}

		var docTargets []imageNodeTarget
		var hasPokkum bool

		for _, node := range nodes {
			targets := findImageNodes(node)
			for _, target := range targets {
				if target.valNode.Kind != yaml.ScalarNode {
					continue
				}
				val := target.valNode.Value
				path, isRef, err := parsePokkumRef(val, req.Strict)
				if err != nil {
					return ports.ResolveResult{}, fmt.Errorf("k8s: document %s: invalid image ref %q: %w", doc.Name, val, core.ErrManifestUnresolved)
				}
				if isRef {
					if path == "" {
						return ports.ResolveResult{}, fmt.Errorf("k8s: document %s: empty project path in %q: %w", doc.Name, val, core.ErrManifestUnresolved)
					}
					hasPokkum = true
					docTargets = append(docTargets, target)
					distinctPathsMap[path] = struct{}{}
				}
			}
		}

		parsedDocs[i] = docParseResult{
			doc:       doc,
			nodes:     nodes,
			targets:   docTargets,
			hasPokkum: hasPokkum,
		}
	}

	distinctPaths := make([]string, 0, len(distinctPathsMap))
	for p := range distinctPathsMap {
		distinctPaths = append(distinctPaths, p)
	}

	resolvedMap := make(map[string]string)
	var g errgroup.Group
	var mu sync.Mutex

	for _, path := range distinctPaths {
		p := path
		g.Go(func() error {
			ref, err := req.Build(ctx, p)
			if err != nil {
				return fmt.Errorf("build %q: %w", p, err)
			}
			if !strings.Contains(ref, "@sha256:") {
				return fmt.Errorf("build %q returned non-digest reference %q", p, ref)
			}
			mu.Lock()
			resolvedMap[p] = ref
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		for _, pd := range parsedDocs {
			for _, t := range pd.targets {
				val := t.valNode.Value
				path, _, _ := parsePokkumRef(val, false)
				if strings.Contains(err.Error(), fmt.Sprintf("%q", path)) {
					return ports.ResolveResult{}, fmt.Errorf("k8s: document %s: %w: %w", pd.doc.Name, err, core.ErrManifestUnresolved)
				}
			}
		}
		return ports.ResolveResult{}, fmt.Errorf("k8s: %w: %w", err, core.ErrManifestUnresolved)
	}

	resultDocs := make([]ports.Document, len(req.Documents))
	var references []ports.Reference

	for i, pd := range parsedDocs {
		if !pd.hasPokkum {
			resultDocs[i] = pd.doc
			continue
		}

		var docRoot *yaml.Node
		if len(pd.nodes) > 0 {
			docRoot = pd.nodes[0]
		}

		// Live cluster annotation inspection: if inspector is configured, query cluster state
		// for this workload before resolving image references.
		var clusterState ports.ClusterWorkloadState
		if req.ClusterInspector != nil && docRoot != nil {
			kind, name, ns := getDocKindAndMeta(docRoot)
			if kind != "" && name != "" {
				if st, err := req.ClusterInspector(ctx, kind, name, ns); err == nil {
					clusterState = st
				}
			}
		}

		var containerNodes []*yaml.Node
		var templateNodes []*yaml.Node
		seenTemplates := make(map[*yaml.Node]bool)
		for _, t := range pd.targets {
			if t.containerNode != nil {
				containerNodes = append(containerNodes, t.containerNode)
			}
			if t.templateNode != nil && !seenTemplates[t.templateNode] {
				seenTemplates[t.templateNode] = true
				templateNodes = append(templateNodes, t.templateNode)
			}
			val := t.valNode.Value
			path, _, _ := parsePokkumRef(val, false)
			resolved := resolvedMap[path]

			// val is always a pokkum:// reference here — targets only ever
			// reach this loop via the isRef branch above — so it can never
			// be the displaced concrete image the way it would be if this
			// document had been hand-edited to a real ref. The only place a
			// prior resolved digest survives between runs is
			// AnnotationCurrentImage, written by the previous Resolve call
			// on this same file; compare against that instead.
			for _, annNode := range []*yaml.Node{t.templateNode, docRoot} {
				if annNode == nil {
					continue
				}
				oldCurrent := getAnnotation(annNode, ports.AnnotationCurrentImage)
				if oldCurrent == "" && (clusterState.Annotations != nil || len(clusterState.Containers) > 0) {
					if clusterCurrent, ok := clusterState.Annotations[ports.AnnotationCurrentImage]; ok && clusterCurrent != "" {
						oldCurrent = clusterCurrent
					} else if len(clusterState.Containers) > 0 {
						var cName string
						if t.containerNode != nil {
							if nNode, ok := mapGet(t.containerNode, "name"); ok && nNode.Kind == yaml.ScalarNode {
								cName = nNode.Value
							}
						}
						if cName != "" && clusterState.Containers[cName] != "" {
							oldCurrent = clusterState.Containers[cName]
						} else {
							for _, img := range clusterState.Containers {
								if img != "" {
									oldCurrent = img
									break
								}
							}
						}
					}
					// Seed existing history from cluster if local template has none
					if getAnnotation(annNode, ports.AnnotationImageHistory) == "" {
						if hist, ok := clusterState.Annotations[ports.AnnotationImageHistory]; ok && hist != "" {
							setAnnotation(annNode, ports.AnnotationImageHistory, hist)
						}
					}
					if getAnnotation(annNode, ports.AnnotationPreviousImage) == "" {
						if prev, ok := clusterState.Annotations[ports.AnnotationPreviousImage]; ok && prev != "" {
							setAnnotation(annNode, ports.AnnotationPreviousImage, prev)
						}
					}
				}

				if oldCurrent != "" && oldCurrent != resolved {
					updateImageHistory(annNode, oldCurrent)
				}
				setAnnotation(annNode, ports.AnnotationCurrentImage, resolved)
			}

			t.valNode.Value = resolved
			references = append(references, ports.Reference{
				Document: pd.doc.Name,
				Path:     path,
				Resolved: resolved,
			})

			if req.SecurityDefaults {
				// Container-level: only this container, since it is the one
				// whose image Pokkum just built. Pod-level: necessarily the
				// whole Pod spec, so a sidecar with no pokkum:// image of its
				// own still ends up inside a hardened pod.
				injectContainerSecurityDefaults(t.containerNode)
				if t.podSpecNode != nil {
					injectPodSecurityDefaults(t.podSpecNode)
				}
			}

			if req.ResourceDefaults {
				injectContainerResourceDefaults(t.containerNode)
			}

			if req.WithOTELSidecar && t.podSpecNode != nil {
				injectOTELCollectorSidecar(t.podSpecNode)
			}
		}

		rewrittenContent, err := encodeYAMLNodes(pd.nodes)
		if err != nil {
			return ports.ResolveResult{}, fmt.Errorf("k8s: document %s: encode yaml: %w: %w", pd.doc.Name, err, core.ErrManifestInvalid)
		}
		resultDocs[i] = ports.Document{
			Name:    pd.doc.Name,
			Content: rewrittenContent,
		}

		workloadLabels := extractWorkloadPodLabels(templateNodes)
		if req.NetworkPolicy {
			portsList := extractWorkloadContainerPorts(containerNodes)
			resultDocs = append(resultDocs, generateNetworkPolicyDocument(pd.doc.Name, portsList, workloadLabels, req.WithOTELSidecar))
		}
		if req.ResourceDefaults {
			if pdbDoc, ok := generatePodDisruptionBudgetDocument(pd.doc.Name, workloadLabels); ok {
				resultDocs = append(resultDocs, pdbDoc)
			}
		}
	}

	return ports.ResolveResult{
		Documents:  resultDocs,
		References: references,
	}, nil
}

func injectOTELCollectorSidecar(podSpec *yaml.Node) {
	if podSpec == nil || podSpec.Kind != yaml.MappingNode {
		return
	}
	containersNode, ok := mapGet(podSpec, "containers")
	if !ok || containersNode.Kind != yaml.SequenceNode {
		return
	}

	for _, c := range containersNode.Content {
		if c.Kind == yaml.MappingNode {
			if nameVal, found := mapGet(c, "name"); found && nameVal.Value == "otel-collector" {
				return
			}
		}
	}

	sidecarYAML := `
name: otel-collector
image: otel/opentelemetry-collector-contrib:latest
args: ["--config=/etc/otelcol/config.yaml"]
ports:
  - containerPort: 4317
    name: otlp-grpc
  - containerPort: 4318
    name: otlp-http
  - containerPort: 8889
    name: metrics
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(sidecarYAML), &node); err == nil && len(node.Content) > 0 {
		containersNode.Content = append(containersNode.Content, node.Content[0])
	}
}

func injectContainerResourceDefaults(container *yaml.Node) {
	if container == nil || container.Kind != yaml.MappingNode {
		return
	}
	if _, ok := mapGet(container, "resources"); ok {
		return
	}
	resYAML := `
requests:
  cpu: 50m
  memory: 64Mi
limits:
  memory: 256Mi
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(strings.TrimSpace(resYAML)), &node); err == nil && len(node.Content) > 0 {
		mapAppend(container, "resources", node.Content[0])
	}
}

func extractWorkloadContainerPorts(containerNodes []*yaml.Node) []int {
	seen := make(map[int]bool)
	var portsList []int

	for _, container := range containerNodes {
		if container == nil || container.Kind != yaml.MappingNode {
			continue
		}
		portsNode, ok := mapGet(container, "ports")
		if !ok || portsNode.Kind != yaml.SequenceNode {
			continue
		}
		for _, pNode := range portsNode.Content {
			if pNode.Kind != yaml.MappingNode {
				continue
			}
			if cPortNode, ok := mapGet(pNode, "containerPort"); ok && cPortNode.Kind == yaml.ScalarNode {
				if portVal, err := strconv.Atoi(cPortNode.Value); err == nil && portVal > 0 {
					if !seen[portVal] {
						seen[portVal] = true
						portsList = append(portsList, portVal)
					}
				}
			}
		}
	}

	if len(portsList) == 0 {
		return []int{3000, 8081}
	}

	sort.Ints(portsList)
	return portsList
}

// extractWorkloadPodLabels merges metadata.labels off every Pod template
// found for a document into one map, keyed by label name. For a
// Deployment/StatefulSet/DaemonSet/(Cron)Job this is spec.template.metadata.labels
// — the labels the running Pod actually carries, and which the owning
// controller's own spec.selector.matchLabels is required to match (the
// Kubernetes API rejects a controller whose selector doesn't match its
// template). For a bare Pod it is the Pod's own metadata.labels. Returns an
// empty (non-nil) map when no labels were found anywhere.
func extractWorkloadPodLabels(templateNodes []*yaml.Node) map[string]string {
	labels := make(map[string]string)
	for _, t := range templateNodes {
		metaNode, ok := mapGet(t, "metadata")
		if !ok {
			continue
		}
		labelsNode, ok := mapGet(metaNode, "labels")
		if !ok || labelsNode.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(labelsNode.Content); i += 2 {
			k, v := labelsNode.Content[i].Value, labelsNode.Content[i+1].Value
			if k != "" {
				labels[k] = v
			}
		}
	}
	return labels
}

// writeMatchLabels renders a "matchLabels:" block at the given indent (in
// spaces), sorted by key for deterministic output. Renders "matchLabels: {}"
// when labels is empty — callers that must not silently fall back to an
// unscoped (namespace-wide) selector should check len(labels) before calling.
func writeMatchLabels(sb *strings.Builder, indent string, labels map[string]string) {
	if len(labels) == 0 {
		sb.WriteString(indent + "matchLabels: {}\n")
		return
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteString(indent + "matchLabels:\n")
	for _, k := range keys {
		fmt.Fprintf(sb, "%s  %s: %q\n", indent, k, labels[k])
	}
}

func generateNetworkPolicyDocument(docName string, portsList []int, workloadLabels map[string]string, withOTEL bool) ports.Document {
	if len(portsList) == 0 {
		portsList = []int{3000, 8081}
	}
	sort.Ints(portsList)

	var sb strings.Builder
	sb.WriteString("apiVersion: networking.k8s.io/v1\n")
	sb.WriteString("kind: NetworkPolicy\n")
	sb.WriteString("metadata:\n")
	sb.WriteString("  name: pokkum-network-policy\n")
	sb.WriteString("spec:\n")
	if len(workloadLabels) > 0 {
		sb.WriteString("  podSelector:\n")
		writeMatchLabels(&sb, "    ", workloadLabels)
	} else {
		sb.WriteString("  podSelector: {}\n")
	}
	sb.WriteString("  policyTypes:\n")
	sb.WriteString("    - Ingress\n")
	sb.WriteString("    - Egress\n")
	sb.WriteString("  ingress:\n")
	sb.WriteString("    - ports:\n")
	for _, p := range portsList {
		sb.WriteString(fmt.Sprintf("        - protocol: TCP\n          port: %d\n", p))
	}
	sb.WriteString("  egress:\n")
	sb.WriteString("    - ports:\n")
	sb.WriteString("        - protocol: UDP\n          port: 53\n")
	sb.WriteString("        - protocol: TCP\n          port: 53\n")
	sb.WriteString("        - protocol: TCP\n          port: 443\n")
	sb.WriteString("        - protocol: TCP\n          port: 4317\n")
	sb.WriteString("        - protocol: TCP\n          port: 4318\n")
	if withOTEL {
		sb.WriteString("        - protocol: TCP\n          port: 8889\n")
	}

	return ports.Document{
		Name:    docName + "#network-policy",
		Content: []byte(sb.String()),
	}
}

// generatePodDisruptionBudgetDocument builds a PodDisruptionBudget scoped to
// workloadLabels, the labels pulled off the workload's own Pod template.
// Returns ok=false and skips generation entirely when no labels could be
// found: an empty selector matches every Pod in the namespace, and silently
// emitting a namespace-wide eviction budget would be worse than emitting
// none — the caller is expected to log or otherwise surface this rather than
// treat it as success.
func generatePodDisruptionBudgetDocument(docName string, workloadLabels map[string]string) (ports.Document, bool) {
	if len(workloadLabels) == 0 {
		return ports.Document{}, false
	}

	var sb strings.Builder
	sb.WriteString("apiVersion: policy/v1\n")
	sb.WriteString("kind: PodDisruptionBudget\n")
	sb.WriteString("metadata:\n")
	sb.WriteString("  name: pokkum-pdb\n")
	sb.WriteString("spec:\n")
	sb.WriteString("  minAvailable: 1\n")
	sb.WriteString("  selector:\n")
	writeMatchLabels(&sb, "    ", workloadLabels)

	return ports.Document{
		Name:    docName + "#pdb",
		Content: []byte(sb.String()),
	}, true
}
