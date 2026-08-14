package comparator_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/comparator"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pokkum-comparator-dockerconfig")
	if err != nil {
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	os.Setenv("DOCKER_CONFIG", dir)
	os.Exit(m.Run())
}

// customCompressedLayer implements v1.Layer with a specific gzip compression level
type customCompressedLayer struct {
	uncompressed []byte
	diffID       v1.Hash
	gzipLevel    int
}

func (c *customCompressedLayer) Digest() (v1.Hash, error) {
	rc, err := c.Compressed()
	if err != nil {
		return v1.Hash{}, err
	}
	defer rc.Close()
	h, _, err := v1.SHA256(rc)
	return h, err
}

func (c *customCompressedLayer) DiffID() (v1.Hash, error) {
	return c.diffID, nil
}

func (c *customCompressedLayer) Compressed() (io.ReadCloser, error) {
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, c.gzipLevel)
	if err != nil {
		return nil, err
	}
	if _, err := gw.Write(c.uncompressed); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

func (c *customCompressedLayer) Uncompressed() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(c.uncompressed)), nil
}

func (c *customCompressedLayer) Size() (int64, error) {
	rc, err := c.Compressed()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	n, _ := buf.ReadFrom(rc)
	return n, nil
}

func (c *customCompressedLayer) MediaType() (types.MediaType, error) {
	return types.DockerLayer, nil
}

func createTarBytes(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(body)),
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	return buf.Bytes()
}

func buildLayer(tarBytes []byte, gzipLevel int) v1.Layer {
	diffID, _, _ := v1.SHA256(bytes.NewReader(tarBytes))
	return &customCompressedLayer{
		uncompressed: tarBytes,
		diffID:       diffID,
		gzipLevel:    gzipLevel,
	}
}

func writeImageTar(t *testing.T, img v1.Image, refStr string) string {
	t.Helper()
	f, err := os.CreateTemp("", "pokkum-test-img-*.tar")
	if err != nil {
		t.Fatalf("create temp tar: %v", err)
	}
	defer f.Close()

	ref, err := name.ParseReference(refStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("parse ref: %v", err)
	}

	if err := tarball.Write(ref, img, f); err != nil {
		t.Fatalf("write tarball: %v", err)
	}

	return f.Name()
}

func TestComparator_InvalidRequests(t *testing.T) {
	c := comparator.NewComparator(slog.Default())
	ctx := context.Background()

	t.Run("empty remote image ref returns ErrInvalidRequest", func(t *testing.T) {
		_, err := c.CompareImages(ctx, ports.ImageComparatorRequest{
			LocalTarball: "/tmp/local.tar",
		})
		if err == nil {
			t.Fatal("expected error for empty remote image ref")
		}
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest, got %v", err)
		}
	})

	t.Run("empty local tarball returns ErrInvalidRequest", func(t *testing.T) {
		_, err := c.CompareImages(ctx, ports.ImageComparatorRequest{
			RemoteImageRef: "ghcr.io/acme/app:v1.0.0",
		})
		if err == nil {
			t.Fatal("expected error for empty local tarball")
		}
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest, got %v", err)
		}
	})
}

func TestComparator_L1_ExactMatch(t *testing.T) {
	c := comparator.NewComparator(slog.Default())
	ctx := context.Background()

	// Build image with identical layer and config
	files := map[string]string{
		"app/server.js":    "console.log('hello world');",
		"app/package.json": `{"name":"app"}`,
	}
	layer := buildLayer(createTarBytes(files), gzip.BestSpeed)
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("append layers: %v", err)
	}

	localTar := writeImageTar(t, img, "ghcr.io/acme/app:v1.0.0")
	remoteTar := writeImageTar(t, img, "ghcr.io/acme/app:v1.0.0")
	defer os.Remove(localTar)
	defer os.Remove(remoteTar)

	res, err := c.CompareImages(ctx, ports.ImageComparatorRequest{
		RemoteImageRef: remoteTar,
		LocalTarball:   localTar,
	})
	if err != nil {
		t.Fatalf("compare images: %v", err)
	}

	if res.Level != "L1" {
		t.Errorf("expected Level L1, got %q", res.Level)
	}
	if !res.L1ExactMatch || !res.L2SemanticMatch {
		t.Errorf("expected L1 and L2 matches true, got L1=%t, L2=%t", res.L1ExactMatch, res.L2SemanticMatch)
	}
}

func TestComparator_L2_SemanticMatch_GzipSkew(t *testing.T) {
	c := comparator.NewComparator(slog.Default())
	ctx := context.Background()

	// Same uncompressed files, different gzip compression levels (e.g. BestSpeed vs BestCompression)
	files := map[string]string{
		"app/index.js":  "const x = 1; module.exports = x;",
		"app/style.css": "body { margin: 0; padding: 0; }",
	}
	tarContent := createTarBytes(files)

	remoteLayer := buildLayer(tarContent, gzip.BestSpeed)
	localLayer := buildLayer(tarContent, gzip.BestCompression)

	remoteImg, _ := mutate.AppendLayers(empty.Image, remoteLayer)
	localImg, _ := mutate.AppendLayers(empty.Image, localLayer)

	// Verify compressed layer digests differ (so L1 fails)
	rDigest, _ := remoteImg.Digest()
	lDigest, _ := localImg.Digest()
	if rDigest == lDigest {
		t.Fatalf("expected different compressed digests for gzip skew test, got %s", rDigest)
	}

	localTar := writeImageTar(t, localImg, "ghcr.io/acme/app:v1.0.0")
	remoteTar := writeImageTar(t, remoteImg, "ghcr.io/acme/app:v1.0.0")
	defer os.Remove(localTar)
	defer os.Remove(remoteTar)

	res, err := c.CompareImages(ctx, ports.ImageComparatorRequest{
		RemoteImageRef: remoteTar,
		LocalTarball:   localTar,
	})
	if err != nil {
		t.Fatalf("compare images: %v", err)
	}

	if res.Level != "L2" {
		t.Errorf("expected Level L2, got %q", res.Level)
	}
	if res.L1ExactMatch {
		t.Errorf("expected L1ExactMatch to be false under gzip skew")
	}
	if !res.L2SemanticMatch {
		t.Errorf("expected L2SemanticMatch to be true under gzip skew")
	}
}

func TestComparator_L3_FileMismatch(t *testing.T) {
	c := comparator.NewComparator(slog.Default())
	ctx := context.Background()

	// Remote has original file
	remoteFiles := map[string]string{
		"app/server.js": "console.log('original code');",
	}
	remoteLayer := buildLayer(createTarBytes(remoteFiles), gzip.BestSpeed)
	remoteImg, _ := mutate.AppendLayers(empty.Image, remoteLayer)

	// Local rebuild has modified file and an extra file
	localFiles := map[string]string{
		"app/server.js": "console.log('tampered / modified code');",
		"app/extra.txt": "unexpected file",
	}
	localLayer := buildLayer(createTarBytes(localFiles), gzip.BestSpeed)
	localImg, _ := mutate.AppendLayers(empty.Image, localLayer)

	localTar := writeImageTar(t, localImg, "ghcr.io/acme/app:v1.0.0")
	remoteTar := writeImageTar(t, remoteImg, "ghcr.io/acme/app:v1.0.0")
	defer os.Remove(localTar)
	defer os.Remove(remoteTar)

	res, err := c.CompareImages(ctx, ports.ImageComparatorRequest{
		RemoteImageRef: remoteTar,
		LocalTarball:   localTar,
	})
	if err != nil {
		t.Fatalf("compare images: %v", err)
	}

	if res.Level != "L3" {
		t.Errorf("expected Level L3, got %q", res.Level)
	}
	if res.L1ExactMatch || res.L2SemanticMatch {
		t.Errorf("expected L1 and L2 matches to be false, got L1=%t, L2=%t", res.L1ExactMatch, res.L2SemanticMatch)
	}
	if len(res.L3FileDiffs) == 0 {
		t.Fatal("expected non-empty L3FileDiffs for modified file")
	}

	foundModified := false
	foundAdded := false
	for _, diff := range res.L3FileDiffs {
		if strings.Contains(diff, "app/server.js") {
			foundModified = true
		}
		if strings.Contains(diff, "app/extra.txt") {
			foundAdded = true
		}
	}
	if !foundModified {
		t.Errorf("expected L3 diffs to mention app/server.js: %v", res.L3FileDiffs)
	}
	if !foundAdded {
		t.Errorf("expected L3 diffs to mention app/extra.txt: %v", res.L3FileDiffs)
	}
}

func TestComparator_RemoteRegistryComparison(t *testing.T) {
	server := httptest.NewServer(registry.New())
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	targetRepo := fmt.Sprintf("%s/app-compare", host)

	files := map[string]string{
		"app/main.js": "console.log('registry test');",
	}
	layer := buildLayer(createTarBytes(files), gzip.BestSpeed)
	img, _ := mutate.AppendLayers(empty.Image, layer)

	tagRef, _ := name.ParseReference(targetRepo+":v1.0.0", name.WeakValidation)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push remote image: %v", err)
	}

	localTar := writeImageTar(t, img, targetRepo+":v1.0.0")
	defer os.Remove(localTar)

	c := comparator.NewComparator(slog.Default())
	ctx := context.Background()

	res, err := c.CompareImages(ctx, ports.ImageComparatorRequest{
		RemoteImageRef: targetRepo + ":v1.0.0",
		LocalTarball:   localTar,
	})
	if err != nil {
		t.Fatalf("compare against remote registry: %v", err)
	}

	if res.Level != "L1" {
		t.Errorf("expected Level L1, got %q", res.Level)
	}
}
