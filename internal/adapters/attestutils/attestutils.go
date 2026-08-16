// Package attestutils defines the shared record shape and deterministic
// aggregate-digest algorithm for Pokkum's layered startup attestation
// (supervisor hardening Option C).
//
// The packager computes the expected digest at build time over the
// authoritative set of files it actually archives into the layered /app tree
// (server, client, prerendered, vendor, native — with pruning and sidecar
// generation already applied, because what is archived is what the container
// will see at runtime). pokkum-init independently re-derives the same digest
// at startup by walking the live /app tree and hashing what it finds, then
// refuses to exec the child on mismatch.
//
// Both sides MUST agree on the record shape and the exact serialization, or a
// healthy image would not start. The pokkum-init binary cannot import this
// package (it is embedded into the CLI with go:embed and must not drag Go port
// types into a program whose only job is to fork one child), so the supervisor
// mirrors the two small functions here verbatim. The parity test in
// attestutils_test.go exercises the mirrored supervisor logic against the same
// synthetic trees to prove the two implementations never drift.
package attestutils

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// recordSeparator separates a relative path from its content hash within each
// serialized record. It is a NUL byte so a path can never collide with it.
const recordSeparator = "\x00"

// Record is one regular file's contribution to the attestation digest.
type Record struct {
	// Rel is the file's slash-separated path relative to /app. The layered
	// trees mount under /app/server, /app/client, /app/prerendered,
	// /app/vendor and /app/native, so a server file is "server/index.js" and a
	// client asset is "client/_app/immutable/x.js".
	Rel string

	// SHA is the lowercase hex SHA-256 of the file's bytes.
	SHA string
}

// RootDigest computes the deterministic aggregate digest over records.
//
// The serialization is deliberately byte-stable: every record contributes
// "<Rel>\x00<SHA>\n", the records are globally sorted by Rel, and the whole
// concatenation is hashed once with SHA-256. Sorting by Rel (not insertion
// order) removes any dependence on directory-walk order or root iteration
// order, so two machines that archive the same tree always agree.
//
// A nil or empty records slice is legal and yields a well-defined (non-empty)
// digest, so an image with an empty /app is attestable rather than a special
// case that would need an error path.
func RootDigest(records []Record) string {
	// The records slice is borrowed, not owned: sort a copy so the caller's
	// slice ordering is left untouched.
	sorted := slices.Clone(records)
	slices.SortFunc(sorted, func(a, b Record) int {
		switch {
		case a.Rel < b.Rel:
			return -1
		case a.Rel > b.Rel:
			return 1
		default:
			return 0
		}
	})

	h := sha256.New()
	for _, r := range sorted {
		h.Write([]byte(r.Rel))
		h.Write([]byte(recordSeparator))
		h.Write([]byte(r.SHA))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
