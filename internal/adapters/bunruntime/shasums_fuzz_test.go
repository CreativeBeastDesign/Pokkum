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
// FIXED: parseSHASUMSEntry (internal/adapters/bunruntime/resolver.go) used
// to validate only the LENGTH of the checksum field (`len(sha) != 64`),
// never its character set. A SHASUMS256.txt line whose first field was 64
// characters of anything — e.g. 64 'g' characters, which are not valid hex
// — was accepted and returned as if it were a genuine SHA-256 digest.
//
// Reproducer (now rejected with an error instead of silently accepted):
// parseSHASUMSEntry([]byte(strings.Repeat("g", 64)+"  bun-linux-x64.zip"), "bun-linux-x64.zip").
// Downstream in Resolve, the accepted-but-malformed value was compared via
// simple string equality against actualSHA (a real hex.EncodeToString
// output, always valid lowercase hex), so in production this exact
// malformed-but-accepted value could never accidentally match a real
// download's computed digest — it manifested as a download that's
// (correctly) rejected as a checksum mismatch, not as a bypass. It was
// nonetheless a real defect in parseSHASUMSEntry's own stated contract ("a
// returned checksum is always well-formed hex of the right length") and a
// latent risk if the function were ever reused in a context that trusts
// its return value more literally. Fixed via a new isHexDigest charset
// check (matching the pattern already used by
// supervisor/cmd/pokkum-init/attest.go), so this target now runs for real.
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
