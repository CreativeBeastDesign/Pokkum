package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dockerclient "github.com/moby/moby/client"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// --- selectIndexChild: pure logic, no daemon and no network required -------

func TestSelectIndexChild_PicksRequestedPlatform(t *testing.T) {
	idx := indexWithPlatforms(t, []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})

	img, skipped, err := selectIndexChild(idx, ports.LinuxARM64)
	if err != nil {
		t.Fatalf("selectIndexChild: %v", err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	if cf.OS != "linux" || cf.Architecture != "arm64" {
		t.Errorf("selected image is %s/%s, want linux/arm64", cf.OS, cf.Architecture)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "amd64") {
		t.Errorf("skipped = %v, want exactly one entry naming amd64", skipped)
	}
}

func TestSelectIndexChild_MissingPlatform(t *testing.T) {
	idx := indexWithPlatforms(t, []ports.Platform{ports.LinuxAMD64})

	_, _, err := selectIndexChild(idx, ports.LinuxARM64)
	if err == nil {
		t.Fatal("selectIndexChild: want error, index has no arm64 child")
	}
}

func TestSelectIndexChild_SkipsAttestationPlaceholder(t *testing.T) {
	idx := indexWithAttestationPlaceholder(t, ports.LinuxAMD64)

	img, skipped, err := selectIndexChild(idx, ports.LinuxAMD64)
	if err != nil {
		t.Fatalf("selectIndexChild: %v", err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	if cf.OS != "linux" || cf.Architecture != "amd64" {
		t.Errorf("selected image is %s/%s, want linux/amd64", cf.OS, cf.Architecture)
	}
	// The unknown/unknown attestation manifest must never be reported as a
	// "skipped platform" — it was never a candidate in the first place.
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none (the only other child is an attestation placeholder)", skipped)
	}
}

// --- Load: error paths that need no daemon ----------------------------------

func TestLoad_EmptyRepo(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.Load(context.Background(), ports.LoadRequest{
		Payload: ports.Payload{Image: randomImage(t)},
	})
	if !errors.Is(err, core.ErrPushFailed) {
		t.Fatalf("err = %v, want core.ErrPushFailed", err)
	}
}

func TestLoad_ZeroPayload(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.Load(context.Background(), ports.LoadRequest{Repo: "pokkum.local/app"})
	if !errors.Is(err, core.ErrPushFailed) {
		t.Fatalf("err = %v, want core.ErrPushFailed", err)
	}
}

// TestLoad_DaemonUnreachable points DOCKER_HOST at a unix socket that cannot
// exist and asserts Load fails fast with core.ErrDaemonUnavailable rather
// than hanging. It needs no real Docker installation — the whole point is
// that a *missing* daemon must be reported quickly and clearly, which is
// exactly the failure mode the package doc calls out (see TestMain and the
// package doc's note on the credential-helper hang that cost W6 a full
// debugging cycle: an unreachable daemon must not repeat it here).
func TestLoad_DaemonUnreachable(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/pokkum-test-daemon.sock")

	a := NewAdapter(nil)
	start := time.Now()
	_, err := a.Load(context.Background(), ports.LoadRequest{
		Repo:    "pokkum.local/app",
		Payload: ports.Payload{Image: randomImage(t)},
	})
	elapsed := time.Since(start)

	if !errors.Is(err, core.ErrDaemonUnavailable) {
		t.Fatalf("err = %v, want core.ErrDaemonUnavailable", err)
	}
	if elapsed > pingTimeout+2*time.Second {
		t.Errorf("Load took %s to fail, want well under the %s ping timeout — it may have hung", elapsed, pingTimeout)
	}
}

// --- Load: real daemon integration, skipped when unavailable ---------------

// dockerAvailable reports whether a Docker daemon can be reached quickly. It
// is used to gate the one test in this file that actually talks to a daemon,
// so the suite never fails on a machine without Docker installed or running.
func dockerAvailable(t *testing.T) bool {
	t.Helper()
	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = cli.Ping(ctx, dockerclient.PingOptions{})
	return err == nil
}

func TestLoad_DaemonIntegration(t *testing.T) {
	if !dockerAvailable(t) {
		t.Skip("no docker daemon reachable, skipping daemon integration test")
	}

	a := NewAdapter(nil)
	img := randomImage(t)
	res, err := a.Load(context.Background(), ports.LoadRequest{
		Repo:    "pokkum-w11-test/load",
		Tags:    []string{"itest"},
		Payload: ports.Payload{Image: img},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", res.Digest, wantDigest)
	}
	if res.Ref == "" {
		t.Errorf("Ref is empty")
	}
}

// TestLoad_DaemonIntegration_WarnsOnDroppedAnnotations proves the `--local`
// half of docs/items/tarball-output-drops-annotations.md: daemon.Write
// streams img through the same annotations-less docker-save format as
// tarball.go's Write, so Load must warn the same way. Gated on a real
// daemon, like TestLoad_DaemonIntegration above, since selectForDaemon's
// caller path is only reachable past a live Ping.
func TestLoad_DaemonIntegration_WarnsOnDroppedAnnotations(t *testing.T) {
	if !dockerAvailable(t) {
		t.Skip("no docker daemon reachable, skipping daemon integration test")
	}

	img := annotatedImage(t, randomImage(t), map[string]string{
		ports.AnnotationPredecessor: "sha256:" + strings.Repeat("a", 64),
	})

	a, buf := newLoggingAdapter()
	if _, err := a.Load(context.Background(), ports.LoadRequest{
		Repo:    "pokkum-w11-test/load-warn",
		Tags:    []string{"itest"},
		Payload: ports.Payload{Image: img},
	}); err != nil {
		t.Fatalf("Load: %v", err)
	}

	warnings := warnLogMessages(t, buf)
	if len(warnings) != 1 {
		t.Fatalf("warn-level log lines = %d, want exactly 1: %v", len(warnings), warnings)
	}
	want := "docker daemon load output drops OCI annotations: pokkum.dev/predecessor" +
		" (annotations survive a registry push — use --output=push to keep them)"
	if warnings[0] != want {
		t.Errorf("warning = %q, want %q", warnings[0], want)
	}
}
