# `internal/adapters/deploy`

Implements `ports.Deployer` for two self-hosted PaaS control planes: **Dokploy** and **SwiftWave**. It runs *after* the reproducible part of a build has finished, handing an image that is already in a registry to a platform that will pull it.

Both platforms live in one package deliberately. They share the entire HTTP contract in `deploy.go` — client construction, the credential-redacting error path, the response-body cap — and Pokkum forbids adapter-to-adapter imports (`internal/architecture_test.go` enforces an empty allowlist), so splitting them would mean maintaining two copies of it.

## The thing this package is actually for

Sending an HTTP request is trivial. What earns a dedicated adapter is that **both platforms answer HTTP 200 for outcomes that are not deployments**, so a status-code check reports a deploy that changed nothing as a success.

| Platform | Response that looks like success but isn't | How it's handled |
|---|---|---|
| SwiftWave webhook | `200` + body `OK - No rebuild` | Classified on the body text; returns `core.ErrDeployNotTriggered` |
| SwiftWave GraphQL | `200` + `{"errors":[…]}`, or `rebuildApplication: false` | Body decoded; both are failures |
| Dokploy | `200` + an HTML login page from a reverse proxy | Body must be Dokploy's own `true`; anything else is unconfirmed |

`core.Deploy` adds a backstop: a result with `Triggered == false` can never be returned alongside a `nil` error.

## Verified contracts

Both platform contracts were read from the projects' **own source**, not from their prose documentation (self-review checklist row 36).

### SwiftWave — `swiftwave_service/rest/webhook.go`

`ANY /webhook/redeploy-app/:app-id/:webhook-token`. For an image-sourced deployment, the handler reduces its configured image to `owner/name` (tag stripped, then the last two path segments) and rebuilds **only if the request body contains that substring**. Otherwise: `200 OK - No rebuild`.

Consequences this adapter is built around:

- The request body must carry the image references. It posts them newline-separated as `text/plain`.
- No percent escapes in the body: the handler runs `url.QueryUnescape` over it and, on failure, continues with the **empty string** — which silently becomes "No rebuild".
- The webhook token is the last path segment of the URL, so no error path may ever print the URL. `redactURLError` unwraps `*url.Error` for exactly this reason.

GraphQL is at `POST /graphql` with `Authorization: Bearer <jwt>`; this adapter issues only `rebuildApplication(id:)`.

**Neither path can repoint an application at a new image.** Changing the image requires a full `updateApplication(input: ApplicationInput!)` round-trip that would have to resupply every field of a running service. So a SwiftWave application must be pinned to a mutable tag Pokkum republishes, and `core.SupportsImageUpdate` rejects `update_image` for this target rather than accepting it and not honouring it.

### Dokploy — `apps/dokploy/server/api/routers/application.ts`

Auth is the `x-api-key` header on every call.

- `POST /api/application.saveDockerProvider` — repoints the application. **This is a full overwrite, not a patch.** The handler writes `dockerImage`, `username`, `password`, `sourceType:"docker"` and `registryUrl` from the request on every call, and `apiSaveDockerProvider` is `.required()` on all five picked fields. So omitting the credentials is a validation error, and sending nulls *clears* the credentials the application pulls with.
- `POST /api/application.deploy` — queues the rollout.

That overwrite semantics is why `deploy.update_image` defaults **off**, why `dokploySaveDockerProviderRequest` uses `*string` without `omitempty` (the keys must be present, the values may be null), and why clearing credentials is reported in `DeployResult.Detail` and warned about on the log rather than happening silently.

## Credential handling rules

- A token reaches the adapter only through `ports.DeployRequest.Token`, resolved from the environment by `core.ResolveDeployRequest`. Nothing in this package reads the environment or a config file.
- No credential is ever placed in a URL query string, an error message, or a log line.
- Redirects are refused (`http.ErrUseLastResponse`): following a 30x would replay the credential header against whatever host the redirect names.
- Response bodies are capped at 1 MiB and drained on every return path.

## Testing

`deploy_test.go` drives both adapters against `httptest` servers and asserts on the **bytes actually sent** rather than on the adapters' own bookkeeping. The two guards that matter most were each proven capable of failing by temporarily reverting the behaviour they protect:

- replacing the SwiftWave body classification with a naive `isSuccess(status)` makes `TestSwiftwaveWebhook_NoRebuildIsAFailure` fail;
- adding `omitempty` to the Dokploy nullable fields makes `TestDokploy_UpdateImageSendsEveryRequiredField` fail, naming the omitted keys.
