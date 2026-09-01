package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// SwiftwaveDeployer drives SwiftWave, either through its per-application
// redeploy webhook or its GraphQL API.
//
// Neither path can move the application to a new image reference. SwiftWave's
// webhook calls RebuildApplication on the CURRENT deployment, and the
// rebuildApplication mutation does the same; repointing an application needs a
// full updateApplication(input: ApplicationInput!) round-trip that would have
// to resupply every field of a running service. So a SwiftWave application
// must be configured with a mutable tag that Pokkum republishes (":latest",
// ":main"), and the deploy makes it pull that tag again. core.SupportsImageUpdate
// encodes this, and rejects update_image for this target rather than accepting
// the setting and quietly not honouring it.
type SwiftwaveDeployer struct {
	logger *slog.Logger
}

var _ ports.Deployer = (*SwiftwaveDeployer)(nil)

// Target reports the platform this adapter drives.
func (s *SwiftwaveDeployer) Target() ports.DeployTarget { return ports.DeploySwiftwave }

// SwiftWave's own webhook response strings, from
// swiftwave_service/rest/webhook.go. They are the entire success signal: the
// handler answers 200 in both the "started a rollout" and the "decided not to"
// case, so the status code alone cannot tell them apart.
const (
	swiftwaveRebuildTriggered = "OK - Rebuild triggered"
	swiftwaveNoRebuild        = "OK - No rebuild"
)

// swiftwaveGraphQLPath is where the GraphQL handler is mounted.
const swiftwaveGraphQLPath = "/graphql"

// swiftwaveRebuildMutation redeploys an application by id. It is the only
// SwiftWave mutation this adapter issues.
const swiftwaveRebuildMutation = `mutation PokkumRebuild($id: String!) { rebuildApplication(id: $id) }`

// Deploy triggers a SwiftWave rollout.
func (s *SwiftwaveDeployer) Deploy(ctx context.Context, req ports.DeployRequest) (ports.DeployResult, error) {
	if req.UpdateImage {
		return ports.DeployResult{}, fmt.Errorf(
			"swiftwave: cannot repoint an application at a specific image; configure the application with a mutable tag instead: %w",
			core.ErrInvalidRequest)
	}

	switch req.Method {
	case ports.DeployMethodWebhook:
		return s.deployViaWebhook(ctx, req)
	case ports.DeployMethodAPI:
		return s.deployViaGraphQL(ctx, req)
	default:
		return ports.DeployResult{}, fmt.Errorf("swiftwave: method %q is not supported: %w", req.Method, core.ErrInvalidDeployMethod)
	}
}

// deployViaWebhook posts to /webhook/redeploy-app/:app-id/:webhook-token.
//
// The body is not decoration. SwiftWave's handler, for an image-sourced
// deployment, reduces its configured image to "owner/name" (tag stripped, then
// the last two path segments) and rebuilds ONLY if the request body contains
// that substring — otherwise it answers 200 "OK - No rebuild". So the body
// must carry the image references, and the response must be classified on its
// text rather than its status.
func (s *SwiftwaveDeployer) deployViaWebhook(ctx context.Context, req ports.DeployRequest) (ports.DeployResult, error) {
	appID := swiftwaveAppIDFromWebhook(req.Endpoint)
	result := ports.DeployResult{
		Target:      ports.DeploySwiftwave,
		Method:      ports.DeployMethodWebhook,
		Application: appID,
	}

	body := swiftwaveWebhookBody(req)
	if body == "" {
		return result, fmt.Errorf(
			"swiftwave: no image reference to post; the webhook only rebuilds when the request body names the application's configured image: %w",
			core.ErrInvalidRequest)
	}

	// text/plain, not JSON: the handler runs url.QueryUnescape over the whole
	// body and, on failure, proceeds with the empty string — which silently
	// becomes "OK - No rebuild". A bare list of image references contains no
	// percent escapes to trip that.
	status, respBody, err := post(ctx, httpClient(req.Timeout),
		req.Endpoint, nil, "text/plain; charset=utf-8", strings.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("swiftwave: redeploy webhook: %v: %w", err, core.ErrDeployFailed)
	}

	text := strings.TrimSpace(string(respBody))
	if !isSuccess(status) {
		// The handler's own failure strings are short and specific
		// ("Unauthorized", "App not found", "No deployment found"), so
		// echoing the body is genuinely useful here. The URL, which carries
		// the webhook token, is never echoed.
		return result, fmt.Errorf("swiftwave: redeploy webhook was rejected: %s: %w", summarize(status, respBody), core.ErrDeployFailed)
	}

	switch {
	case strings.Contains(text, swiftwaveRebuildTriggered):
		result.Triggered = true
		result.Detail = "rebuild triggered"
		s.logger.Debug("swiftwave: rebuild triggered", "application", appID)
		return result, nil

	case strings.Contains(text, swiftwaveNoRebuild):
		// The single most important branch in this adapter. HTTP 200, and
		// nothing happened.
		result.Detail = swiftwaveNoRebuild
		return result, fmt.Errorf(
			"swiftwave: the webhook accepted the request and declined to rebuild (%q). SwiftWave only rebuilds when the posted body contains the application's own configured image, reduced to \"owner/name\"; the body named %s. Check that the application's image matches what Pokkum pushed, and that its source type is Image: %w",
			swiftwaveNoRebuild, swiftwaveBodyRefsForMessage(req), core.ErrDeployNotTriggered)

	default:
		// A 200 this code cannot classify is not evidence of a rollout.
		result.Detail = summarize(status, respBody)
		return result, fmt.Errorf(
			"swiftwave: redeploy webhook returned an unrecognised success response, so the rollout is unconfirmed: %s: %w",
			summarize(status, respBody), core.ErrDeployFailed)
	}
}

// deployViaGraphQL issues the rebuildApplication mutation.
func (s *SwiftwaveDeployer) deployViaGraphQL(ctx context.Context, req ports.DeployRequest) (ports.DeployResult, error) {
	result := ports.DeployResult{
		Target:      ports.DeploySwiftwave,
		Method:      ports.DeployMethodAPI,
		Application: req.Application,
	}
	if strings.TrimSpace(req.Application) == "" {
		return result, fmt.Errorf("swiftwave: no application id: %w", core.ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Token) == "" {
		return result, fmt.Errorf("swiftwave: no JWT: %w", core.ErrDeployTokenMissing)
	}

	payload := map[string]any{
		"query":     swiftwaveRebuildMutation,
		"variables": map[string]string{"id": req.Application},
	}
	headers := map[string]string{"Authorization": "Bearer " + req.Token}

	status, body, err := postJSON(ctx, httpClient(req.Timeout), joinURL(req.Endpoint, swiftwaveGraphQLPath), headers, payload)
	if err != nil {
		return result, fmt.Errorf("swiftwave: rebuildApplication: %v: %w", err, core.ErrDeployFailed)
	}
	if !isSuccess(status) {
		return result, fmt.Errorf("swiftwave: rebuildApplication was rejected: %s: %w", summarize(status, body), core.ErrDeployFailed)
	}

	// GraphQL answers 200 for application-level errors too, with the failure
	// only in the body — the same "success status, unsuccessful outcome"
	// shape as the webhook, so it gets the same treatment.
	var decoded struct {
		Data *struct {
			RebuildApplication *bool `json:"rebuildApplication"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return result, fmt.Errorf("swiftwave: rebuildApplication returned a body that is not GraphQL JSON, so the rollout is unconfirmed: %s: %w",
			summarize(status, body), core.ErrDeployFailed)
	}
	if len(decoded.Errors) > 0 {
		messages := make([]string, 0, len(decoded.Errors))
		for _, e := range decoded.Errors {
			messages = append(messages, e.Message)
		}
		return result, fmt.Errorf("swiftwave: rebuildApplication: %s: %w", strings.Join(messages, "; "), core.ErrDeployFailed)
	}
	if decoded.Data == nil || decoded.Data.RebuildApplication == nil {
		return result, fmt.Errorf("swiftwave: rebuildApplication returned no result field, so the rollout is unconfirmed: %s: %w",
			summarize(status, body), core.ErrDeployFailed)
	}
	if !*decoded.Data.RebuildApplication {
		result.Detail = "rebuildApplication returned false"
		return result, fmt.Errorf("swiftwave: rebuildApplication returned false for application %q: %w", req.Application, core.ErrDeployNotTriggered)
	}

	result.Triggered = true
	result.Detail = "rebuild triggered"
	s.logger.Debug("swiftwave: rebuild triggered", "application", req.Application)
	return result, nil
}

// swiftwaveWebhookBody builds the request body the webhook matches against.
//
// Both the digest-pinned and the tagged reference are included: the handler
// only needs the "owner/name" substring, and either form carries it, but which
// one Pokkum has depends on the build's output. Newline-separated plain text,
// with no percent-escapes (see deployViaWebhook).
func swiftwaveWebhookBody(req ports.DeployRequest) string {
	seen := make(map[string]bool, 2)
	var refs []string
	for _, ref := range []string{req.ImageRef, req.TaggedRef} {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return strings.Join(refs, "\n")
}

// swiftwaveBodyRefsForMessage renders the posted references for an error
// message, so a "No rebuild" failure says what was actually sent.
func swiftwaveBodyRefsForMessage(req ports.DeployRequest) string {
	body := swiftwaveWebhookBody(req)
	if body == "" {
		return "nothing"
	}
	return strings.Join(strings.Split(body, "\n"), ", ")
}

// swiftwaveAppIDFromWebhook extracts the application id from a webhook URL of
// the form /webhook/redeploy-app/:app-id/:webhook-token.
//
// It exists so results and log lines can name the application without ever
// carrying the URL, whose last segment is the token. An unrecognised shape
// yields "", which callers render as "unknown" rather than guessing — the id
// is cosmetic here, and a wrong one in a log is worse than none.
func swiftwaveAppIDFromWebhook(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// .../redeploy-app/<app-id>/<token>
	for i, part := range parts {
		if part == "redeploy-app" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
