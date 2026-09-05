package core_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// This file guards the five stage-reordering changes that moved work off the
// pipeline's critical path. Every test here exists because the reordering
// could have turned a fail-closed gate into a fail-open one, and a gate that
// no longer fires looks exactly like a build that got faster.
//
// Each test was written by first breaking what it guards and watching it go
// red; the specific failure output is recorded in the change's report. A
// green run of a guard nobody has seen fail is not evidence (CLAUDE.md §5).

// ---------------------------------------------------------------------------
// Change 2: the base-image CVE scan moved off the critical path
// ---------------------------------------------------------------------------

// TestBuild_ScannerThresholdBreachStillFailsAfterMove proves the CVE gate
// survived being dispatched concurrently with the remote-cache lookup: a
// scanner that reports ErrVulnerabilityThresholdExceeded must still abort the
// build, with the same wrapped sentinel and the same message prefix, before
// anything is published.
//
// The failure mode this rules out is not "the scan errored and nobody
// noticed" — it is "the scan now runs in a goroutine whose error is joined
// somewhere that no longer aborts", which produces a completely normal,
// successful build.
func TestBuild_ScannerThresholdBreachStillFailsAfterMove(t *testing.T) {
	stdout := &strings.Builder{}
	deps := newFullDeps(stdout)
	pushed := false
	reg := &mockRegistry{pushFn: func(context.Context, ports.PushRequest) (ports.PublishResult, error) {
		pushed = true
		return ports.PublishResult{}, nil
	}}
	deps.Registry = reg
	deps.Scanner = &mockScanner{scanFn: func(_ context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
		return ports.ScanResult{
			Target:           req.Target,
			Passed:           false,
			MaxSeverityFound: ports.SeverityCritical,
			Vulnerabilities:  []ports.Vulnerability{{ID: "CVE-2026-0001", Severity: ports.SeverityCritical}},
		}, fmt.Errorf("base image %s has 1 critical vulnerability: %w",
			req.Target, core.ErrVulnerabilityThresholdExceeded)
	}}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		FailOnCVE:  ports.SeverityHigh,
	}

	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err == nil {
		t.Fatal("BUG: a base image over the --fail-on-cve threshold produced a successful build; the CVE gate is fail-open after the scan was moved off the critical path")
	}
	if !errors.Is(err, core.ErrVulnerabilityThresholdExceeded) {
		t.Fatalf("error = %v, want it to wrap ErrVulnerabilityThresholdExceeded", err)
	}
	if !strings.Contains(err.Error(), "base image vulnerability scan failed") {
		t.Errorf("error = %q, want the original %q prefix preserved verbatim", err, "base image vulnerability scan failed")
	}
	if pushed {
		t.Error("BUG: the image was pushed despite the CVE threshold breach — the gate must abort before publish")
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want nothing written for a failed build", stdout.String())
	}
}

// TestBuild_ScannerThresholdBreachStillFailsOnCacheHit is the half that the
// obvious placement of this change would have broken. Deferring the CVE scan
// into the Stage 5 errgroup (alongside base-image signature verification)
// would skip it entirely on a remote-cache hit — and a cache hit is not a
// no-op: it reconciles this build's release tags onto the cached digest.
//
// Signature verification is safely skippable there because a signature is a
// property of the base image bytes and the cache key already binds
// base.Digest. A CVE scan is not: it queries a live advisory database whose
// answer for a FIXED digest changes as new CVEs land, so a build that fails
// today would silently promote tags tomorrow.
func TestBuild_ScannerThresholdBreachStillFailsOnCacheHit(t *testing.T) {
	stdout := &strings.Builder{}
	deps := newFullDeps(stdout)
	cacher := &mockRemoteCacher{hit: true, verified: true, signerIdentity: "static-key"}
	deps.RemoteCache = cacher
	deps.Scanner = &mockScanner{scanFn: func(_ context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
		return ports.ScanResult{Target: req.Target, MaxSeverityFound: ports.SeverityCritical},
			fmt.Errorf("base image %s has 1 critical vulnerability: %w", req.Target, core.ErrVulnerabilityThresholdExceeded)
	}}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		FailOnCVE:  ports.SeverityHigh,
		Sign:       true,
		CacheVerify: core.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      core.CacheVerifyStaticKey,
		},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err == nil {
		t.Fatalf("BUG: a remote-cache hit returned success (cached=%v, ref=%q) while the base image was over the --fail-on-cve threshold; the cache-hit path promotes release tags, so the CVE gate must still fire there",
			res.Cached, res.Image.Ref)
	}
	if !errors.Is(err, core.ErrVulnerabilityThresholdExceeded) {
		t.Fatalf("error = %v, want it to wrap ErrVulnerabilityThresholdExceeded", err)
	}
	// The CVE gate is joined AHEAD of the lookup (its pokkum.lock write must
	// not race the source-tree read that produces the cache key), so a
	// breach aborts before Check is ever reached. That is strictly stronger
	// than "joined before the hit is honoured", and asserting it here pins
	// the ordering so a later edit cannot quietly move the join past the
	// lookup without this test noticing.
	if cacher.checkCalled {
		t.Error("RemoteCache.Check ran after the CVE gate had already failed — the gate must abort ahead of the lookup, not alongside it")
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want nothing written: the published reference must not be emitted for a build that failed its CVE gate", stdout.String())
	}
}

// ---------------------------------------------------------------------------
// Change 3: the pre-build secret scan overlaps the remote-cache lookup
// ---------------------------------------------------------------------------

// TestBuild_PreBuildSecretStillFailsOnCacheHit is the single most important
// guard in this file, because it is the one most likely to regress silently.
//
// The pre-build secret scan now runs concurrently with ComputeInputHash and
// RemoteCache.Check. A cache hit is an early return that skips essentially
// all remaining work — so if the scan's error is joined anywhere after that
// return, a repository with a hardcoded secret produces a clean, fast,
// "cache hit; build skipped" success that also promotes the release tags.
//
// The pipeline makes that unreachable structurally rather than by a
// well-placed guard: the join sits between the lookup and every use of its
// result, so the hit branch cannot be reached without it.
func TestBuild_PreBuildSecretStillFailsOnCacheHit(t *testing.T) {
	stdout := &strings.Builder{}
	deps := newFullDeps(stdout)
	cacher := &mockRemoteCacher{hit: true, verified: true, signerIdentity: "static-key"}
	deps.RemoteCache = cacher
	guard := &fakeSecretGuard{resultFor: map[string]ports.SecretScanResult{
		"/abs/project": {
			Passed: false,
			Matches: []ports.SecretMatch{{
				FilePath:   "/abs/project/src/lib/keys.ts",
				LineNumber: 3,
				RuleName:   "aws-access-key",
			}},
		},
	}}
	deps.SecretGuard = guard

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
		CacheVerify: core.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      core.CacheVerifyStaticKey,
		},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err == nil {
		t.Fatalf("BUG: a hardcoded secret in the source tree did NOT fail the build on a remote-cache hit (cached=%v, ref=%q). "+
			"A cache hit promotes this build's release tags onto the cached digest, so it is a publish, and the secret gate must be joined before it",
			res.Cached, res.Image.Ref)
	}
	if !errors.Is(err, core.ErrSecretInlined) {
		t.Fatalf("error = %v, want it to wrap ErrSecretInlined", err)
	}
	if !strings.Contains(err.Error(), "secret guard (pre-build source)") {
		t.Errorf("error = %q, want the pre-build source stage named verbatim", err)
	}
	if res.Cached {
		t.Error("BuildResult reports Cached=true for a failed build")
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want nothing: the cached reference must not be emitted when the secret gate failed", stdout.String())
	}
	// The scan must actually have run, not merely have been skipped in a way
	// that happens to leave the build failing for some other reason.
	if len(guard.scannedDirs) == 0 || guard.scannedDirs[0] != "/abs/project" {
		t.Errorf("SecretGuard.ScanDirectory dirs = %v, want the project dir scanned first", guard.scannedDirs)
	}
}

// TestBuild_PreBuildSecretScanOverlapsCacheLookup proves the scan is
// genuinely concurrent with the cache lookup rather than merely still
// correct. Without an observable for the overlap the whole change could be
// reverted to a sequential call and every other test in this file would keep
// passing.
//
// The observable has to distinguish "both were in flight at once" from "one
// finished before the other started", and the naive version ("the scan had
// started by the time Check ran") cannot: it is trivially true of the OLD
// sequential order too. So the probe below asks the opposite question — did
// RemoteCache.Check enter WHILE the secret scan was still running? Under the
// old order the scan blocks, Check has not been called, and the probe times
// out reporting false.
//
// Scanner is nil on purpose. With a base-image scanner wired, the scan
// result is written to pokkum.lock — a non-atomic write inside the tree the
// secret scan is walking — so the pipeline correctly joins the secret scan
// before that write and the overlap does not (and must not) happen. That
// ordering is guarded separately by
// TestBuild_LockfileWriteNeverOverlapsSecretScan.
func TestBuild_PreBuildSecretScanOverlapsCacheLookup(t *testing.T) {
	probe := newOverlapProbe(3 * time.Second)
	deps := newFullDeps(io.Discard)
	deps.Scanner = nil
	deps.SecretGuard = probe
	deps.RemoteCache = probe

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if !probe.checkDuringScan.Load() {
		t.Error("BUG: RemoteCache.Check was never entered while the pre-build secret scan was in flight — the two are sequential, so the advertised sub-100ms verified cache hit still pays a full source-tree scan first")
	}
	if !probe.scanDuringCheck.Load() {
		t.Error("BUG: the pre-build secret scan had not started when RemoteCache.Check was entered — the scan is not being dispatched ahead of the lookup")
	}
}

// overlapProbe implements both SecretGuard and RemoteCacher and reports
// whether each observed the other in flight. Every wait is bounded, so a
// sequential pipeline makes this test fail rather than hang.
type overlapProbe struct {
	scanOnce  sync.Once
	checkOnce sync.Once

	scanStarted  chan struct{}
	checkEntered chan struct{}
	timeout      time.Duration

	checkDuringScan atomic.Bool
	scanDuringCheck atomic.Bool
}

func newOverlapProbe(timeout time.Duration) *overlapProbe {
	return &overlapProbe{
		scanStarted:  make(chan struct{}),
		checkEntered: make(chan struct{}),
		timeout:      timeout,
	}
}

func (o *overlapProbe) ScanDirectory(context.Context, ports.SecretScanRequest) (ports.SecretScanResult, error) {
	first := false
	o.scanOnce.Do(func() {
		first = true
		close(o.scanStarted)
	})
	// Only the pre-build scan participates; the post-build scans (which run
	// long after the join) must not block anything.
	if first {
		select {
		case <-o.checkEntered:
			o.checkDuringScan.Store(true)
		case <-time.After(o.timeout):
		}
	}
	return ports.SecretScanResult{Passed: true}, nil
}

func (o *overlapProbe) ComputeInputHash(context.Context, ports.RemoteCacheInputRequest) (string, error) {
	return "deadbeef", nil
}

func (o *overlapProbe) Check(context.Context, ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	o.checkOnce.Do(func() { close(o.checkEntered) })
	select {
	case <-o.scanStarted:
		o.scanDuringCheck.Store(true)
	case <-time.After(o.timeout):
	}
	return ports.RemoteCacheResult{Hit: false}, nil
}

// TestBuild_BaseScanOverlapsSecretScan is the surviving half of "move the CVE
// scan off the critical path". The scan cannot be carried past the cache
// lookup (it writes pokkum.lock, which the cache key hashes, and it applies
// VEX exemptions the cache key also hashes), so the overlap it CAN have is
// with the other gate — which is the expensive pairing anyway: a base-layer
// pull plus an OSV query alongside a full source-tree walk, instead of one
// after the other.
func TestBuild_BaseScanOverlapsSecretScan(t *testing.T) {
	probe := newGateOverlapProbe(3 * time.Second)
	deps := newFullDeps(io.Discard)
	deps.SecretGuard = probe
	deps.Scanner = probe

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}
	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	// BOTH directions, not either: "the CVE scan had started by the time the
	// secret scan ran" is trivially true of the old sequential order too, so
	// a single flag set by whichever gate happens to run second cannot
	// distinguish concurrent from sequential.
	if !probe.cveSeenDuringScan.Load() {
		t.Error("BUG: the secret scan never observed the CVE scan in flight — the two are still sequential")
	}
	if !probe.scanSeenDuringCVE.Load() {
		t.Error("BUG: the CVE scan never observed the secret scan in flight — the CVE scan is still running ahead of it rather than alongside it")
	}
}

// gateOverlapProbe is a Scanner and a SecretGuard that each wait for the
// other to start. Bounded waits, so a sequential pipeline fails rather than
// hangs.
type gateOverlapProbe struct {
	scanOnce sync.Once
	cveOnce  sync.Once

	scanStarted chan struct{}
	cveStarted  chan struct{}
	timeout     time.Duration

	cveSeenDuringScan atomic.Bool
	scanSeenDuringCVE atomic.Bool
}

func newGateOverlapProbe(timeout time.Duration) *gateOverlapProbe {
	return &gateOverlapProbe{
		scanStarted: make(chan struct{}),
		cveStarted:  make(chan struct{}),
		timeout:     timeout,
	}
}

func (g *gateOverlapProbe) ScanDirectory(context.Context, ports.SecretScanRequest) (ports.SecretScanResult, error) {
	first := false
	g.scanOnce.Do(func() {
		first = true
		close(g.scanStarted)
	})
	if first {
		select {
		case <-g.cveStarted:
			g.cveSeenDuringScan.Store(true)
		case <-time.After(g.timeout):
		}
	}
	return ports.SecretScanResult{Passed: true}, nil
}

func (g *gateOverlapProbe) Scan(_ context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
	g.cveOnce.Do(func() { close(g.cveStarted) })
	select {
	case <-g.scanStarted:
		g.scanSeenDuringCVE.Store(true)
	case <-time.After(g.timeout):
	}
	return ports.ScanResult{Target: req.Target, Passed: true, MaxSeverityFound: ports.SeverityLow}, nil
}

// TestBuild_LockfileWriteNeverOverlapsSecretScan guards the filesystem race
// this reordering introduced and then had to close.
//
// RecordScanResult is the only WRITE in the whole gate section. Its target,
// pokkum.lock, sits inside ProjectDir, and lockfileutils.SaveLockfile writes
// with os.WriteFile — truncate-then-write, not a temp-file rename — so a
// concurrent reader can observe a partial file. Two things read that tree
// alongside it once the gates are concurrent: the pre-build secret scan and
// RemoteCache.ComputeInputHash's source-tree walk. A torn read means either a
// spurious ErrSecretScanIncomplete or a nondeterministic composite input
// hash, and that hash is stamped into the image as
// pokkum.dev/build-input-hash — content-addressed bytes.
//
// So: when the write happens, neither reader may be in flight.
func TestBuild_LockfileWriteNeverOverlapsSecretScan(t *testing.T) {
	var scansInFlight atomic.Int32
	var hashesInFlight atomic.Int32
	var readersDuringWrite atomic.Int32
	var wroteAfterHash atomic.Bool
	var hashed atomic.Bool

	deps := newFullDeps(io.Discard)
	deps.SecretGuard = &slowSecretGuard{inFlight: &scansInFlight, dwell: 60 * time.Millisecond}
	deps.RemoteCache = &countingRemoteCacher{inFlight: &hashesInFlight, hashed: &hashed, dwell: 20 * time.Millisecond}
	deps.BaseImages = &mockBaseImageResolver{
		recordScanResultFn: func(context.Context, string, ports.BaseImagePreset, ports.ScanResult) error {
			readersDuringWrite.Store(scansInFlight.Load() + hashesInFlight.Load())
			if hashed.Load() {
				wroteAfterHash.Store(true)
			}
			return nil
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}
	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if n := readersDuringWrite.Load(); n != 0 {
		t.Errorf("%d project-tree reader(s) were in flight while pokkum.lock was being rewritten — a non-atomic write racing a security gate and the cache key's source-tree hash", n)
	}
	if wroteAfterHash.Load() {
		t.Error("pokkum.lock was written AFTER the composite input hash was computed; the write has always preceded the hash and moving it changes every cache key and the pokkum.dev/build-input-hash annotation baked into the image")
	}
}

// slowSecretGuard dwells so that an overlapping write is observable rather
// than a coin flip.
type slowSecretGuard struct {
	inFlight *atomic.Int32
	dwell    time.Duration
}

func (s *slowSecretGuard) ScanDirectory(context.Context, ports.SecretScanRequest) (ports.SecretScanResult, error) {
	s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	time.Sleep(s.dwell)
	return ports.SecretScanResult{Passed: true}, nil
}

// countingRemoteCacher reports whether its source-tree hash is in flight, and
// whether it has already run.
type countingRemoteCacher struct {
	inFlight *atomic.Int32
	hashed   *atomic.Bool
	dwell    time.Duration
}

func (c *countingRemoteCacher) ComputeInputHash(context.Context, ports.RemoteCacheInputRequest) (string, error) {
	c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	time.Sleep(c.dwell)
	c.hashed.Store(true)
	return "deadbeef", nil
}

func (c *countingRemoteCacher) Check(context.Context, ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	return ports.RemoteCacheResult{Hit: false}, nil
}

// ---------------------------------------------------------------------------
// Change 4: every platform's Bun runtime resolves inside the Stage 5 errgroup
// ---------------------------------------------------------------------------

// TestBuild_BunRuntimeResolvedOncePerPlatform proves the hoisted resolve
// keeps the embedded-vs-host distinction intact and no longer resolves the
// first platform twice. The distinction is load-bearing: bunToolchain is the
// runtime EMBEDDED IN THE IMAGE (what the SBOM and SLSA statement must name),
// while Toolchain.BunVersion is the HOST compiler bun from Preflight.
func TestBuild_BunRuntimeResolvedOncePerPlatform(t *testing.T) {
	deps := newFullDeps(io.Discard)
	var mu sync.Mutex
	seen := map[string]int{}
	deps.BunRuntime = &mockBunRuntimeResolver{resolveFn: func(_ context.Context, req ports.BunResolverRequest) (ports.BunResolverResult, error) {
		mu.Lock()
		seen[req.Platform.String()]++
		mu.Unlock()
		return ports.BunResolverResult{
			Version:  "1.2.2-embedded",
			SHA256:   "embedded-" + req.Platform.Arch,
			Platform: req.Platform,
		}, nil
	}}
	slsaMock := &mockSLSAGenerator{}
	var slsaReq ports.SLSAGeneratorRequest
	slsaMock.generateFn = func(_ context.Context, r ports.SLSAGeneratorRequest) (ports.SLSAStatement, error) {
		mu.Lock()
		slsaReq = r
		mu.Unlock()
		return ports.SLSAStatement{
			Type:          ports.InTotoStatementType,
			PredicateType: ports.SLSAProvenancePredicateType,
			Subject: []ports.ResourceDescriptor{{
				Name:   r.Repo,
				Digest: map[string]string{r.OutputDigest.Algorithm: r.OutputDigest.Hex},
			}},
		}, nil
	}
	deps.SLSAGenerator = slsaMock

	req := signedTestRequest()
	req.Platforms = []core.Platform{core.LinuxAMD64, core.LinuxARM64}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range req.Platforms {
		if got := seen[p.String()]; got != 1 {
			t.Errorf("BunRuntime.Resolve calls for %s = %d, want exactly 1 — the fan-out must consume the pre-resolved result, not resolve again", p, got)
		}
	}
	// The EMBEDDED runtime, not the host bun from Preflight ("1.2.18" in the
	// mock compiler). Getting this backwards is a previously-logged bug.
	if slsaReq.Toolchain.BunVersion != "1.2.2-embedded" {
		t.Errorf("SLSA Toolchain.BunVersion = %q, want the embedded runtime %q, not the host compiler bun", slsaReq.Toolchain.BunVersion, "1.2.2-embedded")
	}
	if slsaReq.Toolchain.BunBinaryHash != "embedded-amd64" {
		t.Errorf("SLSA Toolchain.BunBinaryHash = %q, want the FIRST platform's embedded binary hash %q", slsaReq.Toolchain.BunBinaryHash, "embedded-amd64")
	}
}

// TestBuild_BunRuntimeResolveFailureFailsBuild covers the non-first item
// (checklist row 4): a resolve failure on the SECOND platform must fail the
// build with the per-platform message, while a first-platform failure keeps
// the sbom/provenance message it has always had. Both messages moved call
// sites in this change, so both are pinned.
func TestBuild_BunRuntimeResolveFailureFailsBuild(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failFor     core.Platform
		wantMessage string
	}{
		{"first platform", core.LinuxAMD64, "core: resolve bun runtime for sbom/provenance"},
		{"second platform", core.LinuxARM64, "core: resolve bun runtime for linux/arm64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := newFullDeps(io.Discard)
			deps.BunRuntime = &mockBunRuntimeResolver{resolveFn: func(_ context.Context, r ports.BunResolverRequest) (ports.BunResolverResult, error) {
				if r.Platform == tc.failFor {
					return ports.BunResolverResult{}, errors.New("bun mirror unreachable")
				}
				return ports.BunResolverResult{Version: "1.2.2", Platform: r.Platform}, nil
			}}
			req := signedTestRequest()
			req.Platforms = []core.Platform{core.LinuxAMD64, core.LinuxARM64}

			_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
			if err == nil {
				t.Fatal("BUG: a failed Bun runtime resolve produced a successful build")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantMessage)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Change 5: concurrent signing and post-push self-verification
// ---------------------------------------------------------------------------

// TestBuild_ConcurrentSigningFailsWholeBuildOnOneSubject proves first-error-
// wins survived the parallelisation, and specifically that a NON-FIRST
// subject's failure fails the build (checklist row 4 — a single-item or
// first-item-only failure test can pass while a real multi-item failure is
// swallowed by a group whose error is discarded).
//
// It also pins the two error messages that let an operator tell the two
// recoverable states apart: "pushed but NOT signed" versus "pushed and signed
// but unverifiable". Those strings are what a CI log gets read for.
func TestBuild_ConcurrentSigningFailsWholeBuildOnOneSubject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failNth int32
	}{
		{"first subject", 1},
		{"third subject", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := newFullDeps(io.Discard)
			var n atomic.Int32
			deps.Registry = &mockRegistry{
				attachSignatureFn: func(_ context.Context, r ports.AttachSignatureRequest) (ports.PublishResult, error) {
					if n.Add(1) == tc.failNth {
						return ports.PublishResult{}, errors.New("registry rejected the .sig write")
					}
					return ports.PublishResult{Ref: r.Repo + ":" + ports.SigTag(r.Subject)}, nil
				},
			}
			req := signedTestRequest()
			req.Platforms = []core.Platform{core.LinuxAMD64, core.LinuxARM64}

			_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
			if err == nil {
				t.Fatal("BUG: one subject's signature attach failed and the build still succeeded — the concurrent signing group's error is not reaching the caller")
			}
			// Verbatim: this is the string that distinguishes "pushed but not
			// signed" from every other post-push failure.
			if !strings.Contains(err.Error(), "(the image itself was pushed but is NOT signed)") {
				t.Errorf("error = %q, want the verbatim \"(the image itself was pushed but is NOT signed)\" disambiguator preserved", err)
			}
		})
	}
}

// TestBuild_ConcurrentSelfVerifyFailsWholeBuildOnOneSubject is the same
// proof for the second, read-back phase — and it is what makes the two-phase
// ordering meaningful. If the read-backs were folded into the signing group,
// a subject could be fetched before it was attached and this failure would
// become a flake instead of a gate.
func TestBuild_ConcurrentSelfVerifyFailsWholeBuildOnOneSubject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failNth int32
	}{
		{"first subject", 1},
		{"third subject", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := newFullDeps(io.Discard)
			var n atomic.Int32
			deps.CosignSigner = &mockCosignSigner{
				verifyFn: func(context.Context, ports.CosignSignatureBundle, []byte, string, v1.Hash) error {
					if n.Add(1) == tc.failNth {
						return errors.New("signature does not match the public key")
					}
					return nil
				},
			}
			req := signedTestRequest()
			req.Platforms = []core.Platform{core.LinuxAMD64, core.LinuxARM64}

			_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
			if err == nil {
				t.Fatal("BUG: one subject's post-push read-back verification failed and the build still succeeded — the self-verify group's error is not reaching the caller")
			}
			if !errors.Is(err, core.ErrSignatureSelfVerifyFailed) {
				t.Errorf("error = %v, want it to wrap ErrSignatureSelfVerifyFailed", err)
			}
			if !strings.Contains(err.Error(), "core: self-verify: fetched signature for") {
				t.Errorf("error = %q, want the verbatim self-verify message preserved", err)
			}
		})
	}
}

// TestBuild_SigningRefsKeepSubjectOrder pins the one thing concurrency could
// scramble without failing anything: the reported refs are indexed by
// subject, published digest first, so a caller reading SignatureRefs[0]
// still gets the published digest's signature.
func TestBuild_SigningRefsKeepSubjectOrder(t *testing.T) {
	deps := newFullDeps(io.Discard)
	req := signedTestRequest()
	req.Platforms = []core.Platform{core.LinuxAMD64, core.LinuxARM64}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if res.Signing == nil || len(res.Signing.SignatureRefs) != 3 {
		t.Fatalf("Signing = %+v, want 3 signature refs", res.Signing)
	}
	wantFirst := req.Repo + ":" + ports.SigTag(res.Image.Digest)
	if res.Signing.SignatureRefs[0] != wantFirst {
		t.Errorf("SignatureRefs[0] = %q, want the published digest's ref %q — refs must stay in subject order despite concurrent signing",
			res.Signing.SignatureRefs[0], wantFirst)
	}
	for i, r := range res.Signing.SignatureRefs {
		if r == "" {
			t.Errorf("SignatureRefs[%d] is empty — an indexed slot was never filled", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Change 1: per-stage timings
// ---------------------------------------------------------------------------

// TestBuild_ReportsPerStageTimings proves the breakdown is real, attributed
// and reaches the caller — not a log-only side effect and not a single
// whole-build number wearing a different name.
func TestBuild_ReportsPerStageTimings(t *testing.T) {
	deps := newFullDeps(io.Discard)
	deps.Compiler = &mockCompiler{prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
		time.Sleep(20 * time.Millisecond)
		return ports.PrepareResult{EntrypointPath: "/tmp/project/build/index.js"}, nil
	}}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}
	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(res.Stages) == 0 {
		t.Fatal("BuildResult.Stages is empty — the build reports only an unattributable total")
	}
	byName := map[string]time.Duration{}
	for _, st := range res.Stages {
		if st.Stage == "" {
			t.Error("a stage timing has no name")
		}
		byName[st.Stage] = st.Duration
	}
	for _, want := range []string{"preflight", "base image resolution", "cache lookup", "sveltekit build", "compile", "publish"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("no timing recorded for stage %q; got %v", want, byName)
		}
	}
	// The slow stage must be attributed to the slow stage. Asserting a real
	// floor rather than "greater than zero" is what makes this a measurement
	// instead of a shape check: a timer wired to the wrong boundary would
	// still produce a non-zero number here.
	if byName["sveltekit build"] < 20*time.Millisecond {
		t.Errorf("sveltekit build stage = %v, want >= 20ms — Prepare slept that long, so the timer is not bracketing the stage it names", byName["sveltekit build"])
	}
	if byName["preflight"] >= 20*time.Millisecond {
		t.Errorf("preflight stage = %v, want well under Prepare's 20ms — the slow stage's time is being attributed to the wrong stage", byName["preflight"])
	}
	if res.Duration < byName["sveltekit build"] {
		t.Errorf("total Duration %v is less than one stage's %v", res.Duration, byName["sveltekit build"])
	}
}

// TestBuild_StageTimingsOnEarlyReturnPaths proves the early-return paths
// report the stages they actually reached rather than nothing at all —
// including the cache-hit path, whose whole selling point is being fast and
// which is therefore the one most worth measuring.
func TestBuild_StageTimingsOnEarlyReturnPaths(t *testing.T) {
	t.Run("cache hit", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.RemoteCache = &mockRemoteCacher{hit: true}
		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			Tags:       []string{"v1.0.0"},
		}
		res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if !res.Cached {
			t.Fatalf("expected a cache hit")
		}
		if len(res.Stages) == 0 {
			t.Error("a cache hit reports no stage breakdown at all")
		}
		for _, st := range res.Stages {
			if st.Stage == "sveltekit build" {
				t.Error("a cache hit recorded a sveltekit build stage it never ran")
			}
		}
	})

	t.Run("dry run", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			Tags:       []string{"v1.0.0"},
		}
		res, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if len(res.Stages) == 0 {
			t.Error("a dry run reports no stage breakdown at all")
		}
	})
}

// ---------------------------------------------------------------------------
// Cancellation and goroutine hygiene, across all of the above
// ---------------------------------------------------------------------------

// TestBuild_GatesAreJoinedBeforeBuildReturns is the concrete goroutine-
// hygiene guard for the gates dispatched at stage 4.45. The property is
// "Build never returns while a gate goroutine is still running", and the
// observable is a live in-flight counter read the instant Build returns.
//
// The scan deliberately ignores its context and sleeps a fixed 100ms, so a
// build that abandoned it rather than joining it reads a non-zero counter
// instead of racing a fast goroutine to the finish line.
//
// Cancellation is injected from inside RemoteCache.Check — i.e. in the exact
// window between the gates' dispatch and their join — because that is the
// only window where an abandoned gate is possible at all.
func TestBuild_GatesAreJoinedBeforeBuildReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var running atomic.Int32
	deps := newFullDeps(io.Discard)
	deps.SecretGuard = &countingSecretGuard{running: &running}
	deps.RemoteCache = &cancellingRemoteCacher{cancel: cancel}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}
	if _, err := core.Build(ctx, deps, req, core.BuildOptions{}); err == nil {
		t.Fatal("a build cancelled during the remote-cache lookup reported success")
	}
	if n := running.Load(); n != 0 {
		t.Errorf("%d secret-scan goroutine(s) still in flight the instant Build returned — the gates were abandoned rather than joined", n)
	}
}

// countingSecretGuard reports how many scans are in flight right now. It
// ignores its context on purpose: a guard that returned early on
// cancellation would finish so fast that an abandoned goroutine and a joined
// one look identical.
type countingSecretGuard struct{ running *atomic.Int32 }

func (c *countingSecretGuard) ScanDirectory(context.Context, ports.SecretScanRequest) (ports.SecretScanResult, error) {
	c.running.Add(1)
	defer c.running.Add(-1)
	time.Sleep(100 * time.Millisecond)
	return ports.SecretScanResult{Passed: true}, nil
}

// cancellingRemoteCacher cancels the build from inside the cache lookup, so
// the cancellation lands between the gates' dispatch and their join.
type cancellingRemoteCacher struct{ cancel context.CancelFunc }

func (c *cancellingRemoteCacher) ComputeInputHash(context.Context, ports.RemoteCacheInputRequest) (string, error) {
	return "deadbeef", nil
}

func (c *cancellingRemoteCacher) Check(context.Context, ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	c.cancel()
	return ports.RemoteCacheResult{Hit: false}, nil
}

// TestBuild_CancelledContextTearsDownWithoutLeaks is the broad net over every
// concurrent section this change touched — the gates, the Stage 5 errgroup
// (now carrying every platform's Bun resolve as well as Prepare), the
// per-platform fan-out — rather than any single one of them. It cancels
// mid-Prepare repeatedly and asserts the process settles back to its starting
// goroutine count.
//
// This is a coarse instrument and is not a substitute for
// TestBuild_GatesAreJoinedBeforeBuildReturns above: it catches a goroutine
// abandoned indefinitely, not one abandoned briefly. Both are here on
// purpose.
func TestBuild_CancelledContextTearsDownWithoutLeaks(t *testing.T) {
	before := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		deps := newFullDeps(io.Discard)
		deps.SecretGuard = &fakeSecretGuard{}
		deps.RemoteCache = &mockRemoteCacher{}
		deps.BunRuntime = &mockBunRuntimeResolver{resolveFn: func(c context.Context, r ports.BunResolverRequest) (ports.BunResolverResult, error) {
			<-c.Done()
			return ports.BunResolverResult{}, c.Err()
		}}
		deps.Compiler = &mockCompiler{prepareFn: func(c context.Context, _ ports.PrepareRequest) (ports.PrepareResult, error) {
			cancel()
			<-c.Done()
			return ports.PrepareResult{}, c.Err()
		}}
		req := signedTestRequest()
		req.Platforms = []core.Platform{core.LinuxAMD64, core.LinuxARM64}
		if _, err := core.Build(ctx, deps, req, core.BuildOptions{}); err == nil {
			cancel()
			t.Fatal("a cancelled build reported success")
		}
		cancel()
	}

	// Goroutine teardown is not instantaneous even when nothing leaks, so
	// poll rather than sampling once; a genuine leak of 20 builds' worth of
	// goroutines never drains.
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		after = runtime.NumGoroutine()
		if after <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutines before = %d, after 20 cancelled builds = %d — a cancelled build is leaking goroutines", before, after)
}

var (
	_ ports.SecretGuard  = (*overlapProbe)(nil)
	_ ports.RemoteCacher = (*overlapProbe)(nil)
	_ ports.SecretGuard  = (*gateOverlapProbe)(nil)
	_ ports.Scanner      = (*gateOverlapProbe)(nil)
	_ ports.SecretGuard  = (*slowSecretGuard)(nil)
	_ ports.RemoteCacher = (*countingRemoteCacher)(nil)
	_ ports.SecretGuard  = (*countingSecretGuard)(nil)
	_ ports.RemoteCacher = (*cancellingRemoteCacher)(nil)
)
