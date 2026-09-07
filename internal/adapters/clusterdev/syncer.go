package clusterdev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Syncer is the kubectl-backed ports.ClusterDevSyncer.
type Syncer struct {
	kubectl string
	log     *slog.Logger
}

var _ ports.ClusterDevSyncer = (*Syncer)(nil)

// New returns a Syncer that shells out to the kubectl binary at kubectlPath.
// A nil logger discards output.
func New(logger *slog.Logger, kubectlPath string) *Syncer {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Syncer{kubectl: kubectlPath, log: logger}
}

// podList is the subset of `kubectl get pods -o json` this adapter reads.
// It is deliberately a hand-written subset rather than a corev1.PodList: the
// four fields below are stable API, and depending on k8s.io/api for them
// would pull a large dependency tree into a zero-dependency project for no
// added correctness.
type podList struct {
	Items []pod `json:"items"`
}

type pod struct {
	Metadata struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		DeletionTimestamp string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Containers []struct {
			Name string `json:"name"`
			Env  []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// ResolveTarget implements ports.ClusterDevSyncer.
func (s *Syncer) ResolveTarget(ctx context.Context, q ports.ClusterTargetQuery) (ports.ClusterTarget, error) {
	if strings.TrimSpace(q.Namespace) == "" {
		return ports.ClusterTarget{}, fmt.Errorf("clusterdev: a namespace is required: %w", core.ErrInvalidRequest)
	}
	if strings.TrimSpace(q.Selector) == "" {
		return ports.ClusterTarget{}, fmt.Errorf("clusterdev: a label selector is required: %w", core.ErrInvalidRequest)
	}

	args := []string{"get", "pods", "-n", q.Namespace, "-l", q.Selector, "-o", "json"}
	s.log.DebugContext(ctx, "resolving cluster dev target", "namespace", q.Namespace, "selector", q.Selector)

	cmd := exec.CommandContext(ctx, s.kubectl, args...) //nolint:gosec // kubectlPath is resolved by the composition root via exec.LookPath.
	out, err := cmd.Output()
	if err != nil {
		return ports.ClusterTarget{}, fmt.Errorf("clusterdev: list pods in namespace %s matching %q: %w", q.Namespace, q.Selector, describeExecErr(err))
	}

	return SelectTarget(out, q)
}

// SelectTarget picks the container to sync into out of a `kubectl get pods -o
// json` payload. It is exported and pure so the selection rules — which are
// the part with real edge cases — are testable against literal JSON without a
// cluster or a kubectl binary anywhere in sight.
func SelectTarget(payload []byte, q ports.ClusterTargetQuery) (ports.ClusterTarget, error) {
	var list podList
	if err := json.Unmarshal(payload, &list); err != nil {
		return ports.ClusterTarget{}, fmt.Errorf("clusterdev: parse pod list for selector %q: %w: %w", q.Selector, err, core.ErrInvalidRequest)
	}

	// Candidates are Running and not terminating. Readiness is deliberately
	// NOT required: a pod whose application is broken — including one broken
	// by the previous iteration of this very loop — is exactly the pod a
	// developer needs to push a fix into, and gating on Ready would make the
	// dev loop refuse to work precisely when it is most needed.
	var candidates []pod
	for _, p := range list.Items {
		if p.Status.Phase != "Running" {
			continue
		}
		if p.Metadata.DeletionTimestamp != "" {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return ports.ClusterTarget{}, fmt.Errorf("clusterdev: no running pod in namespace %s matches selector %q (%d pod(s) matched the selector but none were Running): %w",
			q.Namespace, q.Selector, len(list.Items), core.ErrInvalidRequest)
	}

	// Sorted by name, then the first taken, so repeated runs against a
	// multi-replica Deployment keep hitting the same replica instead of
	// hopping between them and leaving half the fleet stale.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Metadata.Name < candidates[j].Metadata.Name })
	p := candidates[0]

	container, err := selectContainer(p, q)
	if err != nil {
		return ports.ClusterTarget{}, err
	}

	namespace := p.Metadata.Namespace
	if namespace == "" {
		namespace = q.Namespace
	}

	target := ports.ClusterTarget{
		Namespace: namespace,
		Pod:       p.Metadata.Name,
		Container: container.Name,
	}
	for _, e := range container.Env {
		switch e.Name {
		case ports.EnvDevMode:
			target.DevModeEnabled = truthy(e.Value)
		case ports.EnvAttestationDigest:
			target.AttestationEnabled = strings.TrimSpace(e.Value) != ""
		}
	}
	return target, nil
}

type containerSpec struct {
	Name string
	Env  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
}

func selectContainer(p pod, q ports.ClusterTargetQuery) (containerSpec, error) {
	names := make([]string, 0, len(p.Spec.Containers))
	for _, c := range p.Spec.Containers {
		names = append(names, c.Name)
	}

	switch {
	case len(p.Spec.Containers) == 0:
		return containerSpec{}, fmt.Errorf("clusterdev: pod %s/%s reports no containers: %w", p.Metadata.Namespace, p.Metadata.Name, core.ErrInvalidRequest)

	case q.Container != "":
		for _, c := range p.Spec.Containers {
			if c.Name == q.Container {
				return containerSpec{Name: c.Name, Env: c.Env}, nil
			}
		}
		return containerSpec{}, fmt.Errorf("clusterdev: pod %s/%s has no container named %q (it has: %s): %w",
			p.Metadata.Namespace, p.Metadata.Name, q.Container, strings.Join(names, ", "), core.ErrInvalidRequest)

	case len(p.Spec.Containers) == 1:
		c := p.Spec.Containers[0]
		return containerSpec{Name: c.Name, Env: c.Env}, nil

	default:
		// Deliberately no heuristic. Pokkum itself injects an OTEL collector
		// sidecar with --with-otel-sidecar, so "the first container" is a
		// coin flip between the application and the collector, and syncing
		// a SvelteKit build into a collector would fail in a way that says
		// nothing about the real mistake.
		return containerSpec{}, fmt.Errorf("clusterdev: pod %s/%s runs %d containers (%s); name the one to sync into with --container: %w",
			p.Metadata.Namespace, p.Metadata.Name, len(names), strings.Join(names, ", "), core.ErrInvalidRequest)
	}
}

// truthy matches the boolean spelling pokkum-init's own config parser
// accepts, so an operator who sets POKKUM_DEV_MODE=true on the workload does
// not get "dev mode is off" from one half of the system and "dev mode is on"
// from the other. Kept in lockstep with parseBoolEnv in
// supervisor/cmd/pokkum-init/config.go.
func truthy(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

// extractSummary is the one JSON line `pokkum-init __dev-sync` prints on
// success. Reporting the extractor's own counts, rather than what this side
// believes it sent, is what makes a result meaningful: a tar that streamed
// perfectly into a container that wrote none of it is indistinguishable from
// a working sync when only the sender counts.
type extractSummary struct {
	Files     int   `json:"files"`
	Bytes     int64 `json:"bytes"`
	Restarted bool  `json:"restarted"`
}

// Sync implements ports.ClusterDevSyncer.
func (s *Syncer) Sync(ctx context.Context, req ports.ClusterSyncRequest) (ports.ClusterSyncResult, error) {
	if req.Target.Pod == "" || req.Target.Container == "" || req.Target.Namespace == "" {
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: sync target is incomplete (namespace=%q pod=%q container=%q): %w",
			req.Target.Namespace, req.Target.Pod, req.Target.Container, core.ErrInvalidRequest)
	}
	if len(req.Dirs) == 0 {
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: no directories to sync: %w", core.ErrInvalidRequest)
	}

	args := []string{
		"exec", "-i",
		"-n", req.Target.Namespace,
		req.Target.Pod,
		"-c", req.Target.Container,
		"--",
		ports.SupervisorPath, ports.DevSyncSubcommand,
	}
	for _, d := range req.Dirs {
		args = append(args, "--root", d.RemoteDir)
	}
	if req.Restart {
		args = append(args, "--restart")
	}

	cmd := exec.CommandContext(ctx, s.kubectl, args...) //nolint:gosec // kubectlPath is resolved by the composition root via exec.LookPath.

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: open stdin for kubectl exec: %w: %w", err, core.ErrInvalidRequest)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		// The pipe is closed here and nowhere else on this path: cmd.Wait
		// closes it for us once Start has succeeded, and closing it twice
		// is not an error worth reporting over the start failure itself.
		_ = stdin.Close()
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: start kubectl exec into %s/%s: %w: %w", req.Target.Namespace, req.Target.Pod, err, core.ErrInvalidRequest)
	}

	// The tar is written synchronously on this goroutine and stdin is closed
	// before Wait, rather than fanning the write out. There is no second
	// goroutine to leak, and closing stdin is what tells the extractor the
	// archive is complete — a Wait that ran first would deadlock waiting for
	// a process that is itself waiting for EOF.
	stats, werr := WriteTar(stdin, req.Dirs)
	cerr := stdin.Close()

	waitErr := cmd.Wait()

	switch {
	case werr != nil:
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: build sync archive: %w (kubectl said: %s)", werr, strings.TrimSpace(stderr.String()))
	case cerr != nil:
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: close sync archive: %w: %w", cerr, core.ErrInvalidRequest)
	case waitErr != nil:
		// The stderr text comes from the buffer, NOT from
		// exec.ExitError.Stderr: that field is only ever populated by
		// cmd.Output(), and this call sets cmd.Stderr explicitly because it
		// also needs stdout for the extractor summary. Reading the wrong one
		// here silently discarded the reason for every failure — the RBAC
		// denial, the missing pod, the "container not found" — and left
		// "exit status 1" in its place.
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: sync into %s/%s (container %s): %w",
			req.Target.Namespace, req.Target.Pod, req.Target.Container, describeCapturedErr(waitErr, stderr.String()))
	}

	for _, skipped := range stats.Skipped {
		s.log.WarnContext(ctx, "skipped a non-regular file while syncing; the pod's tree will not match the local build here", "path", skipped)
	}

	var summary extractSummary
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &summary); err != nil {
		// kubectl exited 0 but the extractor said nothing we understand.
		// That is not "synced zero files" — it means we do not know what
		// landed in the pod — so it is an error, not an empty result.
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: parse extractor summary %q from %s/%s: %w: %w",
			strings.TrimSpace(stdout.String()), req.Target.Namespace, req.Target.Pod, err, core.ErrInvalidRequest)
	}

	// The two sides counting the same number is the loop's end-to-end proof
	// that the bytes this side read off disk are the bytes that side wrote
	// into /app. A mismatch means the archive was truncated in transit, and
	// a truncated sync that reports success is precisely the failure this
	// whole summary channel exists to make impossible.
	if summary.Files != stats.Files || summary.Bytes != stats.Bytes {
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: sync into %s/%s wrote %d file(s)/%d byte(s) but %d/%d were sent: %w",
			req.Target.Namespace, req.Target.Pod, summary.Files, summary.Bytes, stats.Files, stats.Bytes, core.ErrInvalidRequest)
	}
	if req.Restart && !summary.Restarted {
		return ports.ClusterSyncResult{}, fmt.Errorf("clusterdev: sync into %s/%s wrote %d file(s) but the application process was not restarted, so the pod is still serving the previous build: %w",
			req.Target.Namespace, req.Target.Pod, summary.Files, core.ErrInvalidRequest)
	}

	return ports.ClusterSyncResult{Files: summary.Files, Bytes: summary.Bytes, Restarted: summary.Restarted}, nil
}

// describeCapturedErr is describeExecErr for a subprocess whose stderr was
// redirected to a buffer rather than left for os/exec to capture. The two
// cannot be merged: exec.ExitError.Stderr is populated only by cmd.Output(),
// and is always empty once cmd.Stderr is assigned.
func describeCapturedErr(err error, stderr string) error {
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("%s: %w: %w", s, err, core.ErrInvalidRequest)
	}
	return fmt.Errorf("%w: %w", err, core.ErrInvalidRequest)
}

// describeExecErr enriches err with the subprocess's stderr when there is
// any, so a failure names the RBAC denial, missing pod or unreachable API
// server instead of "exit status 1". It mirrors describeKubectlErr in
// cmd/pokkum/k8s.go; the duplication is deliberate, since an adapter may not
// import the command layer and this is four lines.
func describeExecErr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
			return fmt.Errorf("%s: %w: %w", stderr, err, core.ErrInvalidRequest)
		}
	}
	return fmt.Errorf("%w: %w", err, core.ErrInvalidRequest)
}
