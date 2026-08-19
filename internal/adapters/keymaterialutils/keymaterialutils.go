// Package keymaterialutils resolves a public-key *setting* — a string that may
// be either PEM text or a path to a PEM file — into the PEM bytes a verifier
// needs.
//
// It exists because that interpretation was previously duplicated with
// different semantics. cmd/pokkum's build path resolved POKKUM_CACHE_PUBKEY as
// "try it as a path, and if os.ReadFile fails treat the string itself as PEM",
// while every adapter reading the same variable (remotecacheutils, provenance,
// baseimage) did a bare []byte(os.Getenv(...)) and so accepted literal PEM only.
// The same environment variable therefore meant two different things depending
// on which code path consumed it: a path worked for `pokkum build` and would
// have been handed to a Cosign verifier verbatim by any other construction.
//
// That is mem:self_review_checklist row 41's shape — one input read in several
// places, whose error handling drifts — and the same class as the
// --sigstore-trusted-root fail-open fixed in W5-a.
package keymaterialutils

import (
	"fmt"
	"os"
	"strings"
)

// IsUtilityPackage marks this as a shared helper rather than a port adapter,
// per the project's utils-package convention.
const IsUtilityPackage = true

// pemPreamble is the marker that identifies a setting as literal PEM text
// rather than a filesystem path.
const pemPreamble = "-----BEGIN"

// Resolve turns a key setting into PEM bytes.
//
// The setting is classified by its *content*, not by whether a file happens to
// exist at that path: anything containing a PEM preamble is literal key
// material, and anything else is treated as a path and must be readable.
//
// That ordering is deliberate. The previous "try ReadFile, fall back to literal
// on any error" shape silently converted a mistyped path into nonsense PEM,
// which surfaced much later as "signature verification failed" — a message that
// sends the reader to look for a tampered artifact or a wrong key when the real
// problem is a typo in a filename. Classifying by content instead lets a bad
// path fail as a bad path.
//
// An empty setting returns nil with no error: "nothing configured" is a
// legitimate state that callers report themselves, with the names of the flags
// and variables that would populate it.
func Resolve(setting, source string) ([]byte, error) {
	// Trimmed only for emptiness and path use. Literal PEM is returned
	// byte-for-byte: trimming it would strip the trailing newline that closes
	// the final -----END----- line, which some PEM readers require.
	trimmed := strings.TrimSpace(setting)
	if trimmed == "" {
		return nil, nil
	}
	if strings.Contains(trimmed, pemPreamble) {
		return []byte(setting), nil
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%s: read public key file %q: %w", source, trimmed, err)
	}
	if !strings.Contains(string(data), pemPreamble) {
		// Reading succeeded but the contents are not a key. Caught here rather
		// than left to the verifier, which would report an opaque parse or
		// signature failure for what is really "you pointed this at the wrong
		// file".
		return nil, fmt.Errorf("%s: file %q does not contain a PEM public key (no %q marker found)", source, trimmed, pemPreamble)
	}
	return data, nil
}

// ResolveFirst walks candidate settings in precedence order and resolves the
// first non-empty one, returning its bytes and the source that supplied it.
//
// Callers pass pairs of (source, setting) so a failure names the specific flag
// or environment variable at fault rather than the whole chain. A non-empty
// setting that fails to resolve is an error, never a reason to continue down
// the chain: falling through would silently verify against a *different* key
// than the operator named, which is the substitution they set the value to
// prevent.
func ResolveFirst(candidates ...Candidate) ([]byte, string, error) {
	for _, c := range candidates {
		if strings.TrimSpace(c.Setting) == "" {
			continue
		}
		data, err := Resolve(c.Setting, c.Source)
		if err != nil {
			return nil, c.Source, err
		}
		return data, c.Source, nil
	}
	return nil, "", nil
}

// Candidate is one link in a key-resolution chain: the human-facing name of the
// flag or environment variable, and the value it currently holds.
type Candidate struct {
	Source  string
	Setting string
}
