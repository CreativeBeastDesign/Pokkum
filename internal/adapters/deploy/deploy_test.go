package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordedRequest captures what the adapter actually sent, so assertions are
// made against the wire bytes rather than against the adapter's own bookkeeping.
type recordedRequest struct {
	Path        string
	Body        string
	Headers     http.Header
	ContentType string
}

type recorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (r *recorder) record(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, recordedRequest{
		Path:        req.URL.Path,
		Body:        string(body),
		Headers:     req.Header.Clone(),
		ContentType: req.Header.Get("Content-Type"),
	})
}

func (r *recorder) all() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.requests...)
}

// --- SwiftWave webhook -------------------------------------------------------

// TestSwiftwaveWebhook_NoRebuildIsAFailure is the reason this adapter reads
// response bodies at all.
//
// SwiftWave's redeploy webhook (swiftwave_service/rest/webhook.go) answers
// HTTP 200 with "OK - No rebuild" whenever it decides the posted body does not
// concern the application's configured image. A status-code check reports that
// as a successful deployment. It must be an error, and specifically
// core.ErrDeployNotTriggered so callers can tell it apart from a transport or
// auth failure.
func TestSwiftwaveWebhook_NoRebuildIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK - No rebuild"))
	}))
	defer srv.Close()

	d := &SwiftwaveDeployer{logger: testLogger()}
	res, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:   ports.DeploySwiftwave,
		Method:   ports.DeployMethodWebhook,
		Endpoint: srv.URL + "/webhook/redeploy-app/app-123/token-abc",
		ImageRef: "ghcr.io/me/app@sha256:deadbeef",
		Timeout:  5 * time.Second,
	})
	if err == nil {
		t.Fatal("a 200 'OK - No rebuild' response was reported as a successful deploy")
	}
	if !errors.Is(err, core.ErrDeployNotTriggered) {
		t.Errorf("error = %v, want it to wrap ErrDeployNotTriggered", err)
	}
	if res.Triggered {
		t.Error("result.Triggered = true for a response that explicitly declined to rebuild")
	}
	// The failure has to explain itself: the fix is in the SwiftWave
	// application's own image setting, which the message must point at.
	if !strings.Contains(err.Error(), "owner/name") {
		t.Errorf("error message does not explain the image-name matching rule: %v", err)
	}
}

// TestSwiftwaveWebhook_TriggeredIsASuccess is the positive half of the pair
// above: with the fix in place the adapter must still accept the response that
// really does mean a rollout started, or the check above would pass simply by
// rejecting everything.
func TestSwiftwaveWebhook_TriggeredIsASuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK - Rebuild triggered"))
	}))
	defer srv.Close()

	d := &SwiftwaveDeployer{logger: testLogger()}
	res, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:   ports.DeploySwiftwave,
		Method:   ports.DeployMethodWebhook,
		Endpoint: srv.URL + "/webhook/redeploy-app/app-123/token-abc",
		ImageRef: "ghcr.io/me/app@sha256:deadbeef",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !res.Triggered {
		t.Error("result.Triggered = false after 'OK - Rebuild triggered'")
	}
	if res.Application != "app-123" {
		t.Errorf("Application = %q, want %q parsed out of the webhook path", res.Application, "app-123")
	}
}

// TestSwiftwaveWebhook_PostsImageRefsAsPlainText pins the request side of the
// same contract. SwiftWave matches the body against its configured image name,
// and url.QueryUnescape's the body first — so the body must carry the
// references and must not be a format that introduces percent escapes.
func TestSwiftwaveWebhook_PostsImageRefsAsPlainText(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte("OK - Rebuild triggered"))
	}))
	defer srv.Close()

	d := &SwiftwaveDeployer{logger: testLogger()}
	if _, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:    ports.DeploySwiftwave,
		Method:    ports.DeployMethodWebhook,
		Endpoint:  srv.URL + "/webhook/redeploy-app/app-123/token-abc",
		ImageRef:  "ghcr.io/me/app@sha256:deadbeef",
		TaggedRef: "ghcr.io/me/app:latest",
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("got %d requests, want 1", len(got))
	}
	// SwiftWave reduces its configured image to the last two path segments,
	// so "me/app" is the substring that actually has to be present.
	if !strings.Contains(got[0].Body, "me/app") {
		t.Errorf("request body %q does not contain the image name SwiftWave matches on", got[0].Body)
	}
	if !strings.Contains(got[0].Body, "ghcr.io/me/app:latest") {
		t.Errorf("request body %q omits the tagged reference", got[0].Body)
	}
	if strings.Contains(got[0].Body, "%") {
		t.Errorf("request body %q contains a percent escape; SwiftWave url-unescapes the body and falls back to an empty string on failure", got[0].Body)
	}
	if !strings.HasPrefix(got[0].ContentType, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", got[0].ContentType)
	}
}

// TestSwiftwaveWebhook_UnrecognisedSuccessBodyFails covers the third response
// class: a 200 that is neither of SwiftWave's two known strings (a reverse
// proxy's HTML, a captive portal). It is not evidence a rollout started.
func TestSwiftwaveWebhook_UnrecognisedSuccessBodyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>Sign in to continue</body></html>"))
	}))
	defer srv.Close()

	d := &SwiftwaveDeployer{logger: testLogger()}
	res, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:   ports.DeploySwiftwave,
		Method:   ports.DeployMethodWebhook,
		Endpoint: srv.URL + "/webhook/redeploy-app/app-1/tok",
		ImageRef: "ghcr.io/me/app:latest",
	})
	if err == nil {
		t.Fatal("an unrecognised 200 body was accepted as a successful deploy")
	}
	if res.Triggered {
		t.Error("result.Triggered = true for an unrecognised response")
	}
}

// TestSwiftwaveWebhook_ErrorDoesNotLeakToken checks that a failing request
// never puts the webhook URL — whose last path segment is the shared secret —
// into an error a caller will log.
func TestSwiftwaveWebhook_ErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // force a connection-refused transport error

	d := &SwiftwaveDeployer{logger: testLogger()}
	_, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:   ports.DeploySwiftwave,
		Method:   ports.DeployMethodWebhook,
		Endpoint: url + "/webhook/redeploy-app/app-1/super-secret-token",
		ImageRef: "ghcr.io/me/app:latest",
		Timeout:  2 * time.Second,
	})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Errorf("error leaks the webhook token: %v", err)
	}
}

// TestSwiftwave_RejectsUpdateImage pins the capability boundary: neither
// SwiftWave path can repoint an application, so the request must be refused
// rather than accepted and silently not honoured.
func TestSwiftwave_RejectsUpdateImage(t *testing.T) {
	d := &SwiftwaveDeployer{logger: testLogger()}
	_, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:      ports.DeploySwiftwave,
		Method:      ports.DeployMethodWebhook,
		Endpoint:    "https://example.invalid/webhook/redeploy-app/a/b",
		ImageRef:    "ghcr.io/me/app@sha256:beef",
		UpdateImage: true,
	})
	if err == nil {
		t.Fatal("swiftwave accepted update_image, which it cannot honour")
	}
}

// --- SwiftWave GraphQL -------------------------------------------------------

// TestSwiftwaveGraphQL_ResponseClassification walks the response shapes the
// GraphQL path has to tell apart. GraphQL answers 200 for application-level
// errors too, so this is the same "success status, unsuccessful outcome"
// hazard as the webhook.
func TestSwiftwaveGraphQL_ResponseClassification(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		wantErr     bool
		wantSentinl error
	}{
		{
			name:     "rollout started",
			response: `{"data":{"rebuildApplication":true}}`,
		},
		{
			name:        "mutation returned false",
			response:    `{"data":{"rebuildApplication":false}}`,
			wantErr:     true,
			wantSentinl: core.ErrDeployNotTriggered,
		},
		{
			name:        "graphql errors with a 200",
			response:    `{"errors":[{"message":"application not found"}]}`,
			wantErr:     true,
			wantSentinl: core.ErrDeployFailed,
		},
		{
			name:        "no data field at all",
			response:    `{}`,
			wantErr:     true,
			wantSentinl: core.ErrDeployFailed,
		},
		{
			name:        "not graphql json",
			response:    `<html>login</html>`,
			wantErr:     true,
			wantSentinl: core.ErrDeployFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()

			d := &SwiftwaveDeployer{logger: testLogger()}
			res, err := d.Deploy(context.Background(), ports.DeployRequest{
				Target:      ports.DeploySwiftwave,
				Method:      ports.DeployMethodAPI,
				Endpoint:    srv.URL,
				Token:       "jwt-token",
				Application: "app-1",
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("response %q was accepted as a successful deploy", tc.response)
				}
				if !errors.Is(err, tc.wantSentinl) {
					t.Errorf("error = %v, want it to wrap %v", err, tc.wantSentinl)
				}
				if res.Triggered {
					t.Error("result.Triggered = true on a failed deploy")
				}
				return
			}
			if err != nil {
				t.Fatalf("Deploy: %v", err)
			}
			if !res.Triggered {
				t.Error("result.Triggered = false on a successful deploy")
			}
		})
	}
}

// TestSwiftwaveGraphQL_SendsBearerToken pins the auth header SwiftWave's JWT
// middleware expects.
func TestSwiftwaveGraphQL_SendsBearerToken(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte(`{"data":{"rebuildApplication":true}}`))
	}))
	defer srv.Close()

	d := &SwiftwaveDeployer{logger: testLogger()}
	if _, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:      ports.DeploySwiftwave,
		Method:      ports.DeployMethodAPI,
		Endpoint:    srv.URL,
		Token:       "jwt-token",
		Application: "app-1",
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("got %d requests, want 1", len(got))
	}
	if auth := got[0].Headers.Get("Authorization"); auth != "Bearer jwt-token" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer jwt-token")
	}
	if got[0].Path != "/graphql" {
		t.Errorf("path = %q, want /graphql", got[0].Path)
	}
	if !strings.Contains(got[0].Body, "rebuildApplication") {
		t.Errorf("body %q does not carry the rebuildApplication mutation", got[0].Body)
	}
}

// --- Dokploy -----------------------------------------------------------------

// TestDokploy_UpdateImageSendsEveryRequiredField pins the payload against
// Dokploy's own input schema. apiSaveDockerProvider is .required() on all five
// picked fields, so every key must be PRESENT — and because the columns are
// nullable, an unset credential must serialise as null rather than "".
//
// Two cases with differing content, not one: a public image (credentials null)
// and a private one (credentials populated) take different paths through
// nilIfEmpty, and only running both proves the pointer handling works in each
// direction.
func TestDokploy_UpdateImageSendsEveryRequiredField(t *testing.T) {
	tests := []struct {
		name         string
		req          ports.DeployRequest
		wantUser     any
		wantPassword any
		wantRegistry any
	}{
		{
			name: "public image sends explicit nulls",
			req: ports.DeployRequest{
				ImageRef: "ghcr.io/me/app@sha256:beef",
			},
			wantUser:     nil,
			wantPassword: nil,
			wantRegistry: nil,
		},
		{
			name: "private image sends the credentials",
			req: ports.DeployRequest{
				ImageRef:         "ghcr.io/me/app@sha256:beef",
				RegistryURL:      "ghcr.io",
				RegistryUsername: "me",
				RegistryPassword: "pat-token",
			},
			wantUser:     "me",
			wantPassword: "pat-token",
			wantRegistry: "ghcr.io",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				_, _ = w.Write([]byte("true"))
			}))
			defer srv.Close()

			req := tc.req
			req.Target = ports.DeployDokploy
			req.Method = ports.DeployMethodAPI
			req.Endpoint = srv.URL
			req.Token = "api-key"
			req.Application = "app-1"
			req.UpdateImage = true

			d := &DokployDeployer{logger: testLogger()}
			if _, err := d.Deploy(context.Background(), req); err != nil {
				t.Fatalf("Deploy: %v", err)
			}

			got := rec.all()
			if len(got) != 2 {
				t.Fatalf("got %d requests, want 2 (saveDockerProvider then deploy)", len(got))
			}
			if got[0].Path != dokploySaveDockerProviderPath {
				t.Errorf("first request path = %q, want %q", got[0].Path, dokploySaveDockerProviderPath)
			}
			if got[1].Path != dokployDeployPath {
				t.Errorf("second request path = %q, want %q", got[1].Path, dokployDeployPath)
			}
			if key := got[0].Headers.Get(dokployAPIKeyHeader); key != "api-key" {
				t.Errorf("%s = %q, want %q", dokployAPIKeyHeader, key, "api-key")
			}

			// Decode into map[string]any so a MISSING key is distinguishable
			// from a null one — the distinction Dokploy's schema turns on.
			var payload map[string]any
			if err := json.Unmarshal([]byte(got[0].Body), &payload); err != nil {
				t.Fatalf("saveDockerProvider body is not JSON: %v (%s)", err, got[0].Body)
			}
			for _, key := range []string{"applicationId", "dockerImage", "username", "password", "registryUrl"} {
				if _, present := payload[key]; !present {
					t.Errorf("saveDockerProvider payload omits required key %q: %s", key, got[0].Body)
				}
			}
			if payload["username"] != tc.wantUser {
				t.Errorf("username = %#v, want %#v", payload["username"], tc.wantUser)
			}
			if payload["password"] != tc.wantPassword {
				t.Errorf("password = %#v, want %#v", payload["password"], tc.wantPassword)
			}
			if payload["registryUrl"] != tc.wantRegistry {
				t.Errorf("registryUrl = %#v, want %#v", payload["registryUrl"], tc.wantRegistry)
			}
			if payload["dockerImage"] != "ghcr.io/me/app@sha256:beef" {
				t.Errorf("dockerImage = %#v", payload["dockerImage"])
			}
		})
	}
}

// TestDokploy_ClearedCredentialsAreReported checks that the destructive side
// effect of update_image without credentials is visible in the result, rather
// than happening silently.
func TestDokploy_ClearedCredentialsAreReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("true"))
	}))
	defer srv.Close()

	d := &DokployDeployer{logger: testLogger()}
	res, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:      ports.DeployDokploy,
		Method:      ports.DeployMethodAPI,
		Endpoint:    srv.URL,
		Token:       "api-key",
		Application: "app-1",
		ImageRef:    "ghcr.io/me/app@sha256:beef",
		UpdateImage: true,
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !strings.Contains(res.Detail, "registry credentials cleared") {
		t.Errorf("Detail = %q, want it to report that stored registry credentials were cleared", res.Detail)
	}
}

// TestDokploy_NoImageUpdateSkipsSaveDockerProvider proves the default path
// does not touch the application's provider settings at all — the reason
// update_image defaults off.
func TestDokploy_NoImageUpdateSkipsSaveDockerProvider(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte("true"))
	}))
	defer srv.Close()

	d := &DokployDeployer{logger: testLogger()}
	res, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:      ports.DeployDokploy,
		Method:      ports.DeployMethodAPI,
		Endpoint:    srv.URL,
		Token:       "api-key",
		Application: "app-1",
		ImageRef:    "ghcr.io/me/app@sha256:beef",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("got %d requests, want exactly 1 (application.deploy only)", len(got))
	}
	if got[0].Path != dokployDeployPath {
		t.Errorf("path = %q, want %q", got[0].Path, dokployDeployPath)
	}
	if res.ImageUpdated {
		t.Error("ImageUpdated = true without update_image")
	}
}

// TestDokploy_FailedImageUpdateDoesNotDeploy checks the ordering guarantee: if
// repointing the application fails, the rollout must not be triggered, because
// that would deploy the OLD image and report it as a successful new deploy.
func TestDokploy_FailedImageUpdateDoesNotDeploy(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if r.URL.Path == dokploySaveDockerProviderPath {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"invalid image"}`))
			return
		}
		_, _ = w.Write([]byte("true"))
	}))
	defer srv.Close()

	d := &DokployDeployer{logger: testLogger()}
	_, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:      ports.DeployDokploy,
		Method:      ports.DeployMethodAPI,
		Endpoint:    srv.URL,
		Token:       "api-key",
		Application: "app-1",
		ImageRef:    "ghcr.io/me/app@sha256:beef",
		UpdateImage: true,
	})
	if err == nil {
		t.Fatal("a failed saveDockerProvider was not reported as an error")
	}
	for _, r := range rec.all() {
		if r.Path == dokployDeployPath {
			t.Fatal("application.deploy was called after the image update failed, which would roll out the previous image")
		}
	}
}

// TestDokploy_UnrecognisedSuccessBodyFails covers the same class as the
// SwiftWave case: a 200 whose body is not the `true` Dokploy's handlers return
// means the response came from something other than the endpoint asked for.
func TestDokploy_UnrecognisedSuccessBodyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>Dokploy login</html>`))
	}))
	defer srv.Close()

	d := &DokployDeployer{logger: testLogger()}
	res, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:      ports.DeployDokploy,
		Method:      ports.DeployMethodAPI,
		Endpoint:    srv.URL,
		Token:       "api-key",
		Application: "app-1",
	})
	if err == nil {
		t.Fatal("an HTML 200 was accepted as a queued rollout")
	}
	if res.Triggered {
		t.Error("result.Triggered = true for an unrecognised response")
	}
}

// TestDokployReportsSuccess covers both body shapes Dokploy has produced for
// these mutations, and the shapes that must not be mistaken for either.
func TestDokployReportsSuccess(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{`true`, true},
		{`{"result":{"data":true}}`, true},
		{`{"result":{"data":false}}`, false},
		{`false`, false},
		{``, false},
		{`{}`, false},
		{`<html></html>`, false},
	}
	for _, tc := range tests {
		if got := dokployReportsSuccess([]byte(tc.body)); got != tc.want {
			t.Errorf("dokployReportsSuccess(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// TestNoRedirectFollowing checks that a control-plane endpoint answering with a
// redirect does not cause the credential header to be replayed against the
// redirect target.
func TestNoRedirectFollowing(t *testing.T) {
	rec := &recorder{}
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		_, _ = w.Write([]byte("true"))
	}))
	defer attacker.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, attacker.URL+"/api/application.deploy", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	d := &DokployDeployer{logger: testLogger()}
	if _, err := d.Deploy(context.Background(), ports.DeployRequest{
		Target:      ports.DeployDokploy,
		Method:      ports.DeployMethodAPI,
		Endpoint:    srv.URL,
		Token:       "api-key",
		Application: "app-1",
	}); err == nil {
		t.Error("a redirect response was treated as a successful deploy")
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("the redirect was followed and the credential was sent to the redirect target: %+v", got)
	}
}

// TestNew_RejectsUnknownTarget keeps the factory from returning a nil Deployer
// with a nil error for a target nobody implemented.
func TestNew_RejectsUnknownTarget(t *testing.T) {
	d, err := New(ports.DeployTarget("heroku"), testLogger())
	if err == nil {
		t.Fatal("New accepted an unimplemented target")
	}
	if d != nil {
		t.Error("New returned a non-nil Deployer alongside an error")
	}
	if !errors.Is(err, core.ErrInvalidDeployTarget) {
		t.Errorf("error = %v, want it to wrap ErrInvalidDeployTarget", err)
	}
}

// TestDeployersReportTheirOwnTarget guards the identity core.Deploy checks
// before dispatching.
func TestDeployersReportTheirOwnTarget(t *testing.T) {
	for _, target := range []ports.DeployTarget{ports.DeployDokploy, ports.DeploySwiftwave} {
		d, err := New(target, testLogger())
		if err != nil {
			t.Fatalf("New(%q): %v", target, err)
		}
		if d.Target() != target {
			t.Errorf("New(%q).Target() = %q", target, d.Target())
		}
	}
}
