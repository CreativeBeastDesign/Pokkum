package cosign

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// buildPayload constructs a ports.CosignSimpleSigningPayload struct from req.
func buildPayload(req ports.CosignSignRequest) (ports.CosignSimpleSigningPayload, error) {
	repo := strings.TrimSpace(req.Repo)
	if repo == "" {
		return ports.CosignSimpleSigningPayload{}, fmt.Errorf("cosign: repo is required: %w", core.ErrNoDockerRepo)
	}
	if req.Digest.Hex == "" {
		return ports.CosignSimpleSigningPayload{}, fmt.Errorf("cosign: digest is required: %w", core.ErrInvalidRequest)
	}

	payload := ports.CosignSimpleSigningPayload{
		Critical: ports.CosignCritical{
			Identity: ports.CosignCriticalIdentity{
				DockerReference: repo,
			},
			Image: ports.CosignCriticalImage{
				DockerManifestDigest: req.Digest.String(),
			},
			Type: ports.CosignSimpleSigningType,
		},
	}

	// Add optional claims if provided
	creator := req.Creator
	if creator == "" {
		creator = "pokkum"
	}

	payload.Optional = ports.CosignOptional{
		Creator: creator,
	}

	if !req.SourceDateEpoch.IsZero() {
		payload.Optional.Timestamp = req.SourceDateEpoch.Unix()
	}

	if len(req.Annotations) > 0 {
		payload.Optional.Extra = req.Annotations
	}

	return payload, nil
}

// marshalPayload serializes a Simple Signing payload to JSON bytes.
func marshalPayload(payload ports.CosignSimpleSigningPayload) ([]byte, error) {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cosign: marshal payload: %w", err)
	}
	return b, nil
}
