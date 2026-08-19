package integration

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	pokkumregistry "github.com/CreativeBeastDesign/pokkum/pkg/registry"
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

// RegistryHarness wraps an in-memory ggcr registry behind httptest.Server using pkg/registry.
type RegistryHarness struct {
	Server   *pokkumregistry.Server
	counting *countingRegistry
}

// NewRegistryHarness spins up a new in-memory OCI registry using pkg/registry.NewServer.
func NewRegistryHarness(t *testing.T) *RegistryHarness {
	t.Helper()
	cr := &countingRegistry{}
	srv, err := pokkumregistry.NewServer(pokkumregistry.WithHandlerWrapper(func(h http.Handler) http.Handler {
		cr.inner = h
		return cr
	}))
	if err != nil {
		t.Fatalf("failed to start ephemeral registry: %v", err)
	}
	t.Cleanup(srv.Close)

	return &RegistryHarness{
		Server:   srv,
		counting: cr,
	}
}

// URL returns the full base HTTP URL of the test registry, e.g. "http://127.0.0.1:12345".
func (h *RegistryHarness) URL() string {
	return h.Server.URL
}

// Address returns the host:port string of the registry without "http://".
func (h *RegistryHarness) Address() string {
	return h.Server.Host()
}

// Repo constructs a repository reference string for this test registry, e.g. "127.0.0.1:12345/my-repo".
func (h *RegistryHarness) Repo(name string) string {
	return h.Server.Repo(name)
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

// ExtractLayerFiles extracts the real, uncompressed file contents of an OCI
// layer to destDir on disk. Unlike FetchLayerMembers (which only reports tar
// header metadata — name/mode/size/modtime — and discards the payload), this
// writes each regular file's actual bytes to disk so callers can point real
// file-serving logic (e.g. supervisor/cmd/pokkum-static's handler) at the
// extracted tree and exercise it with real Range/ETag/Content-Encoding
// requests against real files (including .gz/.br/.zst sidecars).
//
// It reads layer.Uncompressed(), not layer.Compressed(): callers need actual
// file bytes, not the gzip'd layer blob.
//
// Path handling: tar entry names are preserved verbatim under destDir rather
// than stripping a known OCI prefix such as "app/client" or
// "app/prerendered". E.g. a tar entry "app/client/index.html" is written to
// destDir/app/client/index.html. This is deliberately the simpler of the two
// options: it keeps the helper generic for any layer (it doesn't need to
// know which prefixes a given layer was packaged with), and a caller that
// does know — e.g. via ports.AppClientDirPrefix / ports.AppPrerenderedDirPrefix
// — can just filepath.Join(destDir, "app/client") to get the root to hand to
// a file server.
//
// Only regular files (tar.TypeReg) are written; directories, symlinks and
// other entry types are skipped (directories are implicitly created via
// os.MkdirAll as needed). File mode is fixed at 0644 — this is a test
// fixture extractor, not a general-purpose tar extractor, so exact mode bits
// from the tar header are not preserved.
//
// Every entry is validated against path traversal before it is written:
// after filepath.Clean, an entry must not be absolute and must not resolve
// outside destDir. This mirrors (in miniature) the cleanRelPath/withinRoot
// pattern in supervisor/cmd/pokkum-static/server.go; it is duplicated here
// rather than imported because tests/integration is a separate package and
// the check is only a few lines — not worth extracting into a shared
// utility for a trusted, locally-built layer.
//
// Returns an error rather than calling t.Fatal, matching packager-style
// helpers elsewhere in this repo; callers in tests should wrap it with
// t.Fatalf themselves.
func ExtractLayerFiles(layer v1.Layer, destDir string) error {
	rc, err := layer.Uncompressed()
	if err != nil {
		return fmt.Errorf("ExtractLayerFiles: layer.Uncompressed(): %w", err)
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("ExtractLayerFiles: tar.Next(): %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		destPath, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return fmt.Errorf("ExtractLayerFiles: tar entry %q: %w", hdr.Name, err)
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("ExtractLayerFiles: mkdir for %q: %w", hdr.Name, err)
		}

		f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("ExtractLayerFiles: create %q: %w", destPath, err)
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return fmt.Errorf("ExtractLayerFiles: write %q: %w", destPath, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("ExtractLayerFiles: close %q: %w", destPath, err)
		}
	}
	return nil
}

// safeJoin joins a tar entry name onto destDir, rejecting any entry that is
// absolute or whose cleaned path would escape destDir (e.g. via "..").
// See the path-traversal note on ExtractLayerFiles's doc comment.
func safeJoin(destDir, tarName string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(tarName))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal rejected: %q", tarName)
	}

	joined := filepath.Join(destDir, clean)
	rel, err := filepath.Rel(destDir, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal rejected: %q", tarName)
	}
	return joined, nil
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

	fakeAppContent          = bytes.Repeat([]byte("bun-compiled-app-binary\n"), 100)
	fakeSupervisorContent   = bytes.Repeat([]byte("pokkum-init-supervisor\n"), 50)
	fakeStaticServerContent = bytes.Repeat([]byte("pokkum-static-server\n"), 50)

	// fakePrerenderedHTML is fixture content for a StrategyLayered/StrategyStatic
	// prerendered/index.html file. It must be at least 64 bytes with genuine
	// repetition so internal/adapters/precompressutils's PrecompressFile (which
	// skips files under 64 bytes and only keeps sidecars with real compression
	// savings) actually produces .gz/.br/.zst sidecars for it.
	fakePrerenderedHTML = bytes.Repeat([]byte("<!doctype html><html><body><h1>Pokkum fixture page</h1></body></html>\n"), 20)
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// skipDirNames are top-level entries copyFixtureProject never copies:
// build tool output that a real build regenerates fresh in the scratch
// project directory, plus node_modules, which is symlinked back to the
// original fixture instead (dozens of MB of real installed dependencies —
// copying it would make every run of an isolated test slow for no benefit,
// since nothing any of these tests exist to guard touches dependency
// *installation*).
var skipDirNames = map[string]bool{
	"node_modules": true,
	".svelte-kit":  true,
	".pokkum":      true,
	"build":        true,
	".git":         true,
}

// copyFixtureProject copies fixtureDir's real source tree (package.json,
// bun.lock, src/, static/, vite.config.ts, etc.) into a fresh t.TempDir(),
// symlinking node_modules back to the original rather than copying it, and
// returns the copy's path.
//
// Every test in this package that drives a real build (bunexec.Compiler,
// core.Build, or a Compiler double that writes fixture files) against a
// testdata/fixtures/* project MUST call this first and use its return value
// as ProjectDir, rather than the checked-out fixture path directly. Building
// in place leaves .svelte-kit/, .pokkum/, build/, and (for a real
// BaseImageResolver) pokkum.lock behind in a directory this package's other
// tests — and other test packages/agents working this codebase — also read
// from, making results depend on what a previous test or run left behind
// (see Lessons.md's 2026-08-19 "shared fixture mutation" entry and
// mem:self_review_checklist). copyFixtureProject was originally local to
// TestRuntimeSmoke_LayeredStrategy_BootsAndServes (runtime_smoke_test.go),
// the first test in this package to need it; it moved here once
// TestFixtureDrivenE2E_Static, TestFixtureDrivenE2E_Static_SPAFallback,
// TestFixtureDrivenE2E_AllStrategies, TestRealBuildIsReproducibleAcrossRuns,
// and TestRealBuild_StrategyLayered_PrerenderedRoute all needed the same
// copy-and-symlink shape.
func copyFixtureProject(t *testing.T, fixtureDir string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("copyFixtureProject: mkdir %q: %v", dst, err)
	}

	walkErr := filepath.WalkDir(fixtureDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(fixtureDir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			// The fixture is not expected to contain symlinks of its own
			// (node_modules' internal symlinks are never visited, since the
			// whole directory is skipped above); skip anything unexpected
			// rather than mishandling it.
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if walkErr != nil {
		t.Fatalf("copyFixtureProject: copy %q to %q: %v", fixtureDir, dst, walkErr)
	}

	srcNodeModules := filepath.Join(fixtureDir, "node_modules")
	if _, statErr := os.Stat(srcNodeModules); statErr == nil {
		if err := os.Symlink(srcNodeModules, filepath.Join(dst, "node_modules")); err != nil {
			t.Fatalf("copyFixtureProject: symlink node_modules: %v", err)
		}
	}
	return dst
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

// TestExtractLayerFiles_RoundTrip proves ExtractLayerFiles round-trips real
// file bytes: files are written to a source directory, packaged into a real
// v1.Layer via packager.BuildDirectoryTreeLayer (the same builder used for
// the /app/client and /app/prerendered layers in production packaging), then
// extracted back out with ExtractLayerFiles into a separate directory. The
// extracted tree is diffed byte-for-byte against the originals.
func TestExtractLayerFiles_RoundTrip(t *testing.T) {
	ctx := context.Background()
	srcDir := t.TempDir()

	wantFiles := map[string][]byte{
		"index.html":             []byte("<html><body>hello</body></html>\n"),
		"index.html.gz":          bytes.Repeat([]byte{0x1f, 0x8b, 0x00, 0x01}, 8),
		"assets/app.js":          []byte("console.log('pokkum');\n"),
		"assets/nested/deep.txt": bytes.Repeat([]byte("deep-content\n"), 20),
	}
	for rel, content := range wantFiles {
		full := filepath.Join(srcDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", full, err)
		}
	}

	layer, err := packager.BuildDirectoryTreeLayer(ctx, ports.LinuxAMD64, srcDir, ports.AppClientDirPrefix, testEpoch, ports.CompressionGzip)
	if err != nil {
		t.Fatalf("BuildDirectoryTreeLayer: %v", err)
	}

	destDir := t.TempDir()
	if err := ExtractLayerFiles(layer, destDir); err != nil {
		t.Fatalf("ExtractLayerFiles: %v", err)
	}

	// ports.AppClientDirPrefix is "/app/client"; tar entries strip the
	// leading slash, and ExtractLayerFiles preserves the tar path verbatim,
	// so extracted files land under destDir/app/client/...
	extractedRoot := filepath.Join(destDir, "app", "client")
	for rel, want := range wantFiles {
		got, err := os.ReadFile(filepath.Join(extractedRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("ReadFile extracted %q: %v", rel, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("extracted %q content mismatch: got %d bytes, want %d bytes", rel, len(got), len(want))
		}
	}

	// Confirm no extra files were extracted beyond what was packaged.
	var extractedCount int
	err = filepath.WalkDir(destDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			extractedCount++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%q): %v", destDir, err)
	}
	if extractedCount != len(wantFiles) {
		t.Errorf("extracted %d files, want %d", extractedCount, len(wantFiles))
	}
}

// TestExtractLayerFiles_RejectsPathTraversal proves safeJoin rejects tar
// entries that would escape destDir, e.g. a maliciously (or buggily)
// constructed layer whose entry names contain "..".
func TestExtractLayerFiles_RejectsPathTraversal(t *testing.T) {
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		content := []byte("evil\n")
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "../../etc/evil",
			Mode:     0o644,
			Size:     int64(len(content)),
			ModTime:  testEpoch,
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

	destDir := t.TempDir()
	if err := ExtractLayerFiles(layer, destDir); err == nil {
		t.Fatalf("ExtractLayerFiles: expected path traversal error, got nil")
	}
}
