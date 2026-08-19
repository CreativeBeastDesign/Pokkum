package remotecacheutils

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points DOCKER_CONFIG at an empty directory for the whole package.
//
// Cacher.Check authenticates with authn.DefaultKeychain, which reads
// $DOCKER_CONFIG/config.json. On a machine configured with a credential store
// ("credsStore": "desktop", "osxkeychain", ...) that shells out to a
// docker-credential-* helper binary, and that helper blocks indefinitely when
// Docker Desktop is not running or the keychain needs interactive unlock. The
// symptom is not a failure but a hang: the package burns the whole 10-minute
// go test timeout with no useful output.
//
// internal/adapters/{registry,baseimage,provenance,comparator,packager} and
// tests/integration already do this. This package and cmd/pokkum were the two
// that did not, and they were exactly the two that hung — the fix had been
// applied to the sites where the hang was noticed rather than to every site
// that authenticates. Pointing DOCKER_CONFIG at an empty dir makes
// DefaultKeychain fall back to anonymous auth, which is what the in-memory
// test registry expects, and keeps the suite hermetic.
//
// Cleanup runs before os.Exit rather than via defer: os.Exit does not run
// deferred functions, so the established `defer os.RemoveAll(dir)` form in the
// sibling packages silently leaks its temp directory on every run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pokkum-remotecacheutils-dockerconfig")
	if err != nil {
		fmt.Fprintln(os.Stderr, "remotecacheutils: TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	if err := os.Setenv("DOCKER_CONFIG", dir); err != nil {
		fmt.Fprintln(os.Stderr, "remotecacheutils: TestMain: Setenv:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
