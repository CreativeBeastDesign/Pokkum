package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var updateGolden = flag.Bool("update", false, "update golden files")

func isUpdateGolden() bool {
	return *updateGolden || os.Getenv("UPDATE_GOLDEN") != ""
}

func goldenFilePath(t *testing.T, filename string) string {
	t.Helper()
	// Navigate relative to package root: testdata/golden/<filename>
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", "golden", filename))
	if err != nil {
		t.Fatalf("goldenFilePath(%q): %v", filename, err)
	}
	return p
}

func compareOrUpdateGolden(t *testing.T, filename string, actualJSON []byte) {
	t.Helper()
	path := goldenFilePath(t, filename)

	// Format JSON canonically for comparison
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, actualJSON, "", "  "); err != nil {
		t.Fatalf("json.Indent: %v", err)
	}
	formattedBytes := append(formatted.Bytes(), '\n')

	if isUpdateGolden() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll golden dir: %v", err)
		}
		if err := os.WriteFile(path, formattedBytes, 0o644); err != nil {
			t.Fatalf("WriteFile golden %s: %v", path, err)
		}
		t.Logf("Updated golden file: %s", path)
		return
	}

	expectedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile golden %s (run with -update to generate): %v", path, err)
	}

	if !bytes.Equal(expectedBytes, formattedBytes) {
		t.Errorf("Golden mismatch for %s:\n--- GOT ---\n%s\n--- WANT ---\n%s", filename, formattedBytes, expectedBytes)
	}
}

func TestGoldenOCIManifestAndConfig(t *testing.T) {
	pkg := packager.NewPackager(testLogger())
	req := NewTestPackageRequest(t, ports.LinuxAMD64)

	img, err := pkg.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Packager.Build: %v", err)
	}

	// 1. Assert OCI Manifest JSON
	rawManifest, err := img.RawManifest()
	if err != nil {
		t.Fatalf("img.RawManifest: %v", err)
	}
	compareOrUpdateGolden(t, "manifest_linux_amd64.json", rawManifest)

	// 2. Assert OCI Config JSON
	rawConfig, err := img.RawConfigFile()
	if err != nil {
		t.Fatalf("img.RawConfigFile: %v", err)
	}
	compareOrUpdateGolden(t, "config_linux_amd64.json", rawConfig)
}

func TestGoldenOCIIndex(t *testing.T) {
	pkg := packager.NewPackager(testLogger())
	images := map[ports.Platform]v1.Image{}

	for _, plat := range []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64} {
		img, err := pkg.Build(context.Background(), NewTestPackageRequest(t, plat))
		if err != nil {
			t.Fatalf("Packager.Build(%s): %v", plat, err)
		}
		images[plat] = img
	}

	idx, err := pkg.Index(context.Background(), ports.IndexRequest{
		Images: images,
		Annotations: map[string]string{
			"org.opencontainers.image.source": "https://github.com/example/test-app",
		},
		CreatedAt: testEpoch,
	})
	if err != nil {
		t.Fatalf("Packager.Index: %v", err)
	}

	rawIndex, err := idx.RawManifest()
	if err != nil {
		t.Fatalf("idx.RawManifest: %v", err)
	}
	compareOrUpdateGolden(t, "index_multi_arch.json", rawIndex)
}
