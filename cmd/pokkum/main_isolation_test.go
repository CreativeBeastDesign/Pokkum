package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points DOCKER_CONFIG at an empty directory for the whole package.
//
// Several commands here (build, verify, base, scan) reach a registry through
// authn.DefaultKeychain, which reads $DOCKER_CONFIG/config.json. On a machine
// with a credential store configured, that shells out to a docker-credential-*
// helper which blocks indefinitely when Docker Desktop is not running or the
// keychain needs an interactive unlock — the package then burns the full
// go test timeout with no useful output rather than failing.
//
// See internal/adapters/remotecacheutils/main_test.go for the full diagnosis:
// six packages already isolated this, and the two that did not were exactly
// the two that hung.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pokkum-cmd-dockerconfig")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cmd/pokkum: TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	if err := os.Setenv("DOCKER_CONFIG", dir); err != nil {
		fmt.Fprintln(os.Stderr, "cmd/pokkum: TestMain: Setenv:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
