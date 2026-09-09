// guide.go implements `pokkum guide`: the operating manual for driving
// Pokkum from inside a SvelteKit project, printed by the binary itself.
//
// Why this lives in the binary rather than in a repository markdown file:
// the failure it exists to prevent is version skew. A Homebrew-installed
// 1.0.6 ships a README that does not mention `pokkum deploy` at all, while a
// newer binary earlier on the same PATH carries the whole Dokploy/SwiftWave
// surface — so a reader consulting this repository's docs for a binary they
// did not install is reading fiction. Text compiled into the binary cannot
// describe a different version than the one printing it.
//
// Audience: an operator or an AI agent working in a SvelteKit project that is
// NOT this repository. That audience is served by nothing else here —
// AGENTS.md/CLAUDE.md/DSH.md instruct agents working ON Pokkum, and
// Vocabulary.md is an exhaustive flag reference organised by flag rather than
// by decision, which that audience also cannot read because it lives here and
// not in their project. The guide therefore duplicates reference material
// deliberately; guide_test.go is what keeps the duplication honest.
//
// Anti-drift: this file is AUTHORED, not generated, following the same
// discipline Vocabulary.md already uses (flags_docs_test.go, envvar_docs_test.go)
// rather than the generation used for docs/roadmap. Three guards apply:
//
//  1. guide_test.go asserts every `yaml:` field of ports.ProjectConfig and
//     ports.BuildProfile is named in the config section, by reflection — so a
//     new config field cannot ship with the guide silently incomplete.
//  2. guide_test.go asserts every command in the cobra tree is named somewhere
//     in the guide.
//  3. flagmentions_test.go already AST-scans every string literal under
//     cmd/pokkum and internal, and fails on a `--flag` mention that is not a
//     real pokkum flag. Every flag named below is therefore checked to exist.
package main

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// guideSection is one addressable chapter of the guide. Topic is what
// `pokkum guide <topic>` matches on.
type guideSection struct {
	Topic string
	Title string
	Body  string
}

func newGuideCommand(logger *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "guide [topic]",
		Short: "Print the operating manual for using Pokkum in a SvelteKit project",
		Long: `Guide prints the complete operating manual for driving Pokkum from inside a
SvelteKit project: the invariants that constrain application code, the full
.pokkum.yaml field reference, build strategies, output modes, deployment
recipes, verification, and the failure taxonomy.

It is written for an operator or an AI agent working in a project rather than
on Pokkum itself, and it is compiled into this binary, so it always describes
this exact version rather than whatever documentation happens to be on disk.

With no argument it prints every section. With a topic it prints one; run
"pokkum guide topics" for the list.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				fmt.Fprint(out, renderGuide(guideSections))
				return nil
			}
			topic := strings.ToLower(strings.TrimSpace(args[0]))
			if topic == "topics" || topic == "list" {
				fmt.Fprint(out, renderGuideTopics())
				return nil
			}
			for _, s := range guideSections {
				if s.Topic == topic {
					fmt.Fprint(out, renderGuide([]guideSection{s}))
					return nil
				}
			}
			// An unknown topic is a usage mistake, not a runtime failure: name
			// the valid set rather than making the caller guess twice.
			return fmt.Errorf("unknown guide topic %q\n\n%s", topic, renderGuideTopics())
		},
	}
}

func renderGuideTopics() string {
	var b strings.Builder
	b.WriteString("Available topics (pokkum guide <topic>):\n\n")
	width := 0
	for _, s := range guideSections {
		if len(s.Topic) > width {
			width = len(s.Topic)
		}
	}
	for _, s := range guideSections {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, s.Topic, s.Title)
	}
	return b.String()
}

func renderGuide(sections []guideSection) string {
	var b strings.Builder
	for i, s := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## %s\n\n", s.Title)
		b.WriteString(strings.TrimLeft(s.Body, "\n"))
	}
	return b.String()
}

// guideTopicNames is the ordered topic list, used by tests to assert the
// index and the section set cannot diverge.
func guideTopicNames() []string {
	names := make([]string, 0, len(guideSections))
	for _, s := range guideSections {
		names = append(names, s.Topic)
	}
	sort.Strings(names)
	return names
}

var guideSections = []guideSection{
	{
		Topic: "overview",
		Title: "What Pokkum is, operationally",
		Body: `
SvelteKit project in, OCI container image out. No Dockerfile. The build runs
the project's own SvelteKit build, then assembles the output into a minimal,
reproducible image and pushes it to a registry.

What enters the image:

  - the SvelteKit adapter's build output
  - production dependencies, when the strategy needs them
  - a pinned Bun runtime (strategy: layered) or the embedded static file
    server (strategy: static)
  - pokkum-init, a static PID 1 that reaps orphans, forwards SIGTERM and
    serves health probes

What does NOT enter the image: everything else in the project directory.
There is no implicit "copy the rest of the repo" step. If a file is not
adapter output, it is not in the image. This is the single most costly wrong
assumption when coming from a hand-written Dockerfile with COPY . — see the
"invariants" topic before writing application code that reads from disk.

The commands, in the order a project meets them:

  pokkum guide      this manual (pokkum guide topics for the section list)
  pokkum init       write .pokkum.yaml and .pokkumignore, after analysing the project
  pokkum doctor     preflight the environment and the project
  pokkum build      compile and publish the image
  pokkum dev        local run/watch loop
  pokkum config     inspect and validate .pokkum.yaml
  pokkum explain    inspect a real built image, layer by layer
  pokkum scan       CVE scan a project, image or tarball
  pokkum verify     rebuild and check signatures and attestations
  pokkum resolve    rewrite image refs in Kubernetes manifests
  pokkum apply      resolve, then hand the manifests to kubectl
  pokkum deploy     hand a pushed image to a PaaS control plane
  pokkum rollback   walk image refs in manifests back a generation
  pokkum adopt      migrate an existing project onto Pokkum defaults
  pokkum base       re-resolve and pin base images in pokkum.lock
  pokkum history    read a published image's own annotations
  pokkum repro      reproducibility diagnostics (pokkum repro doctor)
  pokkum upgrade    self-update this binary
  pokkum version    print version, commit and build date
`,
	},
	{
		Topic: "quickstart",
		Title: "The short path, from nothing to a running image",
		Body: `
Each step ends somewhere you can check, rather than at the next command.

  1. Confirm the project has a concrete SvelteKit adapter, not adapter-auto.
     adapter-auto produces no output Pokkum can package. pokkum doctor reports
     this, and pokkum build refuses with the exact remediation.

  2. pokkum doctor
     Cheap. Catches the missing adapter, missing .pokkumignore and absent
     registry credentials before anything expensive runs. Add --fix to repair
     the mechanical ones.

  3. pokkum init --defaults
     Analyses the project (see the "strategy" topic) and writes .pokkum.yaml
     with the detected strategy, runtime and base. --defaults skips the
     questions; the detection still runs. Drop --defaults for the interactive
     form.

  4. pokkum build --local
     Loads straight into the local Docker daemon. No registry, no deploy, no
     signing. Iterate here until it is green. Note that a daemon load drops
     OCI annotations — see the "output" topic — so this proves the image runs,
     not that it is publishable.

  5. Run it, as the real user:
       docker run -u 65532 -p 3000:3000 -e DATABASE_URL=... <image>
     Then check the app, plus the supervisor's endpoints on the probe port:
     /healthz and /readyz. Pass placeholder values for every name in
     image.require_env, or the supervisor will fail fast at boot by design.

     Use docker run, NOT docker exec. exec defaults to root, and root
     bypasses the read-only file modes via DAC_OVERRIDE — so a command that
     works under exec proves nothing about the non-root process that actually
     serves traffic. This has produced a false "it works" in practice.

  6. pokkum build --tag <sha>
     The real push. Add --sign in CI.

  7. Verify: pokkum explain <ref> for the layer breakdown, pokkum verify <ref>
     for a cryptographic verdict.
`,
	},
	{
		Topic: "invariants",
		Title: "Invariants that constrain your application code",
		Body: `
These hold in every Pokkum image and cannot be configured away. They shape how
the application must be written, so read them before writing code, not after a
build fails.

  1. Only the adapter's build output ships.
     Templates, fonts, seed data, migrations and anything else the app reads
     off disk are absent unless the project's own build places them inside the
     adapter output directory. A postbuild step that copies them there works
     today and is the supported pattern.

  2. The application tree is read-only. Mode 0555, every image, no opt-out.
     Any runtime write to a project-relative path fails. Writes must target the
     OS temporary directory, resolved from the environment at runtime — never
     a path computed relative to the working directory or the module location.
     This is a permanent property of how the layers are built, not a default.

  3. The process runs as a non-root user (UID 65532 on the default bases).
     Anything requiring root, a privileged port, or write access outside the
     temp directory will not work.

  4. Required environment is enforced at boot, not at first use.
     Every name in image.require_env must be present and non-empty or the
     supervisor exits before serving traffic. This is deliberate: a missing
     credential becomes a failed rollout instead of a 500 an hour later.

  5. The build never mutates your source tree.
     Adapter and telemetry injection happen under .pokkum/ in the project
     directory. Files you wrote are not rewritten. (pokkum adopt --write-config
     is the one explicit exception, and only when you ask for it.)

Before configuring anything, inventory what the app assumes about its
filesystem. Grep for process.cwd(), spawn(, and any path joined against a
project root; that list is exactly the set of assumptions items 1 and 2 will
break.
`,
	},
	{
		Topic: "config",
		Title: "The .pokkum.yaml field reference",
		Body: `
Written by pokkum init. Validate with pokkum config validate, resolve with
pokkum config view [--profile <name>]. Unknown or misspelled keys are
rejected, at the top level and inside every profile.

Precedence, highest first:
  explicit CLI flag > environment variable > profile override > top-level
  config > built-in default

Only some fields have an environment-variable form; most are config/flag only.

TOP LEVEL

  version               schema version. 1.
  docker                push target. See below.
  strategy              layered (default) or static. See the "strategy" topic.
  runtime               bun (default) or node. Composes with strategy and base
                        — see the composition rule at the end of this section.
  stub_launcher         bool. Emit a stub launcher instead of the real entry.
  base                  base image preset or a full image reference. See the
                        "base" topic.
  platforms             list, e.g. [linux/amd64, linux/arm64]. "local" resolves
                        to the host platform.
  image                 image configuration. See below.
  build                 build-time options. See below.
  security              verification and CVE policy. See below.
  sbom                  SBOM format and attachment. See below.
  cache                 remote build cache. See below.
  otel                  OpenTelemetry. See below.
  deploy                PaaS handoff. Fields below; recipes in the "deploy" topic.
  profiles              named partial overrides. See below.

docker
  repo                  push target repository. Required for the default push
                        output mode; not required for --local or --tarball.
  tags                  tags without the repository prefix. Defaults to
                        [latest]. Overridden by --tag or POKKUM_DOCKER_TAGS.

image
  labels                extra OCI labels, merged with the auto-populated
                        org.opencontainers.image.* set.
  annotations           extra OCI annotations. Lost in non-registry output
                        modes — see the "output" topic.
  env                   environment variables baked into the image config.
  require_env           names that must be present and non-empty at boot, or
                        the supervisor refuses to start.
  ports                 exposed ports.
  port                  the application's listen port. Default 3000.
  probe_port            the supervisor's health-probe port. Default 8081.
  user                  runtime user, e.g. "1000:1000".
  working_dir           container working directory.
  shutdown_timeout      grace period after SIGTERM before SIGKILL, e.g. 30s.
  origin                adapter-node ORIGIN.
  protocol_header       adapter-node PROTOCOL_HEADER.
  host_header           adapter-node HOST_HEADER.
  address_header        adapter-node ADDRESS_HEADER.
  xff_depth             adapter-node XFF_DEPTH.
  body_size_limit       adapter-node BODY_SIZE_LIMIT.

build
  exclude_routes             route paths (not file globs) to drop. A bare path
                             covers its subtree; "*" matches within a segment,
                             "**" across segments. Merged with --exclude-route
                             rather than replaced by it.
  allow_server_code_in_static  bool. Relax the static-strategy gate. Read the
                             "strategy" topic before setting this.

security
  fail_on_cve           low|medium|high|critical. Unset means warn-only.
  verify_base           bool. Verify the base image signature before use.
  allow_incomplete_scans  bool. Default false: an incomplete vulnerability
                        lookup blocks the build rather than passing quietly.
  allow_secret_patterns  glob allowlist for known secret-scanner false
                        positives.
  vex_exemptions        list of OpenVEX exemptions, each with:
                          cve, package, justification, status_notes,
                          expires, owner
                        expires and owner are required — an exemption without
                        an expiry and a name is not accepted.

sbom
  format                spdx-json (default), cyclonedx-json, or none.
  attach                referrer (default, OCI 1.1) or tag (legacy .sbom tag).

cache
  enabled               bool.
  verify_mode           auto (default), static-key, or keyless.
  pubkey                static verification key for cache entries.
  keyless_identity      expected Fulcio SAN for keyless cache verification.
  keyless_issuer        expected OIDC issuer for keyless cache verification.

otel
  sidecar               bool. Inject a collector sidecar into generated
                        Kubernetes manifests.
  tracing               bool.
  metrics               bool.

deploy
  target                dokploy or swiftwave. Empty disables deployment.
  method                api (default) or webhook. SwiftWave only.
  endpoint              the control plane URL.
  endpoint_env          name of an env var holding the endpoint instead.
  application           the platform's application identifier.
  token_env             name of the env var holding the API credential.
                        Defaults to POKKUM_DEPLOY_TOKEN.
  auto                  bool. Deploy automatically after a successful push.
  update_image          bool. Repoint the application at the new reference.
                        Defaults false; read the warning in the "deploy" topic
                        before enabling it, and rejected outright for swiftwave.
  registry_url          pull-credential registry, required when update_image
                        is true or stored credentials are cleared.
  registry_username_env name of the env var holding the registry username.
  registry_password_env name of the env var holding the registry password.
  timeout               request timeout, e.g. 60s.

  Only names of credentials appear here; the values live in the environment.

profiles
  A map of named partial overrides, activated with --profile <name>. Only set
  what should differ; anything omitted falls through to the top level. A
  profile may set output, platforms, base, strategy, runtime, stub_launcher,
  sourcemap, docker, image, build, security, sbom, cache, otel and deploy.

  Two fields are profile-only and have no top-level form:
    output              push (default), local, tarball, or oci-layout.
    sourcemap           bool. Keep source maps, for a debuggable local build.

  --local auto-selects a profile literally named "local" if one exists.
  pokkum config validate checks every profile independently, so a mistake
  confined to one profile is reported as that profile's error rather than
  silently ignored.

THE COMPOSITION RULE

  runtime, strategy and base are not three independent choices.

    runtime: node    requires strategy: layered AND a base that ships a Node
                     binary. pokkum init upgrades the base accordingly rather
                     than emitting a combination that validates and then
                     refuses to build.
    strategy: static ships no JavaScript runtime at all, so runtime is
                     meaningless beside it and is not written.

  pokkum config validate checks fields; this rule is a cross-field constraint
  enforced at build time. Write the three together or let pokkum init do it.
`,
	},
	{
		Topic: "strategy",
		Title: "static vs layered, and what the analysis proves",
		Body: `
  layered (default)  an N-layer image with a pinned Bun runtime, running the
                     SvelteKit server. Works for every project.
  static             a zero-JavaScript, zero-runtime image of a fully
                     prerendered site, served by the embedded static file
                     server. Smaller and with far less attack surface, but only
                     possible if nothing in the project needs a server.

pokkum init runs a disqualifier scan over the route sources and reports
viable, blocked, or unknown. A project it could not scan is reported as
unknown — never as viable.

What rules static out:

  +server files exporting POST, PUT, PATCH, DELETE or QUERY      blocks
  a route directory holding both a +page and a +server file      blocks
  export const actions (form actions)                            blocks, always
  a *.remote file using query(), form() or command()             blocks
  export const prerender = false anywhere                        blocks
  +server files exporting only GET/HEAD/OPTIONS                  fine
  a load function in +page.server or +layout.server              fine
  a *.remote file using only prerender()                         fine
  hooks.server                                                   caveat only

These are SvelteKit's own prerendering rules, read from its source rather than
inferred. Matching ignores comments and string literals, so a commented-out
directive does not count.

The scan is a SOUND NEGATIVE ONLY. "Nothing found" means nothing in the source
requires a server; it does not mean a static build will succeed. A load
function calling a runtime-only API, or a dynamic route the crawler never
reaches, still fails later at build time.

Static also has two prerequisites beyond viability: @sveltejs/adapter-static in
package.json, and export const prerender = true in the routes root layout.
pokkum init reports which of the two is missing.
`,
	},
	{
		Topic: "base",
		Title: "Base image presets",
		Body: `
Set with base: in .pokkum.yaml or --base on the command line. The value is
either a preset name or a full image reference.

  distroless         the default. A small glibc base.
  chainguard         a hardened glibc base, keyless-Sigstore-verified.
  chainguard-static  libc-free, used by strategy: static.
  distroless-node    carries a Node binary; required by runtime: node.
  custom             any full reference (registry/repo:tag or repo@sha256:...).

Presets are matched first. Only a value that looks like a reference — one
containing /, ., : or @ — is parsed as one, so a mistyped preset is rejected
with the valid list rather than becoming a registry lookup that fails much
later at pull time.

A custom reference defaults to static-key verification and FAILS CLOSED: set
POKKUM_BASE_IMAGE_PUBKEY to the Cosign public key that signed it, or pass
--no-verify-base and understand what you have turned off. Each custom
reference gets its own pokkum.lock slot, so two custom bases in one project
do not evict each other.

To see what a preset resolves to right now, run pokkum base check (or
pokkum base update to re-resolve and re-pin). You need this before building a
custom base that must be binary-compatible with a preset — the libc has to
match.

Resolved digests live in pokkum.lock. pokkum build re-verifies the locked
digest against the live signature at build time, including through a mirror.
`,
	},
	{
		Topic: "output",
		Title: "Output modes, and what each one loses",
		Body: `
  (default)         push to the registry named by docker.repo.
  --local           load into the local Docker daemon.
  --tarball <path>  write a docker-save format tarball.
  --to-oci-layout <path>  write an OCI image layout directory.

Only a registry push is fully featured. Specifically:

  Annotations. The docker-save format has no annotations field, so --local and
  --tarball silently drop every OCI annotation Pokkum stamps — including the
  base digest and required-env records that other commands read back. Pokkum
  warns when this happens. Treat these modes as "does it run", not as a
  rehearsal of a real publish.

  Deploy. Automatic deploy and pokkum deploy need something a remote control
  plane can pull, so every non-push mode skips deployment with a warning
  naming the mode.

  Signing and attestation are meaningful only against a registry.

Tags are applied registry-side after the image is hashed, so the tag set never
affects the digest. --tag is repeatable and accepts comma-separated values;
duplicates are rejected.
`,
	},
	{
		Topic: "deploy",
		Title: "Deployment recipes",
		Body: `
Four targets. Pick by what runs the container.

KUBERNETES

  pokkum resolve -f k8s/ --recursive     rewrite image refs, print manifests
  pokkum apply   -f k8s/ --recursive     resolve, then hand to kubectl

  Image references are patched by CONTAINER NAME in your own manifests. Your
  manifests stay yours: sidecars, init containers, volumes and everything else
  survive untouched. Pokkum does not generate a pod spec.

  --probe-defaults, --resource-defaults, --security-context and
  --network-policy fill in hardened defaults; each has a matching opt-out form
  with a "no-" prefix, e.g. --no-probe-defaults.
  --with-otel-sidecar injects an OpenTelemetry Collector (4317 gRPC, 4318 HTTP,
  8889 metrics). That is the ONLY sidecar Pokkum can inject — see the "extras"
  topic if you need another.

  Roll back a generation with pokkum rollback -f <manifest>, which reads the
  image-history annotations Pokkum wrote.

DOKPLOY

  In .pokkum.yaml:

    deploy:
      target: dokploy
      endpoint: https://dokploy.example.com     # or endpoint_env
      application: <applicationId>
      token_env: DOKPLOY_TOKEN                  # default POKKUM_DEPLOY_TOKEN
      auto: true                                # deploy after a successful push
      update_image: false
      timeout: 60s

  update_image repoints the application at the newly pushed reference. It
  defaults to false for a real reason: the underlying Dokploy call is a FULL
  OVERWRITE whose schema requires all five registry fields, so enabling it
  without also setting registry_url, registry_username_env and
  registry_password_env CLEARS the pull credentials Dokploy had stored. Pokkum
  warns when it is about to do that. Set the three registry fields whenever
  update_image is true.

  A private image needs those credentials regardless; a public one removes the
  whole problem, which is worth choosing deliberately up front.

SWIFTWAVE

    deploy:
      target: swiftwave
      method: api                               # or webhook
      endpoint_env: SWIFTWAVE_ENDPOINT
      application: <applicationId>
      token_env: SWIFTWAVE_TOKEN

  Read this before designing the tag scheme: SwiftWave CANNOT be repointed at
  a new image reference. Both its webhook and its rebuild mutation rebuild the
  application's current deployment, so the application must be pinned to a
  MUTABLE TAG that Pokkum republishes. update_image is rejected outright for
  this target rather than silently ignored.

  For method: webhook the secret is already inside the URL, so no token is read.

VANILLA (anything that pulls from a registry)

  Pokkum's output is a plain OCI image, so any runtime that pulls one works
  with no deploy configuration at all. Build, push, and let the platform pull.
  The image already declares its port, its user, its required environment and
  its health probes, so most platforms need nothing else.

  What Pokkum will not do here is call an API it has no adapter for — leave
  deploy unset and drive the platform yourself.

RESPONSE CLASSIFICATION

  Both PaaS targets answer HTTP 200 for outcomes that are not deployments, so
  Pokkum classifies the body rather than the status code and fails closed on
  anything it cannot positively identify as a started rollout. A reported
  failure whose image was already pushed is worth confirming against the
  platform's own API before assuming nothing happened.
`,
	},
	{
		Topic: "env",
		Title: "Environment variables",
		Body: `
CLI AND DEPLOY (read on the machine running pokkum)

  POKKUM_DOCKER_REPO         push target, when docker.repo is unset
  POKKUM_DOCKER_TAGS         tag set, overriding docker.tags
  POKKUM_DEPLOY_TOKEN        default PaaS credential when deploy.token_env is
                             unset. A named-but-empty variable fails the deploy
                             rather than falling back to an unauthenticated call
  POKKUM_BASE_IMAGE_PUBKEY   Cosign public key for base image verification
  POKKUM_SIGNING_PUBKEY      public key for image signature verification

Only the NAMES of deploy credentials live in .pokkum.yaml; the values live in
the environment and are never written to the config file.

RUNTIME (read inside the container)

  Read by the supervisor: the probe port, the shutdown grace period and the
  required-environment list, all baked in from image.* at build time.

  Read by the application: the adapter-node variables configured through
  image.origin, image.protocol_header, image.host_header,
  image.address_header, image.xff_depth and image.body_size_limit.

For the authoritative table including every variable this version reads, see
Vocabulary.md in the Pokkum repository — but prefer the config fields above,
which are checked by pokkum config validate.
`,
	},
	{
		Topic: "verify",
		Title: "Verifying what you built",
		Body: `
DID MY FILE MAKE IT INTO THE IMAGE?

  pokkum explain why <image> <path>

  Traces which layer a file came from, which layer deleted it, or reports it
  was never present. This answers the question without starting a container,
  and it works against a registry reference or a local tarball. Reach for this
  before hand-rolling the same check with docker cp and docker exec.

WHAT IS IN THE IMAGE AT ALL?

  pokkum explain <image>              per-layer digest, size, file count, purpose
  pokkum explain diff <a> <b>         real added/removed/modified files per layer

  Add --platform to pick a child of a multi-arch index.

DOES IT ACTUALLY RUN?

  docker run -u 65532 -p 3000:3000 -e <each require_env name>=<placeholder> <image>

  Then check the app, plus /healthz and /readyz on the probe port.

  Always exercise the container as the real runtime user. docker exec defaults
  to root, and root bypasses the read-only modes described in the "invariants"
  topic via DAC_OVERRIDE — so an exec-shell success is not evidence that the
  serving process can do the same thing.

IS IT WHAT IT CLAIMS TO BE?

  pokkum verify <ref>                 rebuild and compare, plus signatures and
                                      attestations. --no-rebuild for the
                                      cryptographic checks only.
  pokkum history <image>              the image's own git/base annotations.
                                      Reads annotations; does not verify them.
  pokkum scan <target>                CVEs in a project, image or tarball.
  pokkum repro doctor                 bisect a reproducibility failure.
`,
	},
	{
		Topic: "extras",
		Title: "When your app needs another binary or extra files",
		Body: `
EXTRA FILES FROM YOUR OWN PROJECT (templates, fonts, seed data)

  Supported pattern today: a postbuild step in the project's own build that
  copies the directories into the SvelteKit adapter's output directory, so
  they ride along in the layer that already ships that output. Then set
  image.working_dir to the path that layer mounts at, so the application's
  existing relative path resolution keeps working without code changes.

  Matching the container's working directory to the application's assumptions
  is much less invasive than rewriting the application's path logic.

  Confirm it worked with: pokkum explain why <image> <path>

AN EXTRA OS BINARY (Typst, pandoc, ffmpeg, ImageMagick)

  Pokkum builds SvelteKit images. It has no mechanism for vendoring an
  arbitrary third-party binary, and there is no flag or config key for it.
  Do not go looking for one.

  Two working approaches:

  1. A custom base image. Build a base that is the preset plus your binary:

       FROM <upstream tool image> AS tool
       FROM <the preset's resolved reference>
       COPY --from=tool /path/to/binary /usr/local/bin/

     Get the preset's real reference from pokkum base check — the libc must
     match. Verify the upstream tag resolves for EVERY platform you build for
     before pinning it; a tag that exists for one architecture and not another
     fails late and confusingly. Then set base: to your reference, and either
     set POKKUM_BASE_IMAGE_PUBKEY or accept that you have opted out of base
     verification.

  2. A separate image entirely, run alongside. Build the tool's image with
     whatever suits it, write your own Kubernetes manifest with both
     containers, and let pokkum resolve or pokkum apply patch the application
     container's reference by name. Your sidecar survives untouched.

     What Pokkum cannot do is GENERATE that sidecar — --with-otel-sidecar is
     the only injectable one.

  Also note the tool must not need to write into the application tree, which is
  read-only, and it runs as the same non-root user.
`,
	},
	{
		Topic: "escape-hatches",
		Title: "Escape hatches, and when they are lies",
		Body: `
Each of these turns off a real check. Every one of them will make a failing
command exit 0. None of them is an answer to a failure whose cause you have
not yet identified.

  --no-verify-base
      Stops verifying that the base image is signed by whoever you said signs
      it. The usual reason this fails is a custom base with no
      POKKUM_BASE_IMAGE_PUBKEY set. The fix is to set the key.

  --allow-incomplete
      Reports a vulnerability scan as successful even when the database lookup
      FAILED, i.e. when coverage was reduced and unknown. Default is
      fail-closed. Note --offline is different: it skips lookups by design,
      always reports incomplete, and still exits 0 — so gate on the reported
      incomplete field there, not on the exit code.

  --fail-on <lower severity>
      Raising the threshold to silence a finding is a policy decision, not a
      fix. Record it as a VEX exemption under security.vex_exemptions instead,
      which requires a justification, an owner and an expiry.

  --show-secret-values
      Prints the matched text of secret-scanner findings instead of redacting
      it. For local triage of a false positive only. Never in CI, where it
      copies real credentials into build logs.

  --no-inject
      Disables adapter/telemetry injection. The build then validates the
      project's real configuration and fails fast if it is wrong, rather than
      silently building something different.

  --allow-unverified-source
      Lets --expect-source compare against unsigned image annotations, which
      anyone able to push to the repository controls. The result is explicitly
      marked unverified.

  security.allow_incomplete_scans, security.verify_base: false
      The config-file forms of the first two. Same reasoning; additionally they
      apply to every build rather than to one invocation, which is worse.

The general rule: if you cannot say in one sentence what the flag stopped
checking and why that is acceptable here, it is not the right flag.
`,
	},
	{
		Topic: "failures",
		Title: "Failure taxonomy",
		Body: `
  "sveltekit adapter missing" / "adapter misconfigured"
      The adapter the project actually configures is not the one the chosen
      strategy needs — most often a fresh sv create scaffold still on
      @sveltejs/adapter-auto, which produces no output Pokkum can package.

      Check BOTH places the adapter can be configured: svelte.config.js, and
      the SvelteKit plugin's options in vite.config.ts. A current sv create
      scaffold ships no svelte.config.js at all and configures the adapter
      through the Vite config, so "there is no svelte.config.js" is not the
      same as "no adapter is configured".

      pokkum doctor reports this before you build, and the error names the
      exact package and the import to write. pokkum adopt can make the change.

  ErrNotSvelteKit
      The directory is not a SvelteKit project. Check --dir.

  Base image verification failed / no key configured
      A custom base defaults to static-key verification and fails closed. Set
      POKKUM_BASE_IMAGE_PUBKEY, or see the "escape-hatches" topic and decide
      deliberately.

  Static strategy rejected
      Something in the routes requires a server. Run pokkum init (or read the
      "strategy" topic) for the specific finding and its file. Form actions
      cannot be retired by any flag; export const prerender = false can be
      retired with a source edit.

  A config that validates and then refuses to build
      Almost always the runtime/strategy/base composition — see the end of the
      "config" topic. pokkum config validate checks fields, not the cross-field
      rule.

  Deploy reported as failed after a successful push
      Both PaaS adapters fail closed on any response they cannot positively
      identify as a started rollout. The image is already pushed. Confirm
      against the platform's own API before assuming the rollout did not
      happen, and re-run pokkum deploy --image <ref> rather than rebuilding.

  A file is missing at runtime
      See the "invariants" and "extras" topics; confirm with
      pokkum explain why <image> <path>.

  A write fails at runtime
      The application tree is read-only. Write to the OS temp directory.

  Digest differs between two builds of the same source
      pokkum repro doctor. Note that the toolchain used to build matters.
`,
	},
	{
		Topic: "non-goals",
		Title: "What Pokkum will not do",
		Body: `
Stated so you stop looking for a flag that does not exist.

  Build images for non-SvelteKit projects. Pokkum is a SvelteKit compiler;
  there is no general image-building mode and no arbitrary-file input.

  Vendor an arbitrary third-party binary into the image. See the "extras"
  topic for the two working approaches.

  Generate a pod spec, or inject any sidecar other than the OpenTelemetry
  collector. Pokkum patches image references in the manifests you wrote.

  Target edge and serverless platforms that do not run OCI images.

  Load an npm-distributed plugin. An npm extension model would reintroduce
  exactly the supply-chain risk the secret scanning, hermetic build and CVE
  gating exist to mitigate. This is a decision, not an omission.

  Write to your source tree during a build, or read the system clock while
  producing image bytes.
`,
	},
}
