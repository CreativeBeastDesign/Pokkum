package packager

import (
	"archive/tar"
	"io"
	"slices"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestMergeEnv(t *testing.T) {
	tests := []struct {
		name    string
		base    []string
		overlay []envVar
		want    []string
	}{
		{
			name:    "appends new keys in order",
			base:    []string{"PATH=/bin"},
			overlay: []envVar{{"PORT", "3000"}, {"A", "1"}},
			want:    []string{"PATH=/bin", "PORT=3000", "A=1"},
		},
		{
			name:    "replaces in place, preserving base order",
			base:    []string{"PATH=/bin", "LANG=C", "PORT=1"},
			overlay: []envVar{{"PORT", "3000"}},
			want:    []string{"PATH=/bin", "LANG=C", "PORT=3000"},
		},
		{
			name:    "bare key with no equals is a key with an empty value",
			base:    []string{"DEBUG"},
			overlay: []envVar{{"DEBUG", "1"}},
			want:    []string{"DEBUG=1"},
		},
		{
			name:    "value containing equals is not split",
			base:    []string{"OPTS=a=b"},
			overlay: []envVar{{"OPTS", "c=d"}},
			want:    []string{"OPTS=c=d"},
		},
		{
			name:    "nil base",
			base:    nil,
			overlay: []envVar{{"PORT", "3000"}},
			want:    []string{"PORT=3000"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeEnv(tc.base, tc.overlay)
			if !slices.Equal(got, tc.want) {
				t.Errorf("mergeEnv = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMergeEnvDoesNotAliasBase guards against the packager writing into the
// base image's own config slice, which is shared across platforms and across
// builds by the caching base-image resolver.
func TestMergeEnvDoesNotAliasBase(t *testing.T) {
	base := []string{"PATH=/bin", "PORT=1"}
	got := mergeEnv(base, []envVar{{"PORT", "3000"}})
	if base[1] != "PORT=1" {
		t.Errorf("base env was mutated: %q", base)
	}
	if got[1] != "PORT=3000" {
		t.Errorf("merged env = %q", got)
	}
}

// TestPokkumEnvSortsRuntimeEnv is the direct unit-level check on the second map
// W1 flagged. The three contract variables come first in declaration order, and
// everything from RuntimeConfig.Env follows sorted by key.
func TestPokkumEnvSortsRuntimeEnv(t *testing.T) {
	rc := ports.RuntimeConfig{
		Port:            3000,
		ProbePort:       8081,
		ShutdownTimeout: 30 * time.Second,
		Env: map[string]string{
			"ZULU": "z", "ALPHA": "a", "MIKE": "m", "BRAVO": "b", "OSCAR": "o",
		},
	}

	want := []envVar{
		{ports.EnvPort, "3000"},
		{ports.EnvProbePort, "8081"},
		{ports.EnvShutdownTimeout, "30s"},
		{"ALPHA", "a"},
		{"BRAVO", "b"},
		{"MIKE", "m"},
		{"OSCAR", "o"},
		{"ZULU", "z"},
	}
	// Repeated, because a single pass over a five-key map has a one-in-120
	// chance of coming out sorted by luck.
	for i := range 20 {
		if got := pokkumEnv(rc); !slices.Equal(got, want) {
			t.Fatalf("iteration %d: pokkumEnv = %v, want %v", i, got, want)
		}
	}
}

func TestTarEntriesOrderAndDirectories(t *testing.T) {
	files := []layerFile{
		// Deliberately supplied in reverse order, to prove the output order
		// comes from the sort and not from the input.
		{path: ports.SupervisorPath, size: 1, open: bytesOpener([]byte("x"))},
		{path: ports.AppBinaryPath, size: 1, open: bytesOpener([]byte("y"))},
	}
	entries, err := tarEntries(files)
	if err != nil {
		t.Fatalf("tarEntries: %v", err)
	}

	type want struct {
		name     string
		typeflag byte
	}
	expected := []want{
		{"app/", tar.TypeDir},
		{"app/server", tar.TypeReg},
		{"pokkum/", tar.TypeDir},
		{"pokkum/init", tar.TypeReg},
	}
	if len(entries) != len(expected) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(expected), entries)
	}
	for i, e := range expected {
		if entries[i].name != e.name || entries[i].typeflag != e.typeflag {
			t.Errorf("entry %d = %q/%c, want %q/%c", i, entries[i].name, entries[i].typeflag, e.name, e.typeflag)
		}
	}
}

func TestTarEntriesNestedParents(t *testing.T) {
	entries, err := tarEntries([]layerFile{{path: "/a/b/c/file", size: 0, open: bytesOpener(nil)}})
	if err != nil {
		t.Fatalf("tarEntries: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.name)
	}
	want := []string{"a/", "a/b/", "a/b/c/", "a/b/c/file"}
	if !slices.Equal(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}
}

func TestTarEntriesRejectsDuplicates(t *testing.T) {
	_, err := tarEntries([]layerFile{
		{path: "/app/server", open: bytesOpener(nil)},
		{path: "/app/./server", open: bytesOpener(nil)},
	})
	if err == nil {
		t.Fatal("want an error for two files at the same path")
	}
}

func TestTarEntriesRejectsRootPath(t *testing.T) {
	if _, err := tarEntries([]layerFile{{path: "/", open: bytesOpener(nil)}}); err == nil {
		t.Fatal("want an error for a path naming no file")
	}
}

// TestWriteTarDetectsSizeMismatch covers the case where the file changes size
// between the os.Stat that produced the header and the read that fills it: the
// archive must fail loudly rather than ship a layer whose contents do not match
// its own header.
func TestWriteTarDetectsSizeMismatch(t *testing.T) {
	entries := []tarEntry{{
		name:     "app/server",
		typeflag: tar.TypeReg,
		size:     100,
		open:     bytesOpener([]byte("short")),
	}}
	if err := writeTar(io.Discard, entries, buildEpoch); err == nil {
		t.Fatal("want an error when the entry is shorter than its header")
	}
}

func TestPinnedTime(t *testing.T) {
	in := time.Date(2023, 11, 14, 22, 13, 20, 999999999, time.FixedZone("CET", 3600))
	got := pinnedTime(in)
	if got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", got.Location())
	}
	if got.Nanosecond() != 0 {
		t.Errorf("nanoseconds = %d, want 0", got.Nanosecond())
	}
	if want := time.Date(2023, 11, 14, 21, 13, 20, 0, time.UTC); !got.Equal(want) {
		t.Errorf("pinnedTime = %s, want %s", got, want)
	}
}

func TestExposedPortsMergesBase(t *testing.T) {
	base := map[string]struct{}{"1234/tcp": {}}
	got := exposedPorts(base, ports.RuntimeConfig{Port: 3000, ProbePort: 8081, ExposedPorts: []int{9090}})
	for _, want := range []string{"1234/tcp", "3000/tcp", "8081/tcp", "9090/tcp"} {
		if _, ok := got[want]; !ok {
			t.Errorf("exposedPorts = %v, want %s", got, want)
		}
	}
	if _, ok := base["3000/tcp"]; ok {
		t.Error("base exposed-port map was mutated")
	}
}

func TestImageAnnotationsSkipsEmptyLabels(t *testing.T) {
	got := imageAnnotations(map[string]string{
		ports.LabelCreated:  "2023-11-14T22:13:20Z",
		ports.LabelRevision: "",
		"unrelated.label":   "ignored",
	}, "", nil)

	if _, ok := got[ports.LabelRevision]; ok {
		t.Errorf("empty label became an annotation: %v", got)
	}
	if _, ok := got["unrelated.label"]; ok {
		t.Errorf("non-OCI label was mirrored: %v", got)
	}
	if got[ports.LabelCreated] != "2023-11-14T22:13:20Z" {
		t.Errorf("annotations = %v", got)
	}
}

// TestImageAnnotationsBaseRefFallback checks the three-way precedence for
// org.opencontainers.image.base.name: BaseRef is the default, an explicit
// label in Labels overrides it, and an explicit annotation overrides both.
func TestImageAnnotationsBaseRefFallback(t *testing.T) {
	t.Run("baseRef used when no label is set", func(t *testing.T) {
		got := imageAnnotations(nil, "gcr.io/distroless/cc-debian12:nonroot", nil)
		if got[ports.LabelBaseName] != "gcr.io/distroless/cc-debian12:nonroot" {
			t.Errorf("base.name = %q, want the baseRef value", got[ports.LabelBaseName])
		}
	})

	t.Run("explicit label wins over baseRef", func(t *testing.T) {
		labels := map[string]string{ports.LabelBaseName: "from-label:latest"}
		got := imageAnnotations(labels, "from-baseref:latest", nil)
		if got[ports.LabelBaseName] != "from-label:latest" {
			t.Errorf("base.name = %q, want the label value", got[ports.LabelBaseName])
		}
	})

	t.Run("explicit annotation wins over both", func(t *testing.T) {
		labels := map[string]string{ports.LabelBaseName: "from-label:latest"}
		caller := map[string]string{ports.LabelBaseName: "from-annotation:latest"}
		got := imageAnnotations(labels, "from-baseref:latest", caller)
		if got[ports.LabelBaseName] != "from-annotation:latest" {
			t.Errorf("base.name = %q, want the caller annotation value", got[ports.LabelBaseName])
		}
	})

	t.Run("empty baseRef leaves base.name unset", func(t *testing.T) {
		got := imageAnnotations(nil, "", nil)
		if _, ok := got[ports.LabelBaseName]; ok {
			t.Errorf("base.name = %q, want unset", got[ports.LabelBaseName])
		}
	})
}

func TestPokkumEnvWithRequireEnv(t *testing.T) {
	rc := ports.RuntimeConfig{
		Port:            3000,
		ProbePort:       8081,
		ShutdownTimeout: 30 * time.Second,
		RequireEnv:      []string{"DATABASE_URL", "JWT_SECRET"},
	}
	got := pokkumEnv(rc)
	found := false
	for _, env := range got {
		if env.key == ports.EnvRequiredEnv {
			found = true
			if env.value != "DATABASE_URL,JWT_SECRET" {
				t.Errorf("got EnvRequiredEnv value = %q, want DATABASE_URL,JWT_SECRET", env.value)
			}
		}
	}
	if !found {
		t.Errorf("POKKUM_REQUIRED_ENV not found in pokkumEnv output: %v", got)
	}
}

// TestPokkumEnvWithOriginContract is PB-4's regression guard: adapter-node's
// reverse-proxy contract (ORIGIN and friends) previously appeared nowhere in
// the codebase at all. Every one of these must be emitted when set, and
// absent when not — each has its own adapter-node-side default that must be
// allowed to apply rather than Pokkum writing an empty override.
func TestPokkumEnvWithOriginContract(t *testing.T) {
	rc := ports.RuntimeConfig{
		Port:            3000,
		ProbePort:       8081,
		ShutdownTimeout: 30 * time.Second,
		Origin:          "https://example.com",
		ProtocolHeader:  "x-forwarded-proto",
		HostHeader:      "x-forwarded-host",
		AddressHeader:   "x-forwarded-for",
		XFFDepth:        2,
		BodySizeLimit:   "10M",
	}
	got := pokkumEnv(rc)

	want := map[string]string{
		ports.EnvOrigin:         "https://example.com",
		ports.EnvProtocolHeader: "x-forwarded-proto",
		ports.EnvHostHeader:     "x-forwarded-host",
		ports.EnvAddressHeader:  "x-forwarded-for",
		ports.EnvXFFDepth:       "2",
		ports.EnvBodySizeLimit:  "10M",
	}
	gotMap := make(map[string]string, len(got))
	for _, e := range got {
		gotMap[e.key] = e.value
	}
	for k, v := range want {
		if gotMap[k] != v {
			t.Errorf("pokkumEnv()[%s] = %q, want %q (full output: %v)", k, gotMap[k], v, got)
		}
	}
}

// TestPokkumEnvOmitsOriginContractWhenUnset guards the other half: a build
// with none of these set (the common case today, since PB-4 just added the
// fields) must not write empty ORIGIN/PROTOCOL_HEADER/etc into the image —
// that would override adapter-node's own sensible per-variable defaults with
// an empty string, which is worse than not setting them at all.
func TestPokkumEnvOmitsOriginContractWhenUnset(t *testing.T) {
	rc := ports.RuntimeConfig{
		Port:            3000,
		ProbePort:       8081,
		ShutdownTimeout: 30 * time.Second,
	}
	got := pokkumEnv(rc)
	for _, e := range got {
		switch e.key {
		case ports.EnvOrigin, ports.EnvProtocolHeader, ports.EnvHostHeader, ports.EnvAddressHeader, ports.EnvXFFDepth, ports.EnvBodySizeLimit:
			t.Errorf("pokkumEnv() unexpectedly emitted %s=%q with RuntimeConfig fields unset", e.key, e.value)
		}
	}
}

func TestMergeLabelsAndAnnotationsWithRequireEnv(t *testing.T) {
	rc := ports.RuntimeConfig{
		RequireEnv: []string{"DATABASE_URL", "JWT_SECRET"},
	}
	hash := v1.Hash{Algorithm: "sha256", Hex: "1234567890abcdef"}
	labels := mergeLabels(nil, nil, hash, time.Unix(0, 0).UTC(), rc, "", nil)
	if labels[ports.LabelRequiredEnv] != "DATABASE_URL,JWT_SECRET" {
		t.Errorf("got LabelRequiredEnv = %q, want DATABASE_URL,JWT_SECRET", labels[ports.LabelRequiredEnv])
	}

	annotations := imageAnnotations(labels, "", nil)
	if annotations[ports.AnnotationRequiredEnv] != "DATABASE_URL,JWT_SECRET" {
		t.Errorf("got AnnotationRequiredEnv = %q, want DATABASE_URL,JWT_SECRET", annotations[ports.AnnotationRequiredEnv])
	}
}

// TestMergeLabelsAndAnnotationsWithEnvBaked is PB-3's regression guard: an
// image built from a project that imports $env/static/* must carry a
// durable record of which bindings got baked in, mirrored from label to
// manifest annotation exactly like AnnotationRequiredEnv.
func TestMergeLabelsAndAnnotationsWithEnvBaked(t *testing.T) {
	rc := ports.RuntimeConfig{
		EnvBaked: []string{"PUBLIC_API_URL", "DB_PASSWORD"},
	}
	hash := v1.Hash{Algorithm: "sha256", Hex: "1234567890abcdef"}
	labels := mergeLabels(nil, nil, hash, time.Unix(0, 0).UTC(), rc, "", nil)
	if labels[ports.LabelEnvBaked] != "DB_PASSWORD,PUBLIC_API_URL" {
		t.Errorf("got LabelEnvBaked = %q, want DB_PASSWORD,PUBLIC_API_URL (sorted)", labels[ports.LabelEnvBaked])
	}

	annotations := imageAnnotations(labels, "", nil)
	if annotations[ports.AnnotationEnvBaked] != "DB_PASSWORD,PUBLIC_API_URL" {
		t.Errorf("got AnnotationEnvBaked = %q, want DB_PASSWORD,PUBLIC_API_URL", annotations[ports.AnnotationEnvBaked])
	}
}

// TestMergeLabelsAndAnnotationsWithVEXExemptions is PR-6's regression guard:
// an image built with an active --fail-on-cve VEX exemption must carry a
// durable record of which CVEs were exempted, mirrored from label to
// manifest annotation exactly like AnnotationRequiredEnv/AnnotationEnvBaked.
func TestMergeLabelsAndAnnotationsWithVEXExemptions(t *testing.T) {
	rc := ports.RuntimeConfig{
		VEXExemptions: []string{"CVE-2024-99999", "CVE-2024-12345"},
	}
	hash := v1.Hash{Algorithm: "sha256", Hex: "1234567890abcdef"}
	labels := mergeLabels(nil, nil, hash, time.Unix(0, 0).UTC(), rc, "", nil)
	if labels[ports.LabelVEXExemptions] != "CVE-2024-12345,CVE-2024-99999" {
		t.Errorf("got LabelVEXExemptions = %q, want CVE-2024-12345,CVE-2024-99999 (sorted)", labels[ports.LabelVEXExemptions])
	}

	annotations := imageAnnotations(labels, "", nil)
	if annotations[ports.AnnotationVEXExemptions] != "CVE-2024-12345,CVE-2024-99999" {
		t.Errorf("got AnnotationVEXExemptions = %q, want CVE-2024-12345,CVE-2024-99999", annotations[ports.AnnotationVEXExemptions])
	}
}

// TestMergeLabelsOmitsEnvBakedWhenUnset confirms an ordinary build with no
// $env/static/* imports never gets a fabricated/empty annotation — absence
// of the key, not an empty-string value, is how "not detected" is spelled.
func TestMergeLabelsOmitsEnvBakedWhenUnset(t *testing.T) {
	rc := ports.RuntimeConfig{}
	hash := v1.Hash{Algorithm: "sha256", Hex: "1234567890abcdef"}
	labels := mergeLabels(nil, nil, hash, time.Unix(0, 0).UTC(), rc, "", nil)
	if _, ok := labels[ports.LabelEnvBaked]; ok {
		t.Errorf("expected no LabelEnvBaked when EnvBaked is unset, got %q", labels[ports.LabelEnvBaked])
	}

	annotations := imageAnnotations(labels, "", nil)
	if _, ok := annotations[ports.AnnotationEnvBaked]; ok {
		t.Errorf("expected no AnnotationEnvBaked when EnvBaked is unset, got %q", annotations[ports.AnnotationEnvBaked])
	}
}
