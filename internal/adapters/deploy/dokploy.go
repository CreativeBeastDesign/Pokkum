package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// DokployDeployer drives Dokploy's HTTP API.
//
// Contract, read from Dokploy's own router
// (apps/dokploy/server/api/routers/application.ts) rather than inferred:
//
//   - Authentication is the x-api-key header on every call.
//   - POST /api/application.saveDockerProvider repoints an application at an
//     image. Its handler calls updateApplication with dockerImage, username,
//     password, sourceType:"docker" AND registryUrl taken straight from the
//     request, and its input schema (apiSaveDockerProvider) is .required() on
//     all five fields. So the call is a FULL OVERWRITE, not a patch: omitting
//     the credentials is a validation error, and sending nulls clears the
//     credentials the application pulls with. That is why UpdateImage defaults
//     off and why the pull credentials are explicit fields on DeployRequest.
//   - POST /api/application.deploy queues the rollout, taking applicationId
//     plus optional title/description.
type DokployDeployer struct {
	logger *slog.Logger
}

var _ ports.Deployer = (*DokployDeployer)(nil)

// Target reports the platform this adapter drives.
func (d *DokployDeployer) Target() ports.DeployTarget { return ports.DeployDokploy }

// dokployAPIKeyHeader is the header Dokploy's generated OpenAPI document
// requires on every endpoint.
const dokployAPIKeyHeader = "x-api-key"

// Dokploy endpoint paths, relative to the panel's base URL.
const (
	dokploySaveDockerProviderPath = "/api/application.saveDockerProvider"
	dokployDeployPath             = "/api/application.deploy"

	// dokployApplicationOnePath is the ONE read-only Dokploy endpoint this
	// adapter calls. Verified against Dokploy's own router source: application.one
	// is a tRPC `.query()`, not a `.mutation()`, so polling it cannot deploy,
	// restart or mutate anything. That distinction is load-bearing -- it is the
	// reason `pokkum deploy --check` still refuses to confirm an application id
	// by calling an endpoint whose contract was never verified, and the reason
	// this one is safe to call from a path that must not have side effects.
	dokployApplicationOnePath = "/api/application.one"

	// dokployDeploymentTitle is the title Pokkum stamps on every deploy it
	// triggers, and the key confirmRollout matches a deployment row on when no
	// image reference is available. Declared once so the writer and the reader
	// cannot drift apart.
	dokployDeploymentTitle = "pokkum"
)

// dokploySaveDockerProviderRequest mirrors apiSaveDockerProvider exactly.
//
// Every field is a *string rather than a string, and none carry omitempty:
// the schema requires each key to be PRESENT, while the underlying columns are
// nullable. A plain string with omitempty would drop the key and fail
// validation; a plain string without it would write "" where the platform
// expects null. Pointers are the only shape that can express both "this is the
// value" and "this is explicitly null".
type dokploySaveDockerProviderRequest struct {
	ApplicationID string  `json:"applicationId"`
	DockerImage   *string `json:"dockerImage"`
	Username      *string `json:"username"`
	Password      *string `json:"password"`
	RegistryURL   *string `json:"registryUrl"`
}

// dokployDeployRequest mirrors the application.deploy input.
type dokployDeployRequest struct {
	ApplicationID string `json:"applicationId"`
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
}

// Deploy repoints the application (when asked) and queues a rollout.
func (d *DokployDeployer) Deploy(ctx context.Context, req ports.DeployRequest) (ports.DeployResult, error) {
	if req.Method != ports.DeployMethodAPI {
		return ports.DeployResult{}, fmt.Errorf("dokploy: method %q is not supported: %w", req.Method, core.ErrInvalidDeployMethod)
	}
	if strings.TrimSpace(req.Application) == "" {
		return ports.DeployResult{}, fmt.Errorf("dokploy: no application id: %w", core.ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Token) == "" {
		return ports.DeployResult{}, fmt.Errorf("dokploy: no API key: %w", core.ErrDeployTokenMissing)
	}

	client := httpClient(req.Timeout)
	headers := map[string]string{dokployAPIKeyHeader: req.Token}

	result := ports.DeployResult{
		Target:      ports.DeployDokploy,
		Method:      req.Method,
		Application: req.Application,
	}

	var notes []string
	if req.UpdateImage {
		cleared, err := d.saveDockerProvider(ctx, client, headers, req)
		if err != nil {
			return result, err
		}
		result.ImageRef = req.ImageRef
		result.ImageUpdated = true
		if cleared {
			// Not a failure — a public image is a legitimate setup — but the
			// operator must be able to see that a destructive side effect of
			// their update_image setting happened, rather than discover it
			// the next time the app tries to pull a private image.
			notes = append(notes, "registry credentials cleared (none configured; set deploy.registry_username_env/registry_password_env to preserve them)")
			d.logger.Warn("dokploy: application.saveDockerProvider sent null registry credentials, clearing any previously stored ones",
				"application", req.Application,
				"remedy", "set deploy.registry_username_env and deploy.registry_password_env")
		}
	}

	if err := d.triggerDeploy(ctx, client, headers, req); err != nil {
		return result, err
	}
	result.Triggered = true

	detail := "rollout queued"
	if req.UpdateImage {
		detail = "image updated and rollout queued"
	}
	if len(notes) > 0 {
		detail += " (" + strings.Join(notes, "; ") + ")"
	}
	result.Detail = detail
	return result, nil
}

// saveDockerProvider points the application at req.ImageRef, and reports
// whether it did so with null registry credentials.
func (d *DokployDeployer) saveDockerProvider(ctx context.Context, client *http.Client, headers map[string]string, req ports.DeployRequest) (clearedCredentials bool, err error) {
	if strings.TrimSpace(req.ImageRef) == "" {
		return false, fmt.Errorf("dokploy: update_image requested with no image reference: %w", core.ErrInvalidRequest)
	}

	image := req.ImageRef
	payload := dokploySaveDockerProviderRequest{
		ApplicationID: req.Application,
		DockerImage:   &image,
	}
	// nilIfEmpty rather than a pointer to "": Dokploy stores these straight
	// into nullable columns, and an empty-string username is not the same
	// record state as no username.
	payload.Username = nilIfEmpty(req.RegistryUsername)
	payload.Password = nilIfEmpty(req.RegistryPassword)
	payload.RegistryURL = nilIfEmpty(req.RegistryURL)
	clearedCredentials = payload.Username == nil && payload.Password == nil

	status, body, err := postJSON(ctx, client, joinURL(req.Endpoint, dokploySaveDockerProviderPath), headers, payload)
	if err != nil {
		return clearedCredentials, fmt.Errorf("dokploy: application.saveDockerProvider: %v: %w", err, core.ErrDeployFailed)
	}
	if !isSuccess(status) {
		return clearedCredentials, fmt.Errorf("dokploy: application.saveDockerProvider rejected the image update: %s: %w",
			summarize(status, body), core.ErrDeployFailed)
	}

	// The handler returns literal `true` on success. A 200 carrying anything
	// else means the response came from something other than the endpoint
	// asked for — a login page or a proxy, most likely — so it is not
	// evidence the image was written.
	if !dokployReportsSuccess(body) {
		return clearedCredentials, fmt.Errorf("dokploy: application.saveDockerProvider returned success status with an unrecognised body, so the image update is unconfirmed: %s: %w",
			summarize(status, body), core.ErrDeployFailed)
	}

	d.logger.Debug("dokploy: image reference updated", "application", req.Application, "image", req.ImageRef)
	return clearedCredentials, nil
}

// triggerDeploy queues the rollout.
func (d *DokployDeployer) triggerDeploy(ctx context.Context, client *http.Client, headers map[string]string, req ports.DeployRequest) error {
	payload := dokployDeployRequest{
		ApplicationID: req.Application,
		Title:         dokployDeploymentTitle,
	}
	if req.ImageRef != "" {
		payload.Description = "Deployed by Pokkum: " + req.ImageRef
	}

	status, body, err := postJSON(ctx, client, joinURL(req.Endpoint, dokployDeployPath), headers, payload)
	if err != nil {
		return fmt.Errorf("dokploy: application.deploy: %v: %w", err, core.ErrDeployFailed)
	}
	if !isSuccess(status) {
		return fmt.Errorf("dokploy: application.deploy was rejected: %s: %w", summarize(status, body), core.ErrDeployFailed)
	}
	if !dokployReportsSuccess(body) {
		// The strict classifier stays the fast path and is not being softened:
		// a body this code cannot positively identify still never counts as a
		// confirmed mutation on its own. What changes is that "unrecognised"
		// stops being an assumed failure and becomes a question we go and ask.
		//
		// Dokploy answers application.deploy with HTTP 200 and an empty body in
		// practice, and the rollout it queued then completes normally. Reporting
		// that as a failed deploy is a false negative with real cost: the image
		// is already pushed, so the operator is handed a non-zero exit that says
		// nothing about the actual state, and the honest recovery is to go and
		// look at the platform by hand.
		confirmed, detail, pollErr := d.confirmRollout(ctx, client, headers, req)
		switch {
		case pollErr != nil:
			return fmt.Errorf("dokploy: application.deploy returned success status with an unrecognised body (%s) and the rollout could not be confirmed: %v: %w",
				summarize(status, body), pollErr, core.ErrDeployFailed)
		case !confirmed:
			return fmt.Errorf("dokploy: application.deploy returned success status with an unrecognised body (%s) and %s: %w",
				summarize(status, body), detail, core.ErrDeployFailed)
		}
		// Fail-closed is preserved: every path above still fails. Only a
		// positively observed rollout gets through.
		d.logger.Info("dokploy: application.deploy returned an unrecognised body; the rollout was confirmed by reading application.one",
			"application", req.Application, "detail", detail)
		return nil
	}

	d.logger.Debug("dokploy: rollout queued", "application", req.Application)
	return nil
}

// confirmRollout answers whether the rollout Pokkum just asked for actually
// started, by reading application.one.
//
// It matches OUR deployment rather than the application's overall status
// wherever it can: Pokkum stamps every deploy it triggers with title "pokkum"
// and a description naming the exact image reference, so a deployment row
// carrying this build's reference is unambiguous evidence about THIS request.
// applicationStatus alone is not -- it is a property of the application, and on
// a busy instance it can reflect somebody else's deploy, or a previous one that
// finished before ours was queued.
//
// The rollout is asynchronous, so the row may not exist the instant deploy
// returns. Polling is bounded by the caller's context; a cancelled or
// timed-out context reports "could not confirm", never "confirmed".
func (d *DokployDeployer) confirmRollout(ctx context.Context, client *http.Client, headers map[string]string, req ports.DeployRequest) (confirmed bool, detail string, err error) {
	url := joinURL(req.Endpoint, dokployApplicationOnePath) + "?applicationId=" + neturl.QueryEscape(req.Application)

	var last string
	for attempt := 0; ; attempt++ {
		status, body, getErr := getJSON(ctx, client, url, headers)
		switch {
		case getErr != nil:
			return false, "", fmt.Errorf("application.one: %v", getErr)
		case !isSuccess(status):
			return false, "", fmt.Errorf("application.one was rejected: %s", summarize(status, body))
		}

		app, parseErr := parseDokployApplication(body)
		if parseErr != nil {
			return false, "", fmt.Errorf("application.one: %v", parseErr)
		}

		if dep, ok := app.deploymentFor(req.ImageRef); ok {
			switch dep.Status {
			case "done", "running":
				return true, fmt.Sprintf("deployment %s is %q", dep.DeploymentID, dep.Status), nil
			case "error":
				// A rollout that genuinely failed. Reported as a failure, but as
				// the platform's own failure with its own reason, not as
				// "unconfirmed" -- the operator can act on this one.
				msg := dep.ErrorMessage
				if strings.TrimSpace(msg) == "" {
					msg = "no error message recorded"
				}
				return false, fmt.Sprintf("the rollout failed on the platform: deployment %s: %s", dep.DeploymentID, msg), nil
			default:
				last = fmt.Sprintf("deployment %s is %q", dep.DeploymentID, dep.Status)
			}
		} else {
			last = fmt.Sprintf("no deployment for this image reference has appeared yet (application status %q)", app.ApplicationStatus)
		}

		// Wait and re-read, until the caller's deadline says stop.
		select {
		case <-ctx.Done():
			if last == "" {
				last = "no deployment observed"
			}
			return false, "", fmt.Errorf("timed out confirming the rollout: %s", last)
		case <-time.After(dokployPollInterval(attempt)):
		}
	}
}

// dokployPollInterval backs off from a short first re-read -- a real rollout
// observed in the field completed in about four seconds -- to a steady interval,
// so a confirmation usually costs one or two extra requests rather than a fixed
// wait.
func dokployPollInterval(attempt int) time.Duration {
	switch {
	case attempt == 0:
		return 250 * time.Millisecond
	case attempt < 4:
		return 500 * time.Millisecond
	default:
		return time.Second
	}
}

// dokployApplication is the subset of application.one's response this adapter
// reads. Everything else in that payload -- env, secrets, domains, registry
// credentials -- is deliberately not decoded: the struct is the allowlist, so a
// field Pokkum has no use for cannot end up in a log line or an error message.
type dokployApplication struct {
	ApplicationStatus string              `json:"applicationStatus"`
	Deployments       []dokployDeployment `json:"deployments"`
}

type dokployDeployment struct {
	DeploymentID string `json:"deploymentId"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Status       string `json:"status"`
	ErrorMessage string `json:"errorMessage"`
	CreatedAt    string `json:"createdAt"`
}

// parseDokployApplication decodes application.one's body, accepting both shapes
// Dokploy has been observed to answer with -- the bare object, and the tRPC
// envelope {"result":{"data":...}} -- for the same reason dokployReportsSuccess
// accepts both: which one arrives depends on how the instance is fronted, and
// that is not something the operator controls or should have to know.
func parseDokployApplication(body []byte) (dokployApplication, error) {
	var direct dokployApplication
	if err := json.Unmarshal(body, &direct); err == nil && direct.ApplicationStatus != "" {
		return direct, nil
	}

	var wrapped struct {
		Result *struct {
			Data *dokployApplication `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Result != nil && wrapped.Result.Data != nil {
		return *wrapped.Result.Data, nil
	}

	// Neither shape. Never guess -- an unparseable body is "could not confirm",
	// and the caller turns that into a failure.
	return dokployApplication{}, errors.New("response was not an application object in either the bare or tRPC-wrapped shape")
}

// deploymentFor finds the deployment row for the image reference this build
// pushed.
//
// Matching is on the description Pokkum itself wrote, which embeds the exact
// reference, so it identifies THIS request rather than whatever the application
// did last. The rows are NOT returned in chronological order -- the real
// payload interleaves them -- so when no reference is available to match on
// (a redeploy with update_image off sends none), the newest Pokkum-titled row
// is selected by comparing createdAt, never by taking the first or last entry.
func (a dokployApplication) deploymentFor(imageRef string) (dokployDeployment, bool) {
	if ref := strings.TrimSpace(imageRef); ref != "" {
		for _, dep := range a.Deployments {
			if strings.Contains(dep.Description, ref) {
				return dep, true
			}
		}
		return dokployDeployment{}, false
	}

	var newest dokployDeployment
	var found bool
	for _, dep := range a.Deployments {
		if dep.Title != dokployDeploymentTitle {
			continue
		}
		if !found || dep.CreatedAt > newest.CreatedAt {
			newest, found = dep, true
		}
	}
	return newest, found
}

// dokployReportsSuccess reports whether a 2xx body is one Dokploy actually
// produces for these mutations.
//
// Both handlers `return true`, which tRPC serialises as the JSON literal
// `true`, and Dokploy's OpenAPI layer has at points wrapped the same value as
// {"result":{"data":true}}. Both shapes are accepted; anything else — an HTML
// page from a reverse proxy, an empty body — is not, because "200 with a body
// this code does not recognise" must not be read as a confirmed mutation.
func dokployReportsSuccess(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "true" {
		return true
	}

	var wrapped struct {
		Result *struct {
			Data *bool `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil &&
		wrapped.Result != nil && wrapped.Result.Data != nil {
		return *wrapped.Result.Data
	}
	return false
}

// isSuccess reports whether status is a 2xx.
func isSuccess(status int) bool { return status >= 200 && status < 300 }

// nilIfEmpty maps "" to a nil *string, so an unset credential serialises as
// JSON null rather than an empty string.
func nilIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
