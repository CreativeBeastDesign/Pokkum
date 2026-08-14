package sveltekitutils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdoptVercelProject(t *testing.T) {
	tmpDir := t.TempDir()

	packageJSON := `{
  "name": "my-vercel-app",
  "version": "1.0.0",
  "devDependencies": {
    "@sveltejs/adapter-vercel": "^5.0.0",
    "@sveltejs/kit": "^2.31.0"
  },
  "scripts": {
    "build": "vite build"
  }
}`
	svelteConfig := `import adapter from '@sveltejs/adapter-vercel';

const config = {
	kit: {
		adapter: adapter()
	}
};

export default config;
`
	dockerfile := `FROM node:20-alpine
WORKDIR /app
COPY . .
RUN npm install && npm run build
CMD ["node", "build"]
`

	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "svelte.config.js"), []byte(svelteConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".dockerignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Dry run
	res, err := Adopt(AdoptOptions{
		Dir:              tmpDir,
		DryRun:           true,
		RemoveDockerfile: true,
	})
	if err != nil {
		t.Fatalf("Adopt dry-run failed: %v", err)
	}
	if !res.DryRun || res.Status != "dry_run" {
		t.Errorf("expected dry_run status, got %+v", res)
	}
	if !res.PackageJSONUpdated || !res.ConfigUpdated || !res.IgnoreCreated {
		t.Errorf("expected updates detected in dry-run, got %+v", res)
	}
	if len(res.RemovedFiles) != 2 {
		t.Errorf("expected 2 removed files in dry-run, got %v", res.RemovedFiles)
	}

	// Verify files not yet modified on disk during dry-run
	if _, err := os.Stat(filepath.Join(tmpDir, "Dockerfile")); os.IsNotExist(err) {
		t.Errorf("Dockerfile should not be deleted during dry-run")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".pokkumignore")); !os.IsNotExist(err) {
		t.Errorf(".pokkumignore should not be created during dry-run")
	}

	// 2. Real execution
	resReal, err := Adopt(AdoptOptions{
		Dir:              tmpDir,
		DryRun:           false,
		RemoveDockerfile: true,
	})
	if err != nil {
		t.Fatalf("Adopt real run failed: %v", err)
	}
	if resReal.Status != "adopted" {
		t.Errorf("expected status 'adopted', got %q", resReal.Status)
	}

	// Verify Dockerfile & .dockerignore were deleted
	if _, err := os.Stat(filepath.Join(tmpDir, "Dockerfile")); !os.IsNotExist(err) {
		t.Errorf("Dockerfile should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".dockerignore")); !os.IsNotExist(err) {
		t.Errorf(".dockerignore should have been deleted")
	}

	// Verify .pokkumignore created
	if _, err := os.Stat(filepath.Join(tmpDir, ".pokkumignore")); err != nil {
		t.Errorf(".pokkumignore was not created: %v", err)
	}

	// Verify package.json contents
	pkgData, err := os.ReadFile(filepath.Join(tmpDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	pkgStr := string(pkgData)
	if strings.Contains(pkgStr, "@sveltejs/adapter-vercel") {
		t.Errorf("package.json still contains @sveltejs/adapter-vercel: %s", pkgStr)
	}
	if !strings.Contains(pkgStr, "@jesterkit/exe-sveltekit") {
		t.Errorf("package.json missing @jesterkit/exe-sveltekit: %s", pkgStr)
	}
	if !strings.Contains(pkgStr, "pokkum:build") {
		t.Errorf("package.json missing pokkum:build script: %s", pkgStr)
	}

	// Verify svelte.config.js contents
	cfgData, err := os.ReadFile(filepath.Join(tmpDir, "svelte.config.js"))
	if err != nil {
		t.Fatal(err)
	}
	cfgStr := string(cfgData)
	if !strings.Contains(cfgStr, "@jesterkit/exe-sveltekit") {
		t.Errorf("svelte.config.js missing @jesterkit/exe-sveltekit: %s", cfgStr)
	}
	if !strings.Contains(cfgStr, "SOURCE_DATE_EPOCH") {
		t.Errorf("svelte.config.js missing SOURCE_DATE_EPOCH pin: %s", cfgStr)
	}
}

func TestAdoptNoPackageJSON(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := Adopt(AdoptOptions{
		Dir: tmpDir,
	})
	if err == nil {
		t.Fatalf("expected error when package.json is missing, got nil")
	}
}
