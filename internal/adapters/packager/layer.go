package packager

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"crypto/sha256"
	"encoding/hex"
	"runtime"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/attestutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/layercacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/poolutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Tar header pinning. Every one of these is a value archive/tar would otherwise
// take from the host filesystem or invent per entry, and every one of them ends
// up in the layer's diffID.
const (
	// nonrootUID and nonrootGID are the distroless "nonroot" user, matching
	// ports.DefaultUser. Numeric only: the image has no /etc/passwd worth
	// reading, so a name would resolve to nothing at runtime and would also
	// vary with the build host's user database.
	nonrootUID = 65532
	nonrootGID = 65532

	// fileMode is the mode of both binaries: readable and executable, with no
	// write bit at all, matching ports.PackageRequest's App and Supervisor field
	// docs (0555). Nothing in the image writes to them — they are owned by
	// nonrootUID/nonrootGID but the container does not run as that user by
	// default (see ports.DefaultUser) — and dropping the write bit also suits
	// readOnlyRootFilesystem, which is on the project roadmap. A mode that
	// depended on the build host's umask would not be reproducible, so the
	// value is stated rather than inherited.
	fileMode = 0o555

	// dirMode is the mode of the explicit directory entries: the same 0555 as
	// fileMode. Directories need the execute bit to be traversable but nothing
	// ever writes into /app or /pokkum after the layer is built, so there is no
	// more reason for a write bit here than there is on the files inside them.
	dirMode = 0o555

	// tarFormat is set on every header explicitly rather than left as
	// FormatUnknown. With FormatUnknown, archive/tar picks a format per entry
	// from that entry's contents, which means a future change to a path or a
	// size could silently switch one entry to PAX and change every byte after
	// it. Naming PAX also permits USTAR (see Header.allowedFormats), and since
	// no entry here needs an extended record — short ASCII names, whole-second
	// mtimes, zero atime/ctime, small ids — the writer emits plain USTAR
	// headers with no extended-header blocks at all. That is the goal: no
	// variable-length per-run records anywhere in the archive.
	tarFormat = tar.FormatPAX
)

// pinnedImmutableBinaryEpoch is the fixed tar ModTime — and, by extension,
// the only "time-shaped" input that used to reach
// layercacheutils.ComputeKey's cache key, which no longer accepts a
// timestamp parameter at all, see its doc comment — for every layer that
// packages an IMMUTABLE, embedded third-party/tooling binary: the Bun
// runtime (BuildCustomFileLayer), pokkum-init (buildSupervisorLayer), and
// pokkum-static (buildStaticServerLayer).
//
// Deliberately NOT req.CreatedAt / SOURCE_DATE_EPOCH: those three binaries'
// bytes are already fully determined by (in-image path, content, platform,
// compression) — nothing about them derives from this build's source
// snapshot, which is what SOURCE_DATE_EPOCH exists to pin. Baking the
// per-build/per-commit SOURCE_DATE_EPOCH into their tar header instead meant
// the layer's diffID/digest (and therefore its on-disk cache key and its
// registry blob digest) changed on every commit even though the embedded
// binary itself was byte-for-byte identical — defeating both
// internal/adapters/layercacheutils' on-disk cache (guaranteed miss every
// commit for unchanged content) and registry-side cross-image
// deduplication of the ~90MB Bun blob across a fleet of differently
// -committed images (docs/archive/Roadmap.md item 3f).
//
// Pinning these three layers to a fixed constant instead is strictly MORE
// deterministic, not less: determinism means identical inputs produce
// identical output bytes, and these binaries' real inputs (path, content,
// platform, compression) do not include a build timestamp at all. Every
// OTHER layer this package builds — server, client, vendor, native,
// prerendered, and the exe-strategy's compiled app binary — legitimately
// reflects this build's own source snapshot and continues to use
// pinnedTime(req.CreatedAt) exactly as before; only these three do not.
//
// The Unix epoch was chosen as the fixed value because it is the
// conventional "no meaningful timestamp" constant used by other
// reproducible-build tooling, and because it can never collide with a real
// SOURCE_DATE_EPOCH-derived build time (those are always many decades
// later), so a log line or a test failure showing this value is
// unambiguously "the pinned binary epoch", never "someone's real build
// time".
var pinnedImmutableBinaryEpoch = time.Unix(0, 0).UTC()

// layerFile is one file destined for the application layer.
type layerFile struct {
	// path is the absolute in-image path, e.g. "/app/server".
	path string

	// size is the exact number of bytes open() will yield. A mismatch is an
	// error rather than a silently truncated layer.
	size int64

	// open yields the contents. It must be callable repeatedly and must produce
	// identical bytes every time; see buildAppLayer.
	open func() (io.ReadCloser, error)
}

// tarEntry is one resolved archive member, either a directory or a file.
type tarEntry struct {
	name     string // in-archive name: no leading slash, trailing slash on dirs
	typeflag byte
	size     int64
	open     func() (io.ReadCloser, error)
}

// buildAppLayer produces the deterministic layer Pokkum adds for the compiled
// application: a single file at ports.AppBinaryPath, nothing else.
//
// It is kept in its own layer, below nothing (see buildSupervisorLayer and the
// package doc's "Two layers, ordered by volatility" section), because it is
// the layer that changes on every build.
func buildAppLayer(ctx context.Context, req ports.PackageRequest, modTime time.Time) (v1.Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", req.Platform, err)
	}

	info, err := os.Stat(req.App.Path)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: stat application binary %q: %w: %w",
			req.Platform, req.App.Path, err, core.ErrPackageFailed)
	}
	if info.IsDir() {
		// ports.Artifact is documented as a single file with no sibling asset
		// directory; a directory here means the compiler contract was broken
		// upstream and the resulting image would be silently empty.
		return nil, fmt.Errorf("packager: build %s: application binary %q is a directory: %w",
			req.Platform, req.App.Path, core.ErrPackageFailed)
	}

	file := layerFile{
		path: ports.AppBinaryPath,
		size: info.Size(),
		open: fileOpener(req.App.Path),
	}
	return buildLayer(ctx, req.Platform, file, modTime, req.Compression)
}

// buildSupervisorLayer produces the deterministic layer Pokkum adds for
// pokkum-init: a single file at ports.SupervisorPath, nothing else.
//
// It is a separate layer from buildAppLayer's, and appended below it, because
// it changes only when pokkum itself is upgraded while the application layer
// changes on every build — see the package doc's "Two layers, ordered by
// volatility" section for the full rationale.
func buildSupervisorLayer(ctx context.Context, req ports.PackageRequest, modTime time.Time) (v1.Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", req.Platform, err)
	}

	cacheDir := layercacheutils.ResolveCacheDir()
	contentHash := layercacheutils.ComputeBytesSHA256(req.Supervisor)
	cacheKey := layercacheutils.ComputeKey(ports.SupervisorPath, contentHash, req.Platform, req.Compression)

	if cached, ok := layercacheutils.Get(cacheDir, cacheKey, req.Compression); ok {
		return cached, nil
	}

	file := layerFile{
		path: ports.SupervisorPath,
		size: int64(len(req.Supervisor)),
		open: bytesOpener(req.Supervisor),
	}
	layer, err := buildLayer(ctx, req.Platform, file, modTime, req.Compression)
	if err != nil {
		return nil, err
	}

	return layercacheutils.Put(cacheDir, cacheKey, layer, req.Compression)
}

// buildStaticServerLayer produces the deterministic layer Pokkum adds for
// pokkum-static: a single file at ports.StaticServerPath, nothing else. It is
// the static-strategy analogue of buildSupervisorLayer — the static server plays
// PID 1 in a libc-free image where there is no separate supervisor.
func buildStaticServerLayer(ctx context.Context, req ports.PackageRequest, modTime time.Time) (v1.Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", req.Platform, err)
	}

	cacheDir := layercacheutils.ResolveCacheDir()
	contentHash := layercacheutils.ComputeBytesSHA256(req.StaticServer)
	cacheKey := layercacheutils.ComputeKey(ports.StaticServerPath, contentHash, req.Platform, req.Compression)

	if cached, ok := layercacheutils.Get(cacheDir, cacheKey, req.Compression); ok {
		return cached, nil
	}

	file := layerFile{
		path: ports.StaticServerPath,
		size: int64(len(req.StaticServer)),
		open: bytesOpener(req.StaticServer),
	}
	layer, err := buildLayer(ctx, req.Platform, file, modTime, req.Compression)
	if err != nil {
		return nil, err
	}

	return layercacheutils.Put(cacheDir, cacheKey, layer, req.Compression)
}

// countWriter records the total bytes written to it.
type countWriter struct {
	count int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n := len(p)
	c.count += int64(n)
	return n, nil
}

type readCloserWithUnderlying struct {
	io.Reader
	closer io.Closer
}

// Close releases both the decompression reader and the underlying file.
//
// The decompression reader needs a type switch rather than a single
// io.Closer assertion: *gzip.Reader satisfies io.Closer (Close() error) and
// the assertion catches it, but *zstd.Decoder's Close method is Close()
// with no return value, which does NOT satisfy io.Closer. The assertion
// silently failing for zstd — no compile error, no panic, just a Close that
// quietly does nothing — is exactly what let the zstd decoder's internal
// goroutines and buffers leak on every call: see
// TestUncompressed_ZstdDecoderClosed for the regression this guards.
func (r *readCloserWithUnderlying) Close() error {
	switch rd := r.Reader.(type) {
	case *zstd.Decoder:
		// *zstd.Decoder.Close() returns no error, so it can never satisfy an
		// io.Closer type assertion — it must be called explicitly, not found
		// via interface matching.
		rd.Close()
	case io.Closer:
		_ = rd.Close()
	}
	return r.closer.Close()
}

// singlePassLayer implements v1.Layer with precomputed O(1) DiffID, Digest, and Size hashes,
// streaming directly from a single-pass generated compressed layer artifact on disk.
type singlePassLayer struct {
	filePath    string
	diffID      v1.Hash
	digest      v1.Hash
	size        int64
	mediaType   types.MediaType
	compression ports.CompressionAlgorithm
}

var _ v1.Layer = (*singlePassLayer)(nil)

func (l *singlePassLayer) Digest() (v1.Hash, error) {
	return l.digest, nil
}

func (l *singlePassLayer) DiffID() (v1.Hash, error) {
	return l.diffID, nil
}

func (l *singlePassLayer) Size() (int64, error) {
	return l.size, nil
}

func (l *singlePassLayer) MediaType() (types.MediaType, error) {
	return l.mediaType, nil
}

func (l *singlePassLayer) Compressed() (io.ReadCloser, error) {
	return os.Open(l.filePath)
}

func (l *singlePassLayer) Uncompressed() (io.ReadCloser, error) {
	f, err := os.Open(l.filePath)
	if err != nil {
		return nil, err
	}
	if l.compression.Normalize() == ports.CompressionZstd {
		r, err := zstd.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return &readCloserWithUnderlying{Reader: r, closer: f}, nil
	}
	gr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &readCloserWithUnderlying{Reader: gr, closer: f}, nil
}

// buildSinglePassLayer writes the uncompressed tar stream, compresses it, and calculates
// both DiffID (uncompressed SHA256) and Digest (compressed SHA256) concurrently in one single pass.
func buildSinglePassLayer(ctx context.Context, platform ports.Platform, entries []tarEntry, modTime time.Time, compression ports.CompressionAlgorithm) (v1.Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", platform, err)
	}

	// The suffix names the file's actual contents (gzip- or zstd-compressed
	// tar), not a bare tarball, which is what it was named before and what a
	// human inspecting a leaked temp file would have wrongly assumed.
	tmpFile, err := os.CreateTemp("", "pokkum-layer-*"+tempLayerSuffix(compression))
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: create temp layer file: %w: %w", platform, err, core.ErrPackageFailed)
	}
	tmpPath := tmpFile.Name()
	// Registered as soon as the file exists so it is cleaned up even on an
	// error path below that doesn't already os.Remove it explicitly; see
	// trackTempFile's doc comment for who actually calls Remove and when.
	trackTempFile(ctx, tmpPath)

	diffIDHasher := sha256.New()
	digestHasher := sha256.New()
	sizeCounter := &countWriter{}

	compressedWriter := io.MultiWriter(tmpFile, digestHasher, sizeCounter)

	var compressor io.WriteCloser
	mediaType := types.OCILayer
	if compression.Normalize() == ports.CompressionZstd {
		mediaType = types.OCILayerZStd
		zw, err := zstd.NewWriter(compressedWriter)
		if err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("packager: build %s: init zstd compressor: %w: %w", platform, err, core.ErrPackageFailed)
		}
		compressor = zw
	} else {
		gw, err := gzip.NewWriterLevel(compressedWriter, gzip.BestSpeed)
		if err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
			return nil, fmt.Errorf("packager: build %s: init gzip compressor: %w: %w", platform, err, core.ErrPackageFailed)
		}
		compressor = gw
	}

	uncompressedWriter := io.MultiWriter(diffIDHasher, compressor)

	if err := writeTar(uncompressedWriter, entries, modTime); err != nil {
		_ = compressor.Close()
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("packager: build %s: write tar: %w: %w", platform, err, core.ErrPackageFailed)
	}

	if err := compressor.Close(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("packager: build %s: flush compressor: %w: %w", platform, err, core.ErrPackageFailed)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("packager: build %s: close temp layer file: %w: %w", platform, err, core.ErrPackageFailed)
	}

	layer := &singlePassLayer{
		filePath: tmpPath,
		diffID: v1.Hash{
			Algorithm: "sha256",
			Hex:       hex.EncodeToString(diffIDHasher.Sum(nil)),
		},
		digest: v1.Hash{
			Algorithm: "sha256",
			Hex:       hex.EncodeToString(digestHasher.Sum(nil)),
		},
		size:        sizeCounter.count,
		mediaType:   mediaType,
		compression: compression,
	}

	// Defensive backstop only: a CLI process normally exits before the
	// garbage collector ever runs this finalizer, so it must not be the
	// primary cleanup mechanism. The primary mechanism is trackTempFile
	// above plus the caller-owned cleanup func from NewBuildContext, which
	// runs deterministically once the whole build (through registry push,
	// daemon load, or tarball write) is done. This finalizer only helps a
	// long-lived process (e.g. `pokkum dev`'s watch loop) that leaks a
	// layer value without ever calling that cleanup func.
	runtime.SetFinalizer(layer, func(l *singlePassLayer) {
		_ = os.Remove(l.filePath)
	})

	return layer, nil
}

// tempLayerSuffix names a layer temp file after what it actually contains —
// a gzip- or zstd-compressed tar stream, never a bare tar — so a file found
// on disk (mid-build, or leaked) is identifiable by extension alone.
func tempLayerSuffix(compression ports.CompressionAlgorithm) string {
	if compression.Normalize() == ports.CompressionZstd {
		return ".tar.zst"
	}
	return ".tar.gz"
}

// buildContextKey is the context.Context key packager uses to find the
// current build's temp-file tracker, if the caller installed one via
// NewBuildContext.
type buildContextKey struct{}

// tempFileTracker collects every intermediate layer temp file created while
// building through a context returned by NewBuildContext, so they can all be
// removed in one deterministic pass by the tracked context's cleanup func.
//
// A slice guarded by a mutex, not a sync.Map or similar: Packager.Build is
// called concurrently, one goroutine per platform (see fanOut in
// internal/core/pipeline.go), and every one of them can be creating a temp
// file through the same tracker at once.
type tempFileTracker struct {
	mu    sync.Mutex
	paths []string
}

func (t *tempFileTracker) add(path string) {
	t.mu.Lock()
	t.paths = append(t.paths, path)
	t.mu.Unlock()
}

// cleanup removes every tracked file. It is safe to call more than once —
// a second call finds nothing left to remove — and safe to call even if no
// file was ever tracked.
func (t *tempFileTracker) cleanup() {
	t.mu.Lock()
	paths := t.paths
	t.paths = nil
	t.mu.Unlock()
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// NewBuildContext returns a context that collects every intermediate layer
// temp file created by packager functions called with it, plus a cleanup
// func the caller must invoke — typically via defer — once every image built
// from that context has been fully consumed by its destination (a registry
// push, a daemon load, or a tarball write).
//
// That timing matters and is not merely "call it whenever": a
// singlePassLayer's Compressed()/Uncompressed() readers stream directly from
// the temp file on disk for as long as the returned v1.Image is in use, so
// cleaning up right after Packager.Build returns — before the image has
// actually been pushed, loaded, or written out — would remove a file the
// publish step still needs to read from. The composition root (cmd/pokkum)
// is the one place that knows when that has finished, since it is the code
// that calls core.Build and only that caller sees both the image-build and
// the publish step complete.
//
// A context not created this way still works: every layer temp file falls
// back to the runtime.SetFinalizer backstop on singlePassLayer, which is not
// guaranteed to run before a short-lived CLI process exits and is therefore
// not sufficient on its own — see buildSinglePassLayer's SetFinalizer
// comment. Composition roots that build and then publish an image should
// always wrap their context with this.
func NewBuildContext(ctx context.Context) (context.Context, func()) {
	tracker := &tempFileTracker{}
	return context.WithValue(ctx, buildContextKey{}, tracker), tracker.cleanup
}

// trackTempFile registers path with ctx's tempFileTracker, if any. It is a
// no-op when ctx was not produced by NewBuildContext, which keeps every
// existing caller (tests included) working unchanged and relying solely on
// the SetFinalizer backstop.
func trackTempFile(ctx context.Context, path string) {
	if t, ok := ctx.Value(buildContextKey{}).(*tempFileTracker); ok {
		t.add(path)
	}
}

// buildLayer wraps a single file (plus its parent directory entries) into one
// deterministic OCI layer in a single pass.
func buildLayer(ctx context.Context, platform ports.Platform, file layerFile, modTime time.Time, compression ports.CompressionAlgorithm) (v1.Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", platform, err)
	}

	entries, err := tarEntries([]layerFile{file})
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: %w: %w", platform, err, core.ErrPackageFailed)
	}

	return buildSinglePassLayer(ctx, platform, entries, modTime, compression)
}

// tarEntries turns a set of in-image file paths into the complete, ordered list
// of archive members, inserting an explicit directory entry for every parent.
//
// Explicit directories matter because a tar containing only "app/server" leaves
// the mode, ownership and mtime of "/app" to whatever the extracting runtime
// invents. containerd and Docker do not agree on that, and neither is obliged
// to be stable across versions, so the layer would extract to a filesystem that
// differs between runtimes even though the layer bytes were identical.
//
// The returned order is the sorted order of the archive names, which also puts
// each directory ahead of its contents ("app/" sorts before "app/server"). The
// sort is over a map's keys and is therefore mandatory, not cosmetic.
func tarEntries(files []layerFile) ([]tarEntry, error) {
	byName := make(map[string]tarEntry, len(files)*2)

	for _, f := range files {
		name := archiveName(f.path)
		if name == "" {
			return nil, fmt.Errorf("invalid layer path %q", f.path)
		}
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("duplicate layer path %q", f.path)
		}
		for _, dir := range parentDirs(name) {
			byName[dir] = tarEntry{name: dir, typeflag: tar.TypeDir}
		}
		byName[name] = tarEntry{
			name:     name,
			typeflag: tar.TypeReg,
			size:     f.size,
			open:     f.open,
		}
	}

	names := slices.Sorted(maps.Keys(byName))
	out := make([]tarEntry, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out, nil
}

// archiveName converts an absolute in-image path to its tar member name:
// cleaned, slash-separated and without the leading slash, which is the form
// every OCI layer uses. It returns "" for a path that names no file.
func archiveName(p string) string {
	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "" || clean == "." {
		return ""
	}
	return clean
}

// parentDirs returns every ancestor directory of an archive name, outermost
// first, each with the trailing slash tar uses for directory members.
func parentDirs(name string) []string {
	var dirs []string
	for d := path.Dir(name); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		dirs = append(dirs, d+"/")
	}
	slices.Reverse(dirs)
	return dirs
}

// tarOpener returns a re-invocable opener that produces the layer's uncompressed
// tar stream. Each call starts a fresh archive; the goroutine owns the writer
// end of the pipe and closes it with whatever error the archive walk produced,
// so a reader that stops early (which is exactly what the compression sniffer
// does, after two bytes) unblocks the goroutine rather than leaking it.
func tarOpener(entries []tarEntry, modTime time.Time) tarball.Opener {
	return func() (io.ReadCloser, error) {
		pr, pw := io.Pipe()
		go func() {
			_ = pw.CloseWithError(writeTar(pw, entries, modTime))
		}()
		return pr, nil
	}
}

// writeTar writes the archive. Every header field is either set from the pinned
// constants above or left at its zero value; nothing is copied from an
// os.FileInfo, which is what keeps the host's umask, uid, atime and filesystem
// out of the layer digest.
func writeTar(w io.Writer, entries []tarEntry, modTime time.Time) error {
	copyBuf := poolutils.GetCopyBuffer()
	defer poolutils.PutCopyBuffer(copyBuf)

	tw := tar.NewWriter(w)
	for _, e := range entries {
		if err := writeEntry(tw, e, modTime, *copyBuf); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeEntry(tw *tar.Writer, e tarEntry, modTime time.Time, buf []byte) error {
	hdr := &tar.Header{
		Typeflag: e.typeflag,
		Name:     e.name,
		Mode:     dirMode,
		Uid:      nonrootUID,
		Gid:      nonrootGID,
		Uname:    "",
		Gname:    "",
		ModTime:  modTime,
		// AccessTime and ChangeTime are deliberately left zero. With an
		// explicit Format, archive/tar stops clearing them for us, and a
		// non-zero value would either be written into the GNU header or forced
		// into a PAX record — in both cases recording when the build host last
		// touched the file.
		AccessTime: time.Time{},
		ChangeTime: time.Time{},
		Format:     tarFormat,
	}
	if e.typeflag == tar.TypeReg {
		hdr.Mode = fileMode
		hdr.Size = e.size
	}

	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %q: %w", e.name, err)
	}
	if e.typeflag != tar.TypeReg {
		return nil
	}

	rc, err := e.open()
	if err != nil {
		return fmt.Errorf("open %q: %w", e.name, err)
	}
	defer rc.Close() //nolint:errcheck // read-only

	n, err := io.CopyBuffer(tw, rc, buf)
	if err != nil {
		return fmt.Errorf("write tar entry %q: %w", e.name, err)
	}
	if n != e.size {
		// The size went into the header before the copy started, so a file that
		// changed underneath the build produces a corrupt archive rather than a
		// short one. Say so plainly instead of shipping it.
		return fmt.Errorf("tar entry %q: wrote %d bytes, header declared %d", e.name, n, e.size)
	}
	return nil
}

// fileOpener yields the contents of a host file. It is re-invocable, and
// returns identical bytes for as long as the file is not modified — which is
// guaranteed for the compiled artifact, since ports.Artifact documents the file
// as owned by the caller of Compile and never deleted or rewritten.
func fileOpener(p string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return os.Open(p) }
}

// bytesOpener yields the contents of an in-memory buffer. The slice is not
// copied: ports.SupervisorProvider documents the returned bytes as read-only
// and shared, so copying 2 MB per platform would be waste, and mutating it
// would be a contract violation on the provider's side rather than ours.
func bytesOpener(b []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
}

// BuildCustomFileLayer builds a single-file layer at targetPath (e.g. "/usr/local/bin/bun")
// from sourcePath on host disk, pinned to modTime and nonroot ownership.
// It leverages on-disk caching via layercacheutils to skip re-compressing identical immutable binaries.
func BuildCustomFileLayer(ctx context.Context, platform ports.Platform, targetPath string, sourcePath string, modTime time.Time, compression ports.CompressionAlgorithm) (v1.Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", platform, err)
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: stat file %q: %w: %w", platform, sourcePath, err, core.ErrPackageFailed)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("packager: build %s: source path %q is a directory: %w", platform, sourcePath, core.ErrPackageFailed)
	}

	cacheDir := layercacheutils.ResolveCacheDir()
	contentHash, hashErr := layercacheutils.ComputeFileSHA256(sourcePath)
	var cacheKey string
	if hashErr == nil {
		cacheKey = layercacheutils.ComputeKey(targetPath, contentHash, platform, compression)
		if cached, ok := layercacheutils.Get(cacheDir, cacheKey, compression); ok {
			return cached, nil
		}
	}

	file := layerFile{
		path: targetPath,
		size: info.Size(),
		open: fileOpener(sourcePath),
	}
	layer, err := buildLayer(ctx, platform, file, modTime, compression)
	if err != nil {
		return nil, err
	}

	if cacheKey != "" {
		return layercacheutils.Put(cacheDir, cacheKey, layer, compression)
	}
	return layer, nil
}

// BuildDirectoryTreeLayerWithPruning builds an OCI layer from a directory tree on host disk,
// mounting it under targetPrefix in the image, applying pruneOptions to skip junk files.
//
// The returned pruneutils.PruneResult accounts for every file excluded from the layer by
// pruneOpts, so callers can report what was left out of the image (see Packager.Build,
// which logs a summary). Pruning here never touches the host filesystem — a junk file is
// simply omitted from the tar stream being built, unlike pruneutils' own on-disk deletion
// helpers.
//
// The returned []attestutils.Record is the authoritative view of exactly what this layer
// archives to the image: one record per regular file actually written into the tar, keyed
// by its in-image path relative to /app with the SHA-256 of its (post-processing) bytes.
// This is what startup attestation (hardening Option C) hashes — the packager aggregates
// these per /app tree into the expected digest, and pokkum-init re-derives the same value
// from the extracted tree at runtime. It is authoritative specifically because it is
// computed on the pruned, post-preprocessing walk, so it matches the container's view by
// construction, not by re-derivation.
func BuildDirectoryTreeLayerWithPruning(ctx context.Context, platform ports.Platform, hostDir string, targetPrefix string, modTime time.Time, compression ports.CompressionAlgorithm, pruneOpts pruneutils.PruneOptions) (v1.Layer, pruneutils.PruneResult, []attestutils.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, pruneutils.PruneResult{}, nil, fmt.Errorf("packager: build %s: %w", platform, err)
	}

	info, err := os.Stat(hostDir)
	if err != nil {
		return nil, pruneutils.PruneResult{}, nil, fmt.Errorf("packager: build %s: stat directory %q: %w: %w", platform, hostDir, err, core.ErrPackageFailed)
	}
	if !info.IsDir() {
		return nil, pruneutils.PruneResult{}, nil, fmt.Errorf("packager: build %s: source path %q is not a directory: %w", platform, hostDir, core.ErrPackageFailed)
	}

	var files []layerFile
	var pruned pruneutils.PruneResult
	var records []attestutils.Record
	err = filepath.WalkDir(hostDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(hostDir, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)

		if d.IsDir() {
			if pruneutils.IsExcludedDir(relSlash, pruneOpts) {
				return filepath.SkipDir
			}
			return nil
		}

		if pruneutils.IsJunk(rel, false, pruneOpts) {
			pruned.FilesPruned++
			pruned.PrunedPaths = append(pruned.PrunedPaths, relSlash)
			if fi, fiErr := d.Info(); fiErr == nil {
				pruned.BytesSaved += fi.Size()
			}
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			// Non-regular entries (symlinks to dirs, sockets, devices) are not
			// surfaced by the layer's own fileOpener as plain regular content in
			// a way the runtime view can re-derive, so they are excluded from
			// the attestation records exactly as the supervisor's runtime walk
			// excludes them.
			return nil
		}

		inImagePath := path.Join(targetPrefix, relSlash)
		files = append(files, layerFile{
			path: inImagePath,
			size: fi.Size(),
			open: fileOpener(p),
		})
		// relToApp is the file's path relative to /app, e.g. "client/index.js"
		// for /app/client/index.js — the namespace the supervisor walks.
		relToApp := strings.TrimPrefix(inImagePath, ports.WorkingDir+"/")
		sha, err := hashFileSHA256(p)
		if err != nil {
			return fmt.Errorf("packager: hash %s for attestation: %w", p, err)
		}
		records = append(records, attestutils.Record{Rel: relToApp, SHA: sha})
		return nil
	})

	if err != nil {
		return nil, pruneutils.PruneResult{}, nil, fmt.Errorf("packager: build %s: walk directory %q: %w: %w", platform, hostDir, err, core.ErrPackageFailed)
	}

	entries, err := tarEntries(files)
	if err != nil {
		return nil, pruneutils.PruneResult{}, nil, fmt.Errorf("packager: build %s: %w: %w", platform, err, core.ErrPackageFailed)
	}

	layer, err := buildSinglePassLayer(ctx, platform, entries, modTime, compression)
	if err != nil {
		return nil, pruneutils.PruneResult{}, nil, err
	}
	return layer, pruned, records, nil
}

// BuildDirectoryTreeLayer builds an OCI layer from a directory tree on host disk with default options.
func BuildDirectoryTreeLayer(ctx context.Context, platform ports.Platform, hostDir string, targetPrefix string, modTime time.Time, compression ports.CompressionAlgorithm) (v1.Layer, error) {
	layer, _, _, err := BuildDirectoryTreeLayerWithPruning(ctx, platform, hostDir, targetPrefix, modTime, compression, pruneutils.PruneOptions{NoPrune: true})
	return layer, err
}

// hashFileSHA256 returns the lowercase hex SHA-256 of a file's bytes, used to
// build attestation records. The bytes hashed are the same bytes the layer's
// fileOpener streams into the tar (it opens the same path), so the record's
// digest is guaranteed to match what the extracted file contains at runtime.
func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
