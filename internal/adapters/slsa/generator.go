package slsa

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Generator implements ports.SLSAGenerator.
type Generator struct {
	log *slog.Logger
}

// NewGenerator constructs a Generator. A nil logger defaults to slog.Default().
func NewGenerator(log *slog.Logger) *Generator {
	if log == nil {
		log = slog.Default()
	}
	return &Generator{log: log}
}

// Generate creates an in-toto SLSA v1.0 provenance statement for a build.
func (g *Generator) Generate(ctx context.Context, req ports.SLSAGeneratorRequest) (ports.SLSAStatement, error) {
	if req.OutputDigest.Hex == "" {
		return ports.SLSAStatement{}, fmt.Errorf("slsa: output digest is required: %w", core.ErrInvalidRequest)
	}

	// 1. Build Subject descriptor
	subjectURI := req.Repo
	if subjectURI != "" && req.OutputDigest.Hex != "" {
		subjectURI = fmt.Sprintf("%s@%s", req.Repo, req.OutputDigest.String())
	}
	subject := ports.ResourceDescriptor{
		Name: req.Repo,
		URI:  subjectURI,
		Digest: map[string]string{
			req.OutputDigest.Algorithm: req.OutputDigest.Hex,
		},
	}

	// 2. Build Resolved Dependencies
	var deps []ports.ResourceDescriptor

	// 2a. Base Image dependency
	if req.BaseImage.Digest.Hex != "" {
		baseURI := req.BaseImage.PinnedRef
		if baseURI == "" {
			baseURI = req.BaseImage.Ref
		}
		deps = append(deps, ports.ResourceDescriptor{
			Name: "base-image",
			URI:  baseURI,
			Digest: map[string]string{
				req.BaseImage.Digest.Algorithm: req.BaseImage.Digest.Hex,
			},
		})
	}

	// 2b. Source Repository dependency
	if req.GitCommit != "" {
		gitURI := req.GitRepo
		if gitURI == "" {
			gitURI = "git+source"
		}
		deps = append(deps, ports.ResourceDescriptor{
			Name: "source-code",
			URI:  gitURI,
			Digest: map[string]string{
				"gitCommit": req.GitCommit,
			},
		})
	}

	// 2c. Lockfile dependencies
	lockfileDeps, err := inspectLockfiles(req.ProjectDir)
	if err != nil {
		g.log.DebugContext(ctx, "slsa: lockfile inspection warning", "dir", req.ProjectDir, "err", err)
	} else {
		deps = append(deps, lockfileDeps...)
	}
	for lockName, lockHash := range req.LockfileHashes {
		deps = append(deps, ports.ResourceDescriptor{
			Name:   lockName,
			URI:    "file://" + lockName,
			Digest: map[string]string{"sha256": lockHash},
		})
	}

	// 2d. Toolchain dependencies
	if req.Toolchain.BunVersion != "" {
		bunDep := ports.ResourceDescriptor{
			Name: "bun",
			URI:  "pkg:generic/bun@" + req.Toolchain.BunVersion,
		}
		if req.Toolchain.BunBinaryHash != "" {
			bunDep.Digest = map[string]string{"sha256": req.Toolchain.BunBinaryHash}
		}
		deps = append(deps, bunDep)
	}
	if req.Toolchain.SupervisorVersion != "" {
		deps = append(deps, ports.ResourceDescriptor{
			Name: "pokkum-init",
			URI:  "pkg:generic/pokkum-init@" + req.Toolchain.SupervisorVersion,
		})
	}
	if req.Toolchain.GoVersion != "" {
		deps = append(deps, ports.ResourceDescriptor{
			Name: "go",
			URI:  "pkg:generic/go@" + req.Toolchain.GoVersion,
		})
	}

	// 3. Build External Parameters
	platformStrs := make([]string, len(req.Platforms))
	for i, p := range req.Platforms {
		platformStrs[i] = p.String()
	}

	extParams := map[string]any{
		"repository": req.Repo,
		"tags":       req.Tags,
		"platforms":  platformStrs,
		"outputMode": req.OutputMode,
	}

	// 4. Build Internal Parameters
	intParams := map[string]any{
		"hermetic": req.Hermetic,
	}
	if !req.SourceDateEpoch.IsZero() {
		intParams["sourceDateEpoch"] = req.SourceDateEpoch.Unix()
	}
	if req.Toolchain.PokkumVersion != "" {
		intParams["pokkumVersion"] = req.Toolchain.PokkumVersion
	}
	if req.Toolchain.PokkumCommit != "" {
		intParams["pokkumCommit"] = req.Toolchain.PokkumCommit
	}
	if req.Toolchain.BuilderOSArch != "" {
		intParams["builderOSArch"] = req.Toolchain.BuilderOSArch
	}

	// 5. Construct Statement
	stmt := ports.SLSAStatement{
		Type:          ports.InTotoStatementType,
		Subject:       []ports.ResourceDescriptor{subject},
		PredicateType: ports.SLSAProvenancePredicateType,
		Predicate: ports.SLSAPredicate{
			BuildDefinition: ports.SLSABuildDefinition{
				BuildType:            ports.SLSABuildType,
				ExternalParameters:   extParams,
				InternalParameters:   intParams,
				ResolvedDependencies: deps,
			},
			RunDetails: ports.SLSARunDetails{
				Builder: ports.SLSABuilder{
					ID: ports.SLSABuilderID,
				},
				Metadata: ports.SLSABuildMetadata{
					StartedOn:  &req.SourceDateEpoch,
					FinishedOn: &req.SourceDateEpoch,
				},
			},
		},
	}

	g.log.InfoContext(ctx, "slsa: generated provenance statement",
		"subject", req.OutputDigest.String(),
		"dependencies", len(deps),
		"hermetic", req.Hermetic,
	)

	return stmt, nil
}

// SanitizeRepo returns a clean repository URI without trailing slashes.
func SanitizeRepo(repo string) string {
	return strings.TrimSuffix(strings.TrimSpace(repo), "/")
}
