// Package sigstore provides adapters for verifying keyless Sigstore
// (Fulcio/Rekor) signatures on container images.
package sigstore

import (
	"bytes"
	_ "embed"
)

//go:embed trusted-root-public-good.json
var defaultTrustedRootJSON []byte

// DefaultTrustedRootJSON returns a copy of the embedded Sigstore public-good
// trust root snapshot (Fulcio root CA, CT log key, Rekor log key) used to
// verify keyless signatures on the stock distroless/chainguard base image
// presets. See README.md in this package for capture provenance and the
// refresh procedure.
func DefaultTrustedRootJSON() []byte {
	// go:embed hands out the same backing array on every call, so return a
	// copy and let callers treat the result as their own.
	return bytes.Clone(defaultTrustedRootJSON)
}
