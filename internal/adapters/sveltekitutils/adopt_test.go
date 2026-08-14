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
	if !res.PackageJSONUpdated || res.ConfigUpdated || !res.IgnoreCreated {
		t.Errorf("expected package.json + .pokkumignore updates but NOT svelte.config.js (WriteConfig defaults false), got %+v", res)
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
		WriteConfig:      true,
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

// TestAdoptWithoutWriteConfig_LeavesSvelteConfigUntouched confirms
// WriteConfig's default (false) genuinely leaves svelte.config.js
// byte-identical, not just that the ConfigUpdated flag reads false.
func TestAdoptWithoutWriteConfig_LeavesSvelteConfigUntouched(t *testing.T) {
	tmpDir := t.TempDir()

	packageJSON := `{"name": "my-app", "devDependencies": {"@sveltejs/adapter-vercel": "^5.0.0", "@sveltejs/kit": "^2.31.0"}}`
	svelteConfig := `import adapter from '@sveltejs/adapter-vercel';

const config = { kit: { adapter: adapter() } };
export default config;
`
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "svelte.config.js"), []byte(svelteConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Adopt(AdoptOptions{Dir: tmpDir}); err != nil {
		t.Fatalf("Adopt failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(tmpDir, "svelte.config.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != svelteConfig {
		t.Errorf("expected svelte.config.js byte-identical without --write-config, got:\n%s", got)
	}
}

// TestAdoptRejectsNonSvelteKitProject reproduces the exact gap found in
// review: without a detection gate, adopt would happily mutate an
// arbitrary Node project (e.g. a plain Express app) that was never
// SvelteKit, injecting a bogus @jesterkit/exe-sveltekit dependency.
func TestAdoptRejectsNonSvelteKitProject(t *testing.T) {
	tmpDir := t.TempDir()
	pkg := `{"name": "plain-express-app", "dependencies": {"express": "^4.19.0"}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Adopt(AdoptOptions{Dir: tmpDir}); err == nil {
		t.Fatal("expected Adopt to reject a project with no @sveltejs/kit dependency, got nil error")
	}

	got, err := os.ReadFile(filepath.Join(tmpDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != pkg {
		t.Errorf("expected package.json untouched after rejection, got:\n%s", got)
	}
}

// TestAdoptPreservesPackageJSONTopLevelKeyOrder guards against a codemod
// that alphabetizes every top-level key on write, which turns a one-line
// real change into a full-file reordering diff.
func TestAdoptPreservesPackageJSONTopLevelKeyOrder(t *testing.T) {
	tmpDir := t.TempDir()
	packageJSON := `{
  "name": "my-app",
  "version": "1.0.0",
  "type": "module",
  "scripts": {
    "build": "vite build"
  },
  "devDependencies": {
    "@sveltejs/kit": "^2.31.0"
  }
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Adopt(AdoptOptions{Dir: tmpDir}); err != nil {
		t.Fatalf("Adopt failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(tmpDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}

	nameIdx := strings.Index(string(got), `"name"`)
	versionIdx := strings.Index(string(got), `"version"`)
	typeIdx := strings.Index(string(got), `"type"`)
	scriptsIdx := strings.Index(string(got), `"scripts"`)
	devDepsIdx := strings.Index(string(got), `"devDependencies"`)
	if !(nameIdx < versionIdx && versionIdx < typeIdx && typeIdx < scriptsIdx && scriptsIdx < devDepsIdx) {
		t.Errorf("expected original top-level key order (name, version, type, scripts, devDependencies) preserved, got:\n%s", got)
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
