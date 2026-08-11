package layerdiffutils_test

import (
	"archive/tar"
	"bytes"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/layerdiffutils"
)

func createTestTar(entries map[string]string, mtime time.Time) *bytes.Buffer {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for name, body := range entries {
		hdr := &tar.Header{
			Name:    name,
			Mode:    0644,
			Size:    int64(len(body)),
			ModTime: mtime,
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	return &buf
}

func TestCompareTarStreams_Identical(t *testing.T) {
	now := time.Unix(1700000000, 0)
	files := map[string]string{"foo.txt": "hello world"}

	t1 := createTestTar(files, now)
	t2 := createTestTar(files, now)

	res, err := layerdiffutils.CompareTarStreams(t1, t2)
	if err != nil {
		t.Fatalf("unexpected error comparing tars: %v", err)
	}

	if !res.Identical {
		t.Error("expected tars to be identical")
	}
}

func TestCompareTarStreams_TimestampDrift(t *testing.T) {
	t1 := createTestTar(map[string]string{"foo.txt": "hello"}, time.Unix(1700000000, 0))
	t2 := createTestTar(map[string]string{"foo.txt": "hello"}, time.Unix(1700000050, 0))

	res, err := layerdiffutils.CompareTarStreams(t1, t2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Identical {
		t.Error("expected tars to differ on timestamp")
	}
	if len(res.HeaderDiffs) == 0 {
		t.Error("expected header diff entry for mtime")
	}
}

func TestCompareTarStreams_ContentMismatch(t *testing.T) {
	now := time.Unix(1700000000, 0)
	t1 := createTestTar(map[string]string{"foo.txt": "hello v1"}, now)
	t2 := createTestTar(map[string]string{"foo.txt": "hello v2"}, now)

	res, err := layerdiffutils.CompareTarStreams(t1, t2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Identical {
		t.Error("expected tars to differ on content")
	}
	if len(res.ContentDiffs) == 0 {
		t.Error("expected content diff entry")
	}
}
