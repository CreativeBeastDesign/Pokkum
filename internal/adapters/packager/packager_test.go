package packager

import (
	"archive/tar"
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func buildOne(t *testing.T, mutateReq func(*ports.PackageRequest)) v1.Image {
	t.Helper()
	req := newRequest(t, ports.LinuxAMD64)
	if mutateReq != nil {
		mutateReq(&req)
	}
	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return img
}

func configOf(t *testing.T, img v1.Image) *v1.ConfigFile {
	t.Helper()
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	return cfg
}

// TestImageConfigRuntimeContract asserts the runtime contract declared in
// ports/packager.go actually reaches the image config.
func TestImageConfigRuntimeContract(t *testing.T) {
	cfg := configOf(t, buildOne(t, nil))

	wantEntrypoint := []string{ports.SupervisorPath, "--", ports.AppBinaryPath}
	if !slices.Equal(cfg.Config.Entrypoint, wantEntrypoint) {
		t.Errorf("entrypoint = %q, want %q", cfg.Config.Entrypoint, wantEntrypoint)
	}
	if len(cfg.Config.Cmd) != 0 {
		t.Errorf("cmd = %q, want empty", cfg.Config.Cmd)
	}
	if cfg.Config.User != ports.DefaultUser {
		t.Errorf("user = %q, want %q", cfg.Config.User, ports.DefaultUser)
	}
	if cfg.Config.WorkingDir != ports.WorkingDir {
		t.Errorf("workingDir = %q, want %q", cfg.Config.WorkingDir, ports.WorkingDir)
	}
	if cfg.Config.Hostname != "" {
		t.Errorf("hostname = %q, want empty", cfg.Config.Hostname)
	}
	if got, want := cfg.Created.Time.UTC(), buildEpoch; !got.Equal(want) {
		t.Errorf("created = %s, want %s", got, want)
	}
	if cfg.OS != ports.LinuxAMD64.OS || cfg.Architecture != ports.LinuxAMD64.Arch {
		t.Errorf("platform = %s/%s, want %s", cfg.OS, cfg.Architecture, ports.LinuxAMD64)
	}
}

// TestImageConfigEnvMergePreservesBase is the regression guard for the mistake
// that produces an image which cannot start: replacing the base environment
// instead of merging over it.
func TestImageConfigEnvMergePreservesBase(t *testing.T) {
	cfg := configOf(t, buildOne(t, nil))
	env := cfg.Config.Env

	if !slices.Contains(env, basePath) {
		t.Errorf("base PATH missing from env %q", env)
	}
	if !slices.Contains(env, baseCertFile) {
		t.Errorf("base SSL_CERT_FILE missing from env %q", env)
	}
	// The base's entries keep their original positions; Pokkum's are appended.
	if env[0] != basePath || env[1] != baseCertFile {
		t.Errorf("base env was reordered: %q", env)
	}

	want := []string{
		ports.EnvPort + "=3000",
		ports.EnvProbePort + "=8081",
		ports.EnvShutdownTimeout + "=30s",
	}
	for _, w := range want {
		if !slices.Contains(env, w) {
			t.Errorf("env %q missing from %q", w, env)
		}
	}
}

// TestImageConfigEnvOverrides checks that RuntimeConfig.Env wins over both the
// base image and Pokkum's own defaults, and that an override replaces the entry
// in place rather than appending a duplicate — two entries for the same key is
// a coin flip at runtime.
func TestImageConfigEnvOverrides(t *testing.T) {
	cfg := configOf(t, buildOne(t, func(r *ports.PackageRequest) {
		r.Runtime.Port = 8080
		r.Runtime.Env = map[string]string{
			ports.EnvPort: "9999",
			"PATH":        "/only/this",
			"EXTRA":       "value",
		}
	}))
	env := cfg.Config.Env

	counts := map[string]int{}
	for _, e := range env {
		for i := range e {
			if e[i] == '=' {
				counts[e[:i]]++
				break
			}
		}
	}
	for k, n := range counts {
		if n != 1 {
			t.Errorf("env key %q appears %d times in %q", k, n, env)
		}
	}

	if !slices.Contains(env, ports.EnvPort+"=9999") {
		t.Errorf("RuntimeConfig.Env did not override PORT: %q", env)
	}
	if !slices.Contains(env, "PATH=/only/this") {
		t.Errorf("RuntimeConfig.Env did not override the base PATH: %q", env)
	}
	if !slices.Contains(env, "EXTRA=value") {
		t.Errorf("RuntimeConfig.Env entry missing: %q", env)
	}
	// The exposed port still follows RuntimeConfig.Port, not the Env override:
	// Env is a string the app reads, ExposedPorts is metadata the packager owns.
	if _, ok := cfg.Config.ExposedPorts["8080/tcp"]; !ok {
		t.Errorf("exposed ports = %v, want 8080/tcp", cfg.Config.ExposedPorts)
	}
}

// TestImageConfigExposedPorts checks both contract ports are declared, plus any
// extras, plus whatever the base already had.
func TestImageConfigExposedPorts(t *testing.T) {
	cfg := configOf(t, buildOne(t, func(r *ports.PackageRequest) {
		r.Runtime.ExposedPorts = []int{9090}
	}))

	for _, want := range []string{"3000/tcp", "8081/tcp", "9090/tcp"} {
		if _, ok := cfg.Config.ExposedPorts[want]; !ok {
			t.Errorf("exposed ports = %v, want %s", cfg.Config.ExposedPorts, want)
		}
	}
}

// TestImageConfigLabels checks the overlay order stated on PackageRequest.Labels:
// base first, Pokkum's own next, the caller's last.
func TestImageConfigLabels(t *testing.T) {
	img := buildOne(t, func(r *ports.PackageRequest) {
		r.Labels["custom"] = "yes"
		r.Labels[ports.LabelCreated] = "overridden-by-caller"
	})
	cfg := configOf(t, img)
	labels := cfg.Config.Labels

	if labels["base.only.label"] != "preserved" {
		t.Errorf("base label dropped: %v", labels)
	}
	if labels["custom"] != "yes" {
		t.Errorf("caller label missing: %v", labels)
	}
	if labels[ports.LabelCreated] != "overridden-by-caller" {
		t.Errorf("caller could not override a pokkum label: %v", labels)
	}

	baseDigest, err := syntheticBase(t, ports.LinuxAMD64).Digest()
	if err != nil {
		t.Fatalf("base digest: %v", err)
	}
	if labels[ports.LabelBaseDigest] != baseDigest.String() {
		t.Errorf("base digest label = %q, want %q", labels[ports.LabelBaseDigest], baseDigest)
	}
}

// TestImageConfigHistory checks that the appended history entry is pinned to the
// build timestamp and carries no per-run text, and that the base's own history
// is preserved ahead of it.
func TestImageConfigHistory(t *testing.T) {
	cfg := configOf(t, buildOne(t, nil))

	if len(cfg.History) != 2 {
		t.Fatalf("history = %+v, want base entry plus one pokkum entry", cfg.History)
	}
	if cfg.History[0].CreatedBy != "synthetic base" {
		t.Errorf("base history entry lost: %+v", cfg.History[0])
	}
	got := cfg.History[1]
	if !got.Created.Time.UTC().Equal(buildEpoch) {
		t.Errorf("history created = %s, want %s", got.Created.Time, buildEpoch)
	}
	if got.CreatedBy != historyCreatedBy {
		t.Errorf("history createdBy = %q, want %q", got.CreatedBy, historyCreatedBy)
	}
	if got.EmptyLayer {
		t.Error("history entry marked as an empty layer, but it has a layer")
	}
	if len(cfg.RootFS.DiffIDs) != 2 {
		t.Errorf("diffIDs = %v, want base layer plus app layer", cfg.RootFS.DiffIDs)
	}
}

// TestLayerContents asserts the exact shape of the layer this package adds:
// four entries, two directories and two files, with every varying header field
// pinned and no PAX extended records anywhere.
func TestLayerContents(t *testing.T) {
	img := buildOne(t, nil)
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	if len(layers) != 2 {
		t.Fatalf("got %d layers, want base layer plus exactly one pokkum layer", len(layers))
	}
	got := readLayer(t, layers[1])

	want := []tarMember{
		{Name: "app/", Typeflag: tar.TypeDir, Mode: 0o755, UID: 65532, GID: 65532, ModTime: buildEpoch, Size: 0, Format: tar.FormatUSTAR},
		{Name: "app/server", Typeflag: tar.TypeReg, Mode: 0o755, UID: 65532, GID: 65532, ModTime: buildEpoch, Size: int64(len(fakeAppBytes)), Format: tar.FormatUSTAR},
		{Name: "pokkum/", Typeflag: tar.TypeDir, Mode: 0o755, UID: 65532, GID: 65532, ModTime: buildEpoch, Size: 0, Format: tar.FormatUSTAR},
		{Name: "pokkum/init", Typeflag: tar.TypeReg, Mode: 0o755, UID: 65532, GID: 65532, ModTime: buildEpoch, Size: int64(len(fakeSupervisorBytes)), Format: tar.FormatUSTAR},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("layer members:\n got: %+v\nwant: %+v", got, want)
	}

	// Uname/Gname empty and no extended records are asserted above via
	// DeepEqual on the zero values, but they are the two fields most likely to
	// be reintroduced accidentally by someone reaching for
	// tar.FileInfoHeader, so they get their own message.
	for _, m := range got {
		if m.Uname != "" || m.Gname != "" {
			t.Errorf("entry %q carries uname=%q gname=%q; a build-host user database is leaking into the layer", m.Name, m.Uname, m.Gname)
		}
		if m.PAXCount != 0 {
			t.Errorf("entry %q carries %d PAX records; extended headers must not be emitted", m.Name, m.PAXCount)
		}
	}
}

// TestLayerModTimeIsTruncated checks that a sub-second CreatedAt does not force
// archive/tar into emitting a PAX mtime record, which would both bloat and
// de-stabilise the layer.
func TestLayerModTimeIsTruncated(t *testing.T) {
	img := buildOne(t, func(r *ports.PackageRequest) {
		r.CreatedAt = buildEpoch.Add(123456789 * time.Nanosecond)
	})
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	for _, m := range readLayer(t, layers[1]) {
		if !m.ModTime.Equal(buildEpoch) {
			t.Errorf("entry %q modTime = %s, want %s", m.Name, m.ModTime, buildEpoch)
		}
		if m.Format != tar.FormatUSTAR || m.PAXCount != 0 {
			t.Errorf("entry %q became %v with %d PAX records", m.Name, m.Format, m.PAXCount)
		}
	}
}

// TestMediaTypesAreOCI guards the reason for choosing OCI in the first place:
// a Docker schema 2 manifest has no annotations field, so emitting one would
// silently discard everything the annotations test below asserts.
func TestMediaTypesAreOCI(t *testing.T) {
	img := buildOne(t, nil)

	mt, err := img.MediaType()
	if err != nil {
		t.Fatalf("media type: %v", err)
	}
	if mt != types.OCIManifestSchema1 {
		t.Errorf("manifest media type = %s, want %s", mt, types.OCIManifestSchema1)
	}

	m, err := img.Manifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.MediaType != types.OCIManifestSchema1 {
		t.Errorf("manifest.mediaType = %s, want %s", m.MediaType, types.OCIManifestSchema1)
	}
	if m.Config.MediaType != types.OCIConfigJSON {
		t.Errorf("config media type = %s, want %s", m.Config.MediaType, types.OCIConfigJSON)
	}
	if got := m.Layers[len(m.Layers)-1].MediaType; got != types.OCILayer {
		t.Errorf("application layer media type = %s, want %s", got, types.OCILayer)
	}
}

// TestImageAnnotations checks the standard org.opencontainers.image.* set,
// including the base digest read off the resolved base image itself.
func TestImageAnnotations(t *testing.T) {
	img := buildOne(t, func(r *ports.PackageRequest) {
		r.Annotations = map[string]string{"custom.annotation": "yes"}
	})
	m, err := img.Manifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	anns := m.Annotations

	baseDigest, err := syntheticBase(t, ports.LinuxAMD64).Digest()
	if err != nil {
		t.Fatalf("base digest: %v", err)
	}

	want := map[string]string{
		ports.LabelCreated:    buildEpoch.Format(time.RFC3339),
		ports.LabelRevision:   "0123456789abcdef0123456789abcdef01234567",
		ports.LabelSource:     "https://github.com/example/app",
		ports.LabelVersion:    "1.2.3",
		ports.LabelBaseName:   "gcr.io/distroless/cc-debian12:nonroot",
		ports.LabelBaseDigest: baseDigest.String(),
		"custom.annotation":   "yes",
	}
	for k, v := range want {
		if anns[k] != v {
			t.Errorf("annotation %q = %q, want %q", k, anns[k], v)
		}
	}
}

// TestBuildDoesNotMutateBase checks that packaging one platform leaves the base
// image usable, which core relies on when it packages platforms concurrently
// from a shared, cached resolver.
func TestBuildDoesNotMutateBase(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	before, err := req.Base.Digest()
	if err != nil {
		t.Fatalf("base digest: %v", err)
	}
	if _, err := NewPackager(testLogger()).Build(context.Background(), req); err != nil {
		t.Fatalf("build: %v", err)
	}
	after, err := req.Base.Digest()
	if err != nil {
		t.Fatalf("base digest after: %v", err)
	}
	if before != after {
		t.Errorf("base image digest changed from %s to %s", before, after)
	}
}

func TestBuildValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ports.PackageRequest)
		wantErr error
	}{
		{
			name:    "unsupported platform",
			mutate:  func(r *ports.PackageRequest) { r.Platform = ports.Platform{OS: "windows", Arch: "amd64"} },
			wantErr: core.ErrUnsupportedPlatform,
		},
		{
			name:    "zero platform",
			mutate:  func(r *ports.PackageRequest) { r.Platform = ports.Platform{} },
			wantErr: core.ErrUnsupportedPlatform,
		},
		{
			name:    "nil base",
			mutate:  func(r *ports.PackageRequest) { r.Base = nil },
			wantErr: core.ErrPackageFailed,
		},
		{
			name:    "no app path",
			mutate:  func(r *ports.PackageRequest) { r.App.Path = "" },
			wantErr: core.ErrPackageFailed,
		},
		{
			name:    "app for another platform",
			mutate:  func(r *ports.PackageRequest) { r.App.Platform = ports.LinuxARM64 },
			wantErr: core.ErrPackageFailed,
		},
		{
			name:    "missing app file",
			mutate:  func(r *ports.PackageRequest) { r.App.Path = r.App.Path + ".missing" },
			wantErr: core.ErrPackageFailed,
		},
		{
			name:    "empty supervisor",
			mutate:  func(r *ports.PackageRequest) { r.Supervisor = nil },
			wantErr: core.ErrSupervisorUnavailable,
		},
		{
			name:    "zero timestamp",
			mutate:  func(r *ports.PackageRequest) { r.CreatedAt = time.Time{} },
			wantErr: core.ErrPackageFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(t, ports.LinuxAMD64)
			tc.mutate(&req)
			_, err := NewPackager(testLogger()).Build(context.Background(), req)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestBuildHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewPackager(testLogger()).Build(ctx, newRequest(t, ports.LinuxAMD64))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestZeroValuePackagerIsUsable covers the defensive logger fallback: a
// zero-value Packager must not panic on its first log line.
func TestZeroValuePackagerIsUsable(t *testing.T) {
	var p Packager
	if _, err := p.Build(context.Background(), newRequest(t, ports.LinuxAMD64)); err != nil {
		t.Fatalf("build: %v", err)
	}
}
