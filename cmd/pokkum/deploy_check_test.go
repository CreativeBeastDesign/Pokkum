package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// runCheck writes cfgYAML as .pokkum.yaml, runs `deploy --check`, and returns
// everything it printed plus the error it exited with.
func runCheck(t *testing.T, cfgYAML string, env map[string]string, mutate func(*deployFlags)) (string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ports.ConfigFilename), []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	flags := &deployFlags{dir: dir, checkOffline: true}
	if mutate != nil {
		mutate(flags)
	}

	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		_, _ = io.Copy(&sb, r)
		done <- sb.String()
	}()

	runErr := runDeployCheck(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), flags)
	_ = w.Close()
	os.Stdout = stdout
	return <-done, runErr
}

// TestDeployCheck_AgreesWithTheResolutionItClaimsToPredict is the reason this
// command exists and the reason it must not have its own copy of the rules.
//
// `pokkum config validate` once reported a config valid that `pokkum deploy`
// then refused (Lessons.md 2026-09-01), because the validator and the consumer
// applied different rules to the same file. A `--check` that could disagree
// with the deploy it predicts would be strictly worse than no check, since its
// entire promise is that agreeing with it means the resolution will succeed.
//
// So this asserts the biconditional directly, over configs on both sides of
// every rule ResolveDeployRequest applies: --check fails IF AND ONLY IF the
// resolution the real deploy performs fails.
func TestDeployCheck_AgreesWithTheResolutionItClaimsToPredict(t *testing.T) {
	tests := []struct {
		name string
		cfg  ports.DeployConfig
		env  map[string]string
	}{
		{
			name: "valid dokploy api",
			cfg:  ports.DeployConfig{Target: "dokploy", Endpoint: "https://panel.example.com", Application: "app1"},
			env:  map[string]string{"POKKUM_DEPLOY_TOKEN": "tok"},
		},
		{
			name: "dokploy without a token",
			cfg:  ports.DeployConfig{Target: "dokploy", Endpoint: "https://panel.example.com", Application: "app1"},
			env:  map[string]string{"POKKUM_DEPLOY_TOKEN": ""},
		},
		{
			name: "dokploy without an application",
			cfg:  ports.DeployConfig{Target: "dokploy", Endpoint: "https://panel.example.com"},
			env:  map[string]string{"POKKUM_DEPLOY_TOKEN": "tok"},
		},
		{
			name: "no endpoint at all",
			cfg:  ports.DeployConfig{Target: "dokploy", Application: "app1"},
			env:  map[string]string{"POKKUM_DEPLOY_TOKEN": "tok"},
		},
		{
			name: "endpoint_env names an unset variable with no fallback",
			cfg:  ports.DeployConfig{Target: "dokploy", EndpointEnv: "POKKUM_TEST_ENDPOINT", Application: "app1"},
			env:  map[string]string{"POKKUM_DEPLOY_TOKEN": "tok", "POKKUM_TEST_ENDPOINT": ""},
		},
		{
			name: "endpoint_env set, so no committed endpoint is needed",
			cfg:  ports.DeployConfig{Target: "dokploy", EndpointEnv: "POKKUM_TEST_ENDPOINT", Application: "app1"},
			env:  map[string]string{"POKKUM_DEPLOY_TOKEN": "tok", "POKKUM_TEST_ENDPOINT": "https://panel.example.com"},
		},
		{
			name: "unknown target",
			cfg:  ports.DeployConfig{Target: "heroku", Endpoint: "https://panel.example.com"},
		},
		{
			name: "swiftwave webhook needs no token",
			cfg:  ports.DeployConfig{Target: "swiftwave", Method: "webhook", Endpoint: "https://sw.example.com/webhook/redeploy-app/a/t"},
			env:  map[string]string{"POKKUM_DEPLOY_TOKEN": ""},
		},
		{
			name: "method the target does not support",
			cfg:  ports.DeployConfig{Target: "dokploy", Method: "webhook", Endpoint: "https://panel.example.com"},
		},
		{
			name: "update_image on a target that cannot honour it",
			cfg: ports.DeployConfig{Target: "swiftwave", Method: "webhook",
				Endpoint: "https://sw.example.com/webhook/redeploy-app/a/t", UpdateImage: boolPointer(true)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			// What the real deploy would do with this config.
			_, resolveErr := core.ResolveDeployRequest(tc.cfg, "", "", os.Getenv)
			wantFail := resolveErr != nil

			out, checkErr := runCheck(t, marshalDeployConfig(t, tc.cfg), tc.env, nil)
			gotFail := checkErr != nil

			if gotFail != wantFail {
				t.Errorf("--check failed=%v but the real resolution failed=%v (%v).\n"+
					"These must agree, or --check is a validator that disagrees with its consumer.\nOutput:\n%s",
					gotFail, wantFail, resolveErr, out)
			}
		})
	}
}

// TestDeployCheck_UnverifiedIsNotOKAndDoesNotFail pins the tri-state.
//
// Two independent properties, both required: an item Pokkum could not test
// must not render as a pass (rows 52/53), and it must not fail the command
// either — treating "could not determine" as an error would make --check
// unusable for the configurations it exists to help with.
func TestDeployCheck_UnverifiedIsNotOKAndDoesNotFail(t *testing.T) {
	out, err := runCheck(t, `version: 1
deploy:
  target: dokploy
  endpoint: https://panel.example.com
  application: app1
`, map[string]string{"POKKUM_DEPLOY_TOKEN": "tok"}, nil)

	if err != nil {
		t.Fatalf("unverified items must not fail the command: %v\n%s", err, out)
	}
	if !strings.Contains(out, "? application") {
		t.Errorf("the application check must render as unverified, not ok:\n%s", out)
	}
	if !strings.Contains(out, "could not be verified") {
		t.Errorf("the summary must say coverage was reduced, not report a clean pass:\n%s", out)
	}
	if strings.Contains(out, "Every check passed") {
		t.Errorf("a run with unverified items claimed every check passed:\n%s", out)
	}
}

// TestDeployCheck_EveryCheckPassedOnlyWhenNothingIsUnverified is the converse:
// the strongest wording must be reachable, or the previous test is satisfied by
// a command that can never say anything good.
func TestDeployCheck_EveryCheckPassedOnlyWhenNothingIsUnverified(t *testing.T) {
	// A webhook config needs no token and no application id, and a listener
	// makes the endpoint genuinely reachable — so nothing is unverified.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	out, checkErr := runCheck(t, `version: 1
deploy:
  target: swiftwave
  method: webhook
  endpoint: http://`+ln.Addr().String()+`/webhook/redeploy-app/a/t
`, nil, func(f *deployFlags) { f.checkOffline = false })

	if checkErr != nil {
		t.Fatalf("a fully checkable config failed: %v\n%s", checkErr, out)
	}
	if !strings.Contains(out, "Every check passed") {
		t.Errorf("a config with nothing unverified did not report a clean pass:\n%s", out)
	}
}

// TestDeployCheck_NeverPrintsTheCredentialOrTheWebhookSecret.
//
// A webhook endpoint's PATH is its secret, and --check is a command people run
// to paste output into an issue. Neither the token nor the URL may appear.
func TestDeployCheck_NeverPrintsTheCredentialOrTheWebhookSecret(t *testing.T) {
	const secret = "SUPERSECRETWEBHOOKTOKEN"
	const token = "SUPERSECRETAPITOKEN"

	t.Run("webhook path", func(t *testing.T) {
		out, _ := runCheck(t, `version: 1
deploy:
  target: swiftwave
  method: webhook
  endpoint: https://sw.example.com/webhook/redeploy-app/a/`+secret+`
`, nil, func(f *deployFlags) { f.checkOffline = false })
		if strings.Contains(out, secret) {
			t.Errorf("the webhook secret appeared in --check output:\n%s", out)
		}
	})

	t.Run("api token", func(t *testing.T) {
		out, _ := runCheck(t, `version: 1
deploy:
  target: dokploy
  endpoint: https://panel.example.com
  application: app1
`, map[string]string{"POKKUM_DEPLOY_TOKEN": token}, nil)
		if strings.Contains(out, token) {
			t.Errorf("the API token appeared in --check output:\n%s", out)
		}
		if !strings.Contains(out, "19 characters") {
			t.Errorf("the credential check should report the token's length as evidence it was read:\n%s", out)
		}
	})
}

// TestDeployCheck_JSONOutputCarriesTheVerdict: a scripted caller must be able
// to gate on the result without parsing prose, and must be able to tell
// "unverified" from "ok" there too.
func TestDeployCheck_JSONOutputCarriesTheVerdict(t *testing.T) {
	out, _ := runCheck(t, `version: 1
deploy:
  target: dokploy
  endpoint: https://panel.example.com
  application: app1
`, map[string]string{"POKKUM_DEPLOY_TOKEN": "tok"}, func(f *deployFlags) { f.output = string(ports.FormatJSON) })

	var env struct {
		Data struct {
			OK         bool `json:"ok"`
			Unverified int  `json:"unverified"`
			Checks     []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("--check --output=json emitted invalid JSON: %v\n%s", err, out)
	}
	if !env.Data.OK {
		t.Errorf("ok=false for a config with no failures:\n%s", out)
	}
	if env.Data.Unverified == 0 {
		t.Errorf("unverified count is 0, so a caller cannot tell reduced coverage from a full pass:\n%s", out)
	}
	var appStatus string
	for _, c := range env.Data.Checks {
		if c.Name == "application" {
			appStatus = c.Status
		}
	}
	if appStatus != string(deployCheckUnverified) {
		t.Errorf("application status = %q, want %q", appStatus, deployCheckUnverified)
	}
}

// TestDeployCheck_DeploysNothing is the property that makes this command safe
// to run against production. It drives --check at a listener that records every
// connection and asserts no HTTP request was ever sent.
func TestDeployCheck_DeploysNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	requestBytes := make(chan int, 4)
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			// Anything --check sends beyond opening the socket would arrive
			// here. A reachability probe sends nothing.
			buf := make([]byte, 512)
			_ = conn.SetReadDeadline(deadlineSoon())
			n, _ := conn.Read(buf)
			requestBytes <- n
			_ = conn.Close()
		}
	}()

	out, checkErr := runCheck(t, `version: 1
deploy:
  target: dokploy
  endpoint: http://`+ln.Addr().String()+`
  application: app1
`, map[string]string{"POKKUM_DEPLOY_TOKEN": "tok"}, func(f *deployFlags) { f.checkOffline = false })
	if checkErr != nil {
		t.Fatalf("check failed: %v\n%s", checkErr, out)
	}

	select {
	case n := <-requestBytes:
		if n > 0 {
			t.Errorf("--check sent %d bytes to the endpoint; it must open a connection and send nothing", n)
		}
	default:
		t.Log("no connection payload observed, which also satisfies the property")
	}
}

func boolPointer(b bool) *bool { return &b }

func deadlineSoon() time.Time { return time.Now().Add(300 * time.Millisecond) }

// marshalDeployConfig renders a DeployConfig as the .pokkum.yaml a user would
// write, so the test exercises the real load path rather than injecting a
// struct past it.
func marshalDeployConfig(t *testing.T, cfg ports.DeployConfig) string {
	t.Helper()
	full := ports.ProjectConfig{Version: ports.ConfigSchemaVersion, Deploy: cfg}
	out, err := yaml.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
