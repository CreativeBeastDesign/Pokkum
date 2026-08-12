package ports

import "context"

// SecretMatch describes a detected secret or sensitive token leak in a file.
type SecretMatch struct {
	FilePath      string `json:"file_path"`
	LineNumber    int    `json:"line_number"`
	RuleName      string `json:"rule_name"`
	SecretSnippet string `json:"secret_snippet"`
}

// SecretScanRequest configures directory secret scanning options.
type SecretScanRequest struct {
	ProjectDir    string   `json:"project_dir"`
	AllowPatterns []string `json:"allow_patterns,omitempty"`
}

// SecretScanResult reports secret scan findings.
type SecretScanResult struct {
	Matches []SecretMatch `json:"matches"`
	Passed  bool          `json:"passed"`
}

// SecretGuard defines the boundary port for build-time secret leak detection.
type SecretGuard interface {
	ScanDirectory(ctx context.Context, req SecretScanRequest) (SecretScanResult, error)
}
