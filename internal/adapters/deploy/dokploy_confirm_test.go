package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// These cover the false negative reported from a real deployment: Dokploy
// answered application.deploy with HTTP 200 and an empty body, Pokkum reported
// the deploy as failed, and the rollout had in fact completed in about four
// seconds. The image was already pushed, so the operator was handed a non-zero
// exit that said nothing about the actual state.
//
// The fixture below is the real payload's shape, including the detail that
// makes a naive implementation wrong: `deployments` is NOT in chronological
// order.

const testImageRef = "ghcr.io/example/app@sha256:46e1429e0bd072eb10cd41664c03444653729719045b2da8e0a895a5bd028a16"

// dokployAppBody renders an application.one response whose deployment rows are
// deliberately out of chronological order, matching the live payload.
func dokployAppBody(t *testing.T, appStatus string, ours *dokployDeployment) []byte {
	t.Helper()
	deps := []dokployDeployment{
		{DeploymentID: "older-1", Title: "pokkum", Description: "Deployed by Pokkum: ghcr.io/example/app@sha256:oldoldold", Status: "done", CreatedAt: "2026-09-09T11:37:27.165Z"},
		{DeploymentID: "newer-2", Title: "Redeploy by hand", Description: "", Status: "done", CreatedAt: "2026-09-09T12:11:29.663Z"},
		{DeploymentID: "middle-3", Title: "pokkum", Description: "Deployed by Pokkum: ghcr.io/example/app@sha256:middlemid", Status: "done", CreatedAt: "2026-09-09T11:41:44.176Z"},
	}
	if ours != nil {
		deps = append(deps, *ours)
	}
	body, err := json.Marshal(dokployApplication{ApplicationStatus: appStatus, Deployments: deps})
	if err != nil {
		t.Fatalf("[TEST SETUP] marshalling fixture: %v", err)
	}
	return body
}

// dokployServer answers saveDockerProvider and deploy with an EMPTY 200 (the
// reported behaviour) and application.one with whatever the test supplies.
func dokployServer(t *testing.T, appBody func() []byte, onePath *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "application.one") {
			if onePath != nil {
				*onePath++
			}
			if r.Method != http.MethodGet {
				t.Errorf("application.one was called with %s, want GET — it is a tRPC query", r.Method)
			}
			if got := r.URL.Query().Get("applicationId"); got != "app-1" {
				t.Errorf("application.one applicationId = %q, want %q", got, "app-1")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(appBody())
			return
		}
		// The reported shape: 200 with an empty body.
		w.WriteHeader(http.StatusOK)
	}))
}

func testDeployRequest(endpoint string) ports.DeployRequest {
	return ports.DeployRequest{
		Target:      ports.DeployDokploy,
		Method:      ports.DeployMethodAPI,
		Endpoint:    endpoint,
		Application: "app-1",
		Token:       "t",
		ImageRef:    testImageRef,
		Timeout:     10 * time.Second,
	}
}

func newTestDokploy() *DokployDeployer {
	return &DokployDeployer{logger: testLogger()}
}

// TestDokploy_EmptyBodyWithCompletedRolloutIsNotAFailure is the reported case.
func TestDokploy_EmptyBodyWithCompletedRolloutIsNotAFailure(t *testing.T) {
	ours := &dokployDeployment{
		DeploymentID: "8j6tz6W2jigXgDAZwXF9H",
		Title:        "pokkum",
		Description:  "Deployed by Pokkum: " + testImageRef,
		Status:       "done",
		CreatedAt:    "2026-09-09T19:15:39.677Z",
	}
	srv := dokployServer(t, func() []byte { return dokployAppBody(t, "done", ours) }, nil)
	defer srv.Close()

	_, err := newTestDokploy().Deploy(context.Background(), testDeployRequest(srv.URL))
	if err != nil {
		t.Errorf("deploy reported failure for a rollout that completed: %v\n"+
			"\tDokploy answered application.deploy with an empty 200 and the rollout finished\n"+
			"\tnormally; reporting that as failed is a false negative on an image already pushed.", err)
	}
}

// TestDokploy_EmptyBodyWithRunningRolloutIsNotAFailure covers the timing the
// live case actually has: the row exists but has not finished yet.
func TestDokploy_EmptyBodyWithRunningRolloutIsNotAFailure(t *testing.T) {
	ours := &dokployDeployment{
		DeploymentID: "d-running", Title: "pokkum",
		Description: "Deployed by Pokkum: " + testImageRef,
		Status:      "running", CreatedAt: "2026-09-09T19:15:39.677Z",
	}
	srv := dokployServer(t, func() []byte { return dokployAppBody(t, "running", ours) }, nil)
	defer srv.Close()

	if _, err := newTestDokploy().Deploy(context.Background(), testDeployRequest(srv.URL)); err != nil {
		t.Errorf("a started-but-unfinished rollout was reported as failed: %v", err)
	}
}

// TestDokploy_EmptyBodyWithFailedRolloutReportsThePlatformsReason asserts the
// fix did not turn a real failure into a pass, and that the operator gets the
// platform's own reason rather than "unconfirmed".
func TestDokploy_EmptyBodyWithFailedRolloutReportsThePlatformsReason(t *testing.T) {
	ours := &dokployDeployment{
		DeploymentID: "d-bad", Title: "pokkum",
		Description: "Deployed by Pokkum: " + testImageRef,
		Status:      "error", ErrorMessage: "pull access denied for ghcr.io/example/app",
		CreatedAt: "2026-09-09T19:15:39.677Z",
	}
	srv := dokployServer(t, func() []byte { return dokployAppBody(t, "error", ours) }, nil)
	defer srv.Close()

	_, err := newTestDokploy().Deploy(context.Background(), testDeployRequest(srv.URL))
	if err == nil {
		t.Fatal("a rollout the platform recorded as failed was reported as a success")
	}
	if !strings.Contains(err.Error(), "pull access denied") {
		t.Errorf("error does not carry the platform's own reason, so the operator cannot act on it: %v", err)
	}
}

// TestDokploy_EmptyBodyWithNoMatchingRolloutStillFails is the fail-closed half.
// An application whose deployments never include this image reference must not
// be read as a success just because the application itself looks healthy.
func TestDokploy_EmptyBodyWithNoMatchingRolloutStillFails(t *testing.T) {
	srv := dokployServer(t, func() []byte { return dokployAppBody(t, "done", nil) }, nil)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req := testDeployRequest(srv.URL)
	req.Timeout = 2 * time.Second
	_, err := newTestDokploy().Deploy(ctx, req)
	if err == nil {
		t.Error("an unrecognised body with no matching rollout was reported as a success; " +
			"fail-closed must be preserved — only a positively observed rollout may pass")
	}
}

// TestDokploy_UnparseableApplicationBodyStillFails: "could not check" must
// never become "checked and fine".
func TestDokploy_UnparseableApplicationBodyStillFails(t *testing.T) {
	srv := dokployServer(t, func() []byte { return []byte("<html>proxy error</html>") }, nil)
	defer srv.Close()

	if _, err := newTestDokploy().Deploy(context.Background(), testDeployRequest(srv.URL)); err == nil {
		t.Error("an application.one body that could not be parsed was reported as a confirmed rollout")
	}
}

// TestDokploy_RecognisedBodyDoesNotPoll keeps the strict classifier as the fast
// path: a body Dokploy positively confirms must not cost an extra request.
func TestDokploy_RecognisedBodyDoesNotPoll(t *testing.T) {
	var oneCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "application.one") {
			oneCalls++
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "true")
	}))
	defer srv.Close()

	if _, err := newTestDokploy().Deploy(context.Background(), testDeployRequest(srv.URL)); err != nil {
		t.Fatalf("a positively confirmed deploy failed: %v", err)
	}
	if oneCalls != 0 {
		t.Errorf("application.one was polled %d time(s) for a body the classifier already confirmed; "+
			"the strict path must stay the fast path", oneCalls)
	}
}

// TestDokployDeploymentFor_IgnoresRowOrder pins the detail that makes a naive
// implementation wrong: the live payload's deployments are not chronological,
// so "the first row" and "the last row" are both meaningless.
func TestDokployDeploymentFor_IgnoresRowOrder(t *testing.T) {
	ours := dokployDeployment{
		DeploymentID: "ours", Title: "pokkum",
		Description: "Deployed by Pokkum: " + testImageRef,
		Status:      "done", CreatedAt: "2026-09-09T19:15:39.677Z",
	}
	var app dokployApplication
	if err := json.Unmarshal(dokployAppBody(t, "done", &ours), &app); err != nil {
		t.Fatalf("[TEST SETUP] %v", err)
	}
	if len(app.Deployments) < 4 {
		t.Fatalf("[TEST SETUP] fixture has %d rows; it must carry decoys or this proves nothing", len(app.Deployments))
	}

	got, ok := app.deploymentFor(testImageRef)
	if !ok || got.DeploymentID != "ours" {
		t.Errorf("matched %+v (found=%v), want the row whose description carries this image reference", got, ok)
	}

	// With no reference to match on, the newest pokkum-titled row wins — not the
	// first, and not the last.
	got, ok = app.deploymentFor("")
	if !ok || got.DeploymentID != "ours" {
		t.Errorf("with no image ref, matched %+v (found=%v), want the newest pokkum-titled row by createdAt", got, ok)
	}
}
