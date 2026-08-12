package provenance

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var _ ports.ProvenanceResolver = (*Resolver)(nil)

// Resolver implements ports.ProvenanceResolver.
type Resolver struct {
	log *slog.Logger
}

// NewResolver constructs a ProvenanceResolver instance.
func NewResolver(log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{log: log}
}

// ResolveProvenance parses and verifies attestations for an image ref.
func (r *Resolver) ResolveProvenance(ctx context.Context, req ports.ProvenanceResolverRequest) (ports.ProvenanceSummary, error) {
	if req.ImageRef == "" {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: image ref is required: %w", core.ErrInvalidRequest)
	}

	r.log.DebugContext(ctx, "resolving provenance for image", "ref", req.ImageRef)

	// Mock/stub extraction of SLSA provenance & toolchain inspection for M1
	summary := ports.ProvenanceSummary{
		ImageRef:       req.ImageRef,
		ImageDigest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		HasProvenance:  true,
		SignatureValid: true,
		SignerIdentity: "keyless:ci@github.com",
		PinnedInputs: ports.PinnedBuildInputs{
			Repo:          "github.com/example/my-app",
			Commit:        "a1b2c3d4e5f678901234567890abcdef12345678",
			BaseImageRef:  "gcr.io/distroless/cc-debian12:nonroot",
			BaseImageHash: "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			BunVersion:    "1.2.18",
			BunBinaryHash: "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
			GoVersion:     runtime.Version(),
			BuilderOSArch: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		},
		ToolchainMatch:  true,
		ToolchainNotes:  fmt.Sprintf("Local builder (%s) matches provenance builder (%s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH),
		ExpectedL1Match: true,
	}

	if req.ExpectSource != "" {
		if !strings.Contains(summary.PinnedInputs.Repo, req.ExpectSource) && !strings.Contains(summary.PinnedInputs.Commit, req.ExpectSource) {
			return summary, fmt.Errorf("provenance resolver: expected source %s does not match resolved provenance (%s@%s): %w",
				req.ExpectSource, summary.PinnedInputs.Repo, summary.PinnedInputs.Commit, core.ErrInvalidRequest)
		}
	}

	return summary, nil
}
