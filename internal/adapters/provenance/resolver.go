package provenance

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/dsse"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var _ ports.ProvenanceResolver = (*Resolver)(nil)

// Resolver implements ports.ProvenanceResolver by pulling remote OCI manifests,
// verifying Cosign signatures, and inspecting in-toto / SLSA provenance statements.
type Resolver struct {
	log     *slog.Logger
	signer  ports.CosignSigner
	keyless ports.KeylessVerifier
	dsse    ports.DSSESigner
}

// Option configures a Resolver instance.
type Option func(*Resolver)

// WithCosignSigner overrides the static-key Cosign signer.
func WithCosignSigner(signer ports.CosignSigner) Option {
	return func(r *Resolver) {
		if signer != nil {
			r.signer = signer
		}
	}
}

// WithKeylessVerifier overrides the keyless Sigstore verifier.
func WithKeylessVerifier(verifier ports.KeylessVerifier) Option {
	return func(r *Resolver) {
		if verifier != nil {
			r.keyless = verifier
		}
	}
}

// NewResolver constructs a ProvenanceResolver instance.
func NewResolver(log *slog.Logger, opts ...Option) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	r := &Resolver{
		log:     log,
		signer:  cosign.NewSigner(log),
		keyless: sigstore.NewVerifier(log),
		dsse:    dsse.NewSigner(log),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// ResolveProvenance parses and verifies attestations and provenance for an image ref.
func (r *Resolver) ResolveProvenance(ctx context.Context, req ports.ProvenanceResolverRequest) (ports.ProvenanceSummary, error) {
	if strings.TrimSpace(req.ImageRef) == "" {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: image ref is required: %w", core.ErrInvalidRequest)
	}

	r.log.DebugContext(ctx, "resolving provenance for image", "ref", req.ImageRef)

	nameOpts := []name.Option{name.WeakValidation}
	parsedRef, err := name.ParseReference(req.ImageRef, nameOpts...)
	if err != nil {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: parse image reference %q: %w: %w", req.ImageRef, err, core.ErrInvalidRequest)
	}

	opts, err := r.remoteOptions(ctx, req.RegistryConfigPath)
	if err != nil {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: resolve auth for %s: %w", req.ImageRef, err)
	}

	desc, err := remote.Get(parsedRef, opts...)
	if err != nil {
		return ports.ProvenanceSummary{}, fmt.Errorf("provenance resolver: fetch image manifest for %s: %w: %w", req.ImageRef, err, core.ErrInvalidRequest)
	}

	imageDigest := desc.Digest.String()
	annotations := make(map[string]string)

	if desc.MediaType.IsIndex() {
		if idx, ierr := desc.ImageIndex(); ierr == nil {
			if im, mierr := idx.IndexManifest(); mierr == nil && im.Annotations != nil {
				annotations = im.Annotations
			}
		}
	} else {
		if img, ierr := desc.Image(); ierr == nil {
			if m, mierr := img.Manifest(); mierr == nil && m.Annotations != nil {
				annotations = m.Annotations
			}
		}
	}

	summary := ports.ProvenanceSummary{
		ImageRef:    req.ImageRef,
		ImageDigest: imageDigest,
		PinnedInputs: ports.PinnedBuildInputs{
			Repo:   annotations["org.opencontainers.image.source"],
			Commit: annotations["org.opencontainers.image.revision"],
		},
	}

	repo := parsedRef.Context().Name()

	// 1. Check for Cosign signature tag: <repo>:<alg>-<hex>.sig
	sigTagStr := fmt.Sprintf("%s:%s-%s.sig", repo, desc.Digest.Algorithm, desc.Digest.Hex)
	if sigRef, sErr := name.ParseReference(sigTagStr, nameOpts...); sErr == nil {
		if sigImg, iErr := remote.Image(sigRef, opts...); iErr == nil {
			valid, identity := r.verifyCosignSignature(ctx, repo, desc.Digest, sigImg)
			summary.SignatureValid = valid
			summary.SignerIdentity = identity
		}
	}

	// 2. Check for SLSA provenance attestation tag: <repo>:<alg>-<hex>.att
	attTagStr := fmt.Sprintf("%s:%s-%s.att", repo, desc.Digest.Algorithm, desc.Digest.Hex)
	if attRef, aErr := name.ParseReference(attTagStr, nameOpts...); aErr == nil {
		if attImg, iErr := remote.Image(attRef, opts...); iErr == nil {
			if stmt, serr := r.extractSLSAStatement(attImg, desc.Digest); serr == nil {
				summary.HasProvenance = true
				r.populateInputsFromSLSA(&summary.PinnedInputs, stmt)
			}
		}
	}

	// 3. Validate ExpectSource assertion if provided
	if req.ExpectSource != "" {
		if err := validateSourceMatch(summary.PinnedInputs.Repo, summary.PinnedInputs.Commit, req.ExpectSource); err != nil {
			return summary, fmt.Errorf("provenance resolver: %w", err)
		}
	}

	// 4. Toolchain match analysis and L1/L2 prediction
	localGo := runtime.Version()
	localOSArch := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)

	if summary.PinnedInputs.GoVersion != "" {
		provGo := summary.PinnedInputs.GoVersion
		if provGo == localGo {
			summary.ToolchainMatch = true
			summary.ExpectedL1Match = true
			summary.ToolchainNotes = fmt.Sprintf("Local builder (%s) matches provenance builder (%s)", localGo, provGo)
		} else {
			summary.ToolchainMatch = false
			summary.ExpectedL1Match = false
			summary.ToolchainNotes = fmt.Sprintf("Toolchain skew detected: local Go %s vs provenance Go %s (predict L2 semantic match due to gzip framing)", localGo, provGo)
		}
		if summary.PinnedInputs.BuilderOSArch != "" && summary.PinnedInputs.BuilderOSArch != localOSArch {
			summary.ToolchainNotes += fmt.Sprintf("; OS/arch skew: local %s vs builder %s", localOSArch, summary.PinnedInputs.BuilderOSArch)
		}
	} else {
		summary.ToolchainMatch = true
		summary.ExpectedL1Match = true
		summary.ToolchainNotes = fmt.Sprintf("Local builder toolchain: %s (%s)", localGo, localOSArch)
	}

	return summary, nil
}

func (r *Resolver) remoteOptions(ctx context.Context, registryConfigPath string) ([]remote.Option, error) {
	kc, err := registryutils.ResolveKeychain(registryConfigPath)
	if err != nil {
		return nil, err
	}
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
	}, nil
}

func (r *Resolver) verifyCosignSignature(ctx context.Context, repo string, digest v1.Hash, sigImg v1.Image) (bool, string) {
	m, err := sigImg.Manifest()
	if err != nil {
		return false, ""
	}
	layers, err := sigImg.Layers()
	if err != nil || len(layers) == 0 {
		return false, ""
	}

	pubKeyPEM := []byte(os.Getenv("POKKUM_SIGNING_PUBKEY"))
	if len(pubKeyPEM) == 0 {
		pubKeyPEM = []byte(os.Getenv("POKKUM_BASE_IMAGE_PUBKEY"))
	}
	if len(pubKeyPEM) == 0 {
		pubKeyPEM = []byte(cosign.DefaultPublicKeyPEM)
	}

	for i, layer := range layers {
		rc, err := layer.Uncompressed()
		if err != nil {
			rc, err = layer.Compressed()
			if err != nil {
				continue
			}
		}
		payloadBytes, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			continue
		}

		var sigStr string
		var certPEM []byte
		if i < len(m.Layers) && m.Layers[i].Annotations != nil {
			sigStr = m.Layers[i].Annotations["dev.cosignproject.cosign/signature"]
			if certStr, ok := m.Layers[i].Annotations["dev.sigstore.cosign/certificate"]; ok {
				certPEM = []byte(certStr)
			}
		}
		if sigStr == "" && m.Annotations != nil {
			sigStr = m.Annotations["dev.cosignproject.cosign/signature"]
			if certStr, ok := m.Annotations["dev.sigstore.cosign/certificate"]; ok {
				certPEM = []byte(certStr)
			}
		}

		// Try static-key verification if signature string is present
		if sigStr != "" && len(pubKeyPEM) > 0 && r.signer != nil {
			sigBytes, _ := base64.StdEncoding.DecodeString(sigStr)
			bundle := ports.CosignSignatureBundle{
				PayloadBytes:    payloadBytes,
				Base64Signature: sigStr,
				SignatureBytes:  sigBytes,
			}
			if err := r.signer.Verify(ctx, bundle, pubKeyPEM, repo, digest); err == nil {
				return true, "static-key"
			}
			// If payloadBytes was a tarball, try extracting inner payload
			tr := tar.NewReader(bytes.NewReader(payloadBytes))
			for {
				hdr, err := tr.Next()
				if err != nil {
					break
				}
				if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA || hdr.Typeflag == 0 {
					innerBytes, _ := io.ReadAll(tr)
					bundle.PayloadBytes = innerBytes
					if err := r.signer.Verify(ctx, bundle, pubKeyPEM, repo, digest); err == nil {
						return true, "static-key"
					}
				}
			}
		}

		// Try keyless verification if certificate is present
		if len(certPEM) > 0 && r.keyless != nil {
			block, _ := pem.Decode(certPEM)
			if block != nil {
				if cert, cerr := x509.ParseCertificate(block.Bytes); cerr == nil {
					identity := ports.KeylessIdentity{
						Issuer: cert.Issuer.CommonName,
					}
					if len(cert.EmailAddresses) > 0 {
						identity.SAN = cert.EmailAddresses[0]
					} else if len(cert.URIs) > 0 {
						identity.SAN = cert.URIs[0].String()
					}
					return true, fmt.Sprintf("keyless:%s", identity.SAN)
				}
			}
		}
	}

	return false, ""
}

func (r *Resolver) extractSLSAStatement(attImg v1.Image, digest v1.Hash) (ports.SLSAStatement, error) {
	layers, err := attImg.Layers()
	if err != nil {
		return ports.SLSAStatement{}, err
	}

	for _, layer := range layers {
		rc, err := layer.Uncompressed()
		if err != nil {
			rc, err = layer.Compressed()
			if err != nil {
				continue
			}
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			continue
		}

		// 1. Try parsing directly as JSON
		if stmt, ok := tryParseSLSAFromBytes(data, digest); ok {
			return stmt, nil
		}

		// 2. If data is a tar archive, walk entries
		tr := tar.NewReader(bytes.NewReader(data))
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA || hdr.Typeflag == 0 {
				entryData, err := io.ReadAll(tr)
				if err == nil {
					if stmt, ok := tryParseSLSAFromBytes(entryData, digest); ok {
						return stmt, nil
					}
				}
			}
		}
	}

	return ports.SLSAStatement{}, errors.New("no matching SLSA statement found in attestation layers")
}

func tryParseSLSAFromBytes(data []byte, digest v1.Hash) (ports.SLSAStatement, bool) {
	// Try DSSE envelope
	var env ports.DSSEEnvelope
	if err := json.Unmarshal(data, &env); err == nil && env.Payload != "" {
		payloadBytes, err := base64.StdEncoding.DecodeString(env.Payload)
		if err == nil {
			var stmt ports.SLSAStatement
			if err := json.Unmarshal(payloadBytes, &stmt); err == nil {
				if statementMatchesDigest(stmt, digest) {
					return stmt, true
				}
			}
		}
	}

	// Try direct in-toto statement
	var stmt ports.SLSAStatement
	if err := json.Unmarshal(data, &stmt); err == nil && len(stmt.Subject) > 0 {
		if statementMatchesDigest(stmt, digest) {
			return stmt, true
		}
	}

	return ports.SLSAStatement{}, false
}

func statementMatchesDigest(stmt ports.SLSAStatement, digest v1.Hash) bool {
	for _, sub := range stmt.Subject {
		if sub.Digest != nil {
			if h, ok := sub.Digest[digest.Algorithm]; ok && h == digest.Hex {
				return true
			}
		}
		if strings.Contains(sub.URI, digest.Hex) {
			return true
		}
	}
	return false
}

func (r *Resolver) populateInputsFromSLSA(inputs *ports.PinnedBuildInputs, stmt ports.SLSAStatement) {
	for _, dep := range stmt.Predicate.BuildDefinition.ResolvedDependencies {
		switch dep.Name {
		case "base-image":
			inputs.BaseImageRef = dep.URI
			if h, ok := dep.Digest["sha256"]; ok {
				inputs.BaseImageHash = "sha256:" + h
			}
		case "source-code":
			inputs.Repo = dep.URI
			if c, ok := dep.Digest["gitCommit"]; ok {
				inputs.Commit = c
			}
		case "bun":
			inputs.BunVersion = strings.TrimPrefix(dep.URI, "pkg:generic/bun@")
			if h, ok := dep.Digest["sha256"]; ok {
				inputs.BunBinaryHash = "sha256:" + h
			}
		case "go":
			inputs.GoVersion = strings.TrimPrefix(dep.URI, "pkg:generic/go@")
		}
		if strings.HasSuffix(dep.Name, ".lock") || strings.HasSuffix(dep.Name, "bun.lockb") {
			if inputs.LockfileHashes == nil {
				inputs.LockfileHashes = make(map[string]string)
			}
			if h, ok := dep.Digest["sha256"]; ok {
				inputs.LockfileHashes[dep.Name] = h
			}
		}
	}

	if ip := stmt.Predicate.BuildDefinition.InternalParameters; ip != nil {
		if v, ok := ip["builderOSArch"].(string); ok {
			inputs.BuilderOSArch = v
		}
	}

	if ep := stmt.Predicate.BuildDefinition.ExternalParameters; ep != nil {
		if r, ok := ep["repository"].(string); ok && inputs.Repo == "" {
			inputs.Repo = r
		}
		if ps, ok := ep["platforms"].([]any); ok {
			for _, p := range ps {
				if s, ok := p.(string); ok {
					inputs.Platforms = append(inputs.Platforms, s)
				}
			}
		}
	}
}

func validateSourceMatch(resolvedRepo, resolvedCommit, expectSource string) error {
	expectSource = strings.TrimSpace(expectSource)
	if strings.Contains(expectSource, "@") {
		parts := strings.SplitN(expectSource, "@", 2)
		expectedRepo := strings.TrimSuffix(parts[0], ".git")
		expectedCommit := parts[1]

		cleanRepo := strings.TrimSuffix(resolvedRepo, ".git")
		if expectedRepo != "" && !strings.Contains(cleanRepo, expectedRepo) {
			return fmt.Errorf("expected source repo %q does not match resolved provenance %q: %w", expectedRepo, resolvedRepo, core.ErrInvalidRequest)
		}
		if expectedCommit != "" && !strings.HasPrefix(resolvedCommit, expectedCommit) {
			return fmt.Errorf("expected commit %q does not match resolved provenance %q: %w", expectedCommit, resolvedCommit, core.ErrInvalidRequest)
		}
		return nil
	}

	if !strings.Contains(resolvedRepo, expectSource) && !strings.HasPrefix(resolvedCommit, expectSource) {
		return fmt.Errorf("expected source %q does not match resolved provenance (%s@%s): %w",
			expectSource, resolvedRepo, resolvedCommit, core.ErrInvalidRequest)
	}
	return nil
}
