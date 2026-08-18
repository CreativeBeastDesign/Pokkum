// Command pin-bun-checksums is a maintenance tool for expanding
// internal/adapters/bunruntime/resolver.go's pinnedReleaseChecksums static
// pin table.
//
// It reuses the exact same GPG-verification path production Resolve calls
// (bunruntime.Resolver.FetchAndVerifyChecksum, a thin exported pass-through
// to the unexported fetchVerifiedChecksum) — not a reimplementation that
// could silently drift from what actually ships. This means running this
// script for a given version performs the identical signature check the
// binary itself performs on that version's first real resolve; a version
// only ends up in the printed map if it genuinely, cryptographically
// verified.
//
// pinnedReleaseChecksums exists to give a version network-free, zero-latency
// checksum verification. It does NOT provide any additional cryptographic
// guarantee beyond what fetchVerifiedChecksum's real-time GPG check already
// gives a dynamically-resolved version — see resolver.go's "Known
// limitation" doc comment on fetchVerifiedChecksum for the honest scope of
// what static pinning can and cannot close (it narrows, but cannot fully
// close, the first-contact-on-a-fresh-cache gap, since this script's own
// fetch is subject to the identical trust-root limitation as any user's).
//
// Usage: go run ./scripts/pin-bun-checksums <version> [<version> ...]
// (version without the "bun-v" prefix, e.g. "1.3.14")
//
// Prints Go source lines ready to paste into pinnedReleaseChecksums.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
)

// linuxTargetNames mirrors resolver.go's resolveBunTargetName's fixed,
// documented set of Linux release-archive target names (Pokkum only ships
// Linux base images, so only Linux targets are ever resolved in production).
var linuxTargetNames = []string{
	"bun-linux-x64",
	"bun-linux-x64-baseline",
	"bun-linux-aarch64",
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <version> [<version> ...]\n", os.Args[0])
		os.Exit(2)
	}

	resolver := bunruntime.NewResolver("", nil)
	ctx := context.Background()

	fmt.Println("// Verified by scripts/pin-bun-checksums against a real, GPG-signed")
	fmt.Println("// SHASUMS256.txt.asc for each version below.")

	exitCode := 0
	for _, version := range os.Args[1:] {
		for _, targetName := range linuxTargetNames {
			sha, err := resolver.FetchAndVerifyChecksum(ctx, version, targetName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "pin-bun-checksums: %s/%s: %v\n", version, targetName, err)
				exitCode = 1
				continue
			}
			fmt.Printf("\t%q: %q,\n", version+"/"+targetName, sha)
		}
	}
	os.Exit(exitCode)
}
