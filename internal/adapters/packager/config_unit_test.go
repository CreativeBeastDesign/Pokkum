package packager

import (
	"archive/tar"
	"io"
	"slices"
	"testing"
	"time"

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
	}, nil)

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
