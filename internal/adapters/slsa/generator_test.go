package slsa

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestSLSAGenerator_Generate(t *testing.T) {
	// Create temporary directory with a test bun.lock file
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "bun.lock")
	lockContent := []byte("lockfile content for testing")
	if err := os.WriteFile(lockPath, lockContent, 0644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}

	gen := NewGenerator(nil)

	digestStr := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	outHash, err := v1.NewHash(digestStr)
	if err != nil {
		t.Fatalf("parse output hash: %v", err)
	}

	baseHashStr := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	baseHash, err := v1.NewHash(baseHashStr)
	if err != nil {
		t.Fatalf("parse base hash: %v", err)
	}

	now := time.Unix(1700000000, 0).UTC()
	req := ports.SLSAGeneratorRequest{
		ProjectDir:      tmpDir,
		Repo:            "ghcr.io/acme/app",
		Tags:            []string{"v1.0.0", "latest"},
		Platforms:       []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
		OutputMode:      "push",
		OutputDigest:    outHash,
		SourceDateEpoch: now,
		GitRepo:         "https://github.com/acme/app",
		GitCommit:       "abc123456789def",
		Hermetic:        true,
		BaseImage: ports.SLSABaseImage{
			Preset:    ports.BaseImageDistroless,
			Ref:       "gcr.io/distroless/static-debian12:nonroot",
			PinnedRef: "gcr.io/distroless/static-debian12@sha256:2222222222222222222222222222222222222222222222222222222222222222",
			Digest:    baseHash,
		},
		Toolchain: ports.SLSAToolchain{
			PokkumVersion:     "0.1.1",
			BunVersion:        "1.3.14",
			SupervisorVersion: "0.1.0",
		},
	}

	stmt, err := gen.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 1. Check in-toto statement envelopes
	if stmt.Type != ports.InTotoStatementType {
		t.Errorf("Type = %q, want %q", stmt.Type, ports.InTotoStatementType)
	}
	if stmt.PredicateType != ports.SLSAProvenancePredicateType {
		t.Errorf("PredicateType = %q, want %q", stmt.PredicateType, ports.SLSAProvenancePredicateType)
	}

	// 2. Check Subject
	if len(stmt.Subject) != 1 {
		t.Fatalf("len(Subject) = %d, want 1", len(stmt.Subject))
	}
	sub := stmt.Subject[0]
	if sub.Name != "ghcr.io/acme/app" {
		t.Errorf("Subject.Name = %q, want %q", sub.Name, "ghcr.io/acme/app")
	}
	if sub.Digest["sha256"] != "1111111111111111111111111111111111111111111111111111111111111111" {
		t.Errorf("Subject digest = %q", sub.Digest["sha256"])
	}

	// 3. Check Predicate BuildDefinition
	bd := stmt.Predicate.BuildDefinition
	if bd.BuildType != ports.SLSABuildType {
		t.Errorf("BuildType = %q, want %q", bd.BuildType, ports.SLSABuildType)
	}

	if bd.InternalParameters["hermetic"] != true {
		t.Errorf("InternalParameters[hermetic] = %v, want true", bd.InternalParameters["hermetic"])
	}
	if bd.InternalParameters["sourceDateEpoch"] != int64(1700000000) {
		t.Errorf("InternalParameters[sourceDateEpoch] = %v, want 1700000000", bd.InternalParameters["sourceDateEpoch"])
	}

	// 4. Check ResolvedDependencies
	if len(bd.ResolvedDependencies) < 4 {
		t.Fatalf("ResolvedDependencies count = %d, want >= 4", len(bd.ResolvedDependencies))
	}

	// Verify base image dependency exists
	foundBase := false
	foundSource := false
	foundLock := false
	for _, dep := range bd.ResolvedDependencies {
		if dep.Name == "base-image" && dep.Digest["sha256"] == "2222222222222222222222222222222222222222222222222222222222222222" {
			foundBase = true
		}
		if dep.Name == "source-code" && dep.Digest["gitCommit"] == "abc123456789def" {
			foundSource = true
		}
		if dep.Name == "bun.lock" && dep.Digest["sha256"] != "" {
			foundLock = true
		}
	}

	if !foundBase {
		t.Error("Base image dependency not found in ResolvedDependencies")
	}
	if !foundSource {
		t.Error("Source code dependency not found in ResolvedDependencies")
	}
	if !foundLock {
		t.Error("Lockfile dependency (bun.lock) not found in ResolvedDependencies")
	}
}

func TestSLSAGenerator_MissingOutputDigest(t *testing.T) {
	gen := NewGenerator(nil)
	req := ports.SLSAGeneratorRequest{
		Repo: "acme/app",
	}

	_, err := gen.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("Generate expected error for missing OutputDigest, got nil")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("err = %v, want wrapping core.ErrInvalidRequest", err)
	}
}
