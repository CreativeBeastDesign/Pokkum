package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/clusterdev"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// devClusterOutputs are the two host directories one prepare produces, in the
// shape the layered packager would have mounted them at.
type devClusterOutputs struct {
	// ServerDir is the SvelteKit adapter output root, which becomes
	// /app/server. It is the WHOLE output directory rather than its server/
	// subdirectory, and the client/vendor/native/prerendered children are
	// excluded from the sync — exactly what the packager does when it builds
	// the /app/server layer (see internal/adapters/packager/packager.go and
	// the AppServerDir assignment in internal/core/pipeline.go). Getting
	// this wrong in either direction produces a pod whose /app/server does
	// not match what an image build would have put there, which is the one
	// property this whole mode exists to preserve.
	ServerDir string

	// ClientDir is the static client tree, which becomes /app/client.
	ClientDir string
}

// devClusterPreparer is the "rebuild the app" seam of the cluster dev loop,
// factored out for the same reason devBuilder is in dev.go: it lets tests
// drive several sync cycles without a real SvelteKit project, a real bun, or
// a real cluster.
type devClusterPreparer interface {
	Prepare(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string) (devClusterOutputs, error)
}

// bunexecClusterPreparer is the production devClusterPreparer: one
// SvelteKit build, no image, no packaging, no registry.
type bunexecClusterPreparer struct{}

func (bunexecClusterPreparer) Prepare(ctx context.Context, logger *slog.Logger, _ *devFlags, absDir string) (devClusterOutputs, error) {
	cfg, err := config.New(absDir, logger)
	if err != nil {
		return devClusterOutputs{}, fmt.Errorf("config loader: %w", err)
	}
	timestamp, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		// A dev sync produces no image and no digest, so an unresolvable
		// timestamp costs nothing here; Prepare only requires it to be
		// non-zero. The container-parity dev path makes the same call for
		// the same reason.
		timestamp = time.Now().UTC()
	}

	compiler := bunexec.NewCompiler(logger)
	if _, err := compiler.Preflight(ctx, ports.PreflightRequest{
		ProjectDir: absDir,
		Strategy:   ports.StrategyLayered,
	}); err != nil {
		return devClusterOutputs{}, fmt.Errorf("dev --cluster: preflight %s: %w", absDir, err)
	}

	prep, err := compiler.Prepare(ctx, ports.PrepareRequest{
		Strategy:        ports.StrategyLayered,
		ProjectDir:      absDir,
		SourceDateEpoch: timestamp,
		Platforms:       []ports.Platform{ports.LocalPlatform()},
	})
	if err != nil {
		return devClusterOutputs{}, fmt.Errorf("dev --cluster: build %s: %w", absDir, err)
	}

	return devClusterOutputs{
		ServerDir: prep.OutputDir,
		ClientDir: filepath.Join(prep.OutputDir, "client"),
	}, nil
}

// devClusterServerExcludes mirrors the packager's own exclusion list for the
// /app/server layer. It is duplicated rather than shared because the packager
// spells it as a pruneutils option deep inside an adapter, and an adapter may
// not be imported from here — but the two must not drift, which is what
// TestDevClusterServerExcludes_MatchPackager asserts by reading the packager
// source.
var devClusterServerExcludes = []string{"client", "vendor", "native", "prerendered"}

// runClusterDev is the whole `pokkum dev --cluster` execution path: build the
// SvelteKit project, stream /app/server and /app/client straight into a
// running pod, restart the application process, and repeat on every source
// change. No image is built, nothing is pushed, and the pod is never
// recreated.
func runClusterDev(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string, syncer ports.ClusterDevSyncer, preparer devClusterPreparer, pollInterval time.Duration) error {
	target, err := syncer.ResolveTarget(ctx, ports.ClusterTargetQuery{
		Namespace: flags.namespace,
		Selector:  flags.selector,
		Container: flags.container,
	})
	if err != nil {
		return err
	}

	// Fail closed, and fail here. Without POKKUM_DEV_MODE on the workload
	// the in-pod pokkum-init refuses __dev-sync outright, so continuing
	// would mean building the whole project and streaming a tar only to be
	// rejected at the far end — with an error naming a subcommand the
	// developer never typed.
	if !target.DevModeEnabled {
		return fmt.Errorf("dev --cluster: container %s in pod %s/%s does not set %s, so its pokkum-init will refuse an in-pod sync.\n"+
			"Enable it on the workload first, e.g.:\n"+
			"  kubectl set env -n %s deployment/<name> %s=1\n"+
			"and never leave it set on a production workload: it lets anyone who can exec into the pod overwrite /app and restart the server: %w",
			target.Container, target.Namespace, target.Pod, ports.EnvDevMode,
			target.Namespace, ports.EnvDevMode, core.ErrInvalidRequest)
	}

	logger.Info("cluster dev target resolved", "namespace", target.Namespace, "pod", target.Pod, "container", target.Container)

	if target.AttestationEnabled {
		// Startup attestation is verified once, when pokkum-init starts, so
		// syncing does not break the running pod. It breaks the NEXT start
		// of it — an eviction, a node drain, an OOM kill — which then
		// crash-loops on exit code 125 with a digest mismatch against a tree
		// this loop rewrote. Saying so now is the difference between a known
		// trade-off and a baffling incident an hour later.
		logger.Warn("this container has startup attestation enabled; a dev sync rewrites the /app tree it attests, so the pod will fail to start (exit 125, digest mismatch) if it is ever restarted or rescheduled. Delete and redeploy the pod to recover.",
			"pod", target.Pod, "env", ports.EnvAttestationDigest)
	}

	logger.Warn("--cluster patches a running pod in place: the pod no longer matches the image it was started from, nothing is pushed to a registry, and the change is lost the moment the pod is replaced. It is a development loop, not a deployment.")

	// The first cycle is fatal on failure and every later one is not. A
	// first-cycle failure is a configuration problem — wrong selector,
	// missing RBAC, an image that is not layered — and looping on it would
	// bury the one error that matters under a rebuild every two seconds. A
	// later failure is almost always a syntax error the developer is about
	// to fix, and killing the session for that would make the loop useless.
	if err := syncOnce(ctx, logger, flags, absDir, syncer, preparer, target); err != nil {
		return err
	}

	logger.Info("watching for source changes (press Ctrl+C to exit)...", "dir", filepath.Join(absDir, "src"))

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	srcDir := filepath.Join(absDir, "src")
	var lastMod time.Time
	if stat, err := os.Stat(srcDir); err == nil {
		lastMod = stat.ModTime()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			currentMod := getLatestModTime(srcDir)
			if !currentMod.After(lastMod) {
				continue
			}
			lastMod = currentMod
			logger.Info("detected source modifications; rebuilding and syncing...", "dir", srcDir)
			if err := syncOnce(ctx, logger, flags, absDir, syncer, preparer, target); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				logger.Error("sync failed; the pod is still serving the previous build", "error", err)
			}
		}
	}
}

// syncOnce is one full cycle: build, stream, restart.
func syncOnce(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string, syncer ports.ClusterDevSyncer, preparer devClusterPreparer, target ports.ClusterTarget) error {
	out, err := preparer.Prepare(ctx, logger, flags, absDir)
	if err != nil {
		return err
	}

	dirs := []ports.ClusterSyncDir{
		// Client first, server last. The restart happens after the last
		// directory has landed, and the server process re-reads its own code
		// exactly once at start — so putting the tree that needs the restart
		// last means there is no window where a restarted server is serving
		// against half-updated assets.
		{LocalDir: out.ClientDir, RemoteDir: ports.AppClientDirPrefix},
		{LocalDir: out.ServerDir, RemoteDir: ports.AppServerDirPrefix, ExcludeDirs: devClusterServerExcludes},
	}

	res, err := syncer.Sync(ctx, ports.ClusterSyncRequest{
		Target:  target,
		Dirs:    dirs,
		Restart: true,
	})
	if err != nil {
		return err
	}

	// A sync that matched nothing is reported, not celebrated: it is what a
	// wrong project directory, an empty build output or a silently failed
	// prepare all look like from the exit code alone.
	if res.Files == 0 {
		logger.Warn("sync wrote no files; check that the project actually produced build output", "server_dir", out.ServerDir, "client_dir", out.ClientDir)
	}
	logger.Info("synced into pod", "pod", target.Pod, "container", target.Container,
		"files", res.Files, "bytes", res.Bytes, "restarted", res.Restarted)
	return nil
}

// newClusterSyncer resolves kubectl and returns the production syncer. It is
// separate from runClusterDev so the PATH lookup fails in milliseconds,
// before a full SvelteKit build has been spent.
func newClusterSyncer(logger *slog.Logger) (ports.ClusterDevSyncer, error) {
	kubectlPath, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, fmt.Errorf("dev --cluster: kubectl not found on PATH: install kubectl (https://kubernetes.io/docs/tasks/tools/) or add it to PATH: %w", core.ErrInvalidRequest)
	}
	return clusterdev.New(logger, kubectlPath), nil
}

// devClusterStarter is the --cluster execution seam, mirroring
// devContainerRunner/devBuilder/devLocalRunner in dev.go. It exists as a
// factory-shaped interface rather than a pair of injected dependencies so
// that resolving kubectl -- a PATH lookup that fails on a machine without it
// -- happens only when --cluster was actually requested, and never as a side
// effect of constructing dependencies for a mode that does not use it.
type devClusterStarter interface {
	Start(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string) error
}

// kubectlClusterStarter is the production devClusterStarter.
type kubectlClusterStarter struct{}

func (kubectlClusterStarter) Start(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string) error {
	syncer, err := newClusterSyncer(logger)
	if err != nil {
		return err
	}
	return runClusterDev(ctx, logger, flags, absDir, syncer, bunexecClusterPreparer{}, devWatchPollInterval)
}
