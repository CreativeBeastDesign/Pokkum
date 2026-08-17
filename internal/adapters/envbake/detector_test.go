package envbake

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestAdapter_DetectStaticEnv(t *testing.T) {
	projectDir := t.TempDir()
	srcDir := filepath.Join(projectDir, "src", "routes")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "import { PUBLIC_API_URL } from '$env/static/public';\n"
	if err := os.WriteFile(filepath.Join(srcDir, "+page.server.ts"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := NewAdapter()
	res, err := a.DetectStaticEnv(context.Background(), ports.EnvBakeRequest{ProjectDir: projectDir})
	if err != nil {
		t.Fatalf("DetectStaticEnv: %v", err)
	}
	want := []string{"PUBLIC_API_URL"}
	if !reflect.DeepEqual(res.Bindings, want) {
		t.Errorf("got %v, want %v", res.Bindings, want)
	}
}

func TestAdapter_DetectStaticEnv_NoSrcDirIsNotAnError(t *testing.T) {
	// A project directory with no src/ at all (e.g. a --dry-run against a
	// misconfigured path, or a strategy that doesn't use one) must not turn
	// a best-effort scan into a build failure.
	a := NewAdapter()
	res, err := a.DetectStaticEnv(context.Background(), ports.EnvBakeRequest{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatalf("expected a missing src/ dir to be tolerated, got: %v", err)
	}
	if len(res.Bindings) != 0 {
		t.Errorf("expected no bindings, got %v", res.Bindings)
	}
}
