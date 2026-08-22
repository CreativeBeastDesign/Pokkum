package scannerutils

import (
	"fmt"
	"testing"
)

// Every lockfile format can record the same package name at more than one
// version — a hoisted copy plus nested copies belonging to some dependency.
// The catalogue is keyed by name and holds one of them, so which one wins has
// to be decided by a rule rather than by Go's randomized map iteration.
//
// These tests iterate, because a two-entry map-order bug passes a single run
// about half the time. 200 iterations makes a surviving bug a ~10^-60 event.
const determinismRuns = 200

func versionsByName(pkgs []CatalogPackage) map[string]string {
	out := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		if _, ok := out[p.Name]; !ok {
			// First-wins, mirroring how the SBOM generator collapses this
			// slice — so the test sees what the generator would see.
			out[p.Name] = p.Version
		}
	}
	return out
}

// bunLockWithDuplicate mirrors the real shape found in a project's bun.lock:
// a hoisted entry keyed by the bare package name, and a nested entry keyed by
// its dependency path, at a different version.
const bunLockWithDuplicate = `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "devDependencies": { "knip": "^5.0.0" } } },
  "packages": {
    "@oxc-parser/binding-linux-arm64-gnu": ["@oxc-parser/binding-linux-arm64-gnu@0.127.0", "", {}, "sha512-aaa"],
    "knip": ["knip@5.0.0", "", { "dependencies": { "oxc-parser": "0.137.0" } }, "sha512-bbb"],
    "knip/oxc-parser/@oxc-parser/binding-linux-arm64-gnu": ["@oxc-parser/binding-linux-arm64-gnu@0.137.0", "", {}, "sha512-ccc"]
  }
}`

func TestParseBunLock_DuplicateNameResolvesDeterministicallyToTheHoistedCopy(t *testing.T) {
	const dup = "@oxc-parser/binding-linux-arm64-gnu"
	var first string
	for i := 0; i < determinismRuns; i++ {
		pkgs, err := ParseBunLock([]byte(bunLockWithDuplicate))
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		got := versionsByName(pkgs)[dup]
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d resolved %s to %q, run 0 resolved it to %q — the SBOM changes between builds of identical source",
				i, dup, got, first)
		}
	}
	// Not just stable, but the right one: 0.127.0 is the hoisted copy at
	// node_modules/<name>, which is what a bare import actually resolves to.
	if first != "0.127.0" {
		t.Errorf("%s = %q, want the hoisted 0.127.0 rather than the nested copy", dup, first)
	}
}

// npmLockV2WithDuplicate keys entries by install path, so the hoisted copy is
// the one whose path starts at the root node_modules.
const npmLockV2WithDuplicate = `{
  "lockfileVersion": 3,
  "packages": {
    "": { "name": "app" },
    "node_modules/dup": { "version": "1.0.0" },
    "node_modules/wrapper": { "version": "2.0.0" },
    "node_modules/wrapper/node_modules/dup": { "version": "9.9.9" }
  }
}`

func TestParsePackageLock_V2DuplicateResolvesDeterministicallyToTheHoistedCopy(t *testing.T) {
	var first string
	for i := 0; i < determinismRuns; i++ {
		pkgs, err := ParsePackageLock([]byte(npmLockV2WithDuplicate))
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		got := versionsByName(pkgs)["dup"]
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d resolved dup to %q, run 0 to %q", i, got, first)
		}
	}
	if first != "1.0.0" {
		t.Errorf("dup = %q, want the hoisted 1.0.0 rather than the nested 9.9.9", first)
	}
}

// npmLockV1WithDuplicate nests under "dependencies" instead of keying by path.
const npmLockV1WithDuplicate = `{
  "lockfileVersion": 1,
  "dependencies": {
    "dup": { "version": "1.0.0" },
    "wrapper": {
      "version": "2.0.0",
      "dependencies": { "dup": { "version": "9.9.9" } }
    }
  }
}`

func TestParsePackageLock_V1DuplicateResolvesDeterministicallyToTheShallowestCopy(t *testing.T) {
	var first string
	for i := 0; i < determinismRuns; i++ {
		pkgs, err := ParsePackageLock([]byte(npmLockV1WithDuplicate))
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		got := versionsByName(pkgs)["dup"]
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d resolved dup to %q, run 0 to %q", i, got, first)
		}
	}
	if first != "1.0.0" {
		t.Errorf("dup = %q, want the top-level 1.0.0 rather than the nested 9.9.9", first)
	}
}

func TestParsePnpmLock_EmitsPackagesInStableOrder(t *testing.T) {
	const lock = `lockfileVersion: '9.0'
packages:
  /alpha@1.0.0:
    resolution: {integrity: sha512-a}
  /dup@1.0.0:
    resolution: {integrity: sha512-b}
  /dup@9.9.9:
    resolution: {integrity: sha512-c}
  /zeta@1.0.0:
    resolution: {integrity: sha512-d}
`
	var first string
	for i := 0; i < determinismRuns; i++ {
		pkgs, err := ParsePnpmLock([]byte(lock))
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var order string
		for _, p := range pkgs {
			order += fmt.Sprintf("%s@%s ", p.Name, p.Version)
		}
		if i == 0 {
			first = order
			continue
		}
		if order != first {
			t.Fatalf("run %d order = %q, run 0 = %q — the caller keeps the first entry per name, so this picks a random version",
				i, order, first)
		}
	}
}
