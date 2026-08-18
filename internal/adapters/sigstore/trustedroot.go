// Package sigstore provides adapters for verifying keyless Sigstore
// (Fulcio/Rekor) signatures on container images.
package sigstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	_ "embed"
)

//go:embed trusted-root-public-good.json
var defaultTrustedRootJSON []byte

//go:embed trusted-root-metadata.json
var defaultTrustedRootMetadataJSON []byte

// DefaultTrustedRootJSON returns a copy of the embedded Sigstore public-good
// trust root snapshot (Fulcio root CA, CT log key, Rekor log keys) used to
// verify keyless signatures on the stock distroless/chainguard base image
// presets. See README.md in this package for capture provenance and the
// refresh procedure.
//
// The snapshot goes stale as Sigstore rotates keys and brings new Rekor log
// shards online, which is why every use of it is paired with a freshness
// check — see trustedroot_freshness.go and DefaultTrustedRootMetadata.
func DefaultTrustedRootJSON() []byte {
	// go:embed hands out the same backing array on every call, so return a
	// copy and let callers treat the result as their own.
	return bytes.Clone(defaultTrustedRootJSON)
}

// TrustedRootMetadata describes the provenance of an embedded trust-root
// snapshot. It is carried in a sidecar JSON file next to the snapshot itself
// (trusted-root-metadata.json) rather than as Go constants so that the
// refresh procedure only ever rewrites data files, never source.
type TrustedRootMetadata struct {
	// CapturedAt is when the snapshot was fetched from the TUF repository.
	// This is the *only* usable staleness signal: the public-good Fulcio CAs
	// are valid until 2031 and new Rekor shards carry no end date, so nothing
	// inside the snapshot itself reveals that a newer one exists.
	CapturedAt time.Time `json:"capturedAt"`

	// SHA256 is the hex-encoded SHA-256 of the snapshot bytes as captured. It
	// is a tripwire against the snapshot and its provenance record drifting
	// apart (e.g. a hand-edit of the JSON, or a refresh that updated one file
	// and not the other), checked by TestDefaultTrustedRootMetadata_MatchesSnapshot.
	SHA256 string `json:"sha256"`

	// Source records how the bytes were obtained. "tuf-target" means they are
	// the raw, TUF-signature-verified `trusted_root.json` target from
	// TUFRepository — i.e. byte-identical to what a live TUF client would
	// resolve, not a hand-copied example file.
	Source string `json:"source"`

	// TUFRepository is the TUF repository base URL the snapshot came from.
	TUFRepository string `json:"tufRepository"`

	// TUFTarget is the target name within that repository.
	TUFTarget string `json:"tufTarget"`
}

// DefaultTrustedRootMetadata returns the provenance record for the embedded
// snapshot returned by DefaultTrustedRootJSON.
//
// It returns an error only if the embedded sidecar is unparseable, which can
// only happen if this package was edited incorrectly — but it is reported
// rather than panicked on, because the freshness check that consumes it runs
// on a verification path and a verification path must degrade to a clear
// error, never a crash.
func DefaultTrustedRootMetadata() (TrustedRootMetadata, error) {
	var m TrustedRootMetadata
	if err := json.Unmarshal(defaultTrustedRootMetadataJSON, &m); err != nil {
		return TrustedRootMetadata{}, fmt.Errorf(
			"sigstore: cannot parse embedded trusted-root-metadata.json: %w", err)
	}
	if m.CapturedAt.IsZero() {
		return TrustedRootMetadata{}, fmt.Errorf(
			"sigstore: embedded trusted-root-metadata.json has no capturedAt timestamp")
	}
	return m, nil
}

// digestOf returns the hex-encoded SHA-256 of data, the form recorded in
// TrustedRootMetadata.SHA256.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
