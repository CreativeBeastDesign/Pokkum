package ports

// OutputFormat represents the stdout output serialization format.
type OutputFormat string

const (
	FormatText OutputFormat = "text"
	FormatJSON OutputFormat = "json"
)

// JSONEnvelope represents the versioned envelope for all --output=json CLI responses.
type JSONEnvelope struct {
	SchemaVersion string      `json:"schema_version"`
	Command       string      `json:"command"`
	Status        string      `json:"status"` // "success" or "error"
	Data          interface{} `json:"data,omitempty"`
	Error         *ErrorData  `json:"error,omitempty"`
}

// ErrorData holds detailed error information for machine-readable JSON output.
type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// DoctorCheck represents the status of an individual preflight check in `pokkum doctor`.
type DoctorCheck struct {
	Name        string `json:"name"`
	Passed      bool   `json:"passed"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

// DoctorOutput represents the structured data payload for `pokkum doctor`.
type DoctorOutput struct {
	Passed  bool          `json:"passed"`
	Checks  []DoctorCheck `json:"checks"`
	Summary string        `json:"summary"`
}

// LayerExplain describes an individual container layer's size and function.
type LayerExplain struct {
	LayerIndex int    `json:"layer_index"`
	Digest     string `json:"digest"`
	SizeBytes  int64  `json:"size_bytes"`
	Purpose    string `json:"purpose"`
	FileCount  int    `json:"file_count,omitempty"`
}

// ExplainOutput represents the structured data payload for `pokkum explain` / `pokkum why`.
type ExplainOutput struct {
	Target    string         `json:"target"`
	TotalSize int64          `json:"total_size_bytes"`
	Layers    []LayerExplain `json:"layers"`
	Platform  string         `json:"platform,omitempty"`
}

// MetricsOutput represents the status of the OTEL metrics server in `pokkum metrics`.
type MetricsOutput struct {
	ListeningPort int    `json:"listening_port"`
	Status        string `json:"status"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}
