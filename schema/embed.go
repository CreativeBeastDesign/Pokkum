// Package schema embeds the generated JSON Schema for .pokkum.yaml so the CLI
// can print it without the caller having a checkout of this repository.
//
// Why the package lives here rather than under internal/ or pkg/: `go:embed`
// cannot reach a file above the importing package's own directory, so the
// schema is only embeddable from a package sitting beside it. Keeping
// schema/pokkum.schema.json at this path matters — it is the URL an editor's
// YAML plugin is pointed at, so it is a public contract and should not move to
// suit an implementation detail. Adding this file was the cheaper half of that
// trade: the JSON stays where external consumers already expect it, and the
// binary gains a copy that is correct by construction.
//
// The embedded bytes are the checked-in file, read at build time. There is
// therefore no way for the binary to ship a schema that disagrees with the
// repository's — the only drift possible is between the checked-in file and
// internal/ports/config.go, which is exactly what `make check-schema-freshness`
// guards (and `make verify` runs).
package schema

import _ "embed"

// JSON is the generated JSON Schema (draft 2020-12) describing .pokkum.yaml.
// Regenerate it with `make schema`; never hand-edit it.
//
//go:embed pokkum.schema.json
var JSON []byte
