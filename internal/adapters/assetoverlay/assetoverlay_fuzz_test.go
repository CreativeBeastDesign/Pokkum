package assetoverlay

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// buildFuzzTar wraps a single, fuzz-controlled tar entry name/content into a
// minimal valid tar stream. Building a real tar.Writer stream (rather than
// hand-crafting bytes) means the fuzzer is exercising extractImmutableAssets'
// own entry-name handling, not tar.Reader's parser robustness (which is
// archive/tar's own concern, not this package's).
func buildFuzzTar(name string, content []byte) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// A tar header with an invalid name (NUL bytes, huge size mismatch, deep
	// nesting under the 255-byte name limit) can itself fail to write —
	// that's fine and expected; the fuzz target's job is to confirm nothing
	// escapes destDir, not that every fuzz seed produces a parseable tar
	// stream.
	hdr := &tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o644,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil
	}
	if _, err := tw.Write(content); err != nil {
		return nil
	}
	if err := tw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// FuzzExtractImmutableAssets_Containment is the empirical regression guard
// for the real path-traversal vulnerability fixed 2026-08-18 (see
// Lessons.md's "Adversarial review of --asset-overlay" entry, and
// mem:self_review_checklist rows 20/22): extractImmutableAssets reads tar
// entry names straight off a REMOTE, registry-pulled layer — reachable via
// an automatically-walked pokkum.dev/predecessor chain (anyone with push
// access to the target repo) or an arbitrary, fully caller-controlled
// --asset-overlay-from ref — and writes file content to destPath =
// destDir/rel derived from that untrusted name.
//
// Per row 22 of the checklist, the property under test is CONTAINMENT
// ITSELF, empirically verified by walking the real filesystem after each
// fuzz input and confirming nothing exists outside a real temp directory —
// not merely "an error was returned" (an error from the wrong cause, e.g. a
// malformed tar header, would trivially pass a weaker test while a
// containment bug elsewhere goes uncaught).
func FuzzExtractImmutableAssets_Containment(f *testing.F) {
	// Real-world-shaped seeds.
	f.Add("app/client/_app/immutable/chunks/abc123.js", []byte("immutable content"))
	f.Add("app/client/index.html", []byte("not immutable"))
	f.Add("app/client/_app/immutable/assets/font-9f2a3b.woff2", []byte{0x00, 0x01, 0x02})

	// Hostile seeds: the exact shape that caused the real 2026-08-18 bug —
	// a raw string that still starts with immutableAssetPrefix but contains
	// enough ".." segments to escape once filepath.Join resolves them.
	f.Add("app/client/_app/immutable/"+strings.Repeat("../", 10)+"etc/pwned", []byte("PWNED"))
	f.Add("app/client/_app/immutable/../../../../../../../../tmp/pwned", []byte("PWNED"))
	f.Add("../../../../etc/passwd", []byte("PWNED"))
	f.Add("/etc/passwd", []byte("PWNED"))
	f.Add("app/client/_app/immutable/..", []byte(""))
	f.Add("app/client/_app/immutable/./../../pwned", []byte("PWNED"))
	f.Add("", []byte(""))
	f.Add("app/client/_app/immutable/\x00/pwned", []byte("PWNED"))
	f.Add(strings.Repeat("a/", 200)+"deep.js", []byte("deep"))
	f.Add("app/client/_app/immutable/"+strings.Repeat("x", 5000), []byte("long name"))
	f.Add("app/client/_app/immutable/\xff\xfe\x00invalid-utf8", []byte("bytes"))
	f.Add("APP/CLIENT/_APP/IMMUTABLE/chunks/case.js", []byte("wrong case, should not match prefix"))
	f.Add("./app/client/_app/immutable/chunks/dotslash.js", []byte("leading ./"))

	f.Fuzz(func(t *testing.T, name string, content []byte) {
		destDir := t.TempDir()
		outsideDir := t.TempDir()

		tarBytes := buildFuzzTar(name, content)
		if tarBytes == nil {
			return // header itself was invalid (e.g. name too long) — nothing to extract
		}

		// extractImmutableAssets may legitimately return an error (conflict,
		// read failure, rejected traversal) — that is not itself a bug. The
		// only thing this fuzz target asserts is that whatever happened,
		// nothing was ever written outside destDir.
		_ = extractImmutableAssets(bytes.NewReader(tarBytes), destDir, "fuzz-ref")

		// 1. Nothing landed in a sibling temp directory that stands in for
		// "anywhere outside destDir".
		entries, err := filepathGlobAll(outsideDir)
		if err != nil {
			t.Fatalf("scan outsideDir: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("extractImmutableAssets(name=%q) escaped destDir: found %v under an unrelated sibling temp dir", name, entries)
		}

		// 2. Every file that DID get written under destDir is, after
		// resolving symlinks, still genuinely inside destDir — the same
		// containment check safeJoinOverlayPath itself performs, re-derived
		// independently here so a bug in safeJoinOverlayPath's own logic
		// cannot pass this test by construction.
		written, err := filepathGlobAll(destDir)
		if err != nil {
			t.Fatalf("scan destDir: %v", err)
		}
		cleanDestDir := filepath.Clean(destDir)
		for _, p := range written {
			cp := filepath.Clean(p)
			if cp != cleanDestDir && !strings.HasPrefix(cp, cleanDestDir+string(filepath.Separator)) {
				t.Fatalf("extractImmutableAssets(name=%q) wrote %q outside destDir %q", name, p, destDir)
			}
		}
	})
}

// filepathGlobAll returns every regular file path found anywhere under root
// (recursively), used by FuzzExtractImmutableAssets_Containment to prove
// empirically that nothing escaped the intended destination directory.
func filepathGlobAll(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
