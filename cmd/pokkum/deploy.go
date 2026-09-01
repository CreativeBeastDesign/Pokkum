package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/deploy"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// deployFlags holds the command-line flags for `pokkum deploy`.
type deployFlags struct {
	dir     string
	profile string
	image   string
	output  string
}

func newDeployCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &deployFlags{}

	cmd := &cobra.Command{
		Use:   "deploy [dir]",
		Short: "Deploy an already-published image to the configured PaaS",
		Long: `Deploy hands an image that is already in a registry to the PaaS control plane
configured under "deploy:" in .pokkum.yaml (Dokploy or SwiftWave).

It performs no build and no push. Use it to redeploy the current image, to
retry after a deploy failed, or to deploy from a machine that did not build the
image. A successful "pokkum build" deploys on its own unless deploy.auto is
false — see "pokkum build --no-deploy".

The API credential is never read from .pokkum.yaml. It is read from the
environment variable named by deploy.token_env, defaulting to
POKKUM_DEPLOY_TOKEN.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				flags.dir = args[0]
			}
			if outFlag, _ := cmd.Flags().GetString("output"); outFlag != "" {
				flags.output = outFlag
			}
			return runDeploy(ctx, logger, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.dir, "dir", "d", ".", "Path to project directory")
	cmd.Flags().StringVarP(&flags.profile, "profile", "P", "", "Configuration profile whose deploy settings to use")
	cmd.Flags().StringVar(&flags.image, "image", "",
		"Image reference to deploy, overriding the one recorded in configuration; required when the target repoints the application at a specific image")

	return cmd
}

func runDeploy(ctx context.Context, logger *slog.Logger, flags *deployFlags) error {
	outputFormat := ports.OutputFormat(flags.output)

	dir := flags.dir
	if dir == "" {
		dir = "."
	}

	// The same resolution build uses, so `pokkum deploy -P production` and
	// `pokkum build -P production` address the same environment.
	_, projCfg, _, err := resolveProjectConfig(logger, dir, flags.profile, false)
	if err != nil {
		return err
	}
	if projCfg == nil || strings.TrimSpace(projCfg.Deploy.Target) == "" {
		msg := fmt.Sprintf("no deploy target configured in %s (add a `deploy:` block with a target of %q or %q)",
			ports.ConfigFilename, ports.DeployDokploy, ports.DeploySwiftwave)
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteError(os.Stdout, "deploy", "ERR_DEPLOY_NOT_CONFIGURED", msg, "")
		}
		return fmt.Errorf("%s: %w", msg, core.ErrDeployNotConfigured)
	}

	// An explicit --image wins; otherwise the target redeploys whatever
	// reference it already holds, which is the normal case for a tag-pinned
	// application.
	res, err := executeDeploy(ctx, logger, projCfg.Deploy, strings.TrimSpace(flags.image), "")
	if err != nil {
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteError(os.Stdout, "deploy", deployErrorCode(err), err.Error(), "")
		}
		return err
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "deploy", deployPayload(res))
	}
	fmt.Printf("✓ Deployed to %s: %s\n", res.Target, describeDeployResult(res))
	return nil
}

// executeDeploy resolves cfg into a request, constructs the adapter for its
// target, and runs the deploy.
//
// It is the single place both `pokkum deploy` and `pokkum build`'s automatic
// deploy go through, so the two cannot drift in how they resolve credentials
// or in whether they check that a rollout actually started.
func executeDeploy(ctx context.Context, logger *slog.Logger, cfg ports.DeployConfig, imageRef, taggedRef string) (ports.DeployResult, error) {
	req, err := core.ResolveDeployRequest(cfg, imageRef, taggedRef, os.Getenv)
	if err != nil {
		return ports.DeployResult{}, err
	}

	deployer, err := deploy.New(req.Target, logger)
	if err != nil {
		return ports.DeployResult{}, err
	}

	logger.Info("deploying",
		"target", req.Target,
		"method", req.Method,
		"application", req.Application,
		"image", req.ImageRef,
		"update_image", req.UpdateImage)

	return core.Deploy(ctx, deployer, req)
}

// describeDeployResult renders a result for a human, naming the application
// when the platform gave one.
func describeDeployResult(res ports.DeployResult) string {
	app := res.Application
	if app == "" {
		app = "application"
	}
	detail := res.Detail
	if detail == "" {
		detail = "rollout started"
	}
	if res.ImageUpdated && res.ImageRef != "" {
		return fmt.Sprintf("%s → %s (%s)", app, res.ImageRef, detail)
	}
	return fmt.Sprintf("%s (%s)", app, detail)
}

// deployPayload is the --output=json data payload for a deploy.
func deployPayload(res ports.DeployResult) map[string]any {
	return map[string]any{
		"target":        string(res.Target),
		"method":        string(res.Method),
		"application":   res.Application,
		"image":         res.ImageRef,
		"image_updated": res.ImageUpdated,
		"triggered":     res.Triggered,
		"detail":        res.Detail,
	}
}

// deployErrorCode maps a deploy error to a stable machine-readable code, so a
// --output=json consumer can distinguish the failure modes that need different
// responses: a missing credential is fixed in the environment, an untriggered
// rollout is fixed in the platform's own application settings, and a transport
// failure is worth retrying.
func deployErrorCode(err error) string {
	switch {
	case errors.Is(err, core.ErrDeployNotTriggered):
		return "ERR_DEPLOY_NOT_TRIGGERED"
	case errors.Is(err, core.ErrDeployTokenMissing):
		return "ERR_DEPLOY_TOKEN_MISSING"
	case errors.Is(err, core.ErrDeployNotConfigured):
		return "ERR_DEPLOY_NOT_CONFIGURED"
	case errors.Is(err, core.ErrInvalidDeployTarget):
		return "ERR_INVALID_DEPLOY_TARGET"
	case errors.Is(err, core.ErrInvalidDeployMethod):
		return "ERR_INVALID_DEPLOY_METHOD"
	case errors.Is(err, core.ErrDeployFailed):
		return "ERR_DEPLOY_FAILED"
	default:
		return "ERR_DEPLOY_FAILED"
	}
}
