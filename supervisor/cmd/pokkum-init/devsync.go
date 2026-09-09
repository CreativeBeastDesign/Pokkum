package main

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

// devSyncSubcommand is argv[1] for the in-pod extractor half of
// `pokkum dev --cluster`. It is mirrored from ports.DevSyncSubcommand for the
// same reason every other constant in this package is duplicated rather than
// imported: pokkum-init ships as a go:embed blob inside the CLI, and
// importing internal/ports would drag go-containerregistry into a program
// whose whole job is to fork one child and count signals.
const devSyncSubcommand = "__dev-sync"

// devSyncDirMode and devSyncFileMode are the modes extracted entries get.
//
// They deliberately differ from the 0555 the packager writes: the layered
// image is built to be immutable, so /app/server and /app/client carry no
// write bit for anyone, including their owner. Extracting into such a tree
// requires temporarily restoring the directory's write bit — which the owner
// may do even without it — and leaving synced directories writable is the
// honest representation of what this pod now is. A pod that has been
// dev-synced is no longer byte-identical to the image it was started from,
// and pretending otherwise by restoring 0555 afterwards would only mean the
// next sync has to undo it again.
const (
	devSyncDirMode  fs.FileMode = 0o755
	devSyncFileMode fs.FileMode = 0o644
)

// errDevModeDisabled is returned when __dev-sync is invoked on a container
// whose spec does not opt in via POKKUM_DEV_MODE.
var errDevModeDisabled = errors.New(envDevMode + " is not set on this container, so in-pod dev sync is disabled")

// devSyncSummary is the single JSON line printed on success. The sending side
// compares these counts against what it streamed; a sync that transferred
// cleanly into a container that wrote none of it is otherwise
// indistinguishable from a working one.
type devSyncSummary struct {
	Files     int   `json:"files"`
	Bytes     int64 `json:"bytes"`
	Restarted bool  `json:"restarted"`
}

// runDevSync is the whole __dev-sync subcommand: read a tar from in, extract
// it into the allowlisted roots, optionally ask the supervisor to restart the
// application, and print a summary to out.
//
// Every parameter that touches the outside world is injected (args, env,
// stdin, stdout, and the signal function) so the extractor can be driven end
// to end in a test with a real tar stream, real files in a temp directory,
// and a recorded signal, with no container and no PID 1 anywhere.
func runDevSync(args []string, getenv func(string) string, in io.Reader, out io.Writer, errOut io.Writer, signal func(pid int, sig syscall.Signal) error) error {
	flags := flag.NewFlagSet("pokkum-init "+devSyncSubcommand, flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.Usage = func() {
		fmt.Fprint(errOut, "usage: pokkum-init "+devSyncSubcommand+" --root DIR [--root DIR...] [--restart]\n\n"+
			"Reads a tar archive from stdin and extracts it into the named directories,\n"+
			"which must already exist. Refuses to run unless "+envDevMode+" is set.\n\nFlags:\n")
		flags.PrintDefaults()
	}
	var roots devSyncRoots
	flags.Var(&roots, "root", "an existing absolute directory entries may be written into; repeatable")
	restart := flags.Bool("restart", false, "signal the supervisor to restart the application process after a successful extraction")
	signalPID := flags.Int("signal-pid", 1, "the supervisor process to signal for --restart; 1 (PID 1) in a container")
	if err := flags.Parse(args); err != nil {
		return err
	}

	// The env gate is checked after flag parsing purely so that --help still
	// works on a production container, and before anything is read from
	// stdin so that a refused sync never leaves a half-streamed archive.
	if !parseBoolEnv(getenv(envDevMode)) {
		return errDevModeDisabled
	}
	if len(roots) == 0 {
		return errors.New("at least one --root is required")
	}
	for _, r := range roots {
		info, err := os.Stat(r)
		switch {
		case err != nil:
			// A missing root is the signature of syncing into an image that
			// was not built with --strategy=layered (an exe or static image
			// has no /app/server at all). Creating it would produce a pod
			// that looks synced and serves nothing.
			return fmt.Errorf("sync root %s: %w", r, err)
		case !info.IsDir():
			return fmt.Errorf("sync root %s is not a directory", r)
		}
	}

	summary, err := extractTar(in, roots)
	if err != nil {
		return err
	}

	if *restart {
		if err := signal(*signalPID, syscall.SIGHUP); err != nil {
			return fmt.Errorf("signal supervisor (pid %d) to restart the application: %w", *signalPID, err)
		}
		summary.Restarted = true
	}

	enc := json.NewEncoder(out)
	if err := enc.Encode(summary); err != nil {
		return fmt.Errorf("write sync summary: %w", err)
	}
	return nil
}

// devSyncRoots collects repeated --root values, normalising each to a clean
// absolute path so containment checks compare like with like.
type devSyncRoots []string

func (r *devSyncRoots) String() string { return strings.Join(*r, ",") }

func (r *devSyncRoots) Set(v string) error {
	v = strings.TrimSpace(v)
	if !path.IsAbs(v) {
		return fmt.Errorf("--root %q must be an absolute path", v)
	}
	clean := path.Clean(v)
	if clean == "/" {
		return errors.New("--root / is not allowed; name the specific directories to sync into")
	}
	*r = append(*r, clean)
	return nil
}

// extractTar writes every entry of the archive in r into whichever root
// contains it, refusing any entry that belongs to none of them.
//
// Containment is enforced twice, deliberately. The lexical check below
// resolves the entry name and requires it to sit under a root at a path
// segment boundary, which rejects both "../.." escapes and the
// "/app/serverevil" near-miss that a bare strings.HasPrefix would accept.
// Writing then goes through os.Root, which additionally refuses to traverse a
// symlink out of the root — the case a lexical check cannot see, and the one
// that matters here because the tree being written into is one a previous
// sync may already have modified.
func extractTar(r io.Reader, roots []string) (devSyncSummary, error) {
	var summary devSyncSummary

	handles := make(map[string]*os.Root, len(roots))
	defer func() {
		for _, h := range handles {
			_ = h.Close()
		}
	}()
	for _, root := range roots {
		// The root itself has to be made writable before its handle is
		// used, and it cannot be done through the handle: mkdirAllIn only
		// restores the write bit on segments BELOW the root, and a file
		// written directly into the root (/app/server/index.js — the single
		// most important file this whole loop exists to replace) has no
		// such segment. The packager ships /app/server as 0555, so without
		// this the very first entry of every real sync fails with
		// "permission denied" on the unlink.
		if err := ensureWritablePath(root); err != nil {
			return summary, err
		}
		h, err := os.OpenRoot(root)
		if err != nil {
			return summary, fmt.Errorf("open sync root %s: %w", root, err)
		}
		handles[root] = h
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return summary, nil
		}
		if err != nil {
			return summary, fmt.Errorf("read sync archive: %w", err)
		}

		abs := path.Clean("/" + hdr.Name)
		root, rel, ok := locate(abs, roots)
		if !ok {
			return summary, fmt.Errorf("archive entry %q resolves to %s, which is outside every --root (%s)", hdr.Name, abs, strings.Join(roots, ", "))
		}
		if rel == "." {
			// The root itself. It already exists (checked by the caller);
			// there is nothing to create and nothing to write.
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirAllIn(handles[root], rel); err != nil {
				return summary, fmt.Errorf("create %s: %w", path.Join(root, rel), err)
			}
		case tar.TypeReg:
			if err := mkdirAllIn(handles[root], path.Dir(rel)); err != nil {
				return summary, fmt.Errorf("create parent of %s: %w", path.Join(root, rel), err)
			}
			n, err := writeFileIn(handles[root], rel, tr)
			if err != nil {
				return summary, fmt.Errorf("write %s: %w", path.Join(root, rel), err)
			}
			summary.Files++
			summary.Bytes += n
		default:
			// The sending side only ever emits directories and regular
			// files, so anything else means the stream is not the one this
			// program is paired with. Refuse rather than skip: a silently
			// ignored entry is a file the pod is missing and nothing says
			// so.
			return summary, fmt.Errorf("archive entry %q has unsupported type %q", hdr.Name, string(rune(hdr.Typeflag)))
		}
	}
}

// locate returns the root containing abs and abs's path relative to it.
// Matching is on whole path segments: "/app/serverevil" is not inside
// "/app/server".
func locate(abs string, roots []string) (root, rel string, ok bool) {
	for _, r := range roots {
		switch {
		case abs == r:
			return r, ".", true
		case strings.HasPrefix(abs, r+"/"):
			return r, strings.TrimPrefix(abs, r+"/"), true
		}
	}
	return "", "", false
}

// mkdirAllIn creates rel and any missing parents inside root, restoring the
// write bit on each existing ancestor it has to descend through.
func mkdirAllIn(root *os.Root, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	segments := strings.Split(rel, "/")
	for i := range segments {
		partial := path.Join(segments[:i+1]...)
		if err := root.Mkdir(partial, devSyncDirMode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		// The packager writes /app directories as 0555 — no write bit for
		// anyone, owner included — so an existing directory has to be made
		// writable before anything can be created inside it. Chmod is
		// permitted to the owner regardless of the current mode, which is
		// why this works at all as the image's non-root user.
		if err := ensureWritable(root, partial); err != nil {
			return err
		}
	}
	return nil
}

// ensureWritablePath is ensureWritable for a path named directly rather than
// relative to an already-open root. It exists only for the roots themselves,
// which are absolute paths supplied by --root and validated by the caller
// before anything is opened.
func ensureWritablePath(abs string) error {
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat sync root %s: %w", abs, err)
	}
	if info.Mode().Perm()&0o200 != 0 {
		return nil
	}
	if err := os.Chmod(abs, info.Mode().Perm()|0o200|0o100); err != nil {
		return fmt.Errorf("make sync root %s writable (the container user must own the /app tree it is syncing into): %w", abs, err)
	}
	return nil
}

// ensureWritable adds the owner write bit to rel inside root when it is
// missing. A failure is returned rather than ignored: it is the exact
// signature of a container running as a user that does not own /app, and a
// sync that proceeds from here would fail later with a much less
// recognisable error.
func ensureWritable(root *os.Root, rel string) error {
	info, err := root.Stat(rel)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o200 != 0 {
		return nil
	}
	if err := root.Chmod(rel, info.Mode().Perm()|0o200|0o100); err != nil {
		return fmt.Errorf("make %s writable (the container user must own the /app tree it is syncing into): %w", rel, err)
	}
	return nil
}

// writeFileIn replaces rel inside root with the contents of src.
//
// The existing file is removed rather than truncated in place: the packager
// writes 0555 files, and O_TRUNC on a file with no write bit fails for the
// owner too. Removing needs only the (now restored) write bit on the parent
// directory, which mkdirAllIn has already ensured.
func writeFileIn(root *os.Root, rel string, src io.Reader) (int64, error) {
	if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, devSyncFileMode)
	if err != nil {
		return 0, err
	}
	n, cerr := io.Copy(f, src)
	if err := f.Close(); err != nil && cerr == nil {
		cerr = err
	}
	return n, cerr
}
