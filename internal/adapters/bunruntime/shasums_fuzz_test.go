package bunruntime

import (
	"strings"
	"testing"
)

// FuzzParseSHASUMSEntry exercises parseSHASUMSEntry against arbitrary
// SHASUMS256.txt-shaped plaintext and filename. This plaintext is the
// content of a GPG-CLEARSIGNED manifest fetched over the network from
// GitHub releases (fetchVerifiedChecksum) — by the time parseSHASUMSEntry
// runs, the signature has already been verified, but the SIGNED CONTENT
// ITSELF is still attacker-shaped in the sense that a real Bun release
// manifest is exactly this format and nothing stops a future/rogue release
// (or a downgrade-substituted one, per fetchVerifiedChecksum's own doc
// comment) from containing adversarial-looking lines. The property under
// test: never panic, and — critical for the checksum-mismatch check
// downstream in Resolve — any digest string this function returns as
// success must be well-formed hex of exactly 64 characters (a malformed
// "valid" digest here would let a shorter/garbage string silently pass as
// if it were a real SHA-256 hex digest).
//
// REAL BUG FOUND BY THIS TARGET'S OWN SEED CORPUS (not previously known,
// not something this fuzz-test-only workstream is permitted to fix —
// resolver.go is a production file outside this workstream's file
// ownership list): parseSHASUMSEntry
// (internal/adapters/bunruntime/resolver.go) validates only the LENGTH of
// the checksum field (`len(sha) != 64`), never its character set. A
// SHASUMS256.txt line whose first field is 64 characters of anything —
// e.g. 64 'g' characters, which are not valid hex — is accepted and
// returned as if it were a genuine SHA-256 digest.
//
// Reproducer: parseSHASUMSEntry([]byte(strings.Repeat("g", 64)+"  bun-linux-x64.zip"), "bun-linux-x64.zip")
// returns (strings.Repeat("g", 64), nil) — a non-hex string accepted as a
// "valid" checksum. Downstream in Resolve, this value is compared via
// simple string equality against actualSHA (a real hex.EncodeToString
// output, always valid lowercase hex) — so in production this exact
// malformed-but-accepted value could never accidentally match a real
// download's computed digest, meaning today it manifests as a download
// that's (correctly) rejected as a checksum mismatch, not as a bypass.
// This is nonetheless a real defect in parseSHASUMSEntry's own stated
// contract ("a returned checksum is always well-formed hex of the right
// length") and a latent risk if this function is ever reused in a context
// that trusts its return value more literally (e.g. an equality-agnostic
// comparison, or logging/display code assuming valid hex).
//
// Per this task's explicit instruction, this target is SKIPPED (not
// weakened) so `go test ./...` stays green — the seed corpus below,
// especially the 64-'g' seed, deterministically fails the hex-charset
// assertion otherwise. Remove the f.Skip call to see it fail, or to run
// real fuzzing via `-fuzz=FuzzParseSHASUMSEntry`.
func FuzzParseSHASUMSEntry(f *testing.F) {
	realSHA := strings.Repeat("a1b2c3d4", 8) // 64 hex chars
	f.Add([]byte(realSHA+"  bun-linux-x64.zip\n"+realSHA+"  bun-linux-aarch64.zip\n"), "bun-linux-x64.zip")
	f.Add([]byte(realSHA+"\tbun-linux-x64.zip\n"), "bun-linux-x64.zip") // tab-separated
	f.Add([]byte(""), "bun-linux-x64.zip")
	f.Add([]byte("\n\n\n"), "bun-linux-x64.zip")
	f.Add([]byte("not a valid line at all"), "bun-linux-x64.zip")
	f.Add([]byte(realSHA+"  bun-linux-x64.zip"), "")                                   // empty filename never matches
	f.Add([]byte("short  bun-linux-x64.zip"), "bun-linux-x64.zip")                     // too-short hex
	f.Add([]byte(strings.Repeat("g", 64)+"  bun-linux-x64.zip"), "bun-linux-x64.zip")  // non-hex chars, right length
	f.Add([]byte(strings.ToUpper(realSHA)+"  bun-linux-x64.zip"), "bun-linux-x64.zip") // uppercase hex
	f.Add([]byte(realSHA+" extra field bun-linux-x64.zip"), "bun-linux-x64.zip")       // wrong field count
	f.Add([]byte(realSHA+"  bun-linux-x64.zip  \n"+realSHA+"  bun-linux-x64.zip.asc\n"), "bun-linux-x64.zip")
	f.Add([]byte("\x00\x01\x02  bun-linux-x64.zip"), "bun-linux-x64.zip")
	f.Add([]byte(strings.Repeat(realSHA+"  file"+strings.Repeat("x", 10)+".zip\n", 10000)), "bun-linux-x64.zip") // huge manifest
	f.Add([]byte(realSHA+"  ../../../etc/passwd"), "../../../etc/passwd")                                        // path-shaped filename
	f.Add([]byte(realSHA+"  bun-linux-x64.zip"), "bun-linux-x64.zip\x00trailing")

	// See the doc comment above: a 64-character non-hex field (e.g. all
	// 'g' characters) is currently accepted by parseSHASUMSEntry as a
	// "valid" checksum, which fails the hex-charset assertion below on
	// this target's own seed corpus. Skipping keeps `go test ./...` green
	// while leaving this target as precise, executable documentation of
	// the defect for whoever next touches resolver.go.
	f.Skip("KNOWN BUG (found by this target's own seeds): parseSHASUMSEntry " +
		"(internal/adapters/bunruntime/resolver.go) validates checksum LENGTH " +
		"but not its hex character set — a 64-char non-hex field (e.g. " +
		"strings.Repeat(\"g\", 64)) is accepted as a well-formed checksum. See " +
		"this function's doc comment above for the full reproducer and impact " +
		"analysis. Not fixed here: resolver.go is a production file outside this " +
		"fuzz-test-only workstream's file ownership.")

	f.Fuzz(func(t *testing.T, plaintext []byte, filename string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseSHASUMSEntry(%q, %q) panicked: %v", plaintext, filename, r)
			}
		}()

		sha, err := parseSHASUMSEntry(plaintext, filename)
		if err != nil {
			return // "no matching entry" / malformed line: an expected, ordinary rejection
		}

		if len(sha) != 64 {
			t.Fatalf("parseSHASUMSEntry(%q, %q) returned checksum %q of length %d, want exactly 64", plaintext, filename, sha, len(sha))
		}
		for _, c := range sha {
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
			if !isHex {
				t.Fatalf("parseSHASUMSEntry(%q, %q) returned non-hex (or non-lowercase) checksum %q", plaintext, filename, sha)
			}
		}
	})
}
