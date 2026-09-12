# Four ways to containerise the same static SvelteKit app

One static SvelteKit site. Four builds. Same source tree, same machine, same measuring tape.

Following the methodology of the main [three-way SSR benchmark](../three-way/README.md), this suite compares the build size, security posture, attack surface, and operational footprint of compiling static SvelteKit sites:

```bash
./run.sh
```

Flags:
- `--keep`: Leaves built Docker images in your daemon for inspection.
- `--no-scan`: Skips the Trivy/Grype vulnerability and package scan (fast mode).
- `<path>`: Pass any custom `@sveltejs/adapter-static` project directory to measure it.

---

## What it looks like on one machine

Measured on an Apple Silicon Mac using `trivy` and Docker Desktop:

| | Nginx (Debian) | Nginx (Alpine) | Caddy (Alpine) | Pokkum (`--strategy=static`) |
|---|---|---|---|---|
| Image size (MB) | 180.8 | 65.1 | 57.5 | **8.2** |
| OS packages | 134 | 22 | 19 | **3** |
| HIGH+CRITICAL CVEs | 68 | 0 | 0 | **0** |
| Shell in the image | yes | yes | yes | **no** |
| Base distribution | Debian GNU/Linux | Alpine Linux | Alpine Linux | **chainguard/static** (libc-free) |
| PID 1 Process | nginx (master) | nginx (master) | caddy | **pokkum-static** (Go) |
| Precompressed sidecars (.br/.gz/.zst) | manual | manual | manual | **automatic** (`precompressutils`) |
| Reproducible build | no (by construction) | no (by construction) | no (by construction) | **yes** |
| SBOM / SLSA Provenance | no | no | no | **yes** |
| Lines of build & server config | 24 | 24 | 18 | **0** |

---

## What gets built

| Variant | What it represents |
|---|---|
| [`Dockerfile.nginx`](Dockerfile.nginx) | Standard Debian-based `nginx:latest` static hosting setup — the default in most web starter tutorials. |
| [`Dockerfile.alpine`](Dockerfile.alpine) | Tuned multi-stage `nginx:alpine` setup with dedicated server block handling SvelteKit routing. |
| [`Dockerfile.caddy`](Dockerfile.caddy) | Modern `caddy:alpine` static hosting setup using a Caddyfile. |
| `pokkum build --strategy=static` | Statically-linked Go PID 1 file server (`pokkum-static`) atop `cgr.dev/chainguard/static:latest`. Zero Dockerfile, zero Nginx/Caddy config. |

---

## Key Takeaways

1. **Size reduction**: Pokkum's static container image is **~8.2 MB** (composed of a ~2.3 MB compressed Go web server + static assets on a libc-free Chainguard static base). Compared to Alpine Nginx (~65 MB) and Debian Nginx (~180 MB), this represents an **87% to 95% reduction in disk and network transfer footprint**.
2. **Attack surface**: Traditional web server images require an underlying OS userland (`sh`, `apk`, `apt`, libc) which carries OS packages and CVE liability. Pokkum runs on `chainguard/static` with zero shell, zero package manager, and zero dynamic libraries.
3. **HTTP optimizations out-of-the-box**: Pokkum automatically generates `.br`, `.gz`, and `.zst` sidecars via `precompressutils` and serves them dynamically via `pokkum-static` with ETag and RFC 9110 conditional GET revalidation, requiring zero server configuration.
