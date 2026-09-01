// Package deploy hands a published image to a PaaS control plane, implementing
// ports.Deployer for Dokploy and SwiftWave.
//
// Both platforms live in one package on purpose. They share the whole HTTP
// contract below — the client construction, the credential-redacting error
// path, the body-size cap — and Pokkum forbids adapter-to-adapter imports
// (internal/architecture_test.go enforces an empty allowlist), so two packages
// would mean two copies of it. One package, one copy, no cross-adapter edge.
//
// # Why this adapter reads response bodies
//
// The defining hazard of a deploy integration is not the request, it is the
// class of response that says "fine" and means "nothing happened". SwiftWave's
// redeploy webhook answers HTTP 200 with the body "OK - No rebuild" whenever
// it decides the request does not concern the application's configured image
// (verified against swiftwave_service/rest/webhook.go). Every function here
// therefore classifies on the parsed body, and treats "2xx" alone as
// insufficient evidence that a rollout started.
package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// maxResponseBody caps how much of a platform response is read into memory.
//
// The bodies this adapter cares about are a short string or a small JSON
// object. The cap exists so a misconfigured endpoint (a panel that answers
// with an HTML login page, or a proxy streaming an error page) cannot make a
// deploy allocate without bound.
const maxResponseBody = 1 << 20 // 1 MiB

// New returns the ports.Deployer for target.
//
// It is the single construction point for both platforms, so cmd/pokkum never
// names a concrete adapter type — the composition root wires a target string
// through here and receives an interface.
func New(target ports.DeployTarget, logger *slog.Logger) (ports.Deployer, error) {
	if logger == nil {
		logger = slog.Default()
	}
	switch target {
	case ports.DeployDokploy:
		return &DokployDeployer{logger: logger}, nil
	case ports.DeploySwiftwave:
		return &SwiftwaveDeployer{logger: logger}, nil
	default:
		return nil, fmt.Errorf("deploy adapter: no implementation for target %q: %w", target, core.ErrInvalidDeployTarget)
	}
}

// httpClient builds the client for one deploy exchange.
//
// The timeout is per-attempt and comes from the request rather than a package
// default, and redirects are refused: a 30x from a control-plane endpoint would
// otherwise replay the Authorization/x-api-key header against whatever host the
// redirect names, which is a credential-forwarding bug, not a convenience.
func httpClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = ports.DefaultDeployTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// postJSON sends body as JSON to url with the supplied headers and returns the
// status code and response body.
//
// The response body is always drained and closed, on every return path
// including the error ones, so a failed deploy does not leak a connection back
// into the pool half-read.
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body any) (int, []byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("encode request: %w", err)
	}
	return post(ctx, client, url, headers, "application/json", bytes.NewReader(encoded))
}

// post performs the request and reads the (capped) response body.
func post(ctx context.Context, client *http.Client, url string, headers map[string]string, contentType string, body io.Reader) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		// url may be a webhook URL carrying its own secret, so it is never
		// placed in an error message.
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		// http.Client errors embed the request URL, which for a webhook is
		// secret-bearing. Report the transport failure without it.
		return 0, nil, fmt.Errorf("request failed: %w", redactURLError(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
		_ = resp.Body.Close()
	}()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", redactURLError(err))
	}
	return resp.StatusCode, data, nil
}

// redactURLError strips the URL out of a *url.Error so that a webhook secret
// in the request URL cannot reach a log line or a user-facing error.
//
// net/http wraps every transport failure in a *url.Error whose Error() string
// prints the full request URL — and the URL path is exactly where SwiftWave
// puts the webhook token. Unwrapping to the underlying cause keeps the useful
// part (connection refused, TLS failure, context deadline) and drops the
// secret-bearing URL.
func redactURLError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Err != nil {
			return fmt.Errorf("%s %s", urlErr.Op, urlErr.Err.Error())
		}
		return errors.New(urlErr.Op + " failed")
	}
	return err
}

// summarize renders a response body for a human, bounded and single-line.
//
// Platform errors are frequently HTML or a long JSON blob; a deploy failure
// should name what came back without pasting a page into the terminal.
func summarize(status int, body []byte) string {
	text := strings.TrimSpace(string(body))
	text = strings.Join(strings.Fields(text), " ")
	// Truncate on a rune boundary: a platform error can be non-ASCII, and
	// slicing bytes would emit a broken rune into the message.
	const limit = 300
	if len(text) > limit {
		runes := []rune(text)
		if len(runes) > limit {
			runes = runes[:limit]
		}
		text = string(runes) + "…"
	}
	if text == "" {
		return fmt.Sprintf("HTTP %d (empty response)", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, text)
}

// joinURL joins a base URL and a path, tolerating a trailing slash on the base
// and a leading slash on the path.
func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}
