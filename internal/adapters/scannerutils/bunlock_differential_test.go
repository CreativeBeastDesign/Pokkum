package scannerutils

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// renderPackages renders a []CatalogPackage into a single stable string.
//
// It sorts, because ParseBunLock's *return order* has never been
// deterministic and this change does not make it so: the final loop ranges
// over the name-keyed `entries` map, and every caller re-sorts (see
// sbom.Generator and ExtractImagePackages). What must be stable — and what
// the 2026-08-22 incident was about — is the *content*: which duplicate won,
// and therefore which versions and scopes the document ends up claiming.
// Sorting here makes that the thing being compared, rather than an
// incidental iteration order that would make every comparison fail for a
// reason nobody cares about.
func renderPackages(pkgs []CatalogPackage) string {
	lines := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\tresolved=%v\tscope=%s",
			p.Name, p.Version, p.Type, p.Ecosystem, p.Resolved, p.Scope))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// bunLockCorpus is the differential corpus. Each entry is a bun.lock the
// typed decode and the map[string]any oracle must agree on exactly.
//
// The shapes here are chosen from what the old implementation's type
// assertions could actually distinguish — `entry.([]any)`,
// `arr[0].(string)`, `len(arr) < 3`, `arr[2].(map[string]any)`,
// `meta[key].(map[string]any)` — because those are precisely the places a
// typed decode can silently diverge. A corpus of only well-formed lockfiles
// would exercise none of them.
var bunLockCorpus = map[string]string{
	"empty object": `{}`,

	"empty packages": `{"lockfileVersion": 1, "workspaces": {}, "packages": {}}`,

	"no packages key": `{"lockfileVersion": 1, "workspaces": {"": {"name": "app", "dependencies": {"a": "^1.0.0"}}}}`,

	// The duplicate case the determinism incident was about: one name, two
	// versions, one hoisted (key == name) and one nested (key is a
	// dependency path).
	"hoisted plus nested duplicate": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "devDependencies": { "knip": "^5.0.0" } } },
  "packages": {
    "@oxc-parser/binding-linux-arm64-gnu": ["@oxc-parser/binding-linux-arm64-gnu@0.127.0", "", {}, "sha512-aaa"],
    "knip": ["knip@5.0.0", "", { "dependencies": { "oxc-parser": "0.137.0" } }, "sha512-bbb"],
    "knip/oxc-parser/@oxc-parser/binding-linux-arm64-gnu": ["@oxc-parser/binding-linux-arm64-gnu@0.137.0", "", {}, "sha512-ccc"]
  }
}`,

	// Two nested copies and NO hoisted sibling: the tie-break is "lexically
	// first key wins", which only a sorted-key iteration produces.
	"two nested duplicates no hoisted copy": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "alpha": "^1.0.0", "zeta": "^1.0.0" } } },
  "packages": {
    "alpha": ["alpha@1.0.0", "", { "dependencies": { "shared": "^2.0.0" } }, "sha512-a"],
    "zeta": ["zeta@1.0.0", "", { "dependencies": { "shared": "^9.0.0" } }, "sha512-z"],
    "zeta/shared": ["shared@9.9.9", "", {}, "sha512-z2"],
    "alpha/shared": ["shared@2.2.2", "", {}, "sha512-a2"]
  }
}`,

	// Three copies of one name where the hoisted one sorts LAST, so a
	// first-seen-wins rule would pick the wrong entry.
	"hoisted copy sorts last": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "zzz": "^1.0.0" } } },
  "packages": {
    "AAA/zzz": ["zzz@0.0.1", "", {}, "sha512-1"],
    "BBB/zzz": ["zzz@0.0.2", "", {}, "sha512-2"],
    "zzz": ["zzz@7.7.7", "", { "dependencies": { "dep-of-hoisted": "^1.0.0" } }, "sha512-3"],
    "dep-of-hoisted": ["dep-of-hoisted@1.0.0", "", {}, "sha512-4"]
  }
}`,

	// Trailing commas everywhere `bun install` actually writes them.
	"trailing commas": `{
  "lockfileVersion": 1,
  "workspaces": {
    "": {
      "name": "app",
      "dependencies": {
        "prod-a": "^1.0.0",
      },
      "devDependencies": {
        "dev-a": "^2.0.0",
      },
    },
  },
  "packages": {
    "prod-a": ["prod-a@1.0.0", "", { "dependencies": { "prod-b": "^1.0.0", }, }, "sha512-a"],
    "prod-b": ["prod-b@1.5.0", "", {}, "sha512-b"],
    "dev-a": ["dev-a@2.0.0", "", { "peerDependencies": { "prod-b": "*", }, }, "sha512-c"],
  },
}`,

	// A comma before '}' that lives INSIDE a string must not be stripped.
	"comma inside string value": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "weird": "^1.0.0" } } },
  "packages": {
    "weird": ["weird@1.0.0", "", { "dependencies": { "sub": "^1.0.0" } }, "sha512-a,}b\",} c"],
    "sub": ["sub@1.0.0", "", {}, "sha512-x"]
  }
}`,

	"optional and peer dependency edges": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "root": "^1.0.0" }, "devDependencies": { "devroot": "^1.0.0" } } },
  "packages": {
    "root": ["root@1.0.0", "", { "optionalDependencies": { "opt": "^1.0.0" }, "peerDependencies": { "peer": "^1.0.0" } }, "sha512-r"],
    "opt": ["opt@1.0.0", "", {}, "sha512-o"],
    "peer": ["peer@1.0.0", "", {}, "sha512-p"],
    "devroot": ["devroot@1.0.0", "", { "dependencies": { "devdep": "^1.0.0" } }, "sha512-d"],
    "devdep": ["devdep@1.0.0", "", {}, "sha512-dd"],
    "orphan": ["orphan@1.0.0", "", {}, "sha512-or"]
  }
}`,

	// Several workspaces, so the prod/dev root union is built from more than
	// one source.
	"multiple workspaces": `{
  "lockfileVersion": 1,
  "workspaces": {
    "": { "name": "root", "dependencies": { "shared-prod": "^1.0.0" } },
    "packages/api": { "name": "api", "dependencies": { "api-only": "^1.0.0" }, "devDependencies": { "api-dev": "^1.0.0" } },
    "packages/web": { "name": "web", "devDependencies": { "web-dev": "^1.0.0" } }
  },
  "packages": {
    "shared-prod": ["shared-prod@1.0.0", "", {}, "sha512-a"],
    "api-only": ["api-only@1.0.0", "", {}, "sha512-b"],
    "api-dev": ["api-dev@1.0.0", "", {}, "sha512-c"],
    "web-dev": ["web-dev@1.0.0", "", { "dependencies": { "shared-prod": "^1.0.0" } }, "sha512-d"]
  }
}`,

	// Tuple shapes the old type assertions handled by falling back rather
	// than failing. Every one of these must keep behaving identically.
	"odd tuple shapes": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "ok": "^1.0.0" } } },
  "packages": {
    "ok": ["ok@1.0.0", "", { "dependencies": { "two-elem": "^1.0.0" } }, "sha512-a"],
    "two-elem": ["two-elem@1.0.0", ""],
    "one-elem": ["one-elem@1.0.0"],
    "zero-elem": [],
    "not-an-array": { "version": "1.0.0" },
    "null-entry": null,
    "string-entry": "just-a-string",
    "number-first": [42, "", {}, "sha512-b"],
    "meta-is-string": ["meta-is-string@1.0.0", "", "not-an-object", "sha512-c"],
    "meta-is-null": ["meta-is-null@1.0.0", "", null, "sha512-d"],
    "meta-is-array": ["meta-is-array@1.0.0", "", ["nope"], "sha512-e"],
    "deps-is-string": ["deps-is-string@1.0.0", "", { "dependencies": "nope" }, "sha512-f"],
    "deps-is-array": ["deps-is-array@1.0.0", "", { "dependencies": ["nope"] }, "sha512-g"],
    "extra-elements": ["extra-elements@1.0.0", "", { "dependencies": { "ok": "^1.0.0" } }, "sha512-h", "extra", 9],
    "id-without-at": ["no-at-sign", "", {}, "sha512-i"],
    "id-empty-string": ["", "", {}, "sha512-j"],
    "name@1.2.3": ["", "", {}, "sha512-k"],
    "bare-key-no-at": ["", "", {}, "sha512-l"],
    "": ["empty-key@1.0.0", "", {}, "sha512-m"]
  }
}`,

	// Tuple position 1 is the registry, which bun writes as "" for the
	// default registry and as a URL otherwise. Every other fixture here, and
	// every committed one, leaves it empty -- which makes positions 0 and 1
	// indistinguishable to any test, so a parser that confused them would go
	// unnoticed. Found by deliberately breaking a decode to read position 0
	// concatenated with position 1 and watching the whole suite stay green.
	"non-empty registry at tuple position 1": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "private-pkg": "^1.0.0" } } },
  "packages": {
    "private-pkg": ["private-pkg@1.0.0", "https://npm.internal.example.com/", { "dependencies": { "sub": "^1.0.0" } }, "sha512-a"],
    "sub": ["sub@1.0.0", "registry-with@sign", {}, "sha512-b"]
  }
}`,

	// Meta keys whose spelling differs only in case must NOT be treated as
	// dependency edges: the parser looks them up with an exact
	// `meta["dependencies"]` map index. This is not hypothetical pedantry --
	// FuzzParseBunLock found it against a struct-tagged decode written during
	// this change, where encoding/json's case-INsensitive tag fallback
	// matched "dependenCies" and moved a package from ScopeUnknown to
	// ScopeProduction, changing the SBOM's package count. Any future rewrite
	// of this decode has to keep the lookup case-sensitive.
	"meta keys differing only in case are not edges": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "root": "^1.0.0" } } },
  "packages": {
    "root": ["root@1.0.0", "", { "dependenCies": { "not-an-edge": "^1.0.0" }, "OptionalDependencies": { "also-not": "^1.0.0" }, "PEERDEPENDENCIES": { "nope": "^1.0.0" } }, "sha512-a"],
    "not-an-edge": ["not-an-edge@1.0.0", "", {}, "sha512-b"],
    "also-not": ["also-not@1.0.0", "", {}, "sha512-c"],
    "nope": ["nope@1.0.0", "", {}, "sha512-d"]
  }
}`,

	// Non-registry sources, whose tuples bun writes at other arities and
	// whose ids carry protocol text after the '@'.
	"workspace git and tarball sources": `{
  "lockfileVersion": 1,
  "workspaces": {
    "": { "name": "root", "dependencies": { "pkg-a": "workspace:*", "gitdep": "github:o/r", "tardep": "https://x/y.tgz" } },
    "packages/a": { "name": "pkg-a" }
  },
  "packages": {
    "pkg-a": ["pkg-a@workspace:packages/a"],
    "gitdep": ["gitdep@git+ssh://git@github.com/o/r.git#abcdef", { "dependencies": { "sub": "^1.0.0" } }, "abcdef"],
    "tardep": ["tardep@https://example.com/t.tgz", "", { "dependencies": { "sub": "^1.0.0" } }, "sha512-t"],
    "sub": ["sub@1.0.0", "", {}, "sha512-s"]
  }
}`,

	// Meta carrying the non-edge keys real lockfiles use, which must be
	// skipped rather than treated as dependency edges.
	"meta with os cpu and bin keys": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "native": "^1.0.0" } } },
  "packages": {
    "native": ["native@1.0.0", "", { "os": "linux", "cpu": "x64", "bin": { "native": "bin.js" }, "dependencies": { "sub": "^1.0.0" } }, "sha512-a"],
    "sub": ["sub@1.0.0", "", {}, "sha512-b"]
  }
}`,

	// A scoped name whose '@' placement is what makes parseBunPackageEntry
	// split on the LAST '@' rather than the first.
	"scoped names": `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "app", "dependencies": { "@scope/pkg": "^1.0.0" } } },
  "packages": {
    "@scope/pkg": ["@scope/pkg@1.2.3", "", { "dependencies": { "@other/dep": "^2.0.0" } }, "sha512-a"],
    "@other/dep": ["@other/dep@2.0.0-beta.1", "", {}, "sha512-b"],
    "@scope/pkg/@other/dep": ["@other/dep@9.9.9", "", {}, "sha512-c"]
  }
}`,
}

// realBunLockFixtures returns the repo's committed bun.lock files, read from
// disk. Synthetic fixtures and the code under test tend to encode the same
// assumption about the format and agree with each other while both are wrong
// (Lessons.md, 2026-09-01: "Test a pattern against a real specimen"), so the
// differential corpus is anchored on genuine `bun install` output as well.
func realBunLockFixtures(t *testing.T) map[string][]byte {
	t.Helper()
	root := filepath.Join("..", "..", "..", "testdata", "fixtures")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	out := make(map[string][]byte)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p := filepath.Join(root, d.Name(), "bun.lock")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out[p] = data
	}
	// Floor assertion: this test's whole value is that it ran against real
	// lockfiles, and a moved/renamed fixture directory would otherwise turn
	// it into a green test that checked nothing (row 47).
	if len(out) < 3 {
		t.Fatalf("expected at least 3 committed bun.lock fixtures under %s, found %d", root, len(out))
	}
	return out
}

// TestParseBunLock_MatchesPinnedReference is the differential guard:
// ParseBunLock must produce exactly what the pinned reference produces, on
// every corpus entry and on every real committed lockfile.
func TestParseBunLock_MatchesPinnedReference(t *testing.T) {
	check := func(t *testing.T, label string, data []byte) {
		t.Helper()
		wantPkgs, wantErr := parseBunLockOracle(data)
		gotPkgs, gotErr := ParseBunLock(data)

		switch {
		case wantErr == nil && gotErr != nil:
			t.Fatalf("%s: the pinned reference parsed it, ParseBunLock failed: %v", label, gotErr)
		case wantErr != nil && gotErr == nil:
			t.Fatalf("%s: the pinned reference failed (%v), ParseBunLock parsed it", label, wantErr)
		case wantErr != nil && gotErr != nil:
			return // both rejected it; that is agreement
		}

		want, got := renderPackages(wantPkgs), renderPackages(gotPkgs)
		if want != got {
			t.Errorf("%s: ParseBunLock disagrees with the pinned reference\n--- reference (%d pkgs) ---\n%s\n--- ParseBunLock (%d pkgs) ---\n%s",
				label, len(wantPkgs), want, len(gotPkgs), got)
		}
	}

	for name, lock := range bunLockCorpus {
		t.Run(name, func(t *testing.T) { check(t, name, []byte(lock)) })
	}

	for path, data := range realBunLockFixtures(t) {
		t.Run("fixture "+filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			check(t, path, data)
			// A real fixture that parsed to nothing would make the
			// comparison above trivially true on both sides.
			pkgs, err := ParseBunLock(data)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if len(pkgs) == 0 {
				t.Fatalf("%s parsed to zero packages — the differential comparison for it is vacuous", path)
			}
		})
	}
}

// TestParseBunLock_IsByteIdenticalAcrossRepeatedParses is the determinism
// regression guard. The 2026-08-22 incident (Lessons.md) shipped six
// different SBOM digests from six builds of unchanged source, because the
// duplicate-name winner was chosen by Go's randomized map iteration. A
// two-way collision passes a single run about half the time, so this repeats.
func TestParseBunLock_IsByteIdenticalAcrossRepeatedParses(t *testing.T) {
	const runs = 200

	inputs := map[string][]byte{}
	for name, lock := range bunLockCorpus {
		inputs[name] = []byte(lock)
	}
	for path, data := range realBunLockFixtures(t) {
		inputs["fixture "+filepath.Base(filepath.Dir(path))] = data
	}

	for name, data := range inputs {
		t.Run(name, func(t *testing.T) {
			first, err := ParseBunLock(data)
			if err != nil {
				t.Skipf("input does not parse (%v); determinism of a parse error is not what this guards", err)
			}
			want := renderPackages(first)
			for i := 1; i < runs; i++ {
				got, err := ParseBunLock(data)
				if err != nil {
					t.Fatalf("run %d: %v", i, err)
				}
				if rendered := renderPackages(got); rendered != want {
					t.Fatalf("run %d differs from run 0 — the SBOM changes between builds of identical source\n--- run 0 ---\n%s\n--- run %d ---\n%s",
						i, want, i, rendered)
				}
			}
		})
	}
}

// TestParseBunLock_DuplicatePrecedenceIsHoistedThenLexicallyFirstKey pins the
// precedence rule itself, in isolation from the differential oracle. The
// oracle proves "unchanged"; this proves *what* is unchanged, so a future
// reader does not have to reconstruct the rule from two implementations.
func TestParseBunLock_DuplicatePrecedenceIsHoistedThenLexicallyFirstKey(t *testing.T) {
	versions := func(t *testing.T, lock string) map[string]string {
		t.Helper()
		pkgs, err := ParseBunLock([]byte(lock))
		if err != nil {
			t.Fatalf("ParseBunLock: %v", err)
		}
		out := make(map[string]string, len(pkgs))
		for _, p := range pkgs {
			out[p.Name] = p.Version
		}
		return out
	}

	t.Run("hoisted copy wins even when it sorts last", func(t *testing.T) {
		got := versions(t, bunLockCorpus["hoisted copy sorts last"])["zzz"]
		if got != "7.7.7" {
			t.Errorf("zzz = %q, want the hoisted 7.7.7 (key == package name), not a nested copy", got)
		}
	})

	t.Run("with no hoisted copy the lexically first key wins", func(t *testing.T) {
		// Keys are "alpha/shared" and "zeta/shared"; "alpha/shared" sorts
		// first, so shared@2.2.2 wins. Arbitrary, but stable — and stable is
		// the property the 2026-08-22 fix was after.
		got := versions(t, bunLockCorpus["two nested duplicates no hoisted copy"])["shared"]
		if got != "2.2.2" {
			t.Errorf("shared = %q, want 2.2.2 from the lexically-first key \"alpha/shared\"", got)
		}
	})

	t.Run("the winner's dependency edges are the ones that build the graph", func(t *testing.T) {
		// "zzz"'s hoisted copy is the only one declaring dep-of-hoisted, and
		// zzz is a production root. If a nested copy had won, its (empty)
		// edge list would have replaced the hoisted one's and dep-of-hoisted
		// would fall out of production scope entirely — the second half of
		// the 2026-08-22 incident, where the package COUNT moved too.
		pkgs, err := ParseBunLock([]byte(bunLockCorpus["hoisted copy sorts last"]))
		if err != nil {
			t.Fatalf("ParseBunLock: %v", err)
		}
		for _, p := range pkgs {
			if p.Name == "dep-of-hoisted" {
				if p.Scope != ScopeProduction {
					t.Errorf("dep-of-hoisted scope = %q, want %q — the hoisted entry's edges did not build the graph", p.Scope, ScopeProduction)
				}
				return
			}
		}
		t.Fatal("dep-of-hoisted missing from the catalogue entirely")
	})
}

// TestStripJSONTrailingCommas_UnchangedInputIsReturnedAsIs pins the one
// observable of the strip rewrite that is not just speed: an input needing no
// change is returned without copying, and one needing changes still produces
// exactly the same bytes.
func TestStripJSONTrailingCommas_UnchangedInputIsReturnedAsIs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"strict json untouched", `{"a":[1,2,3],"b":{"c":1}}`, `{"a":[1,2,3],"b":{"c":1}}`},
		{"object trailing comma", `{"a":1,}`, `{"a":1}`},
		{"array trailing comma", `[1,2,]`, `[1,2]`},
		{"comma then whitespace then brace", "{\"a\":1,\n  \n}", "{\"a\":1\n  \n}"},
		{"nested trailing commas", `{"a":{"b":1,},"c":[1,],}`, `{"a":{"b":1},"c":[1]}`},
		{"comma inside string kept", `{"a":"x,}y","b":2}`, `{"a":"x,}y","b":2}`},
		{"escaped quote then comma in string", `{"a":"x\",}y",}`, `{"a":"x\",}y"}`},
		{"empty", ``, ``},
		{"only a comma", `,`, `,`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(stripJSONTrailingCommas([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("stripJSONTrailingCommas(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// FuzzParseBunLock is the differential guard extended to inputs nobody wrote
// by hand. The typed decode replaced a set of type assertions over
// map[string]any that could only ever fail softly; a fuzzer is the cheapest
// way to find the tuple shape where "softly" stopped meaning the same thing
// in both implementations.
//
// The property is agreement, not correctness: whatever the old parser did
// with a malformed lockfile — including returning an error, or silently
// dropping an entry — the new one must do too.
func FuzzParseBunLock(f *testing.F) {
	for _, lock := range bunLockCorpus {
		f.Add([]byte(lock))
	}
	for _, data := range realBunLockFixtures(&testing.T{}) {
		f.Add(data)
	}
	// Shapes worth reaching quickly from a small mutation budget.
	f.Add([]byte(`{"packages":{"a":["a@1",0,{"dependencies":{"b":1}},""]}}`))
	f.Add([]byte(`{"packages":{"a":["a@1","",{"dependencies":{}},""]},"workspaces":{"":{"dependencies":{"a":"1"}}}}`))
	f.Add([]byte(`{"packages":{"@s/p":["@s/p@1.0.0"],"@s/p/@s/p":["@s/p@2.0.0"]}}`))
	f.Add([]byte(`{"packages":{"":["x@1","",{},""],}}`))
	f.Add([]byte(`{"packages":{"a":["a@1","https://r.example/",{"dependencies":{"b":"1"}},"s"],"b":["b@2","r@2",{},"s"]}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		wantPkgs, wantErr := parseBunLockOracle(data)
		gotPkgs, gotErr := ParseBunLock(data)

		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("error disagreement: old err = %v, new err = %v\ninput: %q", wantErr, gotErr, data)
		}
		if wantErr != nil {
			return
		}
		if want, got := renderPackages(wantPkgs), renderPackages(gotPkgs); want != got {
			t.Fatalf("output disagreement\ninput: %q\n--- old ---\n%s\n--- new ---\n%s", data, want, got)
		}
	})
}
