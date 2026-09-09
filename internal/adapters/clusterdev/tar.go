// Package clusterdev implements ports.ClusterDevSyncer on top of the kubectl
// binary: it locates a running pod by label selector, streams freshly built
// SvelteKit output into it as a tar, and asks the in-pod pokkum-init to
// restart the application process.
//
// kubectl is shelled out to rather than linking client-go, matching the
// existing cluster-facing code in cmd/pokkum (newKubectlClusterInspector) and
// the project's zero-dependency posture. The practical consequence is that
// this adapter inherits the user's kubeconfig, current context, exec
// credential plugins and RBAC for free, which is exactly what a developer
// pointing a dev loop at their own cluster expects.
//
// Unlike every adapter on the build path, this one is a side effect against a
// live system: it is never called from core.Build, and the determinism and
// zero-clock invariants that bind the packager do not apply to it.
package clusterdev

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TarStats is what a WriteTar call actually put on the wire.
type TarStats struct {
	// Files and Bytes count regular files and their total content size.
	Files int
	Bytes int64

	// Skipped names every entry that was deliberately not sent because it
	// is not a regular file or directory — a symlink, socket, fifo or
	// device node. They are named rather than counted so the caller can log
	// them: a build output tree should contain none of these, so any entry
	// here means either the local tree is not what we think it is, or the
	// dev loop is quietly failing to sync something the image would have
	// contained.
	Skipped []string
}

// WriteTar streams dirs into w as a single uncompressed tar archive.
//
// Entry names are the in-pod absolute destination with the leading slash
// removed (so /app/server/index.js is written as "app/server/index.js"),
// which is the ordinary tar convention and lets the extractor root itself at
// "/" and treat every name as relative — the shape that makes traversal
// containment expressible with os.Root rather than with string checks.
//
// Directory entries are emitted for every directory that ends up containing
// something, ahead of their contents, so the extractor never has to infer a
// parent from a file path.
func WriteTar(w io.Writer, dirs []ports.ClusterSyncDir) (TarStats, error) {
	var stats TarStats
	tw := tar.NewWriter(w)

	for _, d := range dirs {
		if err := writeOneDir(tw, d, &stats); err != nil {
			return stats, err
		}
	}

	if err := tw.Close(); err != nil {
		return stats, fmt.Errorf("clusterdev: finish tar stream: %w: %w", err, core.ErrInvalidRequest)
	}
	return stats, nil
}

func writeOneDir(tw *tar.Writer, d ports.ClusterSyncDir, stats *TarStats) error {
	root, err := filepath.Abs(d.LocalDir)
	if err != nil {
		return fmt.Errorf("clusterdev: resolve %s: %w: %w", d.LocalDir, err, core.ErrInvalidRequest)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("clusterdev: stat %s: %w: %w", root, err, core.ErrInvalidRequest)
	}
	if !info.IsDir() {
		return fmt.Errorf("clusterdev: %s is not a directory: %w", root, core.ErrInvalidRequest)
	}

	prefix := strings.TrimPrefix(path.Clean(d.RemoteDir), "/")
	if prefix == "" || prefix == "." {
		return fmt.Errorf("clusterdev: remote directory %q must be an absolute path below the filesystem root: %w", d.RemoteDir, core.ErrInvalidRequest)
	}

	return filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("clusterdev: walk %s: %w: %w", p, err, core.ErrInvalidRequest)
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("clusterdev: relativize %s against %s: %w: %w", p, root, err, core.ErrInvalidRequest)
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)

		// Exclusions are matched on the FIRST path segment only, mirroring
		// the packager's own ExcludeDirs semantics for the /app/server
		// layer (internal/adapters/packager/packager.go): "client" means
		// the client tree at the output root, not every directory named
		// "client" anywhere in the bundle.
		if entry.IsDir() && !strings.Contains(slashRel, "/") && slices.Contains(d.ExcludeDirs, slashRel) {
			return fs.SkipDir
		}

		name := prefix + "/" + slashRel

		switch {
		case entry.IsDir():
			return writeHeader(tw, &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     name + "/",
				Mode:     0o755,
				Format:   tar.FormatPAX,
			})
		case entry.Type().IsRegular():
			fi, err := entry.Info()
			if err != nil {
				return fmt.Errorf("clusterdev: stat %s: %w: %w", p, err, core.ErrInvalidRequest)
			}
			if err := writeHeader(tw, &tar.Header{
				Typeflag: tar.TypeReg,
				Name:     name,
				Mode:     int64(fi.Mode().Perm()),
				Size:     fi.Size(),
				Format:   tar.FormatPAX,
			}); err != nil {
				return err
			}
			f, err := os.Open(p) //nolint:gosec // p comes from walking a caller-supplied build output directory.
			if err != nil {
				return fmt.Errorf("clusterdev: open %s: %w: %w", p, err, core.ErrInvalidRequest)
			}
			// Closed on every path below, including the copy error, rather
			// than deferred to the end of the whole walk — a deferred close
			// inside a WalkDir callback holds every descriptor of a large
			// output tree open at once.
			n, err := io.Copy(tw, f)
			cerr := f.Close()
			if err != nil {
				return fmt.Errorf("clusterdev: read %s: %w: %w", p, err, core.ErrInvalidRequest)
			}
			if cerr != nil {
				return fmt.Errorf("clusterdev: close %s: %w: %w", p, cerr, core.ErrInvalidRequest)
			}
			// A file that changed size between the header and the copy
			// would desynchronise the whole stream: tar has already
			// committed to fi.Size() bytes. Fail loudly instead of
			// shipping an archive whose next header lands mid-content.
			if n != fi.Size() {
				return fmt.Errorf("clusterdev: %s changed size during read (header said %d, copied %d): %w", p, fi.Size(), n, core.ErrInvalidRequest)
			}
			stats.Files++
			stats.Bytes += n
			return nil
		default:
			stats.Skipped = append(stats.Skipped, p)
			return nil
		}
	})
}

func writeHeader(tw *tar.Writer, h *tar.Header) error {
	if err := tw.WriteHeader(h); err != nil {
		return fmt.Errorf("clusterdev: write tar header %s: %w: %w", h.Name, err, core.ErrInvalidRequest)
	}
	return nil
}
