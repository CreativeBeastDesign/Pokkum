package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/k8s"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// securityContextEnabled merges the --security-context default and the
// --no-security-context override into the single boolean the resolver
// wants. --no-security-context always wins, mirroring --hardened's
// precedence over --base in the build command.
func securityContextEnabled(securityContext, noSecurityContext bool) bool {
	return securityContext && !noSecurityContext
}

// resolveManifestsOptions holds options for resolveManifests.
type resolveManifestsOptions struct {
	File               string
	Recursive          bool
	SecurityContext    bool
	NetworkPolicy      bool
	ResourceDefaults   bool
	RegistryConfigPath string
	ImageBuilder       ports.ImageBuilder
	ClusterInspector   ports.ClusterInspector
}

// resolveManifests is the shared engine behind `pokkum resolve` and `pokkum apply`.
func resolveManifests(ctx context.Context, logger *slog.Logger, opts resolveManifestsOptions) ([]byte, error) {
	// Both commands push, exactly like `pokkum build`, so they need the same
	// destination repository — and since resolving means building, failing
	// fast here (before reading any file or touching the network) mirrors
	// build's own contract around POKKUM_DOCKER_REPO.
	dockerRepo := strings.TrimSpace(os.Getenv("POKKUM_DOCKER_REPO"))
	if dockerRepo == "" {
		return nil, fmt.Errorf("POKKUM_DOCKER_REPO must be set: resolving pokkum:// references builds and pushes images: %w", core.ErrNoDockerRepo)
	}

	docs, err := loadDocuments(opts.File, opts.Recursive)
	if err != nil {
		return nil, err
	}

	baseDir, err := manifestBaseDir(opts.File)
	if err != nil {
		return nil, err
	}

	builder := opts.ImageBuilder
	if builder == nil {
		builder = newImageBuilder(logger, baseDir, dockerRepo)
	}

	resolver := k8s.NewResolver()
	res, err := resolver.Resolve(ctx, ports.ResolveRequest{
		Documents:          docs,
		Build:              builder,
		Strict:             true,
		SecurityDefaults:   opts.SecurityContext,
		NetworkPolicy:      opts.NetworkPolicy,
		ResourceDefaults:   opts.ResourceDefaults,
		RegistryConfigPath: opts.RegistryConfigPath,
		ClusterInspector:   opts.ClusterInspector,
	})
	if err != nil {
		return nil, err
	}

	// Cluster inspection is best-effort history enrichment: it never fails
	// resolveManifests, but a failure here means a workload's rollback
	// history may now be incomplete rather than genuinely fresh, and that
	// distinction is invisible to the operator unless we say so at a log
	// level that is on by default.
	for _, w := range res.ClusterInspectionWarnings {
		logger.Warn("cluster inspection failed; proceeding without live cluster state for this workload (rollback history may be incomplete)",
			"kind", w.Kind, "name", w.Name, "namespace", w.Namespace, "error", w.Err)
	}

	logger.Info("manifests resolved", "documents", len(res.Documents), "references", len(res.References))
	for _, ref := range res.References {
		logger.Info("resolved image reference", "document", ref.Document, "path", ref.Path, "resolved", ref.Resolved)
	}

	return joinDocuments(res.Documents), nil
}

// newKubectlClusterInspector creates a ports.ClusterInspector that queries live cluster workload state via kubectl.
func newKubectlClusterInspector(logger *slog.Logger, kubectlPath string) ports.ClusterInspector {
	return func(ctx context.Context, kind, name, namespace string) (ports.ClusterWorkloadState, error) {
		if kubectlPath == "" {
			return ports.ClusterWorkloadState{}, nil
		}
		target := fmt.Sprintf("%s/%s", strings.ToLower(kind), name)
		args := []string{"get", target, "-o", "json"}
		if namespace != "" {
			args = append(args, "-n", namespace)
		}

		logger.DebugContext(ctx, "inspecting live cluster annotations", "workload", target, "namespace", namespace)
		cmd := exec.CommandContext(ctx, kubectlPath, args...)
		out, err := cmd.Output()
		if err != nil {
			if isKubectlNotFoundErr(err) {
				// Genuine "no such resource" — the expected, unremarkable
				// case for a first-ever deployment. Empty state, no error.
				logger.DebugContext(ctx, "workload not found in cluster (treating as fresh workload)", "workload", target, "namespace", namespace)
				return ports.ClusterWorkloadState{}, nil
			}
			// Every other failure — unreachable cluster, expired/invalid
			// credentials, RBAC denial, malformed kubeconfig, kubectl itself
			// missing or crashing — is NOT the same thing as "this workload
			// is new", and must not be silently folded into empty state:
			// that would quietly reset a workload's rollback history on
			// every apply for as long as the cluster stays unreachable. Make
			// it a real error so the caller can decide how loudly to
			// surface it.
			return ports.ClusterWorkloadState{}, fmt.Errorf("inspect cluster workload %s: %w", target, describeKubectlErr(err))
		}

		var obj struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Metadata struct {
						Annotations map[string]string `json:"annotations"`
					} `json:"metadata"`
					Spec struct {
						Containers []struct {
							Name  string `json:"name"`
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		}

		if jerr := json.Unmarshal(out, &obj); jerr != nil {
			// kubectl exited 0 but produced something we can't parse as the
			// workload object we expect. That is not "no history" either —
			// it means we genuinely don't know the cluster's state — so
			// surface it rather than silently treating it as fresh.
			return ports.ClusterWorkloadState{}, fmt.Errorf("unmarshal live workload json for %s: %w", target, jerr)
		}

		ann := make(map[string]string)
		for k, v := range obj.Metadata.Annotations {
			ann[k] = v
		}
		for k, v := range obj.Spec.Template.Metadata.Annotations {
			if _, exists := ann[k]; !exists {
				ann[k] = v
			}
		}

		containers := make(map[string]string)
		for _, c := range obj.Spec.Template.Spec.Containers {
			if c.Name != "" && c.Image != "" {
				containers[c.Name] = c.Image
			}
		}
		for _, c := range obj.Spec.Containers {
			if c.Name != "" && c.Image != "" {
				containers[c.Name] = c.Image
			}
		}

		return ports.ClusterWorkloadState{
			Annotations: ann,
			Containers:  containers,
		}, nil
	}
}

// isKubectlNotFoundErr reports whether err is kubectl exiting because the
// requested resource genuinely does not exist in the cluster, as opposed to
// any other failure (connectivity, auth, RBAC, a malformed kubeconfig, or
// kubectl failing to even run).
//
// `kubectl get <kind>/<name> -o json` reports a missing resource as an API
// server error wrapped by kubectl's own error printer in the form
// "Error from server (NotFound): <resource> \"<name>\" not found" — the
// "(NotFound)" parenthetical is the Kubernetes API's machine-readable
// StatusReason and is stable across kubectl versions and server
// implementations, unlike the free-text message around it. Any other
// exec failure — including one where kubectl produced no stderr at all,
// e.g. because it could not be executed — is treated as NOT a not-found and
// must be surfaced to the caller as a real error.
func isKubectlNotFoundErr(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// Not a process exit at all (e.g. kubectl binary missing, context
		// cancelled) — definitely not "resource not found".
		return false
	}
	return strings.Contains(string(exitErr.Stderr), "(NotFound)")
}

// describeKubectlErr enriches err with kubectl's stderr, when available, so
// that a warning or error surfaced further up the stack tells the operator
// *why* cluster inspection failed (unreachable server, RBAC denial,
// malformed kubeconfig, ...) instead of just "exit status 1".
func describeKubectlErr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
			return fmt.Errorf("%s: %w", stderr, err)
		}
	}
	return err
}

// newImageBuilder returns the ports.ImageBuilder that turns a pokkum://<path>
// reference into a built, pushed, digest-pinned image.
//
// The port's Build signature is func(ctx, path) (string, error) — it carries
// no document identity, because the resolver deduplicates purely by path
// string across every document in one Resolve call (see the ImageBuilder and
// ResolveRequest.Build doc comments in internal/ports/k8s.go). That means a
// single Resolve call can only have one base directory for relative paths,
// not one per manifest file. We take that base directory to be the -f
// argument itself: the file's own directory for a single file, or the
// directory passed to -f when it names a directory (recursive or not) — so
// `pokkum resolve -f manifests/ --recursive` resolves every pokkum://./x
// path against manifests/, regardless of which nested file it was found in.
// This is the natural generalisation of "relative to the manifest" once a
// single invocation can span more than one manifest file, and it matches
// ko's single-module-root convention for ko://.
func newImageBuilder(logger *slog.Logger, baseDir, dockerRepo string) ports.ImageBuilder {
	return func(ctx context.Context, path string) (string, error) {
		projectDir := filepath.Join(baseDir, path)
		repo := deriveRepo(dockerRepo, path)
		logger.Info("building pokkum:// reference", "path", path, "projectDir", projectDir, "repo", repo)

		req, err := buildRequestForPath(projectDir, repo, logger)
		if err != nil {
			return "", fmt.Errorf("pokkum://%s: %w", path, err)
		}

		// Stdout is nil (discarded): the build's own "repo@sha256:…" line
		// must not land on the same stream as the rewritten manifest.
		res, err := core.Build(ctx, buildDeps(logger, nil), req, core.BuildOptions{})
		if err != nil {
			return "", fmt.Errorf("build pokkum://%s: %w", path, err)
		}
		return res.Image.Ref, nil
	}
}

// deriveRepo derives a per-project destination repository from the
// configured POKKUM_DOCKER_REPO and the pokkum:// path being built.
//
// A single manifest set may reference several distinct projects (e.g.
// pokkum://./frontend and pokkum://./backend); pushing every one of them
// under the exact same repository name would still be valid — registries
// distinguish images by digest, not name — but it makes `docker inspect` and
// registry browsing indistinguishable between unrelated projects. Instead
// each distinct path gets its own sub-repository, named after its cleaned,
// slash-to-hyphen path (e.g. "./frontend" -> "<repo>/frontend",
// "./services/api" -> "<repo>/services-api"). The root reference
// (pokkum://. or pokkum://./ ) uses the repo as-is, unchanged, since there is
// nothing to disambiguate. This naming is not specified by the ports
// contract; it is this command's own policy, documented here so it can be
// revisited.
func deriveRepo(dockerRepo, path string) string {
	clean := pathpkg.Clean(filepath.ToSlash(path))
	clean = strings.TrimPrefix(clean, "/")

	segs := strings.Split(clean, "/")
	kept := make([]string, 0, len(segs))
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			continue
		}
		kept = append(kept, strings.ToLower(s))
	}
	if len(kept) == 0 {
		return dockerRepo
	}
	return strings.TrimSuffix(dockerRepo, "/") + "/" + strings.Join(kept, "-")
}

// manifestBaseDir determines the directory that pokkum:// paths in the
// resolved manifests are resolved against. See newImageBuilder for why this
// is a single directory per invocation rather than per document.
func manifestBaseDir(file string) (string, error) {
	if file == "-" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("determine working directory for stdin input: %w: %w", err, core.ErrInvalidRequest)
		}
		return wd, nil
	}

	info, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w: %w", file, err, core.ErrInvalidRequest)
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", fmt.Errorf("resolve %s to an absolute path: %w: %w", file, err, core.ErrInvalidRequest)
	}
	if info.IsDir() {
		return abs, nil
	}
	return filepath.Dir(abs), nil
}

// loadDocuments reads the manifests named by file into ports.Document values,
// entirely in the command layer per the k8s adapter's design: file and
// directory walking never happens inside the resolver. "-" reads a single
// document from stdin. A plain file is read as-is. A directory is expanded to
// its *.yaml/*.yml files, non-recursively unless recursive is set.
func loadDocuments(file string, recursive bool) ([]ports.Document, error) {
	if file == "-" {
		content, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w: %w", err, core.ErrInvalidRequest)
		}
		return []ports.Document{{Name: "-", Content: content}}, nil
	}

	files, err := collectManifestFiles(file, recursive)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.yaml/*.yml manifests found in %s: %w", file, core.ErrInvalidRequest)
	}

	docs := make([]ports.Document, 0, len(files))
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w: %w", f, err, core.ErrInvalidRequest)
		}
		docs = append(docs, ports.Document{Name: f, Content: content})
	}
	return docs, nil
}

// collectManifestFiles resolves file to the list of manifest paths it names:
// itself, if it is a regular file, or its *.yaml/*.yml children if it is a
// directory (recursing into subdirectories only when recursive is true).
// Results are sorted for deterministic output.
func collectManifestFiles(file string, recursive bool) ([]string, error) {
	info, err := os.Stat(file)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w: %w", file, err, core.ErrInvalidRequest)
	}
	if !info.IsDir() {
		return []string{file}, nil
	}

	var files []string
	if recursive {
		err = filepath.WalkDir(file, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if isYAMLFile(d.Name()) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w: %w", file, err, core.ErrInvalidRequest)
		}
	} else {
		entries, err := os.ReadDir(file)
		if err != nil {
			return nil, fmt.Errorf("read directory %s: %w: %w", file, err, core.ErrInvalidRequest)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if isYAMLFile(e.Name()) {
				files = append(files, filepath.Join(file, e.Name()))
			}
		}
	}

	sort.Strings(files)
	return files, nil
}

// isYAMLFile reports whether name has a .yaml or .yml extension, matched
// case-insensitively.
func isYAMLFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// joinDocuments concatenates rewritten documents into a single YAML stream
// suitable for stdout or `kubectl apply -f -`, separating multiple documents
// with "---" the way a multi-document manifest file would.
func joinDocuments(docs []ports.Document) []byte {
	var buf bytes.Buffer
	for i, d := range docs {
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(d.Content)
	}
	return buf.Bytes()
}
