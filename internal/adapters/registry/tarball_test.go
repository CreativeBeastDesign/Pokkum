package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestTarballWrite_EmptyPath(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.Write(context.Background(), ports.TarballRequest{
		Repo:    "pokkum.local/app",
		Payload: ports.Payload{Image: randomImage(t)},
	})
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Fatalf("err = %v, want core.ErrTarballFailed", err)
	}
}

func TestTarballWrite_ZeroPayload(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.Write(context.Background(), ports.TarballRequest{
		Repo: "pokkum.local/app",
		Path: filepath.Join(t.TempDir(), "out.tar"),
	})
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Fatalf("err = %v, want core.ErrTarballFailed", err)
	}
}

func TestTarballWrite_SingleImage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")
	img := randomImage(t)
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: img},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", res.Digest, wantDigest)
	}
	if res.Path != path {
		t.Errorf("Path = %q, want %q", res.Path, path)
	}
	if len(res.Tags) != 1 || res.Tags[0] != "v1" {
		t.Errorf("Tags = %v, want [v1]", res.Tags)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("archive not present at %s: %v", path, err)
	}

	tag, err := name.NewTag("pokkum.local/app:v1")
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	pulled, err := tarball.ImageFromPath(path, &tag)
	if err != nil {
		t.Fatalf("tarball.ImageFromPath: %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatalf("pulled.Digest: %v", err)
	}
	if gotDigest != wantDigest {
		t.Errorf("archive digest = %s, want %s", gotDigest, wantDigest)
	}
}

func TestTarballWrite_Index_FlattensEveryPlatform(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")
	idx := indexWithPlatforms(t, []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})
	wantDigest, err := idx.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Index: idx},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want index digest %s", res.Digest, wantDigest)
	}

	wantTags := []string{"v1-linux-amd64", "v1-linux-arm64"}
	gotTags := append([]string(nil), res.Tags...)
	sort.Strings(gotTags)
	sort.Strings(wantTags)
	if strings.Join(gotTags, ",") != strings.Join(wantTags, ",") {
		t.Fatalf("Tags = %v, want %v", res.Tags, wantTags)
	}

	// Every platform must be independently readable back out of the one
	// archive — nothing silently dropped.
	for _, want := range []struct {
		tag      string
		os, arch string
	}{
		{"v1-linux-amd64", "linux", "amd64"},
		{"v1-linux-arm64", "linux", "arm64"},
	} {
		tag, err := name.NewTag("pokkum.local/app:" + want.tag)
		if err != nil {
			t.Fatalf("NewTag(%q): %v", want.tag, err)
		}
		img, err := tarball.ImageFromPath(path, &tag)
		if err != nil {
			t.Fatalf("tarball.ImageFromPath(%q): %v", want.tag, err)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatalf("ConfigFile(%q): %v", want.tag, err)
		}
		if cf.OS != want.os || cf.Architecture != want.arch {
			t.Errorf("tag %q image is %s/%s, want %s/%s", want.tag, cf.OS, cf.Architecture, want.os, want.arch)
		}
	}
}

// TestTarballWrite_FailureLeavesNoTruncatedFile proves the temp-file-and-
// rename strategy: when the underlying write fails partway through, the
// destination path is left exactly as it was before the call (here,
// nonexistent), never a partially-written file that a later `docker load`
// would accept and then fail on.
func TestTarballWrite_FailureLeavesNoTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tar")

	a := NewAdapter(nil)
	_, err := a.Write(context.Background(), ports.TarballRequest{
		Path:    path,
		Repo:    "pokkum.local/app",
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: brokenImage{randomImage(t)}},
	})
	if err == nil {
		t.Fatal("Write: want error from a broken image, got nil")
	}
	if !errors.Is(err, core.ErrTarballFailed) {
		t.Errorf("err = %v, want core.ErrTarballFailed", err)
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("destination path exists after a failed write: %v", statErr)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("temp directory not cleaned up, found: %v", names)
	}
}

// brokenImage wraps a real image but fails Layers(), so tarball.
// MultiWriteToFile errors out partway through — deep enough that the failure
// exercises the real write path, not just Write's own up-front validation.
type brokenImage struct{ v1.Image }

func (brokenImage) Layers() ([]v1.Layer, error) {
	return nil, errors.New("brokenImage: injected failure")
}
