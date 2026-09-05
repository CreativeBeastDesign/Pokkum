package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Startup attestation (layered hardening Option C).
//
// At build time the packager computes a SHA-256 root digest over the
// authoritative set of files it actually archives into the layered /app tree
// (server, client, prerendered, vendor, native — with pruning and sidecar
// generation already applied) and stamps the expected digest into the image
// config via POKKUM_ATTESTATION_DIGEST. At startup this file re-derives the
// same digest by walking the live /app tree, and refuses to exec the child on
// mismatch. That restores the tamper-evidence property of a sealed artifact
// without depending on cluster-level readonly-rootfs policy.
//
// The record shape, serialization and walk scoping below are deliberately a
// verbatim mirror of internal/adapters/attestutils (which the packager uses)
// and the supervisor's copied root list. The two must never drift — the parity
// test in internal/adapters/attestutils/attestutils_test.go proves the mirrored
// logic here agrees with the packager's on the same tree, and is the tripwire
// if either side changes.

// attestAppDir is the image working directory the attestation scopes to. The
// roots below are children of it. It is a variable, not a constant, so the unit
// tests can point the whole attestation namespace at a temporary directory
// without touching the real /app.
var attestAppDir = "/app"

// attestRoots mirrors ports.AttestationRoots: the fixed set of in-image
// directories covered. Order does not matter for the digest (records are
// globally sorted by relative path), so this need only contain the same set.
//
// This list cannot import ports (see the package comment), so it is a literal
// hand-copy — and a hand-copy that silently fell out of sync is precisely how
// every layered image came to refuse to start once the packager began
// archiving /app/node_modules without adding it here. The copy is therefore
// no longer trusted to reviewers: TestAttestationRoots_MatchSupervisorMirror
// parses THIS declaration out of THIS file with go/ast and fails if it does
// not equal ports.AttestationRoots element-for-element. Keep the entries as
// plain string literals, one per line, or that parser cannot read them.
var attestRoots = []string{
	"/app/server",
	"/app/client",
	"/app/prerendered",
	"/app/vendor",
	"/app/node_modules",
	"/app/native",
}

// attestRecord is one regular file's contribution, mirroring
// attestutils.Record.
type attestRecord struct {
	rel string // slash-separated, relative to /app, e.g. "server/index.js"
	sha string // lowercase hex SHA-256 of the file bytes
}

// isHexDigest reports whether s is a well-formed 64-char lowercase hex SHA-256
// digest, the only shape the packager stamps. Anything else is treated as
// "no expectation" (attestation disabled), so a malformed env value can never
// wedge a container at startup.
func isHexDigest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// verifyAttestation walks the live /app tree and compares its root digest
// against the expected value. When expected is empty (attestation disabled) it
// returns nil immediately. On mismatch it returns an error describing the
// expectation so an operator can diagnose a tampered or mis-built image.
func verifyAttestation(log *slog.Logger, expected string) error {
	if expected == "" {
		log.Debug("startup attestation disabled (no expected digest set)")
		return nil
	}

	// Only verify the sub-tree when /app itself exists; if the working dir is
	// missing entirely the child could not run anyway, and the walk below
	// would report every root absent and match a *non-empty* expected digest
	// vacuously. Guard explicitly instead of trusting that accident.
	if fi, err := os.Stat(attestAppDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("startup attestation: %s is not a directory", attestAppDir)
	}

	records, err := walkAttestTree()
	if err != nil {
		return fmt.Errorf("startup attestation: %w", err)
	}
	got := attestRootDigest(records)
	if got != expected {
		return fmt.Errorf(
			"startup attestation mismatch: /app runtime tree does not match the build-time manifest (expected %s, got %s); refusing to start. This means /app was modified after the image was built (e.g. a mutated volume or an injected payload) or the image is corrupted",
			expected, got)
	}
	log.Info("startup attestation verified", "digest", got, "files", len(records))
	return nil
}

// attestHashBufSize is the read buffer one hashing worker streams a file
// through. Files are hashed with a fixed-size buffer rather than read whole
// (the previous root.ReadFile + sha256.Sum256 shape), so peak memory is
// workers x this constant instead of "the largest file in /app", and the
// 128 MB/op a 100 MB tree used to allocate collapses to a few hundred KB.
const attestHashBufSize = 128 << 10

// attestHashBufPool recycles those buffers across walks and across workers.
// Held as *[]byte so putting one back does not allocate a slice header on the
// heap (staticcheck SA6002).
var attestHashBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, attestHashBufSize)
		return &b
	},
}

// attestTask is one file the hashing phase must read: which open root handle
// to resolve it against, its path relative to that root, and the /app-relative
// path that goes into the record.
type attestTask struct {
	root int    // index into the []*os.Root the walk phase opened
	sub  string // path relative to that root, e.g. "chunks/index.js"
	rel  string // slash-separated, relative to /app, e.g. "server/chunks/index.js"
}

// walkAttestTree walks every attestation root that exists and returns the
// records for all regular files under it. Absent roots contribute nothing,
// matching the packager which only archives roots whose staging directory
// existed. Only regular files are hashed; directories and any non-regular
// entries (sockets, devices) are ignored, as the packager archives only
// regular files too.
//
// It runs in two phases, deliberately separated:
//
//  1. Walk every root and collect the (root, sub, rel) triples. This phase is
//     the only one that can fail for a reason other than reading a file, and
//     it runs with zero goroutines in flight — so no fallible decision ever
//     sits between a dispatch and its Wait (mem:self_review_checklist row 1,
//     the validate-then-dispatch shape).
//  2. Hash the collected triples across GOMAXPROCS workers.
//
// Splitting them is also what makes the fan-out balanced: one fan-out covers
// every root at once, so a tiny /app/native cannot leave workers idle while
// /app/node_modules is still being hashed by one goroutine.
//
// The digest is unaffected by any of this: records are returned in walk order
// and attestRootDigest sorts globally by rel before folding, so worker count
// and completion order cannot change the result. TestAttestParallelHashing_
// DigestStableAcrossWorkerCounts pins that.
func walkAttestTree() ([]attestRecord, error) {
	roots, tasks, err := collectAttestTasks()
	// Close every handle on every return path, including the error paths
	// below — registered immediately after collectAttestTasks returns them
	// (mem:self_review_checklist row 2: cleanup at allocation, not at
	// confirmed success; collectAttestTasks returns the handles it managed to
	// open even when it then fails).
	defer func() {
		for _, r := range roots {
			if r != nil {
				_ = r.Close()
			}
		}
	}()
	if err != nil {
		return nil, err
	}
	return hashAttestTasks(roots, tasks)
}

// collectAttestTasks opens an os.Root per existing attestation root and walks
// each one, returning the open handles and the flat task list. The handles are
// returned even on error so the caller's deferred close covers the ones that
// were opened before the failure.
//
// filepath.WalkDir does not follow symlinks, and the d.Info().Mode().IsRegular()
// filter excludes any symlink the walk reports, so no statically-present
// symlink is ever hashed. What the os.Root handle adds is closing the TOCTOU
// window that filter cannot: between the lstat behind d.Info() and the read,
// the entry can be replaced by a symlink pointing out of /app. openAttestFile
// resolves against the root handle, so such a replacement is refused instead
// of silently hashing foreign bytes into the digest the container's startup
// gate trusts. See gosec G122.
func collectAttestTasks() ([]*os.Root, []attestTask, error) {
	var (
		roots []*os.Root
		tasks []attestTask
	)
	for _, root := range attestRoots {
		dir := filepath.FromSlash(root)
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue // absent root contributes nothing, on both sides
		}
		// baseRel is root relative to /app without the leading slash, e.g.
		// "server" for "/app/server".
		baseRel := strings.TrimPrefix(root, attestAppDir+"/")

		// A directory that os.Stat reported as a directory but that cannot be
		// opened would fail the walk below with the same error, since WalkDir
		// has to open it to read its entries — so this is not a new way for a
		// container to refuse to boot, just an earlier report of the same one.
		rootHandle, err := os.OpenRoot(dir)
		if err != nil {
			return roots, nil, err
		}
		roots = append(roots, rootHandle)
		idx := len(roots) - 1

		err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			// d.Type(), not d.Info(): Type() serves the mode bits the
			// directory read already carried (falling back to an lstat only
			// when the filesystem reported DT_UNKNOWN), so the common case
			// costs neither a syscall nor a FileInfo allocation per file.
			// Mode().IsRegular() and Type().IsRegular() test the same bits.
			if !d.Type().IsRegular() {
				return nil // skip non-regular entries, matching the packager
			}
			sub, rerr := filepath.Rel(dir, p)
			if rerr != nil {
				return rerr
			}
			tasks = append(tasks, attestTask{
				root: idx,
				sub:  sub,
				rel:  filepath.ToSlash(filepath.Join(baseRel, sub)),
			})
			return nil
		})
		if err != nil {
			return roots, nil, err
		}
	}
	return roots, tasks, nil
}

// hashAttestTasks hashes every task across runtime.GOMAXPROCS(0) workers,
// each streaming its file through a pooled buffer, and returns the records in
// task order.
//
// Fail-closed contract, which is the whole reason this function exists in this
// shape: ANY worker error aborts the WHOLE verification. It is recorded as the
// returned error and no records are returned at all, so a partial result can
// never be folded into a digest and can never be compared against the expected
// value. A file that cannot be opened or read is a verification failure, not a
// file that contributes nothing.
func hashAttestTasks(roots []*os.Root, tasks []attestTask) ([]attestRecord, error) {
	if len(tasks) == 0 {
		return nil, nil
	}

	records := make([]attestRecord, len(tasks))

	workers := runtime.GOMAXPROCS(0)
	if workers > len(tasks) {
		workers = len(tasks)
	}
	if workers < 1 {
		workers = 1
	}

	var (
		next     atomic.Int64
		aborted  atomic.Bool
		errMu    sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		aborted.Store(true)
	}

	// Nothing fallible sits between this dispatch loop and wg.Wait(): every
	// error is routed through fail() and read only after the join, so there is
	// no return path that can leak a worker (row 1).
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			bufp, _ := attestHashBufPool.Get().(*[]byte)
			defer attestHashBufPool.Put(bufp)
			h := sha256.New()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(tasks) {
					return
				}
				if aborted.Load() {
					return
				}
				t := tasks[i]
				sum, err := hashAttestFile(roots[t.root], t.sub, *bufp, h)
				if err != nil {
					fail(fmt.Errorf("hashing %s: %w", t.rel, err))
					return
				}
				// Exactly one goroutine ever writes records[i] (indices are
				// handed out by a single atomic counter), so this needs no
				// lock and is race-clean under -race.
				records[i] = attestRecord{rel: t.rel, sha: sum}
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return records, nil
}

// hashAttestFile streams one attested file through h and returns its lowercase
// hex SHA-256. buf is the caller's reusable read buffer and h is reset before
// use, so hashing a file allocates nothing but the returned digest string —
// the digest is byte-identical to the previous sha256.Sum256(wholeFile) shape
// because SHA-256 is a streaming construction and chunking cannot change it.
func hashAttestFile(root *os.Root, sub string, buf []byte, h hash.Hash) (string, error) {
	f, err := openAttestFile(root, sub)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h.Reset()
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return "", rerr
		}
	}
	var sum [sha256.Size]byte
	return hex.EncodeToString(h.Sum(sum[:0])), nil
}

// openAttestFile opens one attested file through root, where sub is the file's
// path relative to root. Resolution cannot leave root: a component (or the
// final element) that is a symlink whose target escapes the attestation root
// is an error, not a followed link, while a relative symlink resolving back
// inside the root is followed exactly as os.Open would follow it. Kept as its
// own function so the containment property has a direct, testable seam — see
// TestOpenAttestFile_RefusesEscapingSymlink.
//
// The caller owns the returned handle and must close it.
func openAttestFile(root *os.Root, sub string) (*os.File, error) {
	return root.Open(sub)
}

// attestRootDigest mirrors attestutils.RootDigest: the deterministic SHA-256
// over every record's "<rel>\x00<sha>\n", globally sorted by rel.
func attestRootDigest(records []attestRecord) string {
	sorted := append([]attestRecord(nil), records...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].rel < sorted[j].rel })
	h := sha256.New()
	for _, r := range sorted {
		h.Write([]byte(r.rel))
		h.Write([]byte{0})
		h.Write([]byte(r.sha))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
