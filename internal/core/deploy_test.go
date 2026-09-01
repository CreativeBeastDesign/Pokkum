package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// env builds a getenv function from a map, so resolution is tested without
// touching the process environment.
func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func boolp(b bool) *bool { return &b }

// TestResolveDeployRequest_TokenFailsClosed is the central credential rule: a
// missing API token must abort the deploy, never degrade to an unauthenticated
// call. The three cases differ in *why* the token is absent, and all three
// must fail — an absent variable, a named-but-empty one, and one that is only
// whitespace.
func TestResolveDeployRequest_TokenFailsClosed(t *testing.T) {
	base := DeployConfig{
		Target:      "dokploy",
		Endpoint:    "https://panel.example.com",
		Application: "app-1",
	}

	tests := []struct {
		name string
		cfg  DeployConfig
		env  map[string]string
	}{
		{"default variable unset", base, nil},
		{"default variable empty", base, map[string]string{DefaultDeployTokenEnv: ""}},
		{"default variable whitespace", base, map[string]string{DefaultDeployTokenEnv: "   "}},
		{
			name: "named variable unset",
			cfg: DeployConfig{
				Target: "dokploy", Endpoint: "https://panel.example.com",
				Application: "app-1", TokenEnv: "MY_TOKEN",
			},
			env: map[string]string{DefaultDeployTokenEnv: "wrong-variable-is-set"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveDeployRequest(tc.cfg, "ghcr.io/me/app@sha256:beef", "", env(tc.env))
			if err == nil {
				t.Fatal("resolution succeeded with no credential available")
			}
			if !errors.Is(err, ErrDeployTokenMissing) {
				t.Errorf("error = %v, want it to wrap ErrDeployTokenMissing", err)
			}
		})
	}
}

// TestResolveDeployRequest_TokenEnvIsHonoured is the positive counterpart: the
// checks above would pass just as well if resolution always failed.
func TestResolveDeployRequest_TokenEnvIsHonoured(t *testing.T) {
	req, err := ResolveDeployRequest(DeployConfig{
		Target:      "dokploy",
		Endpoint:    "https://panel.example.com",
		Application: "app-1",
		TokenEnv:    "MY_TOKEN",
	}, "ghcr.io/me/app@sha256:beef", "ghcr.io/me/app:latest",
		env(map[string]string{"MY_TOKEN": "secret", DefaultDeployTokenEnv: "the-default-one"}))
	if err != nil {
		t.Fatalf("ResolveDeployRequest: %v", err)
	}
	if req.Token != "secret" {
		t.Errorf("Token = %q, want the value of the variable token_env names, not the default", req.Token)
	}
	if req.Method != ports.DeployMethodAPI {
		t.Errorf("Method = %q, want dokploy's default of %q", req.Method, ports.DeployMethodAPI)
	}
	if req.TaggedRef != "ghcr.io/me/app:latest" {
		t.Errorf("TaggedRef = %q", req.TaggedRef)
	}
}

// TestResolveDeployRequest_WebhookNeedsNoToken checks that the webhook method,
// whose secret lives in the URL, is not blocked by the credential rule.
func TestResolveDeployRequest_WebhookNeedsNoToken(t *testing.T) {
	req, err := ResolveDeployRequest(DeployConfig{
		Target:      "swiftwave",
		EndpointEnv: "SW_WEBHOOK",
	}, "ghcr.io/me/app:latest", "", env(map[string]string{
		"SW_WEBHOOK": "https://sw.example.com/webhook/redeploy-app/a/b",
	}))
	if err != nil {
		t.Fatalf("ResolveDeployRequest: %v", err)
	}
	if req.Method != ports.DeployMethodWebhook {
		t.Errorf("Method = %q, want swiftwave's default of %q", req.Method, ports.DeployMethodWebhook)
	}
	if req.Token != "" {
		t.Error("a token was resolved for the webhook method")
	}
}

// TestResolveDeployRequest_EndpointEnvWinsOverFile pins the precedence that
// lets a secret-bearing webhook URL stay out of the committed config.
func TestResolveDeployRequest_EndpointEnvWinsOverFile(t *testing.T) {
	req, err := ResolveDeployRequest(DeployConfig{
		Target:      "swiftwave",
		Endpoint:    "https://placeholder.example.com/webhook/redeploy-app/a/b",
		EndpointEnv: "SW_WEBHOOK",
	}, "ghcr.io/me/app:latest", "", env(map[string]string{
		"SW_WEBHOOK": "https://real.example.com/webhook/redeploy-app/x/y",
	}))
	if err != nil {
		t.Fatalf("ResolveDeployRequest: %v", err)
	}
	if !strings.Contains(req.Endpoint, "real.example.com") {
		t.Errorf("Endpoint = %q, want the environment value to win", req.Endpoint)
	}

	// And with the variable unset, the file value is the documented fallback.
	req, err = ResolveDeployRequest(DeployConfig{
		Target:      "swiftwave",
		Endpoint:    "https://fallback.example.com/webhook/redeploy-app/a/b",
		EndpointEnv: "SW_WEBHOOK",
	}, "ghcr.io/me/app:latest", "", env(nil))
	if err != nil {
		t.Fatalf("ResolveDeployRequest with unset endpoint_env: %v", err)
	}
	if !strings.Contains(req.Endpoint, "fallback.example.com") {
		t.Errorf("Endpoint = %q, want the file value as fallback", req.Endpoint)
	}
}

// TestResolveDeployRequest_RejectsNonHTTPEndpoint keeps a non-http scheme from
// reaching a client that is about to attach a credential.
func TestResolveDeployRequest_RejectsNonHTTPEndpoint(t *testing.T) {
	for _, endpoint := range []string{"file:///etc/passwd", "panel.example.com", "ftp://example.com"} {
		_, err := ResolveDeployRequest(DeployConfig{
			Target: "dokploy", Endpoint: endpoint, Application: "app-1",
		}, "ref", "", env(map[string]string{DefaultDeployTokenEnv: "t"}))
		if err == nil {
			t.Errorf("endpoint %q was accepted", endpoint)
		}
	}
}

// TestResolveDeployRequest_RejectsUnsupportedUpdateImage checks that
// update_image against a target that cannot honour it is an error rather than
// a silently ignored setting — the operator believes their deploy is
// digest-pinned.
func TestResolveDeployRequest_RejectsUnsupportedUpdateImage(t *testing.T) {
	_, err := ResolveDeployRequest(DeployConfig{
		Target:      "swiftwave",
		Endpoint:    "https://sw.example.com/webhook/redeploy-app/a/b",
		UpdateImage: boolp(true),
	}, "ghcr.io/me/app@sha256:beef", "", env(nil))
	if err == nil {
		t.Fatal("update_image was accepted for swiftwave, which cannot repoint an application")
	}
}

// TestResolveDeployRequest_UpdateImageDefaultsOff pins the default that keeps
// Dokploy's overwrite-semantics saveDockerProvider from running unasked.
func TestResolveDeployRequest_UpdateImageDefaultsOff(t *testing.T) {
	req, err := ResolveDeployRequest(DeployConfig{
		Target: "dokploy", Endpoint: "https://panel.example.com", Application: "app-1",
	}, "ghcr.io/me/app@sha256:beef", "", env(map[string]string{DefaultDeployTokenEnv: "t"}))
	if err != nil {
		t.Fatalf("ResolveDeployRequest: %v", err)
	}
	if req.UpdateImage {
		t.Error("UpdateImage defaulted to true; Dokploy's saveDockerProvider clears registry credentials, so it must be opt-in")
	}
}

// TestResolveDeployRequest_HalfConfiguredRegistryCredentials refuses the shape
// that would authenticate as nobody while looking configured.
func TestResolveDeployRequest_HalfConfiguredRegistryCredentials(t *testing.T) {
	for _, cfg := range []DeployConfig{
		{RegistryUsernameEnv: "U"},
		{RegistryPasswordEnv: "P"},
	} {
		cfg.Target = "dokploy"
		cfg.Endpoint = "https://panel.example.com"
		cfg.Application = "app-1"
		cfg.UpdateImage = boolp(true)

		_, err := ResolveDeployRequest(cfg, "ghcr.io/me/app@sha256:beef", "", env(map[string]string{
			DefaultDeployTokenEnv: "t", "U": "user", "P": "pass",
		}))
		if err == nil {
			t.Errorf("a half-configured credential pair was accepted: %+v", cfg)
		}
	}
}

// TestResolveDeployRequest_NamedRegistryVariableMustNotBeEmpty covers the same
// fail-closed rule as the token: a named variable that the environment does not
// carry is an error, not an anonymous pull.
func TestResolveDeployRequest_NamedRegistryVariableMustNotBeEmpty(t *testing.T) {
	_, err := ResolveDeployRequest(DeployConfig{
		Target: "dokploy", Endpoint: "https://panel.example.com", Application: "app-1",
		UpdateImage:         boolp(true),
		RegistryUsernameEnv: "U",
		RegistryPasswordEnv: "P",
	}, "ghcr.io/me/app@sha256:beef", "", env(map[string]string{
		DefaultDeployTokenEnv: "t", "U": "user", // P deliberately absent
	}))
	if err == nil {
		t.Fatal("a named-but-unset registry password variable was accepted")
	}
	if !errors.Is(err, ErrDeployTokenMissing) {
		t.Errorf("error = %v, want it to wrap ErrDeployTokenMissing", err)
	}
}

// TestShouldAutoDeploy enumerates every veto. A deploy is a side effect against
// a live system, so each of these must independently prevent one.
func TestShouldAutoDeploy(t *testing.T) {
	configured := DeployConfig{Target: "dokploy"}

	tests := []struct {
		name          string
		cfg           DeployConfig
		mode          OutputMode
		dryRun        bool
		printManifest bool
		disabled      bool
		want          bool
	}{
		{name: "configured push deploys", cfg: configured, mode: OutputPush, want: true},
		{name: "no target never deploys", cfg: DeployConfig{}, mode: OutputPush, want: false},
		{name: "auto false opts out", cfg: DeployConfig{Target: "dokploy", Auto: boolp(false)}, mode: OutputPush, want: false},
		{name: "auto true is explicit opt-in", cfg: DeployConfig{Target: "dokploy", Auto: boolp(true)}, mode: OutputPush, want: true},
		{name: "--no-deploy vetoes", cfg: configured, mode: OutputPush, disabled: true, want: false},
		{name: "--dry-run vetoes", cfg: configured, mode: OutputPush, dryRun: true, want: false},
		{name: "--print-manifest vetoes", cfg: configured, mode: OutputPush, printManifest: true, want: false},
		{name: "--local has nothing to pull", cfg: configured, mode: OutputLocal, want: false},
		{name: "--tarball has nothing to pull", cfg: configured, mode: OutputTarball, want: false},
		{name: "--to-oci-layout has nothing to pull", cfg: configured, mode: OutputOCILayout, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldAutoDeploy(tc.cfg, tc.mode, tc.dryRun, tc.printManifest, tc.disabled)
			if got != tc.want {
				t.Errorf("ShouldAutoDeploy = %v, want %v", got, tc.want)
			}
		})
	}
}

// stubDeployer is a Deployer whose result and error are dictated by the test.
type stubDeployer struct {
	target ports.DeployTarget
	result ports.DeployResult
	err    error
}

func (s *stubDeployer) Target() ports.DeployTarget { return s.target }
func (s *stubDeployer) Deploy(context.Context, ports.DeployRequest) (ports.DeployResult, error) {
	return s.result, s.err
}

// TestDeploy_UntriggeredResultIsAnError is the backstop that makes the
// adapter contract enforceable: an adapter returning "no rollout" with a nil
// error would otherwise report a deployment that never happened as a success.
func TestDeploy_UntriggeredResultIsAnError(t *testing.T) {
	_, err := Deploy(context.Background(), &stubDeployer{
		target: ports.DeployDokploy,
		result: ports.DeployResult{Target: ports.DeployDokploy, Triggered: false, Detail: "platform said no"},
	}, DeployRequest{Target: ports.DeployDokploy, Application: "app-1"})
	if err == nil {
		t.Fatal("a result with Triggered=false and a nil error was reported as a successful deploy")
	}
	if !errors.Is(err, ErrDeployNotTriggered) {
		t.Errorf("error = %v, want it to wrap ErrDeployNotTriggered", err)
	}
	if !strings.Contains(err.Error(), "platform said no") {
		t.Errorf("error drops the platform's own explanation: %v", err)
	}
}

// TestDeploy_RejectsMismatchedDeployer keeps a deployer for one platform from
// being handed a request addressed to another.
func TestDeploy_RejectsMismatchedDeployer(t *testing.T) {
	_, err := Deploy(context.Background(), &stubDeployer{
		target: ports.DeploySwiftwave,
		result: ports.DeployResult{Triggered: true},
	}, DeployRequest{Target: ports.DeployDokploy})
	if err == nil {
		t.Fatal("a swiftwave deployer served a dokploy request")
	}

	if _, err := Deploy(context.Background(), nil, DeployRequest{Target: ports.DeployDokploy}); err == nil {
		t.Fatal("a nil deployer was accepted")
	}
}

// TestValidateDeployConfig covers what `pokkum config validate` must catch
// before a build runs — each case a distinct misconfiguration, plus the two
// valid shapes, so the validator is shown to accept as well as reject.
func TestValidateDeployConfig(t *testing.T) {
	tests := []struct {
		name      string
		cfg       DeployConfig
		wantError bool
		contains  string
	}{
		{
			name: "empty config is valid",
			cfg:  DeployConfig{},
		},
		{
			name: "valid dokploy api",
			cfg:  DeployConfig{Target: "dokploy", Endpoint: "https://panel.example.com", Application: "app-1"},
		},
		{
			name: "valid swiftwave webhook",
			cfg:  DeployConfig{Target: "swiftwave", EndpointEnv: "SW_WEBHOOK"},
		},
		{
			name:      "settings with no target would never run",
			cfg:       DeployConfig{Endpoint: "https://panel.example.com", Application: "app-1"},
			wantError: true, contains: "deploy.target is empty",
		},
		{
			name:      "unknown target",
			cfg:       DeployConfig{Target: "heroku", Endpoint: "https://example.com"},
			wantError: true, contains: "invalid deploy.target",
		},
		{
			name:      "unknown method",
			cfg:       DeployConfig{Target: "dokploy", Method: "carrier-pigeon", Endpoint: "https://example.com", Application: "a"},
			wantError: true, contains: "invalid deploy.method",
		},
		{
			name:      "dokploy rejects the webhook method",
			cfg:       DeployConfig{Target: "dokploy", Method: "webhook", Endpoint: "https://example.com", Application: "a"},
			wantError: true, contains: "does not support method",
		},
		{
			name:      "no endpoint at all",
			cfg:       DeployConfig{Target: "dokploy", Application: "a"},
			wantError: true, contains: "deploy.endpoint",
		},
		{
			name:      "api without an application id",
			cfg:       DeployConfig{Target: "dokploy", Endpoint: "https://example.com"},
			wantError: true, contains: "deploy.application is required",
		},
		{
			name:      "update_image on an unsupported combination",
			cfg:       DeployConfig{Target: "swiftwave", Endpoint: "https://example.com/webhook/redeploy-app/a/b", UpdateImage: boolp(true)},
			wantError: true, contains: "update_image is not supported",
		},
		{
			name:      "unparseable timeout",
			cfg:       DeployConfig{Target: "dokploy", Endpoint: "https://example.com", Application: "a", Timeout: "soon"},
			wantError: true, contains: "invalid deploy.timeout",
		},
		{
			name:      "negative timeout",
			cfg:       DeployConfig{Target: "dokploy", Endpoint: "https://example.com", Application: "a", Timeout: "-5s"},
			wantError: true, contains: "must be positive",
		},
		{
			name:      "half-configured registry credentials",
			cfg:       DeployConfig{Target: "dokploy", Endpoint: "https://example.com", Application: "a", RegistryUsernameEnv: "U"},
			wantError: true, contains: "must both be set",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			problems := ValidateDeployConfig(tc.cfg)
			if tc.wantError {
				if len(problems) == 0 {
					t.Fatalf("config %+v passed validation", tc.cfg)
				}
				if tc.contains != "" && !strings.Contains(strings.Join(problems, "; "), tc.contains) {
					t.Errorf("problems %v do not mention %q", problems, tc.contains)
				}
				return
			}
			if len(problems) != 0 {
				t.Errorf("valid config %+v was rejected: %v", tc.cfg, problems)
			}
		})
	}
}

// TestDeployTimeoutDefault checks the documented default reaches the request.
func TestDeployTimeoutDefault(t *testing.T) {
	req, err := ResolveDeployRequest(DeployConfig{
		Target: "dokploy", Endpoint: "https://panel.example.com", Application: "app-1",
	}, "ref", "", env(map[string]string{DefaultDeployTokenEnv: "t"}))
	if err != nil {
		t.Fatalf("ResolveDeployRequest: %v", err)
	}
	if req.Timeout != ports.DefaultDeployTimeout {
		t.Errorf("Timeout = %v, want %v", req.Timeout, ports.DefaultDeployTimeout)
	}

	req, err = ResolveDeployRequest(DeployConfig{
		Target: "dokploy", Endpoint: "https://panel.example.com", Application: "app-1", Timeout: "5m",
	}, "ref", "", env(map[string]string{DefaultDeployTokenEnv: "t"}))
	if err != nil {
		t.Fatalf("ResolveDeployRequest: %v", err)
	}
	if req.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v, want 5m", req.Timeout)
	}
}
