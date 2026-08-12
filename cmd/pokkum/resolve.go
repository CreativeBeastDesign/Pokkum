package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// resolveFlags holds all command-line flags for the resolve command.
type resolveFlags struct {
	file               string
	recursive          bool
	securityContext    bool
	noSecurityContext  bool
	networkPolicy      bool
	noNetworkPolicy    bool
	resourceDefaults   bool
	noResourceDefaults bool
}

func newResolveCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &resolveFlags{}

	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve pokkum:// references in Kubernetes manifests",
		Long: `Resolve processes Kubernetes manifests and resolves pokkum:// image references
to their concrete digest form, validating that the images have been built and are available.

This is useful for generating deployment manifests with pinned image digests for
reproducibility and auditability.

The rewritten manifests are written to stdout, so this composes with kubectl:

  pokkum resolve -f deploy.yaml | kubectl apply -f -

All logging goes to stderr.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runResolve(ctx, logger, flags)
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

	// file is required
	_ = cmd.MarkFlagRequired("file")

	return cmd
}

func runResolve(ctx context.Context, logger *slog.Logger, flags *resolveFlags) error {
	if flags.file == "" {
		return fmt.Errorf("--file is required: %w", core.ErrInvalidRequest)
	}
	secCtx := securityContextEnabled(flags.securityContext, flags.noSecurityContext)
	netPol := flags.networkPolicy && !flags.noNetworkPolicy
	resDefs := flags.resourceDefaults && !flags.noResourceDefaults

	logger.Debug("resolve command started",
		"file", flags.file,
		"recursive", flags.recursive,
		"securityContext", secCtx,
		"networkPolicy", netPol,
		"resourceDefaults", resDefs)

	out, err := resolveManifests(ctx, logger, resolveManifestsOptions{
		File:             flags.file,
		Recursive:        flags.recursive,
		SecurityContext:  secCtx,
		NetworkPolicy:    netPol,
		ResourceDefaults: resDefs,
	})
	if err != nil {
		return err
	}

	if _, err := os.Stdout.Write(out); err != nil {
		return fmt.Errorf("write resolved manifests to stdout: %w", err)
	}
	logger.Info("resolve complete")
	return nil
}
