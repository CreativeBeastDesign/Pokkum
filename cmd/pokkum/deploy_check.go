package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// deployCheckStatus is the outcome of one check.
//
// Three states, not two. "Checked it and it is fine" and "could not check it"
// must never render the same, or `pokkum deploy --check` becomes a command that
// reports success for a configuration it never verified — the exact shape of
// the three fail-open checks found in one pass on 2026-08-21
// (mem:self_review_checklist rows 52 and 53).
type deployCheckStatus string

const (
	deployCheckOK deployCheckStatus = "ok"
	// deployCheckFailed means this will not work as configured.
	deployCheckFailed deployCheckStatus = "failed"
	// deployCheckUnverified means Pokkum cannot determine this either way.
	// It never fails the command: absence of evidence is not evidence.
	deployCheckUnverified deployCheckStatus = "unverified"
)

// deployCheck is one reported line.
type deployCheck struct {
	Name   string            `json:"name"`
	Status deployCheckStatus `json:"status"`
	Detail string            `json:"detail"`
}

// runDeployCheck validates the deploy configuration without deploying anything.
//
// The whole point is that it resolves through core.ResolveDeployRequest — the
// SAME call executeDeploy makes — rather than re-implementing the rules. A
// checker with its own copy of the rules is a validator that can disagree with
// its consumer, which this repo shipped once already: `pokkum config validate`
// reported a config valid that `pokkum deploy` then refused (Lessons.md
// 2026-09-01). A `--check` that did that would be worse than no check, because
// its whole promise is that agreeing with it means the deploy will work.
func runDeployCheck(ctx context.Context, logger *slog.Logger, flags *deployFlags) error {
	outputFormat := ports.OutputFormat(flags.output)

	dir := flags.dir
	if dir == "" {
		dir = "."
	}

	// Same resolution as runDeploy, including profile merging, so
	// `--check -P production` checks what `deploy -P production` would run.
	_, projCfg, _, err := resolveProjectConfig(logger, dir, flags.profile, false)
	if err != nil {
		return err
	}

	var checks []deployCheck
	cfg := ports.DeployConfig{}
	if projCfg != nil {
		cfg = projCfg.Deploy
	}

	if strings.TrimSpace(cfg.Target) == "" {
		checks = append(checks, deployCheck{
			Name:   "configuration",
			Status: deployCheckFailed,
			Detail: fmt.Sprintf("no deploy target configured in %s (add a `deploy:` block with a target of %q or %q)",
				ports.ConfigFilename, ports.DeployDokploy, ports.DeploySwiftwave),
		})
		return reportDeployCheck(outputFormat, "", "", checks)
	}

	req, resolveErr := core.ResolveDeployRequest(cfg, strings.TrimSpace(flags.image), "", os.Getenv)
	if resolveErr != nil {
		checks = append(checks, deployCheck{
			Name:   "configuration",
			Status: deployCheckFailed,
			Detail: resolveErr.Error(),
		})
		return reportDeployCheck(outputFormat, cfg.Target, cfg.Method, checks)
	}

	checks = append(checks, deployCheck{
		Name:   "configuration",
		Status: deployCheckOK,
		Detail: fmt.Sprintf("target %s, method %s, resolved by the same code `pokkum deploy` runs", req.Target, req.Method),
	})
	checks = append(checks, credentialCheck(cfg, req))
	checks = append(checks, applicationCheck(req))
	checks = append(checks, endpointCheck(ctx, req, flags.checkOffline))

	sortDeployChecks(checks)
	return reportDeployCheck(outputFormat, string(req.Target), string(req.Method), checks)
}

// credentialCheck reports whether the credential the deploy will use exists.
//
// It reports PRESENCE, never validity: nothing here asks the platform whether
// the token is accepted, and the detail line says so rather than letting "ok"
// imply an authentication that never happened.
func credentialCheck(cfg ports.DeployConfig, req ports.DeployRequest) deployCheck {
	if req.Method != ports.DeployMethodAPI {
		return deployCheck{
			Name:   "credential",
			Status: deployCheckOK,
			Detail: fmt.Sprintf("not required for method %s — the webhook URL carries its own secret", req.Method),
		}
	}
	name := strings.TrimSpace(cfg.TokenEnv)
	if name == "" {
		name = core.DefaultDeployTokenEnv
	}
	// ResolveDeployRequest already failed the whole check if this were empty,
	// so reaching here means it is set. Its LENGTH is reported, never any part
	// of its value.
	return deployCheck{
		Name:   "credential",
		Status: deployCheckOK,
		Detail: fmt.Sprintf("%s is set (%d characters) — presence only; Pokkum does not ask %s whether it is accepted",
			name, len(req.Token), req.Target),
	}
}

// applicationCheck reports the application id the deploy would address.
//
// Deliberately unverified. Confirming an id exists needs a read-only endpoint,
// and the only two Dokploy endpoints whose contract this repo has verified
// against Dokploy's own source both MUTATE (application.deploy queues a
// rollout, application.saveDockerProvider overwrites credentials). Calling an
// unverified endpoint to make this line say "ok" would be a guess reported as a
// fact — and calling a mutating one would make `--check` deploy.
func applicationCheck(req ports.DeployRequest) deployCheck {
	if req.Method != ports.DeployMethodAPI {
		return deployCheck{
			Name:   "application",
			Status: deployCheckOK,
			Detail: fmt.Sprintf("not required for method %s — the application is identified by the webhook URL", req.Method),
		}
	}
	return deployCheck{
		Name:   "application",
		Status: deployCheckUnverified,
		Detail: fmt.Sprintf("%q is set and syntactically usable, but Pokkum cannot confirm it exists: "+
			"every %s endpoint with a contract Pokkum has verified is a mutating one, and a check must not deploy",
			req.Application, req.Target),
	}
}

// endpointCheck dials the endpoint's host to prove it resolves and accepts a
// connection.
//
// A TCP/TLS-free dial, not an HTTP request: it must work for a webhook endpoint
// whose URL path contains the secret, so nothing beyond host:port is ever used
// and no request is ever sent. That also means it proves reachability only —
// not that anything is listening on the right path, and not that the credential
// works. The detail line says which.
func endpointCheck(ctx context.Context, req ports.DeployRequest, offline bool) deployCheck {
	if offline {
		return deployCheck{
			Name:   "endpoint",
			Status: deployCheckUnverified,
			Detail: "--offline was set, so reachability was not tested",
		}
	}

	parsed, err := url.Parse(req.Endpoint)
	if err != nil || parsed.Host == "" {
		// ResolveDeployRequest already validated the endpoint's shape, so this
		// is defensive. The URL is never echoed: for a webhook it holds a secret.
		return deployCheck{
			Name:   "endpoint",
			Status: deployCheckFailed,
			Detail: "the configured endpoint is not a URL with a host",
		}
	}

	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, dialErr := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(host, port))
	if dialErr != nil {
		return deployCheck{
			Name:   "endpoint",
			Status: deployCheckFailed,
			// host:port only — never parsed.String(), which for a webhook
			// endpoint contains the token in its path.
			Detail: fmt.Sprintf("%s could not be reached: %v", net.JoinHostPort(host, port), redactDialError(dialErr)),
		}
	}
	_ = conn.Close()

	return deployCheck{
		Name:   "endpoint",
		Status: deployCheckOK,
		Detail: fmt.Sprintf("%s accepted a TCP connection — reachability only; no request was sent, so this does not test the path or the credential",
			net.JoinHostPort(host, port)),
	}
}

// redactDialError strips any URL a dial error may carry.
//
// net.Dialer errors carry an address, not a URL, so this is belt-and-braces —
// but the endpoint may be a webhook URL whose path is a secret, and the cost of
// being wrong here is printing a credential.
func redactDialError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// sortDeployChecks orders checks for stable output.
func sortDeployChecks(checks []deployCheck) {
	order := map[string]int{"configuration": 0, "credential": 1, "application": 2, "endpoint": 3}
	sort.SliceStable(checks, func(i, j int) bool { return order[checks[i].Name] < order[checks[j].Name] })
}

// deployCheckSymbol renders a status for a human.
func deployCheckSymbol(s deployCheckStatus) string {
	switch s {
	case deployCheckOK:
		return "✓"
	case deployCheckFailed:
		return "✗"
	default:
		return "?"
	}
}

// reportDeployCheck renders the checks and decides the exit status.
//
// Only a `failed` check fails the command. An `unverified` one never does:
// treating "Pokkum could not determine this" as an error would make --check
// unusable for exactly the configurations it exists to help with, and would
// teach users that its failures are noise.
func reportDeployCheck(format ports.OutputFormat, target, method string, checks []deployCheck) error {
	failed := 0
	unverified := 0
	for _, c := range checks {
		switch c.Status {
		case deployCheckFailed:
			failed++
		case deployCheckUnverified:
			unverified++
		}
	}

	if format == ports.FormatJSON {
		payload := map[string]any{
			"target":     target,
			"method":     method,
			"checks":     checks,
			"failed":     failed,
			"unverified": unverified,
			"ok":         failed == 0,
		}
		if failed > 0 {
			// Still a structured success envelope: the command ran and
			// produced a verdict. The verdict is in "ok"/"failed", which is
			// what a caller should gate on.
			payload["status"] = "failed"
		} else {
			payload["status"] = "ok"
		}
		if err := jsonutils.WriteSuccess(os.Stdout, "deploy-check", payload); err != nil {
			return err
		}
		if failed > 0 {
			return errDeployCheckFailed
		}
		return nil
	}

	fmt.Println("=== pokkum deploy --check ===")
	if target != "" {
		fmt.Printf("Target: %s", target)
		if method != "" {
			fmt.Printf(" (method %s)", method)
		}
		fmt.Println()
	}
	fmt.Println()
	for _, c := range checks {
		fmt.Printf("  %s %-14s %s\n", deployCheckSymbol(c.Status), c.Name, c.Detail)
	}
	fmt.Println()

	switch {
	case failed > 0:
		fmt.Printf("✗ %d check(s) failed — `pokkum deploy` would not succeed as configured.\n", failed)
		return errDeployCheckFailed
	case unverified > 0:
		// Row 47: what this prints when it verified little must not read like
		// a clean bill of health.
		fmt.Printf("✓ Nothing is wrong with what Pokkum can check, but %d item(s) could not be verified.\n", unverified)
		fmt.Println("  This is not a guarantee the deploy will succeed — see the ? lines above for what was not tested.")
	default:
		fmt.Println("✓ Every check passed.")
	}
	return nil
}

// errDeployCheckFailed makes `--check` exit non-zero without the CLI printing a
// second error line under the report it just rendered.
var errDeployCheckFailed = &silentExitError{}

// silentExitError is an error whose message is empty because the command has
// already printed a full, formatted report.
type silentExitError struct{}

func (e *silentExitError) Error() string { return "" }

// isSilentExit reports whether err is a failure whose command already printed
// its own report.
func isSilentExit(err error) bool {
	var silent *silentExitError
	return errors.As(err, &silent)
}
