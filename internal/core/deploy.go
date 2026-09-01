package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Deploy vocabulary re-exported from ports, so core-facing code can spell it
// core.DeployTarget without a second declaration. An alias is the same type.
type (
	// DeployTarget is ports.DeployTarget.
	DeployTarget = ports.DeployTarget

	// DeployMethod is ports.DeployMethod.
	DeployMethod = ports.DeployMethod

	// DeployRequest is ports.DeployRequest.
	DeployRequest = ports.DeployRequest

	// DeployResult is ports.DeployResult.
	DeployResult = ports.DeployResult

	// Deployer is ports.Deployer.
	Deployer = ports.Deployer

	// DeployConfig is ports.DeployConfig.
	DeployConfig = ports.DeployConfig
)

// Deploy sentinel errors.
var (
	// ErrInvalidDeployTarget reports a deploy.target that names no supported
	// platform.
	ErrInvalidDeployTarget = errors.New("invalid deploy target")

	// ErrInvalidDeployMethod reports a deploy.method that is either unknown or
	// unsupported for the chosen target. The two cases are distinguished in
	// the message, not the sentinel, because both are the same user fix.
	ErrInvalidDeployMethod = errors.New("invalid deploy method")

	// ErrDeployNotConfigured reports that a deploy was requested explicitly
	// but .pokkum.yaml names no target. It is never returned for an automatic
	// deploy — an unconfigured project simply does not deploy.
	ErrDeployNotConfigured = errors.New("no deploy target configured")

	// ErrDeployTokenMissing reports that the environment variable named by
	// deploy.token_env is unset or empty. Fail closed: a deploy attempted with
	// no credential would either 401 against the panel or, worse, succeed
	// against an unauthenticated one.
	ErrDeployTokenMissing = errors.New("deploy credential not found in environment")

	// ErrDeployFailed reports that the platform rejected, or failed to answer,
	// the deploy call.
	ErrDeployFailed = errors.New("deploy failed")

	// ErrDeployNotTriggered reports that the platform ACCEPTED the request and
	// then declined to start a rollout.
	//
	// This is a distinct sentinel, not a flavour of ErrDeployFailed, because
	// it is the failure mode a status-code check cannot see: SwiftWave's
	// redeploy webhook answers "200 OK - No rebuild" when the posted body does
	// not mention the application's currently configured image, so a deploy
	// that changed nothing is indistinguishable from one that worked unless
	// the body is read. Reporting it as its own error keeps "the platform said
	// no" from being logged as a successful deployment.
	ErrDeployNotTriggered = errors.New("deploy accepted but no rollout was triggered")
)

// DefaultDeployTokenEnv is the environment variable consulted when
// deploy.token_env is unset.
//
// variable, not a credential. It exists precisely so that no credential is
// ever stored in .pokkum.yaml or in this source tree.
//
//nolint:gosec // G101 false positive: this is the NAME of an environment
const DefaultDeployTokenEnv = "POKKUM_DEPLOY_TOKEN"

// ParseDeployTarget converts user input to a DeployTarget, accepting any case
// and surrounding whitespace.
func ParseDeployTarget(s string) (DeployTarget, error) {
	switch DeployTarget(strings.ToLower(strings.TrimSpace(s))) {
	case ports.DeployDokploy:
		return ports.DeployDokploy, nil
	case ports.DeploySwiftwave:
		return ports.DeploySwiftwave, nil
	default:
		return "", fmt.Errorf("deploy target %q (expected %q or %q): %w",
			s, ports.DeployDokploy, ports.DeploySwiftwave, ErrInvalidDeployTarget)
	}
}

// ParseDeployMethod converts user input to a DeployMethod, accepting any case
// and surrounding whitespace. It does not check the method against a target;
// ValidateDeployMethod owns that.
func ParseDeployMethod(s string) (DeployMethod, error) {
	switch DeployMethod(strings.ToLower(strings.TrimSpace(s))) {
	case ports.DeployMethodAPI:
		return ports.DeployMethodAPI, nil
	case ports.DeployMethodWebhook:
		return ports.DeployMethodWebhook, nil
	default:
		return "", fmt.Errorf("deploy method %q (expected %q or %q): %w",
			s, ports.DeployMethodAPI, ports.DeployMethodWebhook, ErrInvalidDeployMethod)
	}
}

// DefaultDeployMethod is the method used when deploy.method is unset.
//
// Dokploy defaults to its API because that is the only way to repoint the
// application at the digest that was just pushed. SwiftWave defaults to its
// webhook because its GraphQL path needs a JWT the operator must obtain and
// refresh separately, whereas the webhook URL is copy-pasteable from the
// application page and carries its own secret.
func DefaultDeployMethod(target DeployTarget) DeployMethod {
	if target == ports.DeploySwiftwave {
		return ports.DeployMethodWebhook
	}
	return ports.DeployMethodAPI
}

// ValidateDeployMethod reports whether method is supported for target.
//
// The matrix lives here rather than inside each adapter so that an unsupported
// combination is rejected during config validation — before a build runs —
// instead of after a push has already happened.
func ValidateDeployMethod(target DeployTarget, method DeployMethod) error {
	switch target {
	case ports.DeployDokploy:
		if method == ports.DeployMethodWebhook {
			// Dokploy's per-application webhook is a git-provider callback:
			// it redeploys from the configured git source, which is not what
			// a Pokkum-built image is. Refuse rather than post to it and
			// report a deployment of somebody else's artifact.
			return fmt.Errorf("target %q does not support method %q (use %q): %w",
				target, method, ports.DeployMethodAPI, ErrInvalidDeployMethod)
		}
	case ports.DeploySwiftwave:
		// Both methods are supported.
	default:
		return fmt.Errorf("target %q: %w", target, ErrInvalidDeployTarget)
	}
	return nil
}

// SupportsImageUpdate reports whether target/method can repoint an application
// at a specific image reference, rather than only redeploying whatever
// reference the platform already holds.
//
// Only Dokploy's API can: application.saveDockerProvider takes the image.
// SwiftWave's webhook triggers a rebuild of the existing deployment, and its
// rebuildApplication mutation does the same; changing the image there needs a
// full updateApplication(input:) round-trip that would have to reconstruct
// every field of the application, which is not a safe thing for a build tool
// to do to a running service.
func SupportsImageUpdate(target DeployTarget, method DeployMethod) bool {
	return target == ports.DeployDokploy && method == ports.DeployMethodAPI
}

// ValidateDeployConfig checks a DeployConfig for internal consistency without
// touching the environment or the network. It is what `pokkum config validate`
// runs, so a broken deploy block is reported at validation time rather than
// after a successful push.
//
// An empty Target means deployment is disabled, which is valid; but a config
// that sets other deploy fields without a target is a typo worth reporting,
// since it would otherwise silently never deploy.
func ValidateDeployConfig(cfg DeployConfig) []string {
	var errs []string

	target := strings.TrimSpace(cfg.Target)
	if target == "" {
		if cfg.Method != "" || cfg.Endpoint != "" || cfg.EndpointEnv != "" ||
			cfg.Application != "" || cfg.TokenEnv != "" || cfg.Auto != nil ||
			cfg.UpdateImage != nil || cfg.Timeout != "" || cfg.RegistryURL != "" ||
			cfg.RegistryUsernameEnv != "" || cfg.RegistryPasswordEnv != "" {
			errs = append(errs, "deploy: fields are set but deploy.target is empty, so no deployment would ever run")
		}
		return errs
	}

	parsedTarget, err := ParseDeployTarget(target)
	if err != nil {
		// Everything below is target-relative, so stop here rather than
		// emitting a cascade of errors derived from an unknown target.
		return append(errs, fmt.Sprintf("invalid deploy.target %q: %v", cfg.Target, err))
	}

	method := DefaultDeployMethod(parsedTarget)
	if strings.TrimSpace(cfg.Method) != "" {
		parsedMethod, err := ParseDeployMethod(cfg.Method)
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid deploy.method %q: %v", cfg.Method, err))
			return errs
		}
		method = parsedMethod
	}
	if err := ValidateDeployMethod(parsedTarget, method); err != nil {
		errs = append(errs, fmt.Sprintf("invalid deploy.method %q: %v", method, err))
	}

	// An endpoint may legitimately come from the environment instead of the
	// file, so only demand one of the two here.
	if strings.TrimSpace(cfg.Endpoint) == "" && strings.TrimSpace(cfg.EndpointEnv) == "" {
		errs = append(errs, "deploy: one of deploy.endpoint or deploy.endpoint_env is required")
	}
	if e := strings.TrimSpace(cfg.Endpoint); e != "" {
		if err := validateDeployEndpoint(e); err != nil {
			errs = append(errs, fmt.Sprintf("invalid deploy.endpoint %q: %v", cfg.Endpoint, err))
		}
	}

	if method == ports.DeployMethodAPI && strings.TrimSpace(cfg.Application) == "" {
		errs = append(errs, fmt.Sprintf("deploy: deploy.application is required for method %q", method))
	}

	// update_image: true against a combination that cannot honour it must be
	// an error, not a silently ignored field. A user who set it believes their
	// deploy is digest-pinned.
	if cfg.UpdateImage != nil && *cfg.UpdateImage && !SupportsImageUpdate(parsedTarget, method) {
		errs = append(errs, fmt.Sprintf(
			"deploy: update_image is not supported for target %q with method %q — that combination redeploys the reference the platform already holds, so pin the application to a mutable tag instead",
			parsedTarget, method))
	}

	// Mirrors the same rule ResolveDeployRequest enforces, so a half-configured
	// credential pair is reported by `pokkum config validate` and not first
	// discovered after a push.
	if (strings.TrimSpace(cfg.RegistryUsernameEnv) == "") != (strings.TrimSpace(cfg.RegistryPasswordEnv) == "") {
		errs = append(errs, "deploy: registry_username_env and registry_password_env must both be set or both be empty")
	}

	if t := strings.TrimSpace(cfg.Timeout); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			errs = append(errs, fmt.Sprintf("invalid deploy.timeout %q: %v", cfg.Timeout, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Sprintf("invalid deploy.timeout %q: must be positive", cfg.Timeout))
		}
	}

	return errs
}

// validateDeployEndpoint rejects anything that is not an absolute http(s) URL.
//
// A relative or scheme-less value would otherwise be joined into a request
// path and produce a confusing transport error much later; and a non-http
// scheme (file://, gopher://) has no business reaching an HTTP client that is
// about to attach a bearer credential.
func validateDeployEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("missing host")
	}
	return nil
}

// ShouldAutoDeploy reports whether a finished build should deploy on its own.
//
// Every condition is a veto, and they are all checked here rather than spread
// across cmd/pokkum, so that "did this build deploy?" has exactly one answer to
// read. A deploy is a side effect against a live system: it must never happen
// for a build that produced nothing publishable.
func ShouldAutoDeploy(cfg DeployConfig, mode OutputMode, dryRun, printManifest, disabled bool) bool {
	if disabled || dryRun || printManifest {
		return false
	}
	if strings.TrimSpace(cfg.Target) == "" {
		return false
	}
	// Only a registry push leaves an image the platform can pull. --local,
	// --tarball and --to-oci-layout all produce something that exists solely
	// on this machine, so there is nothing for a remote PaaS to deploy.
	if mode != OutputPush {
		return false
	}
	if cfg.Auto != nil {
		return *cfg.Auto
	}
	// Configuring a target is the opt-in.
	return true
}

// ResolveDeployRequest turns a DeployConfig plus the image that was just
// published into a ready-to-execute DeployRequest.
//
// getenv is injected rather than calling os.Getenv directly so that resolution
// is testable and so core keeps its no-ambient-state property. Pass os.Getenv
// from cmd.
//
// imageRef must be the digest-pinned reference and taggedRef the primary
// mutable tag (either may be empty if the build produced only one of them).
func ResolveDeployRequest(cfg DeployConfig, imageRef, taggedRef string, getenv func(string) string) (DeployRequest, error) {
	if getenv == nil {
		return DeployRequest{}, fmt.Errorf("resolve deploy request: getenv is nil: %w", ErrInvalidRequest)
	}
	if strings.TrimSpace(cfg.Target) == "" {
		return DeployRequest{}, ErrDeployNotConfigured
	}

	target, err := ParseDeployTarget(cfg.Target)
	if err != nil {
		return DeployRequest{}, err
	}

	method := DefaultDeployMethod(target)
	if strings.TrimSpace(cfg.Method) != "" {
		if method, err = ParseDeployMethod(cfg.Method); err != nil {
			return DeployRequest{}, err
		}
	}
	if err := ValidateDeployMethod(target, method); err != nil {
		return DeployRequest{}, err
	}

	// Endpoint: the environment wins over the file, so a webhook URL (which
	// embeds its own secret) never has to be committed.
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if name := strings.TrimSpace(cfg.EndpointEnv); name != "" {
		if fromEnv := strings.TrimSpace(getenv(name)); fromEnv != "" {
			endpoint = fromEnv
		} else if endpoint == "" {
			return DeployRequest{}, fmt.Errorf(
				"deploy.endpoint_env names %s, which is unset or empty, and deploy.endpoint provides no fallback: %w",
				name, ErrInvalidRequest)
		}
	}
	if endpoint == "" {
		return DeployRequest{}, fmt.Errorf("deploy: no endpoint configured: %w", ErrInvalidRequest)
	}
	if err := validateDeployEndpoint(endpoint); err != nil {
		// The endpoint may have come from EndpointEnv and may therefore be a
		// webhook URL containing a secret, so the value itself is never
		// echoed — only what was wrong with it.
		return DeployRequest{}, fmt.Errorf("deploy: endpoint is not usable (%v): %w", err, ErrInvalidRequest)
	}

	// Token: required for the API method, meaningless for a webhook whose URL
	// already carries its secret.
	token := ""
	if method == ports.DeployMethodAPI {
		tokenEnv := strings.TrimSpace(cfg.TokenEnv)
		if tokenEnv == "" {
			tokenEnv = DefaultDeployTokenEnv
		}
		token = strings.TrimSpace(getenv(tokenEnv))
		if token == "" {
			return DeployRequest{}, fmt.Errorf(
				"deploy: %s is unset or empty (set it, or point deploy.token_env at the variable holding the %s credential): %w",
				tokenEnv, target, ErrDeployTokenMissing)
		}
	}

	if method == ports.DeployMethodAPI && strings.TrimSpace(cfg.Application) == "" {
		return DeployRequest{}, fmt.Errorf("deploy: deploy.application is required for method %q: %w", method, ErrInvalidRequest)
	}

	// update_image defaults OFF (see DeployConfig.UpdateImage for why), and is
	// refused — not ignored — where the target cannot honour it.
	updateImage := false
	if cfg.UpdateImage != nil {
		if *cfg.UpdateImage && !SupportsImageUpdate(target, method) {
			return DeployRequest{}, fmt.Errorf(
				"deploy: update_image is not supported for target %q with method %q: %w",
				target, method, ErrInvalidRequest)
		}
		updateImage = *cfg.UpdateImage
	}
	if updateImage && strings.TrimSpace(imageRef) == "" {
		return DeployRequest{}, fmt.Errorf(
			"deploy: update_image is enabled but no image reference is available to point %q at: %w",
			cfg.Application, ErrInvalidRequest)
	}

	// Pull credentials, resolved only when they can actually be used. Reading
	// them unconditionally would report a missing-variable error for a
	// redeploy that never touches the image reference.
	var registryURL, registryUser, registryPass string
	if updateImage {
		registryURL = strings.TrimSpace(cfg.RegistryURL)
		var err error
		if registryUser, err = lookupDeploySecret(cfg.RegistryUsernameEnv, "deploy.registry_username_env", getenv); err != nil {
			return DeployRequest{}, err
		}
		if registryPass, err = lookupDeploySecret(cfg.RegistryPasswordEnv, "deploy.registry_password_env", getenv); err != nil {
			return DeployRequest{}, err
		}
		// A half-configured credential pair is a typo, not a public image:
		// sending one of the two would authenticate as nobody while looking
		// configured. Refuse rather than degrade to an anonymous pull.
		if (registryUser == "") != (registryPass == "") {
			return DeployRequest{}, fmt.Errorf(
				"deploy: registry_username_env and registry_password_env must both be set or both be empty: %w",
				ErrInvalidRequest)
		}
	}

	timeout := ports.DefaultDeployTimeout
	if t := strings.TrimSpace(cfg.Timeout); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			return DeployRequest{}, fmt.Errorf("deploy: invalid timeout %q: %w", cfg.Timeout, ErrInvalidRequest)
		}
		if d <= 0 {
			return DeployRequest{}, fmt.Errorf("deploy: timeout %q must be positive: %w", cfg.Timeout, ErrInvalidRequest)
		}
		timeout = d
	}

	return DeployRequest{
		Target:           target,
		Method:           method,
		Endpoint:         endpoint,
		Token:            token,
		Application:      strings.TrimSpace(cfg.Application),
		ImageRef:         strings.TrimSpace(imageRef),
		TaggedRef:        strings.TrimSpace(taggedRef),
		UpdateImage:      updateImage,
		RegistryURL:      registryURL,
		RegistryUsername: registryUser,
		RegistryPassword: registryPass,
		Timeout:          timeout,
	}, nil
}

// lookupDeploySecret reads the environment variable named by envName, failing
// closed when the variable is named but unset.
//
// An empty envName means "not configured", which is a legitimate state. A
// NAMED but empty variable is not: it means the operator intended to supply a
// credential and the environment did not carry it, and silently proceeding
// without one is how a deploy authenticates anonymously against a private
// registry and then reports success. The variable's value is never echoed.
func lookupDeploySecret(envName, field string, getenv func(string) string) (string, error) {
	name := strings.TrimSpace(envName)
	if name == "" {
		return "", nil
	}
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return "", fmt.Errorf("deploy: %s names %s, which is unset or empty: %w", field, name, ErrDeployTokenMissing)
	}
	return value, nil
}

// Deploy runs one deployment through deployer.
//
// It exists so that the "did the platform actually start a rollout?" check has
// a single home shared by `pokkum build`'s automatic deploy and the standalone
// `pokkum deploy` command, rather than each call site trusting the adapter's
// error return and reading a result field it might forget to check.
func Deploy(ctx context.Context, deployer Deployer, req DeployRequest) (DeployResult, error) {
	if deployer == nil {
		return DeployResult{}, fmt.Errorf("deploy: no deployer configured for target %q: %w", req.Target, ErrInvalidRequest)
	}
	if deployer.Target() != req.Target {
		return DeployResult{}, fmt.Errorf("deploy: deployer for %q cannot serve target %q: %w",
			deployer.Target(), req.Target, ErrInvalidRequest)
	}

	res, err := deployer.Deploy(ctx, req)
	if err != nil {
		return res, err
	}

	// An adapter returning (Triggered=false, nil) would report a deployment
	// that never started as a success. Adapters are required to fail in that
	// case; this is the backstop that makes the requirement enforceable rather
	// than documentary.
	if !res.Triggered {
		detail := res.Detail
		if detail == "" {
			detail = "platform did not confirm a rollout"
		}
		return res, fmt.Errorf("deploy %q application %q: %s: %w",
			req.Target, req.Application, detail, ErrDeployNotTriggered)
	}
	return res, nil
}
