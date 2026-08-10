package packager

import (
	"archive/tar"
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestMain points DOCKER_CONFIG at an empty directory for the whole package.
//
// Nothing in this package reads a registry credential — pushing is W11's
// problem — but go-containerregistry's default keychain is initialised lazily
// and reachable from a surprising number of call paths. On a machine whose
// ~/.docker/config.json names credsStore "desktop" while Docker Desktop is not
// running, resolving it blocks forever rather than failing, which turns a unit
// test suite into a hang with no output. An empty DOCKER_CONFIG makes that
// impossible regardless of what the developer's machine looks like.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pokkum-packager-docker-config")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("DOCKER_CONFIG", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// The fixed inputs every test builds from. They are constants so that the
// digests in TestGoldenDigests mean something: change one of these and the
// golden values must change with it.
var (
	// buildEpoch is the SOURCE_DATE_EPOCH-derived timestamp under test:
	// 2023-11-14T22:13:20Z. Deliberately not the Unix epoch, so that a code
	// path that dropped the timestamp entirely and fell back to a zero time
	// would show up as a difference rather than coincidentally match.
	buildEpoch = time.Unix(1700000000, 0).UTC()

	// baseEpoch is the synthetic base image's own timestamp, distinct from
	// buildEpoch so that tests can tell "inherited from the base" apart from
	// "stamped by the packager".
	baseEpoch = time.Unix(1600000000, 0).UTC()

	// basePath and baseCertFile are the base image's environment. The packager
	// must preserve both; discarding PATH or SSL_CERT_FILE is the classic way
	// to produce a distroless image that cannot start or cannot do TLS.
	basePath     = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	baseCertFile = "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt"
)

// fakeAppBytes and fakeSupervisorBytes stand in for the ~90 MB Bun executable
// and the 2.2 MB supervisor. A few kilobytes each keeps the suite fast while
// exercising every byte of the layer-assembly path; nothing here depends on
// size beyond the header field.
var (
	fakeAppBytes        = bytes.Repeat([]byte("bun-compiled-server\n"), 200)
	fakeSupervisorBytes = bytes.Repeat([]byte("pokkum-init\n"), 100)
)

// testLogger discards output so that a passing run is quiet, while still
// exercising the logging call paths.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeBinary writes content into a fresh temp file and returns its path. Each
// call uses a new directory, which is what lets the reproducibility tests prove
// the host path never reaches the layer.
func writeBinary(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// syntheticBase builds a fully deterministic stand-in for a distroless base:
// one small layer, a pinned config, a pinned history entry, and the environment
// and labels a real base would carry. It never touches the network.
func syntheticBase(t *testing.T, plat ports.Platform) v1.Image {
	t.Helper()

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		content := []byte("synthetic ca bundle\n")
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "etc/ssl/certs/ca-certificates.crt",
			Mode:     0o644,
			Size:     int64(len(content)),
			ModTime:  baseEpoch,
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(content); err != nil {
			return nil, err
		}
		if err := tw.Close(); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
	})
	if err != nil {
		t.Fatalf("build synthetic base layer: %v", err)
	}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("append synthetic base layer: %v", err)
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("read synthetic base config: %v", err)
	}
	cfg = cfg.DeepCopy()
	cfg.Created = v1.Time{Time: baseEpoch}
	cfg.OS = plat.OS
	cfg.Architecture = plat.Arch
	cfg.Variant = plat.Variant
	cfg.Config.Env = []string{basePath, baseCertFile}
	cfg.Config.User = "0"
	cfg.Config.Entrypoint = []string{"/bin/sh"}
	cfg.Config.WorkingDir = "/"
	cfg.Config.Labels = map[string]string{"base.only.label": "preserved"}
	cfg.History = []v1.History{{
		Created:   v1.Time{Time: baseEpoch},
		CreatedBy: "synthetic base",
	}}

	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatalf("apply synthetic base config: %v", err)
	}
	return img
}

// newRequest assembles a PackageRequest with fresh temp files, so that two
// requests built from it differ in every host-specific way that could possibly
// leak into the image.
func newRequest(t *testing.T, plat ports.Platform) ports.PackageRequest {
	t.Helper()
	return ports.PackageRequest{
		Platform: plat,
		Base:     syntheticBase(t, plat),
		App: ports.Artifact{
			Platform: plat,
			Path:     writeBinary(t, "server", fakeAppBytes),
			Size:     int64(len(fakeAppBytes)),
		},
		Supervisor: append([]byte(nil), fakeSupervisorBytes...),
		CreatedAt:  buildEpoch,
		Labels: map[string]string{
			ports.LabelRevision: "0123456789abcdef0123456789abcdef01234567",
			ports.LabelSource:   "https://github.com/example/app",
			ports.LabelVersion:  "1.2.3",
			ports.LabelBaseName: "gcr.io/distroless/cc-debian12:nonroot",
		},
	}
}

// tarMember is one decoded archive entry, for assertions about the layer.
type tarMember struct {
	Name     string
	Typeflag byte
	Mode     int64
	UID      int
	GID      int
	Uname    string
	Gname    string
	ModTime  time.Time
	Size     int64
	Format   tar.Format
	PAXCount int
}

// readLayer decodes a layer's uncompressed tar stream into its members.
func readLayer(t *testing.T, layer v1.Layer) []tarMember {
	t.Helper()
	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatalf("uncompressed: %v", err)
	}
	defer rc.Close() //nolint:errcheck // test cleanup

	var out []tarMember
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		out = append(out, tarMember{
			Name:     hdr.Name,
			Typeflag: hdr.Typeflag,
			Mode:     hdr.Mode,
			UID:      hdr.Uid,
			GID:      hdr.Gid,
			Uname:    hdr.Uname,
			Gname:    hdr.Gname,
			ModTime:  hdr.ModTime.UTC(),
			Size:     hdr.Size,
			Format:   hdr.Format,
			PAXCount: len(hdr.PAXRecords),
		})
		if _, err := io.Copy(io.Discard, tr); err != nil {
			t.Fatalf("drain tar entry %q: %v", hdr.Name, err)
		}
	}
	return out
}

// imageFingerprint is everything about a built image that must not vary between
// two runs on identical inputs.
type imageFingerprint struct {
	Manifest       string
	Config         string
	ConfigSize     int64
	LayerDigests   []string
	LayerDiffIDs   []string
	LayerMediaType []string
	RawManifest    string
	RawConfig      string
}

func fingerprint(t *testing.T, img v1.Image) imageFingerprint {
	t.Helper()

	d, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	rawManifest, err := img.RawManifest()
	if err != nil {
		t.Fatalf("raw manifest: %v", err)
	}
	rawConfig, err := img.RawConfigFile()
	if err != nil {
		t.Fatalf("raw config: %v", err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}

	fp := imageFingerprint{
		Manifest:    d.String(),
		Config:      m.Config.Digest.String(),
		ConfigSize:  m.Config.Size,
		RawManifest: string(rawManifest),
		RawConfig:   string(rawConfig),
	}
	for _, l := range layers {
		ld, err := l.Digest()
		if err != nil {
			t.Fatalf("layer digest: %v", err)
		}
		diffID, err := l.DiffID()
		if err != nil {
			t.Fatalf("layer diffID: %v", err)
		}
		mt, err := l.MediaType()
		if err != nil {
			t.Fatalf("layer media type: %v", err)
		}
		fp.LayerDigests = append(fp.LayerDigests, ld.String())
		fp.LayerDiffIDs = append(fp.LayerDiffIDs, diffID.String())
		fp.LayerMediaType = append(fp.LayerMediaType, string(mt))
	}
	return fp
}
