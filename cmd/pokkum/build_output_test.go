package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// --- newBuildJSONOutput ---------------------------------------------------
//
// Pure, adapter-free by construction, so it is tested directly with
// fabricated core.BuildResult/ports.DeployResult values -- no real build
// pipeline, no network, no filesystem.

func fakeBuildResult(t *testing.T) core.BuildResult {
	t.Helper()
	const hex = "a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4"
	digest, err := v1.NewHash("sha256:" + hex)
	if err != nil {
		t.Fatalf("v1.NewHash: %v", err)
	}
	return core.BuildResult{
		Image: core.ImageResult{
			Mode:   core.OutputPush,
			Ref:    "registry.example.com/app@sha256:" + hex,
			Digest: digest,
			Tags:   []string{"latest", "v1.2.3"},
			Platforms: []core.Platform{
				{OS: "linux", Arch: "amd64"},
				{OS: "linux", Arch: "arm64", Variant: "v8"},
			},
			IsIndex: true,
		},
		Cached: false,
		BaseImage: core.BaseImageInfo{
			PinnedRef: "gcr.io/distroless/base@sha256:deadbeef",
		},
		Duration: 42 * time.Second,
	}
}

func TestNewBuildJSONOutput_NoDeploy(t *testing.T) {
	res := fakeBuildResult(t)

	out := newBuildJSONOutput(res, nil)

	if out.Ref != res.Image.Ref {
		t.Errorf("Ref = %q, want %q", out.Ref, res.Image.Ref)
	}
	if out.Digest != res.Image.Digest.String() {
		t.Errorf("Digest = %q, want %q", out.Digest, res.Image.Digest.String())
	}
	if len(out.Tags) != 2 || out.Tags[0] != "latest" || out.Tags[1] != "v1.2.3" {
		t.Errorf("Tags = %v, want [latest v1.2.3]", out.Tags)
	}
	if out.OutputMode != "push" {
		t.Errorf("OutputMode = %q, want %q", out.OutputMode, "push")
	}
	wantPlatforms := []string{"linux/amd64", "linux/arm64/v8"}
	if len(out.Platforms) != len(wantPlatforms) {
		t.Fatalf("Platforms = %v, want %v", out.Platforms, wantPlatforms)
	}
	for i, p := range wantPlatforms {
		if out.Platforms[i] != p {
			t.Errorf("Platforms[%d] = %q, want %q", i, out.Platforms[i], p)
		}
	}
	if !out.IsIndex {
		t.Error("IsIndex = false, want true")
	}
	if out.Cached {
		t.Error("Cached = true, want false")
	}
	if out.BaseImageRef != res.BaseImage.PinnedRef {
		t.Errorf("BaseImageRef = %q, want %q", out.BaseImageRef, res.BaseImage.PinnedRef)
	}
	if out.DurationSec != 42 {
		t.Errorf("DurationSec = %v, want 42", out.DurationSec)
	}
	if out.Deploy != nil {
		t.Errorf("Deploy = %+v, want nil (no deploy)", out.Deploy)
	}

	// Round-trip through the real JSON envelope this feeds, the same way a
	// caller of `pokkum build --output json` would parse it.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(out); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var roundTrip map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &roundTrip); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := roundTrip["deploy"]; ok {
		t.Errorf("expected \"deploy\" key to be omitted when there was no deploy, got: %s", buf.String())
	}
}

func TestNewBuildJSONOutput_WithDeploy(t *testing.T) {
	res := fakeBuildResult(t)
	deploy := &ports.DeployResult{
		Target:       ports.DeployDokploy,
		Method:       ports.DeployMethodAPI,
		Application:  "storefront",
		ImageRef:     res.Image.Ref,
		ImageUpdated: true,
		Triggered:    true,
	}

	out := newBuildJSONOutput(res, deploy)

	if out.Deploy == nil {
		t.Fatal("expected non-nil Deploy")
	}
	if out.Deploy.Target != string(ports.DeployDokploy) {
		t.Errorf("Deploy.Target = %q, want %q", out.Deploy.Target, ports.DeployDokploy)
	}
	if out.Deploy.Method != string(ports.DeployMethodAPI) {
		t.Errorf("Deploy.Method = %q, want %q", out.Deploy.Method, ports.DeployMethodAPI)
	}
	if out.Deploy.Application != "storefront" {
		t.Errorf("Deploy.Application = %q, want %q", out.Deploy.Application, "storefront")
	}
	if !out.Deploy.ImageUpdated || !out.Deploy.Triggered {
		t.Errorf("Deploy = %+v, want ImageUpdated and Triggered both true", out.Deploy)
	}
}

// --- buildFail -------------------------------------------------------------

func TestBuildFail_TextModeIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	wantErr := errors.New("boom")

	gotErr := buildFail(&buf, ports.FormatText, wantErr)

	if gotErr != wantErr {
		t.Errorf("buildFail returned %v, want the original error unchanged", gotErr)
	}
	if buf.Len() != 0 {
		t.Errorf("text mode must not write anything to stdout, got: %q", buf.String())
	}
}

func TestBuildFail_TextModeNilErrIsNoOp(t *testing.T) {
	var buf bytes.Buffer

	if err := buildFail(&buf, ports.FormatText, nil); err != nil {
		t.Errorf("buildFail(nil) = %v, want nil", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output, got: %q", buf.String())
	}
}

func TestBuildFail_JSONModeWritesErrorEnvelope(t *testing.T) {
	var buf bytes.Buffer
	wantErr := errors.New("validation failed: cannot specify both --dry-run and --print-manifest")

	gotErr := buildFail(&buf, ports.FormatJSON, wantErr)

	if gotErr != wantErr {
		t.Errorf("buildFail returned %v, want the original error unchanged (exit code correctness)", gotErr)
	}

	var env ports.JSONEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("buildFail did not write valid JSON: %v, raw: %s", err, buf.String())
	}
	if env.Command != "build" {
		t.Errorf("Command = %q, want %q", env.Command, "build")
	}
	if env.Status != "error" {
		t.Errorf("Status = %q, want %q", env.Status, "error")
	}
	if env.Error == nil || env.Error.Message != wantErr.Error() {
		t.Errorf("Error = %+v, want Message %q", env.Error, wantErr.Error())
	}
}

func TestBuildFail_JSONModeNilErrWritesNothing(t *testing.T) {
	var buf bytes.Buffer

	if err := buildFail(&buf, ports.FormatJSON, nil); err != nil {
		t.Errorf("buildFail(nil) = %v, want nil", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a nil error must not produce an error envelope, got: %q", buf.String())
	}
}

// --- runBuild --output json wiring, end to end for the early paths --------
//
// runBuild's error paths after the network-touching stages (base image
// resolution, bun preflight, registry push) are intentionally not exercised
// here -- see reconcileStaticStrategy's comment in build_test.go for why a
// flag-level test has no business reaching those adapters. The
// --dry-run/--print-manifest conflict is the one error runBuild reports
// before calling any adapter at all (it fires immediately after
// buildRequestFromResolvedConfig, well before runCoreBuild), which makes it
// the one path that can exercise the real runBuild function end-to-end
// without a real registry, bun binary, or base image resolver.

func TestRunBuild_OutputJSON_EarlyErrorEmitsEnvelope(t *testing.T) {
	logger := discardLogger()
	flags := &buildFlags{
		output:        "json",
		platforms:     []string{"linux/amd64"},
		dryRun:        true,
		printManifest: true,
	}

	out, err := captureStdout(t, func() error {
		return runBuild(context.Background(), logger, flags, nil)
	})

	if err == nil {
		t.Fatal("expected the --dry-run/--print-manifest conflict to still fail the command")
	}

	var env ports.JSONEnvelope
	if decErr := json.Unmarshal([]byte(out), &env); decErr != nil {
		t.Fatalf("--output json did not print a valid JSON envelope on failure: %v, raw: %q", decErr, out)
	}
	if env.Command != "build" || env.Status != "error" {
		t.Errorf("unexpected envelope: %+v, raw: %q", env, out)
	}
	if env.Error == nil || env.Error.Message != err.Error() {
		t.Errorf("envelope error %+v does not match returned error %v", env.Error, err)
	}
}

// TestBuildCommand_OutputJSONFlagIsWiredThroughToJSONEnvelope drives the real
// public entrypoint (newBuildCommand + cmd.Execute()) rather than calling
// runBuild directly, so it also exercises the one line the other tests in
// this file bypass: RunE's `outFlag, _ := cmd.Flags().GetString("output");
// if outFlag != "" { flags.output = outFlag }` copy from the (here,
// manually simulated) persistent root flag into *buildFlags.
func TestBuildCommand_OutputJSONFlagIsWiredThroughToJSONEnvelope(t *testing.T) {
	logger := discardLogger()
	cmd := newBuildCommand(context.Background(), logger)
	// Simulates the persistent --output flag main.go registers on the root
	// command: newBuildCommand's returned command has no parent in this
	// test, so nothing provides --output unless the test adds it itself.
	cmd.Flags().String("output", "text", "")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--output=json", "--dry-run", "--print-manifest", "--platform=linux/amd64"})

	out, err := captureStdout(t, func() error {
		return cmd.Execute()
	})

	if err == nil {
		t.Fatal("expected the --dry-run/--print-manifest conflict to fail the command")
	}
	var env ports.JSONEnvelope
	if decErr := json.Unmarshal([]byte(out), &env); decErr != nil {
		t.Fatalf("expected --output=json set via cmd.Execute() to emit a JSON envelope: %v, raw: %q", decErr, out)
	}
	if env.Command != "build" || env.Status != "error" {
		t.Errorf("unexpected envelope: %+v, raw: %q", env, out)
	}
}

// --- Long help text: the read-only /app invariant -------------------------

func TestBuildCommand_HelpMentionsReadOnlyApp(t *testing.T) {
	cmd := newBuildCommand(context.Background(), discardLogger())

	if !containsFold(cmd.Long, "/app") || !containsFold(cmd.Long, "read-only") {
		t.Fatalf("expected build --help to document that /app ships read-only, got:\n%s", cmd.Long)
	}
	if !containsFold(cmd.Long, "temp") {
		t.Fatalf("expected build --help to point runtime writes at the OS temp directory, got:\n%s", cmd.Long)
	}
}

func containsFold(s, substr string) bool {
	return bytes.Contains(bytes.ToLower([]byte(s)), bytes.ToLower([]byte(substr)))
}

// TestRunBuild_TextOutput_EarlyErrorPrintsNothing is the regression guard for
// the task's "human-readable output must be completely unchanged" mandate:
// the exact same failure, without --output json, must not print anything to
// stdout (unchanged from before this feature existed) while still returning
// the identical error for the exit code.
func TestRunBuild_TextOutput_EarlyErrorPrintsNothing(t *testing.T) {
	logger := discardLogger()
	flags := &buildFlags{
		platforms:     []string{"linux/amd64"},
		dryRun:        true,
		printManifest: true,
	}

	out, err := captureStdout(t, func() error {
		return runBuild(context.Background(), logger, flags, nil)
	})

	if err == nil {
		t.Fatal("expected the --dry-run/--print-manifest conflict to still fail the command")
	}
	if out != "" {
		t.Errorf("text mode must print nothing to stdout, got: %q", out)
	}
}
