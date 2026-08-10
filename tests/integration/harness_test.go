package integration

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestMain(m *testing.M) {
	// Point DOCKER_CONFIG at a temporary directory to isolate tests from host credentials
	// and prevent credential helper hangs when Docker Desktop is not running.
	dir, err := os.MkdirTemp("", "pokkum-integration-dockerconfig")
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: TestMain MkdirTemp: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	os.Setenv("DOCKER_CONFIG", dir)

	os.Exit(m.Run())
}

// RecordedRequest tracks an HTTP request observed by the test registry.
type RecordedRequest struct {
	Method string
	Path   string
}

type countingRegistry struct {
	inner http.Handler
	mu    sync.Mutex
	reqs  []RecordedRequest
}

func (c *countingRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.reqs = append(c.reqs, RecordedRequest{Method: r.Method, Path: r.URL.Path})
	c.mu.Unlock()
	c.inner.ServeHTTP(w, r)
}

func (c *countingRegistry) requests() []RecordedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]RecordedRequest, len(c.reqs))
	copy(out, c.reqs)
	return out
}

func (c *countingRegistry) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

// RegistryHarness wraps an in-memory ggcr registry behind httptest.Server.
type RegistryHarness struct {
	Server   *httptest.Server
	counting *countingRegistry
}

// NewRegistryHarness spins up a new in-memory OCI registry.
func NewRegistryHarness(t *testing.T) *RegistryHarness {
	t.Helper()
	cr := &countingRegistry{inner: registry.New()}
	s := httptest.NewServer(cr)
	t.Cleanup(s.Close)

	return &RegistryHarness{
		Server:   s,
		counting: cr,
	}
}

// URL returns the full base HTTP URL of the test registry, e.g. "http://127.0.0.1:12345".
func (h *RegistryHarness) URL() string {
	return h.Server.URL
}

// Address returns the host:port string of the registry without "http://".
func (h *RegistryHarness) Address() string {
	return strings.TrimPrefix(h.Server.URL, "http://")
}

// Repo constructs a repository reference string for this test registry, e.g. "127.0.0.1:12345/my-repo".
func (h *RegistryHarness) Repo(name string) string {
	return h.Address() + "/" + name
}

// RecordedRequests returns a copy of all HTTP requests received by the test registry.
func (h *RegistryHarness) RecordedRequests() []RecordedRequest {
	return h.counting.requests()
}

// RequestCount returns the total number of HTTP requests processed by the registry.
func (h *RegistryHarness) RequestCount() int {
	return h.counting.count()
}

// FetchImage retrieves an image from the test registry given a reference string.
func (h *RegistryHarness) FetchImage(t *testing.T, refStr string) v1.Image {
	t.Helper()
	ref, err := name.ParseReference(refStr, name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", refStr, err)
	}
	img, err := remote.Image(ref)
	if err != nil {
		t.Fatalf("remote.Image(%q): %v", refStr, err)
	}
	return img
}

// FetchIndex retrieves an image index from the test registry given a reference string.
func (h *RegistryHarness) FetchIndex(t *testing.T, refStr string) v1.ImageIndex {
	t.Helper()
	ref, err := name.ParseReference(refStr, name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", refStr, err)
	}
	idx, err := remote.Index(ref)
	if err != nil {
		t.Fatalf("remote.Index(%q): %v", refStr, err)
	}
	return idx
}

// FetchManifest retrieves an OCI manifest from an image ref in the test registry.
func (h *RegistryHarness) FetchManifest(t *testing.T, refStr string) (*v1.Manifest, []byte) {
	t.Helper()
	img := h.FetchImage(t, refStr)
	m, err := img.Manifest()
	if err != nil {
		t.Fatalf("img.Manifest(): %v", err)
	}
	raw, err := img.RawManifest()
	if err != nil {
		t.Fatalf("img.RawManifest(): %v", err)
	}
	return m, raw
}

// FetchConfigFile retrieves the raw and parsed OCI ConfigFile from an image.
func (h *RegistryHarness) FetchConfigFile(t *testing.T, img v1.Image) (*v1.ConfigFile, []byte) {
	t.Helper()
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("img.ConfigFile(): %v", err)
	}
	raw, err := img.RawConfigFile()
	if err != nil {
		t.Fatalf("img.RawConfigFile(): %v", err)
	}
	return cfg, raw
}

// TarMember represents an entry in an uncompressed layer tarball.
type TarMember struct {
	Name     string
	Typeflag byte
	Mode     int64
	UID      int
	GID      int
	Size     int64
	ModTime  time.Time
}

// FetchLayerMembers extracts and lists all files inside a layer.
func (h *RegistryHarness) FetchLayerMembers(t *testing.T, layer v1.Layer) []TarMember {
	t.Helper()
	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatalf("layer.Uncompressed(): %v", err)
	}
	defer rc.Close()

	var members []TarMember
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next(): %v", err)
		}
		members = append(members, TarMember{
			Name:     hdr.Name,
			Typeflag: hdr.Typeflag,
			Mode:     hdr.Mode,
			UID:      hdr.Uid,
			GID:      hdr.Gid,
			Size:     hdr.Size,
			ModTime:  hdr.ModTime.UTC(),
		})
		if _, err := io.Copy(io.Discard, tr); err != nil {
			t.Fatalf("drain tar entry %q: %v", hdr.Name, err)
		}
	}
	return members
}

// FetchAttachedSBOM retrieves the attached SBOM image for a given published digest.
func (h *RegistryHarness) FetchAttachedSBOM(t *testing.T, repo string, subjectDigest v1.Hash) (v1.Image, []byte) {
	t.Helper()
	// Pokkum attaches SBOM with tag: sha256-<hash>.sbom
	tagStr := fmt.Sprintf("%s:sha256-%s.sbom", repo, subjectDigest.Hex)
	img := h.FetchImage(t, tagStr)
	layers, err := img.Layers()
	if err != nil || len(layers) == 0 {
		t.Fatalf("SBOM image has no layers: %v", err)
	}
	rc, err := layers[0].Uncompressed()
	if err != nil {
		t.Fatalf("SBOM layer uncompressed: %v", err)
	}
	defer rc.Close()

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, rc); err != nil {
		t.Fatalf("SBOM layer copy: %v", err)
	}
	return img, buf.Bytes()
}

// Fixed test fixtures for synthetic builds
var (
	testEpoch = time.Unix(1700000000, 0).UTC()
	baseEpoch = time.Unix(1600000000, 0).UTC()

	fakeAppContent        = bytes.Repeat([]byte("bun-compiled-app-binary\n"), 100)
	fakeSupervisorContent = bytes.Repeat([]byte("pokkum-init-supervisor\n"), 50)
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeTempBinary(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("writeTempBinary %s: %v", p, err)
	}
	return p
}

// SyntheticBaseImage constructs a deterministic base image without network calls.
func SyntheticBaseImage(t *testing.T, plat ports.Platform) v1.Image {
	t.Helper()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		content := []byte("ca-certificates\n")
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
		t.Fatalf("LayerFromOpener: %v", err)
	}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("AppendLayers: %v", err)
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	cfg = cfg.DeepCopy()
	cfg.Created = v1.Time{Time: baseEpoch}
	cfg.OS = plat.OS
	cfg.Architecture = plat.Arch
	cfg.Variant = plat.Variant
	cfg.Config.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
	}
	cfg.Config.User = "65532:65532"
	cfg.Config.WorkingDir = "/"

	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatalf("mutate.ConfigFile: %v", err)
	}
	return img
}

// NewTestPackageRequest returns a deterministic PackageRequest.
func NewTestPackageRequest(t *testing.T, plat ports.Platform) ports.PackageRequest {
	t.Helper()
	return ports.PackageRequest{
		Platform: plat,
		Base:     SyntheticBaseImage(t, plat),
		App: ports.Artifact{
			Platform: plat,
			Path:     writeTempBinary(t, "server", fakeAppContent),
			Size:     int64(len(fakeAppContent)),
		},
		Supervisor: append([]byte(nil), fakeSupervisorContent...),
		CreatedAt:  testEpoch,
		Labels: map[string]string{
			ports.LabelRevision: "1234567890abcdef1234567890abcdef12345678",
			ports.LabelSource:   "https://github.com/example/test-app",
			ports.LabelVersion:  "1.0.0",
			ports.LabelBaseName: "gcr.io/distroless/cc-debian12:nonroot",
		},
	}
}
