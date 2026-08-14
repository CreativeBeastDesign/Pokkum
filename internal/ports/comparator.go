package ports

import "context"

// ImageComparatorRequest represents a request to compare a remote registry image against a locally built OCI tarball.
type ImageComparatorRequest struct {
	RemoteImageRef     string
	LocalTarball       string
	RegistryConfigPath string
}

// ImageComparatorResult holds the multi-level L1/L2/L3 comparison output.
type ImageComparatorResult struct {
	Level           string   `json:"level"` // "L1", "L2", "L3"
	L1ExactMatch    bool     `json:"l1_exact_match"`
	L2SemanticMatch bool     `json:"l2_semantic_match"`
	L3FileDiffs     []string `json:"l3_file_diffs,omitempty"`
	Summary         string   `json:"summary"`
}

// ImageComparator defines the port for comparing remote and local OCI image layers.
type ImageComparator interface {
	CompareImages(ctx context.Context, req ImageComparatorRequest) (ImageComparatorResult, error)
}
