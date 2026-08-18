package slsa

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var _ ports.SLSAGenerator = (*Generator)(nil)

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

	// 2b. Source Repository dependency.
	//
	// GitRepo/GitCommit are documented as "optional/discovered"
	// (ports.SLSAGeneratorRequest) — a caller may supply them explicitly
	// (tests, or a future non-git source), and pipeline.go's real build path
	// currently never does, leaving discovery here as the only thing that
	// actually populates them for a real build. This is a genuine
	// measurement of req.ProjectDir's actual working tree at build time via
	// git, not an assertion accepted from a flag — the same principle
	// already applied a few lines below to the lockfile dependencies (hashed
	// from the real file, never taken on trust). Only attempted when the
	// caller left the corresponding field empty; an explicit value always
	// wins.
	gitRepo, gitCommit := req.GitRepo, req.GitCommit
	if gitCommit == "" {
		if commit, dirty := discoverGitCommit(ctx, req.ProjectDir); commit != "" {
			gitCommit = commit
			if dirty {
				// A bare commit hash asserts the tree matched that commit
				// exactly, which is false with uncommitted changes present.
				// "-dirty" mirrors the established convention already used
				// elsewhere in this codebase for the same signal (see
				// cmd/pokkum/git_metadata.go's getGitVersion, which uses
				// `git describe --dirty`) rather than silently recording a
				// misleadingly-precise commit hash.
				gitCommit += "-dirty"
			}
			g.log.DebugContext(ctx, "slsa: discovered git commit for source-code provenance", "dir", req.ProjectDir, "dirty", dirty)
		}
	}
	if gitRepo == "" {
		if src := discoverGitSource(ctx, req.ProjectDir); src != "" {
			gitRepo = src
		}
	}
	if gitCommit != "" {
		gitURI := gitRepo
		if gitURI == "" {
			gitURI = "git+source"
		}
		deps = append(deps, ports.ResourceDescriptor{
			Name: "source-code",
			URI:  gitURI,
			Digest: map[string]string{
				"gitCommit": gitCommit,
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
	// The application runtime (--runtime) is an external parameter in SLSA
	// terms: user-supplied, and a verifier must replay the same value to
	// reproduce the image (a bun and a node build of identical source are
	// different images). Only recorded when the caller supplied it, so
	// statements from callers predating the field are byte-identical to
	// before.
	if req.Toolchain.AppRuntime != "" {
		extParams["runtime"] = req.Toolchain.AppRuntime
	}

	// 4. Build Internal Parameters
	intParams := map[string]any{
		"hermetic": req.Hermetic,
	}
	if req.HermeticEnforcement != "" {
		intParams["hermeticEnforcement"] = req.HermeticEnforcement
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
		"hermeticEnforcement", req.HermeticEnforcement,
	)

	return stmt, nil
}

// SanitizeRepo returns a clean repository URI without trailing slashes.
func SanitizeRepo(repo string) string {
	return strings.TrimSuffix(strings.TrimSpace(repo), "/")
}
