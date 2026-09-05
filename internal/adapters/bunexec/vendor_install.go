package bunexec

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// Production dependency vendoring for the layered strategy.
//
// Why this exists: Vite's SSR build externalises dependencies, so adapter-node
// output keeps bare imports (`valibot`, `bcryptjs`, …) and expects a
// node_modules tree beside it at runtime — its documented deployment model is
// "ship build/ AND production node_modules". Pokkum shipped only the first half.
//
// The consequences were not subtle. On Bun the gap was masked: Bun's runtime
// auto-install silently fetched the missing packages from the public npm
// registry into the running container, so the app appeared to work while
// executing code that is not in the image, not in the SBOM, not covered by the
// signature and not named in the provenance. Under `pokkum resolve`'s own
// readOnlyRootFilesystem the write failed and the pod crash-looped after
// reporting a successful rollout. On Node, which has no auto-install, images
// simply could not serve a route that touched an externalised dependency.
//
// Determinism: the install runs with --frozen-lockfile against the project's own
// lockfile, so versions are exactly what the lockfile pins; the packager then
// normalises every timestamp when it builds the layer. A missing lockfile is a
// hard error rather than a best-effort install, because an unpinned dependency
// tree cannot be reproduced and reproducibility is the product.

// lockfileNames are the lockfiles `bun install` can honour, in the order they
// are looked for.
var lockfileNames = []string{"bun.lock", "bun.lockb", "package-lock.json"}

// vendorStampFilename is the digest sidecar recording which (package.json,
// lockfile) content pair the tree in this staging directory was installed
// from. It lives beside node_modules inside .pokkum/vendor, never in the
// user's own tree.
const vendorStampFilename = ".pokkum-vendor.json"

// vendorStampSchema versions the staleness key's *inputs*, not the file
// format. Bump it whenever the install arguments change (adding a flag that
// alters what lands in node_modules, say), so every existing staged tree is
// treated as stale rather than silently reused under semantics it was not
// installed with.
const vendorStampSchema = "1"

// vendorStamp records a completed install: the content key of the inputs it
// was performed from, and whether it actually produced a node_modules tree.
//
// HasModules is carried explicitly rather than re-derived by stat'ing the
// directory, because "this project's dependencies are all devDependencies, so
// there is legitimately nothing to vendor" and "the tree was installed and has
// since been deleted" are different states that a bare stat cannot tell apart
// — and only the first is safe to serve from cache.
type vendorStamp struct {
	Schema     string `json:"schema"`
	Key        string `json:"key"`
	HasModules bool   `json:"hasModules"`
}

// stageProductionDependencies installs the project's production dependencies
// into a staging tree under .pokkum/ and returns the path of the resulting
// node_modules directory.
//
// Nothing in the user's own tree is written to: the staging directory holds a
// copy of package.json and the lockfile, and bun installs beneath it.
//
// The install is skipped entirely when the staged tree is already current.
// Currency is decided on the *content* of package.json and the lockfile — never
// on their modification times. An mtime key looks equivalent and is not:
// checking out an older lockfile, reverting a dependency bump, or restoring a
// file from a backup all produce content that must trigger a reinstall while
// carrying a timestamp older than the staged tree, and an mtime comparison
// would serve the wrong dependency versions into the image with no error
// anywhere. See vendorCacheKey.
func stageProductionDependencies(ctx context.Context, projectDir string, hermetic bool, log *slog.Logger) (string, error) {
	manifestPath := filepath.Join(projectDir, "package.json")
	manifest, err := os.ReadFile(manifestPath) //nolint:gosec // path is constructed from the caller's own project dir
	if err != nil {
		return "", fmt.Errorf("bunexec: vendor: no package.json in %s: %w", projectDir, core.ErrInvalidRequest)
	}

	// A lockfile is strongly preferred but not required. Refusing to build
	// without one would turn a reproducibility preference into a hard gate on
	// building at all, which is the wrong trade for a tool whose first job is
	// producing a working image — and every project without a lockfile would be
	// refused outright. Warn instead, loudly, and say what it costs.
	lockName, lockPath := findLockfile(projectDir)
	var lock []byte
	if lockName == "" {
		log.Warn("bunexec: vendor: no lockfile found; installing production dependencies unpinned. "+
			"The image will work, but this build is not reproducible: a rebuild may resolve different versions. "+
			"Commit a lockfile to pin them",
			"projectDir", projectDir, "lookedFor", lockfileNames)
	} else {
		lock, err = os.ReadFile(lockPath) //nolint:gosec // path came from findLockfile over the caller's own project dir
		if err != nil {
			return "", fmt.Errorf("bunexec: vendor: read %s: %w", lockName, err)
		}
	}

	stage := filepath.Join(projectDir, ".pokkum", "vendor")
	key := vendorCacheKey(manifest, lockName, lock)

	// Reuse before wipe. Without this the whole production dependency tree was
	// deleted and re-materialised on every single build, including the
	// overwhelmingly common one where neither package.json nor the lockfile
	// changed since the last build — several seconds of pure waste per build.
	if modules, hit := reuseStagedVendorTree(stage, key); hit {
		log.Info("bunexec: vendor: reusing staged production dependencies; package.json and the lockfile are unchanged",
			"dir", stage, "lockfile", lockName, "modules", modules)
		return modules, nil
	}

	if err := os.RemoveAll(stage); err != nil {
		return "", fmt.Errorf("bunexec: vendor: clear staging dir: %w", err)
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", fmt.Errorf("bunexec: vendor: create staging dir: %w", err)
	}
	// Staged from the bytes the cache key was computed over, not by re-reading
	// the source files: the install must be performed against exactly the
	// content the stamp will claim it was performed against.
	if err := os.WriteFile(filepath.Join(stage, "package.json"), manifest, 0o600); err != nil {
		return "", fmt.Errorf("bunexec: vendor: stage package.json: %w", err)
	}
	if lockName != "" {
		if err := os.WriteFile(filepath.Join(stage, lockName), lock, 0o600); err != nil {
			return "", fmt.Errorf("bunexec: vendor: stage %s: %w", lockName, err)
		}
	}

	args := []string{"install", "--production", "--no-save"}
	if lockName != "" {
		// Exactly the versions the lockfile pins, or fail — never a silent
		// resolution drift in a build that claims to be reproducible.
		args = append(args, "--frozen-lockfile")
	}
	if hermetic {
		// A hermetic build has no egress, so the install must be served
		// entirely from Bun's local cache. If it is cold this fails, which is
		// the correct outcome: silently shipping an image without its
		// dependencies is what this whole change exists to stop.
		args = append(args, "--offline")
	}

	cmd := exec.CommandContext(ctx, "bun", args...)
	cmd.Dir = stage
	// The install runs concurrently with the SvelteKit build (see vendorJob),
	// so a cancelled build must be able to tear it down promptly. bun install
	// forks helpers that inherit the CombinedOutput pipes; without a process
	// group kill and a WaitDelay backstop, cancelling the context kills the
	// direct child while Wait blocks on a grandchild still holding the pipe.
	setNewProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		hint := ""
		if hermetic {
			hint = " Hermetic builds install from Bun's local cache only; run a normal `bun install` once to warm it, or build without --hermetic."
		}
		return "", fmt.Errorf("bunexec: vendor: bun install --production failed in %s: %w: %s.%s", stage, err, string(out), hint)
	}

	modules := filepath.Join(stage, "node_modules")
	info, statErr := os.Stat(modules)
	hasModules := statErr == nil && info.IsDir()
	if !hasModules {
		// Legitimately possible: a project whose dependencies are all
		// devDependencies. Report it rather than treating it as failure.
		modules = ""
		log.Info("bunexec: vendor: no production dependencies to vendor", "projectDir", projectDir, "lockfile", lockName)
	}

	// Written only now, after an install that actually succeeded. A stamp
	// written before or alongside a failing install would turn the next build
	// into a cache hit on a tree that was never completed.
	if err := writeVendorStamp(stage, key, hasModules); err != nil {
		return "", fmt.Errorf("bunexec: vendor: %w", err)
	}

	if hasModules {
		log.Info("bunexec: vendor: production dependencies staged", "dir", modules, "lockfile", lockName, "hermetic", hermetic)
	}
	return modules, nil
}

// vendorCacheKey is the content digest of everything that decides what
// `bun install --production` puts in node_modules: the manifest, which
// lockfile format governs, and that lockfile's bytes.
//
// Each field is length-prefixed so no two different input tuples can
// concatenate to the same byte stream (a manifest ending in what looks like a
// lockfile name would otherwise be able to collide with a shorter one).
//
// Known gap, recorded rather than papered over: the *Bun version* is not in
// the key, so upgrading bun reuses a tree the previous bun staged. Keying on
// it needs the version threaded down here, and ports.PrepareRequest does not
// carry one (only PreflightResult does), so it would be a ports change. In
// practice a --frozen-lockfile production install of identical inputs is
// stable across bun patch releases, and `rm -rf .pokkum/vendor` is the escape
// hatch; the roadmap's monorepo-vendor-cache item is where this belongs.
//
// Deliberately NOT in the key: req.Hermetic. It changes where bun is allowed
// to fetch from, not what the lockfile resolves to — an already-staged tree is
// byte-identical either way, and excluding it means a hermetic build can reuse
// a tree a previous non-hermetic build staged instead of failing on a cold
// offline cache. No egress happens on a cache hit, so hermeticity is not
// weakened by the reuse.
func vendorCacheKey(manifest []byte, lockName string, lock []byte) string {
	h := sha256.New()
	writeKeyField := func(b []byte) {
		var lenBuf [8]byte
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(b)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(b)
	}
	writeKeyField([]byte(vendorStampSchema))
	writeKeyField(manifest)
	writeKeyField([]byte(lockName))
	writeKeyField(lock)
	return hex.EncodeToString(h.Sum(nil))
}

// reuseStagedVendorTree reports whether the staging directory already holds a
// completed install of exactly these inputs, returning the node_modules path
// to serve (empty when the recorded install legitimately produced none).
//
// Anything unexpected — no stamp, an unparseable stamp, a different schema, a
// different key, or a recorded tree that is no longer on disk — is a miss, and
// a miss is always safe: it costs one reinstall.
func reuseStagedVendorTree(stage, key string) (modules string, hit bool) {
	data, err := os.ReadFile(filepath.Join(stage, vendorStampFilename)) //nolint:gosec // path is under the caller's own project dir
	if err != nil {
		return "", false
	}
	var stamp vendorStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return "", false
	}
	if stamp.Schema != vendorStampSchema || stamp.Key != key {
		return "", false
	}
	if !stamp.HasModules {
		return "", true
	}
	modules = filepath.Join(stage, "node_modules")
	if info, err := os.Stat(modules); err != nil || !info.IsDir() {
		return "", false
	}
	return modules, true
}

// writeVendorStamp records a completed install in the staging directory.
func writeVendorStamp(stage, key string, hasModules bool) error {
	data, err := json.Marshal(vendorStamp{Schema: vendorStampSchema, Key: key, HasModules: hasModules})
	if err != nil {
		return fmt.Errorf("encode staleness stamp: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stage, vendorStampFilename), data, 0o600); err != nil {
		return fmt.Errorf("write staleness stamp: %w", err)
	}
	return nil
}

// vendorJob is one production-dependency install running concurrently with the
// SvelteKit build.
//
// The two are independent by construction: the install reads package.json and
// the lockfile and writes only into .pokkum/vendor, while the Vite build reads
// src/ and node_modules/ and writes build/. Serializing them cost a 3-10s
// install in front of a 20-60s build for no reason.
//
// Ownership is the whole point of the type. The install holds a subprocess, so
// every path out of Prepare between dispatch and join — a hermetic sandbox
// verification failure, a cmd.Start failure, a build error — must tear it down.
// Rather than a guard at each of those returns (which only holds until the next
// edit adds a return nobody re-checked, mem:self_review_checklist row 1), the
// caller registers a single `defer job.stop()` at the point of dispatch:
// cleanup is attached to the allocation, so a leak is unreachable by
// construction regardless of how Prepare returns.
type vendorJob struct {
	done   chan struct{}
	cancel context.CancelFunc

	// Written by the goroutine before done is closed, read by join/stop only
	// after receiving from done — the channel close is what orders the two.
	modulesDir string
	err        error
}

// startVendorInstall begins staging production dependencies in the background.
// The caller MUST `defer job.stop()` immediately.
func startVendorInstall(ctx context.Context, projectDir string, hermetic bool, log *slog.Logger) *vendorJob {
	installCtx, cancel := context.WithCancel(ctx)
	job := &vendorJob{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(job.done)
		job.modulesDir, job.err = stageProductionDependencies(installCtx, projectDir, hermetic, log)
	}()
	return job
}

// join blocks until the install finishes and returns its result. A nil job
// (no install was dispatched for this strategy) joins to a clean empty result,
// so callers need no nil check of their own.
func (j *vendorJob) join() (string, error) {
	if j == nil {
		return "", nil
	}
	<-j.done
	return j.modulesDir, j.err
}

// stop cancels the install if it is still running and blocks until the
// goroutine has exited and the bun process is reaped. Idempotent, and safe to
// call after join — on the normal path it is already finished and stop only
// releases the context.
func (j *vendorJob) stop() {
	if j == nil {
		return
	}
	j.cancel()
	<-j.done
}

// findLockfile returns the first recognised lockfile in projectDir as
// (name, absolute path), or ("", "") when none is present.
func findLockfile(projectDir string) (string, string) {
	for _, name := range lockfileNames {
		p := filepath.Join(projectDir, name)
		if _, err := os.Stat(p); err == nil {
			return name, p
		}
	}
	return "", ""
}
