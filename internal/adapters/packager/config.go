package packager

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// ociAnnotationKeys are the org.opencontainers.image.* keys that are mirrored
// from the image config's labels onto the manifest's annotations.
//
// The mirror exists because ports.PackageRequest carries most of the
// provenance values — revision, source, version — only as Labels, while
// generic OCI tooling (cosign, oras, registry UIs, `docker buildx imagetools
// inspect`) reads annotations. Rather than invent a second request field for
// each of them, the packager treats a label in the standard namespace as
// authoritative for the annotation of the same name.
//
// org.opencontainers.image.base.name is the one exception: it also has a
// dedicated field, PackageRequest.BaseRef, because relying on the mirror alone
// would mean a standard OCI annotation only gets populated if the caller
// happens to know to stuff it into Labels under this exact key — a magic
// string with nothing in the port's types pointing at it. BaseRef gives that
// value a discoverable home; an explicit ports.LabelBaseName entry in Labels
// still wins if the caller sets both — see imageAnnotations.
// PackageRequest.Annotations, as ever, wins over both.
//
// The order is fixed only so the list reads sensibly; the destination is a map
// and encoding/json sorts map keys, so iteration order here cannot reach the
// output.
var ociAnnotationKeys = []string{
	ports.LabelCreated,
	ports.LabelBaseName,
	ports.LabelBaseDigest,
	ports.LabelRevision,
	ports.LabelSource,
	ports.LabelVersion,
}

// applyRuntime returns a new config file: baseCfg with Pokkum's runtime
// contract applied. baseCfg is never modified — it belongs to the caller's base
// image, which is shared across platforms and across builds.
func applyRuntime(
	baseCfg *v1.ConfigFile,
	rc ports.RuntimeConfig,
	req ports.PackageRequest,
	baseDigest v1.Hash,
	ts time.Time,
) *v1.ConfigFile {
	cfg := baseCfg.DeepCopy()

	cfg.Created = v1.Time{Time: ts}
	cfg.Config.Entrypoint = slices.Clone(rc.Entrypoint)
	cfg.Config.Cmd = slices.Clone(rc.Cmd)
	cfg.Config.User = rc.User
	cfg.Config.WorkingDir = rc.WorkingDir
	cfg.Config.Env = mergeEnv(baseCfg.Config.Env, pokkumEnv(rc))
	cfg.Config.ExposedPorts = exposedPorts(baseCfg.Config.ExposedPorts, rc)
	cfg.Config.Labels = mergeLabels(baseCfg.Config.Labels, req.Labels, baseDigest, ts, rc)

	// Fields that record where an image was assembled rather than what it
	// contains. They are pure build-host noise and would defeat reproducibility
	// if a base image ever carried them.
	cfg.Container = "" //nolint:staticcheck // deprecated field, cleared deliberately
	cfg.Config.Hostname = ""

	return cfg
}

// envVar is one environment entry, kept as a pair so the merge can compare keys
// without re-splitting strings.
type envVar struct {
	key   string
	value string
}

// pokkumEnv returns the environment Pokkum contributes, in a fixed order.
//
// The always-present contract variables (PORT, POKKUM_PROBE_PORT,
// POKKUM_SHUTDOWN_TIMEOUT) come first, in the order they are declared in
// ports, followed by the optional adapter-node reverse-proxy contract
// variables (ORIGIN and friends — only emitted when set, since each has its
// own sensible adapter-node-side default when absent), followed by
// RuntimeConfig.Env sorted by key. That sort is one of the two
// map-iteration hazards W1 called out: Env is a map so that callers need
// not care about order, but the image config's Env is an ordered JSON array, so
// draining the map directly would produce a different config digest on almost
// every run.
//
// A key that appears in both wins from RuntimeConfig.Env, which is how a user
// pins an unusual PORT — see the field docs on RuntimeConfig.Env.
func pokkumEnv(rc ports.RuntimeConfig) []envVar {
	out := []envVar{
		{ports.EnvPort, strconv.Itoa(rc.Port)},
		{ports.EnvProbePort, strconv.Itoa(rc.ProbePort)},
		{ports.EnvShutdownTimeout, rc.ShutdownTimeout.String()},
	}
	if rc.Origin != "" {
		out = append(out, envVar{ports.EnvOrigin, rc.Origin})
	}
	if rc.ProtocolHeader != "" {
		out = append(out, envVar{ports.EnvProtocolHeader, rc.ProtocolHeader})
	}
	if rc.HostHeader != "" {
		out = append(out, envVar{ports.EnvHostHeader, rc.HostHeader})
	}
	if rc.AddressHeader != "" {
		out = append(out, envVar{ports.EnvAddressHeader, rc.AddressHeader})
	}
	if rc.XFFDepth != 0 {
		out = append(out, envVar{ports.EnvXFFDepth, strconv.Itoa(rc.XFFDepth)})
	}
	if rc.BodySizeLimit != "" {
		out = append(out, envVar{ports.EnvBodySizeLimit, rc.BodySizeLimit})
	}
	if len(rc.RequireEnv) > 0 {
		reqEnvs := slices.Clone(rc.RequireEnv)
		slices.Sort(reqEnvs)
		reqEnvs = slices.Compact(reqEnvs)
		out = append(out, envVar{ports.EnvRequiredEnv, strings.Join(reqEnvs, ",")})
	}
	for _, k := range slices.Sorted(maps.Keys(rc.Env)) {
		out = append(out, envVar{k, rc.Env[k]})
	}
	return out
}

// mergeEnv overlays entries onto the base image's environment.
//
// An existing key is replaced where it stands rather than appended, so the base
// image's own ordering survives; a new key is appended in the order given. This
// is what keeps the base's PATH and SSL_CERT_FILE — dropping them is the classic
// way to produce a distroless image whose binary cannot find the dynamic loader
// or verify a TLS certificate.
func mergeEnv(base []string, overlay []envVar) []string {
	out := slices.Clone(base)
	if out == nil {
		out = []string{}
	}
	for _, e := range overlay {
		entry := e.key + "=" + e.value
		if i := envIndex(out, e.key); i >= 0 {
			out[i] = entry
			continue
		}
		out = append(out, entry)
	}
	return out
}

// envIndex finds a KEY=VALUE entry by key, or reports -1. An entry with no "="
// is treated as a bare key, which is how some base images spell an empty value.
func envIndex(env []string, key string) int {
	for i, e := range env {
		k, _, found := strings.Cut(e, "=")
		if !found {
			k = e
		}
		if k == key {
			return i
		}
	}
	return -1
}

// exposedPorts returns the image's exposed port set: whatever the base declared,
// plus the application port and the supervisor's probe port, plus any extras the
// caller asked for.
//
// The probe port is exposed as well as the application port because probes are
// the reason the supervisor exists; a manifest generator reading only the image
// config would otherwise have to know the port out of band.
//
// The destination is a set, and encoding/json emits map keys sorted, so no
// ordering work is needed here.
func exposedPorts(base map[string]struct{}, rc ports.RuntimeConfig) map[string]struct{} {
	out := make(map[string]struct{}, len(base)+2+len(rc.ExposedPorts))
	maps.Copy(out, base)
	out[tcpPort(rc.Port)] = struct{}{}
	out[tcpPort(rc.ProbePort)] = struct{}{}
	for _, p := range rc.ExposedPorts {
		out[tcpPort(p)] = struct{}{}
	}
	return out
}

func tcpPort(p int) string { return strconv.Itoa(p) + "/tcp" }

// mergeLabels builds the image config's labels: the base image's, then
// Pokkum's, then the caller's.
//
// The overlay order is the contract stated on PackageRequest.Labels — Pokkum's
// own labels are applied first so a caller can override them. Because each
// stage is a whole-map overlay onto a map, iteration order within a stage
// cannot affect the result; only the order of the stages can, and that is
// fixed.
func mergeLabels(base, caller map[string]string, baseDigest v1.Hash, ts time.Time, rc ports.RuntimeConfig) map[string]string {
	out := make(map[string]string, len(base)+len(caller)+3)
	maps.Copy(out, base)

	out[ports.LabelCreated] = ts.Format(time.RFC3339)
	// The digest of the manifest this image was actually layered on. Taken from
	// the resolved base image rather than from a request field, because it is
	// the only value that cannot be wrong: it is read off the very object the
	// layers were appended to.
	out[ports.LabelBaseDigest] = baseDigest.String()
	if len(rc.RequireEnv) > 0 {
		reqEnvs := slices.Clone(rc.RequireEnv)
		slices.Sort(reqEnvs)
		reqEnvs = slices.Compact(reqEnvs)
		out[ports.LabelRequiredEnv] = strings.Join(reqEnvs, ",")
	}
	if len(rc.EnvBaked) > 0 {
		baked := slices.Clone(rc.EnvBaked)
		slices.Sort(baked)
		baked = slices.Compact(baked)
		out[ports.LabelEnvBaked] = strings.Join(baked, ",")
	}
	if len(rc.VEXExemptions) > 0 {
		exempted := slices.Clone(rc.VEXExemptions)
		slices.Sort(exempted)
		exempted = slices.Compact(exempted)
		out[ports.LabelVEXExemptions] = strings.Join(exempted, ",")
	}

	maps.Copy(out, caller)
	return out
}

// imageAnnotations returns the manifest annotations for a built image: the
// standard OCI keys mirrored from the finished labels, then base.name falling
// back to baseRef if the mirror did not already supply it, then the caller's
// explicit annotations.
//
// The fallback sits between the label mirror and the caller's annotations so
// that the precedence is, from lowest to highest: PackageRequest.BaseRef, an
// explicit ports.LabelBaseName entry in PackageRequest.Labels, an explicit
// org.opencontainers.image.base.name entry in PackageRequest.Annotations. See
// the field docs on PackageRequest.BaseRef for why the label wins over the
// field.
func imageAnnotations(labels map[string]string, baseRef string, caller map[string]string) map[string]string {
	out := make(map[string]string, len(ociAnnotationKeys)+len(caller)+1)
	for _, k := range ociAnnotationKeys {
		if v, ok := labels[k]; ok && v != "" {
			out[k] = v
		}
	}
	if v, ok := labels[ports.LabelRequiredEnv]; ok && v != "" {
		out[ports.AnnotationRequiredEnv] = v
	}
	if v, ok := labels[ports.LabelEnvBaked]; ok && v != "" {
		out[ports.AnnotationEnvBaked] = v
	}
	if v, ok := labels[ports.LabelVEXExemptions]; ok && v != "" {
		out[ports.AnnotationVEXExemptions] = v
	}
	if _, ok := out[ports.LabelBaseName]; !ok && baseRef != "" {
		out[ports.LabelBaseName] = baseRef
	}
	maps.Copy(out, caller)
	return out
}

// indexAnnotations returns the manifest annotations for the image index. The
// index has no config and therefore no labels to mirror, so only the timestamp
// is derived; everything else is the caller's.
func indexAnnotations(req ports.IndexRequest, ts time.Time) map[string]string {
	out := make(map[string]string, len(req.Annotations)+1)
	out[ports.LabelCreated] = ts.Format(time.RFC3339)
	maps.Copy(out, req.Annotations)
	return out
}
