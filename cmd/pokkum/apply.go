package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// applyFlags holds all command-line flags for the apply command.
type applyFlags struct {
	file               string
	recursive          bool
	securityContext    bool
	noSecurityContext  bool
	networkPolicy      bool
	noNetworkPolicy    bool
	resourceDefaults   bool
	noResourceDefaults bool
	clusterInspect     bool
	noClusterInspect   bool
	registryConfig     string
	since              string
}

func newApplyCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &applyFlags{}

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply Kubernetes manifests with pokkum:// image references",
		Long: `Apply processes Kubernetes manifests with pokkum:// image references,
resolves them to concrete images, and applies the resolved manifests to the cluster.

This combines resolution and Kubernetes application in a single step, automatically
updating deployment manifests with the latest built images. It requires kubectl on
PATH and is equivalent to:

  pokkum resolve -f deploy.yaml | kubectl apply -f -`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runApply(ctx, logger, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.file, "file", "f", "",
		"File or directory containing Kubernetes manifests (\"-\" reads from stdin)")
	cmd.Flags().BoolVar(&flags.recursive, "recursive", false,
		"Process all YAML files in the directory recursively")
	cmd.Flags().BoolVar(&flags.securityContext, "security-context", true,
		"Inject hardened securityContext defaults (runAsNonRoot, seccompProfile, allowPrivilegeEscalation, dropped capabilities) for pokkum-built containers")
	cmd.Flags().BoolVar(&flags.noSecurityContext, "no-security-context", false,
		"Disable securityContext default injection (shorthand for --security-context=false)")
	cmd.Flags().BoolVar(&flags.networkPolicy, "network-policy", true,
		"Generate NetworkPolicy document restricting ingress and egress for resolved workloads")
	cmd.Flags().BoolVar(&flags.noNetworkPolicy, "no-network-policy", false,
		"Disable NetworkPolicy generation")
	cmd.Flags().BoolVar(&flags.resourceDefaults, "resource-defaults", true,
		"Inject default CPU/memory requests and limits and append a PodDisruptionBudget")
	cmd.Flags().BoolVar(&flags.noResourceDefaults, "no-resource-defaults", false,
		"Disable resource default injection and PodDisruptionBudget generation")
	cmd.Flags().BoolVar(&flags.clusterInspect, "cluster-inspect", true,
		"Inspect live cluster workload annotations before resolving to seed multi-generation history")
	cmd.Flags().BoolVar(&flags.noClusterInspect, "no-cluster-inspect", false,
		"Disable live cluster annotation inspection (shorthand for --cluster-inspect=false)")
	cmd.Flags().StringVar(&flags.registryConfig, "registry-config", "",
		"Path to custom OCI registry auth config file (config.json)")
	cmd.Flags().StringVar(&flags.since, "since", "",
		"Git ref (commit, branch, tag) to diff against for monorepo affected-detection; unchanged apps skip building when a prior digest is known")

	// file is required
	_ = cmd.MarkFlagRequired("file")

	return cmd
}

func runApply(ctx context.Context, logger *slog.Logger, flags *applyFlags) error {
	if flags.file == "" {
		return fmt.Errorf("--file is required: %w", core.ErrInvalidRequest)
	}
	secCtx := securityContextEnabled(flags.securityContext, flags.noSecurityContext)
	netPol := flags.networkPolicy && !flags.noNetworkPolicy
	resDefs := flags.resourceDefaults && !flags.noResourceDefaults
	inspectCluster := flags.clusterInspect && !flags.noClusterInspect

	logger.Debug("apply command started",
		"file", flags.file,
		"recursive", flags.recursive,
		"securityContext", secCtx,
		"networkPolicy", netPol,
		"resourceDefaults", resDefs,
		"clusterInspect", inspectCluster,
		"since", flags.since)

	// Look up kubectl before doing any of the (potentially expensive) build
	// and push work, so a missing kubectl fails in milliseconds rather than
	// after images have already been pushed.
	kubectlPath, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl not found on PATH: install kubectl (https://kubernetes.io/docs/tasks/tools/) or add it to PATH before running `pokkum apply`: %w", core.ErrInvalidRequest)
	}

	var inspector ports.ClusterInspector
	if inspectCluster {
		inspector = newKubectlClusterInspector(logger, kubectlPath)
	}

	out, err := resolveManifests(ctx, logger, resolveManifestsOptions{
		File:               flags.file,
		Recursive:          flags.recursive,
		SecurityContext:    secCtx,
		NetworkPolicy:      netPol,
		ResourceDefaults:   resDefs,
		RegistryConfigPath: flags.registryConfig,
		Since:              flags.since,
		ClusterInspector:   inspector,
	})
	if err != nil {
		return err
	}

	logger.Info("applying resolved manifests", "kubectl", kubectlPath)

	if err := runKubectlApply(ctx, kubectlPath, out, os.Stdout, os.Stderr); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// kubectl already wrote its own diagnostics to stderr; propagate
			// its exact exit code rather than collapsing it to the generic 1
			// that main() would use for a returned error.
			logger.Error("kubectl apply failed", "exitCode", exitErr.ExitCode())
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("run kubectl apply: %w", err)
	}

	logger.Info("apply complete")
	return nil
}

// runKubectlApply pipes manifest into `kubectl apply -f -`. It is factored
// out of runApply so it can be exercised in tests against a stand-in
// executable, without needing a real cluster or even a real kubectl.
//
// exec.CommandContext ties kubectl's lifetime to ctx, so the Ctrl-C handling
// wired up in main() kills an in-flight `kubectl apply` instead of leaving it
// to finish on its own after pokkum exits.
func runKubectlApply(ctx context.Context, kubectlPath string, manifest []byte, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, kubectlPath, "apply", "-f", "-")
	cmd.Stdin = bytes.NewReader(manifest)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
