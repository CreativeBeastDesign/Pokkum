package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

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
		Title:         "pokkum",
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
		return fmt.Errorf("dokploy: application.deploy returned success status with an unrecognised body, so the rollout is unconfirmed: %s: %w",
			summarize(status, body), core.ErrDeployFailed)
	}

	d.logger.Debug("dokploy: rollout queued", "application", req.Application)
	return nil
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
