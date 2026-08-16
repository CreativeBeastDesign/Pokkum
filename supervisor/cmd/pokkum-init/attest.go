package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
var attestRoots = []string{
	"/app/server",
	"/app/client",
	"/app/prerendered",
	"/app/vendor",
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

// walkAttestTree walks every attestation root that exists and returns the
// records for all regular files under it. Absent roots contribute nothing,
// matching the packager which only archives roots whose staging directory
// existed. Only regular files are hashed; directories and any non-regular
// entries (sockets, devices) are ignored, as the packager archives only
// regular files too.
func walkAttestTree() ([]attestRecord, error) {
	var records []attestRecord
	for _, root := range attestRoots {
		dir := filepath.FromSlash(root)
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue // absent root contributes nothing, on both sides
		}
		// baseRel is root relative to /app without the leading slash, e.g.
		// "server" for "/app/server".
		baseRel := strings.TrimPrefix(root, attestAppDir+"/")
		err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil || !info.Mode().IsRegular() {
				return nil // skip non-regular entries, matching the packager
			}
			sub, rerr := filepath.Rel(dir, p)
			if rerr != nil {
				return rerr
			}
			rel := filepath.ToSlash(filepath.Join(baseRel, sub))
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			sum := sha256.Sum256(data)
			records = append(records, attestRecord{rel: rel, sha: hex.EncodeToString(sum[:])})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return records, nil
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
