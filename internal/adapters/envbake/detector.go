// Package envbake implements ports.EnvBakeDetector, wrapping
// sveltekitutils.DetectStaticEnvBindings's source scan behind the
// hexagonal port boundary so internal/core can trigger it without importing
// an adapter/utility package directly.
package envbake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var _ ports.EnvBakeDetector = (*Adapter)(nil)

// Adapter implements ports.EnvBakeDetector.
type Adapter struct{}

// NewAdapter constructs a new Adapter.
func NewAdapter() *Adapter {
	return &Adapter{}
}

// DetectStaticEnv scans req.ProjectDir's conventional SvelteKit source root
// (src/) for $env/static/* imports. A missing src/ directory is reported as
// "nothing detected", not an error — this is a best-effort pre-build scan,
// not a project-shape validator (Preflight/checkEffectiveAdapter already own
// that job), so it should never be the reason a build fails.
func (a *Adapter) DetectStaticEnv(_ context.Context, req ports.EnvBakeRequest) (ports.EnvBakeResult, error) {
	bindings, err := sveltekitutils.DetectStaticEnvBindings(filepath.Join(req.ProjectDir, "src"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.EnvBakeResult{}, nil
		}
		return ports.EnvBakeResult{}, fmt.Errorf("envbake adapter: scan %s: %w", req.ProjectDir, err)
	}
	return ports.EnvBakeResult{Bindings: bindings}, nil
}
