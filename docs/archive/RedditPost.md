**Disclaimer**: This tool is mostly vibe-coded, probably Theseus'-style totally vibe coded. Reasons, if interested, below. The post is human-written and un-edited by any kind of intelligence, neither artificial nor mine, you might have to bear with it. Or not. And yeah, also the formatting is done by yours truly, I'm putting in some effort here.

# What it is - Ko for SvelteKit

If you know [Ko](https://github.com/ko-build/ko), you know how awesome it is. And due to running into a unfixed CVE in `gcr.io/distroless/nodejs24-debian12:nonroot` and not wanting to have a trivy-ignore, which I'd have to carry over versions, check from time to time, and most certainly forget, I was longing for 'Ko for SvelteKit'. And why **Pokkum**? Well, I like **po**ss**ums**, and it is **K**o. I'm really bad at naming. And, if anyone thinks that's silly: It is. Keep your whimsy!

So, Pokkum is an image builder for SvelteKit. To be more specific, it's a "zero-dependency OCI container image compiler for SvelteKit applications". You don't need a dockerfile, no docker daemon, and, if you need it security-wise, reproducible builds.

Oh, and for some parts and the first version, pokkum heavily relies and relied on [Hugo-Dz's EXE](https://github.com/Hugo-Dz/exe) \- kudos to him!

# What it does - builds images and pushes to registry

That's the easy and neat part: Basically, it builds the image and pushes it to your container registry. Built upon `distroless`, containing everything you need, adding SBOM, pushing it to where you point it to.

There are also a plethora of options (ok, around 105-ish or so), to really suit your needs. Plus some Wizard and tools to not make it overwhelming.

So, if you need a hardened image, you can run pokkum with flags like `--security-context`, `--network-policy`, `--resource-defaults`, `-f deployment.yaml`, and `--registry-config=~/.docker/config.json`. Then pokkum uses the pinned immutable image digest, ingests hardened security contexts like `runAsNonRoot: true`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, and generates restricted `NetworkPolicy` ingress/egress rules and injects CPU/memory `requests`/`limits` with a `PodDisruptionBudget`.

And loads of middle ground in between. Did I mention the 116-ish flags? Yeah, that was probably an overkill. Let me know, if you think that should be done differently.

# Selected Features

- **`pokkum verify` instead of 'trust me, bro'** \- compares the image against what's in the registry on three levels, _L1_ for the manifest digest, _L2_ for semantic diffIDs, and _L3_ for file-level layer diffs. If you buggered up _somewhere_, `pokkum verify` will let you know: _There_ it is.
- **Bit-for-bit reproducible images** \- verifiable and deterministic images, in combination with `pokkum verify` (L1/L2/L3 comparison diagnostics), hopefully water-proof for auditability.
- **Supply-chain security** \- SLSA v1.0 provenance, Cosign/DSSE signing, SBOMs via the OCI Referrers API, and base-image signature verification (even keyless Sigstore).
- **Embedded supervisor** \- a light-weight supervisor which not only provides `/healthz` and `/readyz`, but also allows zombie reaping (which, again, sounds more awesome than it is), signal forwarding, graceful shutdown, ...
- **Kubernetes shenanigans** \- I loathe Kubernetes, but am forced to work with it. So, pokkum should alleviate my burden as much as possible, by having `pokkum resolve`, `apply`, `rollback`: Declarative URI resolution, resolve & deploy in a single step, injects `securityContext`, and generates `NetworkPolicy` and `PodDisruptionBudget`.
- **Hermetic builds** (if you're into that) - no network egress during build, should you need air-gapped environments.
- **Node.js Runtime Target** \- Not only Bun is supported, but also Node.js (via `--runtime=node`). So, might be even easier to try Pokkum. Yay for adoption!
- **Static images** \- SPA but don't want 90MB runtime? Pokkum's got you covered! `--strategy=static` adds a tiny Go file server, so no Node, no Bun, and nothing to CVE-scan in the runtime layer.

# Why vibe-coded

Vibe-coding seems to become a skill that recruiters are looking for. I have only dabbled in vibe-coding before, did lots of 'AI-assisted coding', yadayada.

Basically, I needed to know how to "best" vibe-code. Or at least how to vibe-code so I can somewhat feel at ease with shipping it. And that's why I "built" this.

Also open for suggestions, if you have any. I tried Claude Desktop, Antigravity, Deepseek Harness, Zed with various models, Serena MCP, some plugins for DSH, ... - and basically, what I found is that tests are crucial, Antigravity/Gemini 3.7 Flash _always_ downgrades my Go-versions (why would you do that, you little prat!), Claude is _really_ good, especially for reviews, Claude Desktop is not as user-friendly as Antigravity, DSH is verbose and not yet as mature as the rest. I used Hermes before, don't know why I skipped Pi/OMP (probably because I'm lazy..?), and really eager to try [Prime Agent](https://github.com/PrimeIntellect-ai/prime-agent).

**Caveat**: Vibe-coding also oftentimes means that projects get created, but not maintained. **Will this project be maintained forever?** No idea. It _should_ work for quite some time and need little maintaining, but there are a few things I took upon me, e.g. bun runtime or the targeted CVE-scanner instead of using `syft`. So, yeah, hopefully, but can't promise. You can also ping me, if you notice that it's not up to date anymore.

A hint for others who are stuggling with **Roadmaps** and agents: Claude built quite a handy system for generating roadmaps and having a single source of truth, do have a look at `scripts/gen-docs`

# Where to find

- [GitHub](https://github.com/creativebeastdesign/pokkum)
- One-liner: `curl -fsSL https://raw.githubusercontent.com/CreativeBeastDesign/pokkum/main/install.sh | sh`
- After the 24-hour block (roughly 06:30am UTC): `npm install -g pokkum/cli`
- Brew:

```
brew tap CreativeBeastDesign/pokkum
brew trust CreativeBeastDesign/pokkum
brew install pokkum
```

# What I'd be ever so grateful about & caveats

**Testing** - I'm doing what I can, but my time is limited. Everything _should_ work, but I am certain I have not covered all edge cases. Or even cases.
**Release Tags** - Getting the release pipeline working cost a few releases that are now empty. The pipeline should work now, but, as you might have noticed with the 24-hour block, it was quite a struggle. So, yeah, tags look weird (there was this meme about the version numbers, v**1**.0.0 etc. are the proud ones, v1.0.**29** are... less so - same here)
**SLSA provenance** - Images built with Pokkum get SLSA v1 provenance, while Pokkum itself is v0.2 - for what it's worth. I don't know whether anyone would check that, but hey, you never know.
