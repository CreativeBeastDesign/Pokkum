package ports

import "context"

// EnvBakeRequest configures a $env/static/* source scan.
type EnvBakeRequest struct {
	ProjectDir string `json:"project_dir"`
}

// EnvBakeResult reports which $env/static/* bindings a source scan found —
// see AnnotationEnvBaked's doc comment for what a non-empty result means for
// the built image.
type EnvBakeResult struct {
	Bindings []string `json:"bindings"`
}

// EnvBakeDetector defines the boundary port for detecting SvelteKit
// $env/static/* imports: values SvelteKit inlines as literal build output at
// compile time, pinning the resulting image to whatever environment built
// it — unlike $env/dynamic/*, which is read at container startup and never
// baked in.
type EnvBakeDetector interface {
	DetectStaticEnv(ctx context.Context, req EnvBakeRequest) (EnvBakeResult, error)
}
