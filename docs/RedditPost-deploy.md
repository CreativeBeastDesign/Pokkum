**Disclaimer, same as last time**: still mostly vibe-coded, still human-written post (un-edited by anything artificial, and only lightly by me). Formatting by yours truly. Bear with it, or don't.

# I read two PaaS codebases so you don't have to, and found two HTTP 200s that mean "no"

Short version: I taught [Pokkum](https://github.com/creativebeastdesign/pokkum) (my "Ko for SvelteKit" image builder, [previous post here]) to deploy straight to **Dokploy** and **SwiftWave** after it pushes. While doing it I went and read both projects' actual source instead of their docs, and found two things that I think are worth knowing whether or not you ever touch my tool. So this post is 30% "I added a feature" and 70% "here is a footgun in software you might already be running".

# Gotcha 1: SwiftWave's redeploy webhook says 200 OK when it has decided to do nothing

You know the pattern. You've got a container image, you push a new tag, you POST to the app's redeploy webhook, you get a `200`, everyone goes home.

Except SwiftWave's webhook handler, for an image-sourced app, takes the image it's configured with, strips the tag, keeps the last two path segments (so `ghcr.io/me/myapp:latest` becomes `me/myapp`), and then does a **substring check against your request body**. If your POST body doesn't contain that string, it replies:

```
200 OK - No rebuild
```

...and does nothing at all. Which, if you're checking the status code — and why wouldn't you be, it's a webhook — looks exactly like a successful deploy. Forever. Silently. Your "deployments" work great and your app never changes.

There's a second layer to it, too: the handler runs the body through `url.QueryUnescape` first, and **on failure it carries on with the empty string**. So a stray `%` in your body also quietly turns into "no rebuild". A 200. Again.

To be clear, I don't think this is a bug exactly — it's a webhook designed for git-provider payloads, where "does this payload concern me?" is a sensible question to ask. It's just that nothing tells you, and the failure mode is the worst kind: the one that looks like success.

(Pokkum now posts the image refs as the body, as plain text with no escapes, reads the reply text rather than the status, and treats `OK - No rebuild` as a hard failure with an error explaining the `owner/name` matching rule. Which is a lot of words for "it tells you when nothing happened".)

# Gotcha 2: Dokploy's "set the image" endpoint also rewrites your registry password

Dokploy has `application.saveDockerProvider`. Sounds like it sets the docker provider. It does! It sets **all** of it: `dockerImage`, `username`, `password`, and `registryUrl`, every single call, straight from your request — and the input schema marks all five fields as required.

So:

- send just `{applicationId, dockerImage}` → validation error, fair enough
- send `{applicationId, dockerImage, username: null, password: null, registryUrl: null}` → **your app's registry credentials are now gone**

There is no "just change the image" call. If your registry is private, the next pull fails, and the thing that broke it was an endpoint whose name says nothing about credentials.

Again — not really a *bug*, it's a full-resource update and it's honest about being one if you read the handler. But "saveDockerProvider" reads like a targeted setter, and the docs don't mention it. I only found it because I went looking for whether it was a PATCH or a PUT.

(So in Pokkum that whole feature is **off by default**, and when you turn it on you tell it where the registry credentials live, as env var names. If you don't, it still works — fine for a public image — but it warns you loudly that it just cleared them, rather than letting you discover it at 2am.)

# The actual feature, briefly

```yaml
# .pokkum.yaml
deploy:
  target: dokploy
  endpoint: https://panel.example.com
  application: <app id>
  token_env: DOKPLOY_API_KEY   # the NAME of the env var. Not the token.
```

`pokkum build` now deploys after it pushes. `pokkum deploy` runs it standalone. `--no-deploy` for when you don't want it. Per-profile blocks, so `-P staging` and `-P production` go to different panels.

No credential goes in the config file — only the *name* of an environment variable. Felt a bit daft to ship a tool with a secret scanner and then ask people to paste an API key into a committed YAML file.

One honest limitation: **SwiftWave can't be pointed at a new image**, by either of its routes. Both of them mean "redeploy what you've already got". So pin your SwiftWave app to a moving tag (`:latest`, `:main`) and the deploy re-pulls it. Pokkum refuses the "update the image" setting for SwiftWave outright rather than accepting it and quietly not doing it, which I think is the right call even though it's the more annoying one.

And anything else that pulls from a registry — Coolify, CapRover, Dokku, Fly, Cloud Run, whatever — already worked and still does. It's just an OCI image. The two above only got special treatment because they're the two I actually use.

# Also: a benchmark you can run yourself and disagree with

I got tired of saying "smaller and reproducible" without a number, so there's now a `benchmarks/three-way` directory. One SvelteKit app, three builds: the Dockerfile most people write first, a properly tuned multi-stage one, and Pokkum. Same source, same machine, same measuring tape. Spits out a markdown table.

Deliberately: **it uses trivy or grype, not my own scanner**, because a comparison I win using my own scanner is worth exactly nothing. And when neither is installed the CVE column says `n/a` rather than `0`, because "didn't measure" and "measured, found nothing" are not the same thing and I've been annoyed by tools that conflate them.

It also documents where it's unfair to itself, which felt more useful than pretending it isn't.

# Caveats (the recurring section)

**Still vibe-coded, still uncertain about long-term maintenance.** Nothing's changed there. It works, I use it, I can't promise a decade.

**These two platform quirks might change.** I read the source at a point in time. Both are moving projects. Pokkum fails closed on anything it can't positively identify as a started rollout, so if they change the reply strings you'll get a loud error rather than a silent no-op — which is the failure mode I'd want, but it *is* a failure.

**Only two targets.** Dokploy and SwiftWave, because those are mine. If you want another one it's a fairly small adapter now that the port exists — or just keep using the webhook you already have, honestly.

# Where

- [GitHub](https://github.com/creativebeastdesign/pokkum)
- `curl -fsSL https://raw.githubusercontent.com/CreativeBeastDesign/pokkum/main/install.sh | sh`
- `npm install -g @pokkum/cli`

If you're running Dokploy or SwiftWave and want to check the above yourself, it's `apps/dokploy/server/api/routers/application.ts` and `swiftwave_service/rest/webhook.go` respectively. Took about twenty minutes each and I'd genuinely recommend it — I'm now slightly suspicious of every webhook I've ever fired.
