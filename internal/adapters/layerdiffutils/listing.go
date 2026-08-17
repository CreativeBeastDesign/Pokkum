package layerdiffutils

import (
	"archive/tar"
	"fmt"
	"io"
	"path"
	"strings"
)

// whiteoutPrefix marks an OCI/AUFS whiteout entry: the presence of a file
// named ".wh.<name>" in a layer means "<name>" was deleted by that layer.
const whiteoutPrefix = ".wh."

// opaqueWhiteoutName marks an entire directory as reset by a layer: every
// entry an earlier layer contributed under that directory is gone, even
// though none of them carry their own per-file whiteout.
const opaqueWhiteoutName = ".wh..wh..opq"

// TarEntry describes a single entry within a layer's tar stream.
type TarEntry struct {
	Path     string
	Size     int64
	Mode     int64
	Typeflag byte
}

// IsWhiteout reports whether the entry deletes a sibling file: its base name
// starts with whiteoutPrefix and it is not itself the opaque marker.
func (e TarEntry) IsWhiteout() bool {
	base := path.Base(e.Path)
	return strings.HasPrefix(base, whiteoutPrefix) && base != opaqueWhiteoutName
}

// WhitesOutPath reports the path this whiteout entry deletes, given IsWhiteout
// is true. E.g. "app/.wh.old.txt" whites out "app/old.txt".
func (e TarEntry) WhitesOutPath() string {
	dir := path.Dir(e.Path)
	name := strings.TrimPrefix(path.Base(e.Path), whiteoutPrefix)
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// IsOpaqueWhiteout reports whether the entry resets the directory it lives in.
func (e TarEntry) IsOpaqueWhiteout() bool {
	return path.Base(e.Path) == opaqueWhiteoutName
}

// OpaqueWhiteoutDir reports the directory this opaque whiteout entry resets,
// given IsOpaqueWhiteout is true.
func (e TarEntry) OpaqueWhiteoutDir() string {
	return path.Dir(e.Path)
}

// ListTarPaths walks a tar stream and returns each entry's path and metadata,
// including OCI whiteout markers as literal entries (see IsWhiteout /
// IsOpaqueWhiteout) — callers needing overlay-delete semantics inspect the
// returned entries rather than having them filtered out here.
func ListTarPaths(r io.Reader) ([]TarEntry, error) {
	tr := tar.NewReader(r)
	var out []TarEntry

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("layerdiffutils adapter: read tar entry: %w", err)
		}

		out = append(out, TarEntry{
			Path:     hdr.Name,
			Size:     hdr.Size,
			Mode:     hdr.Mode,
			Typeflag: hdr.Typeflag,
		})
	}

	return out, nil
}
