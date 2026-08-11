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
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"

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
	return buildLayer(ctx, req.Platform, file, modTime)
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

	file := layerFile{
		path: ports.SupervisorPath,
		size: int64(len(req.Supervisor)),
		open: bytesOpener(req.Supervisor),
	}
	return buildLayer(ctx, req.Platform, file, modTime)
}

// buildLayer wraps a single file (plus its parent directory entries) into one
// deterministic OCI layer. It is shared by buildAppLayer and
// buildSupervisorLayer so the tar-header pinning and streaming behaviour below
// is written, and reasoned about, once.
//
// # Why the opener does not capture ctx
//
// tarball.LayerFromOpener calls the opener at least three times before it
// returns — once to sniff whether the stream is already compressed, once to
// compute the compressed digest, once to compute the diffID — and
// go-containerregistry calls it again later, from whatever goroutine and
// whatever context is pushing or writing the image. Binding the opener to the
// build's context would therefore make a perfectly good layer unreadable the
// moment the build context was cancelled, which for a caller that builds and
// then pushes is a use-after-free in slow motion. ctx is instead honoured at
// the step boundaries in Build, and only checked here up front.
//
// # Why the tar is streamed rather than buffered
//
// The application binary is around 90 MB. Buffering the finished tar in memory
// so that the opener could hand out a bytes.Reader would cost that much
// resident memory per platform for the whole build. Regenerating the tar from
// the same source file (or the same in-memory buffer, for the supervisor) on
// each call costs nothing but a re-read, and produces identical bytes because
// every field in every header is pinned.
func buildLayer(ctx context.Context, platform ports.Platform, file layerFile, modTime time.Time) (v1.Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", platform, err)
	}

	entries, err := tarEntries([]layerFile{file})
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: %w: %w", platform, err, core.ErrPackageFailed)
	}

	layer, err := tarball.LayerFromOpener(
		tarOpener(entries, modTime),
		tarball.WithMediaType(types.OCILayer),
	)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: build layer: %w: %w",
			platform, err, core.ErrPackageFailed)
	}
	return layer, nil
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
	tw := tar.NewWriter(w)
	for _, e := range entries {
		if err := writeEntry(tw, e, modTime); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeEntry(tw *tar.Writer, e tarEntry, modTime time.Time) error {
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

	n, err := io.Copy(tw, rc)
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
func BuildCustomFileLayer(ctx context.Context, platform ports.Platform, targetPath string, sourcePath string, modTime time.Time) (v1.Layer, error) {
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

	file := layerFile{
		path: targetPath,
		size: info.Size(),
		open: fileOpener(sourcePath),
	}
	return buildLayer(ctx, platform, file, modTime)
}

// BuildDirectoryTreeLayer builds an OCI layer from a directory tree on host disk,
// mounting it under targetPrefix in the image (e.g., hostDir="build/client", targetPrefix="/app/client").
// All entries are explicitly sorted and pinned to modTime and nonroot ownership.
func BuildDirectoryTreeLayer(ctx context.Context, platform ports.Platform, hostDir string, targetPrefix string, modTime time.Time) (v1.Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("packager: build %s: %w", platform, err)
	}

	info, err := os.Stat(hostDir)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: stat directory %q: %w: %w", platform, hostDir, err, core.ErrPackageFailed)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("packager: build %s: source path %q is not a directory: %w", platform, hostDir, core.ErrPackageFailed)
	}

	var files []layerFile
	err = filepath.WalkDir(hostDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(hostDir, p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}

		inImagePath := path.Join(targetPrefix, filepath.ToSlash(rel))
		files = append(files, layerFile{
			path: inImagePath,
			size: fi.Size(),
			open: fileOpener(p),
		})
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("packager: build %s: walk directory %q: %w: %w", platform, hostDir, err, core.ErrPackageFailed)
	}

	entries, err := tarEntries(files)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: %w: %w", platform, err, core.ErrPackageFailed)
	}

	layer, err := tarball.LayerFromOpener(
		tarOpener(entries, modTime),
		tarball.WithMediaType(types.OCILayer),
	)
	if err != nil {
		return nil, fmt.Errorf("packager: build %s: build tree layer: %w: %w", platform, err, core.ErrPackageFailed)
	}
	return layer, nil
}

