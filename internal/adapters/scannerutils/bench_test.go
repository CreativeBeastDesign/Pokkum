package scannerutils

import (
	"fmt"
	"strings"
	"testing"
)

// benchBunLock renders a synthetic bun.lock with numPackages entries, shaped
// exactly like the real fixtures under testdata/fixtures/*/bun.lock:
//
//   - JSONC, not strict JSON — every object carries the trailing comma
//     `bun install` writes, which is what forces ParseBunLock through
//     stripJSONTrailingCommas on every real input.
//   - a top-level "workspaces" object splitting direct dependencies from
//     devDependencies, which is what drives the production/development
//     reachability walk.
//   - four-element "packages" values: ["name@version", "", {meta}, "sha512-..."],
//     where meta carries "dependencies"/"optionalDependencies"/"peerDependencies"
//     edges so bunReachableFrom actually has a graph to traverse. A flat
//     lockfile with no edges would skip that walk entirely and under-report
//     the cost by a wide margin.
//   - a tail of nested (non-hoisted) duplicate keys such as
//     "knip/oxc-parser/@scope/pkg", which is what makes the sorted-key
//     hoisting pass in ParseBunLock do real work.
func benchBunLock(numPackages int) []byte {
	var b strings.Builder
	b.Grow(numPackages * 220)

	name := func(i int) string {
		if i%4 == 0 {
			return fmt.Sprintf("@scope%d/pkg-%d", i%7, i)
		}
		return fmt.Sprintf("pkg-%d", i)
	}
	version := func(i int) string {
		return fmt.Sprintf("%d.%d.%d", i%9, i%17, i%23)
	}
	// A fabricated, structurally valid sha512 integrity field. It is never
	// parsed, but it is ~95 bytes of every real entry and therefore a real
	// part of the JSON scanning cost.
	integrity := "sha512-" + strings.Repeat("AbCdEfGh01234567", 5) + "=="

	b.WriteString("{\n  \"lockfileVersion\": 1,\n  \"configVersion\": 1,\n  \"workspaces\": {\n    \"\": {\n      \"name\": \"bench-app\",\n")

	// Packages are split into a production half and a development half, with
	// dependency edges kept inside their own half (plus occasional dev ->
	// prod edges, which real lockfiles have). Without that partition a
	// handful of production roots reach every package through the graph and
	// the whole catalogue comes back ScopeProduction, leaving the
	// development reachability walk unmeasured.
	half := numPackages / 2
	b.WriteString("      \"dependencies\": {\n")
	for i := 0; i < half; i += 10 {
		fmt.Fprintf(&b, "        %q: %q,\n", name(i), "^"+version(i))
	}
	b.WriteString("      },\n      \"devDependencies\": {\n")
	for i := half; i < numPackages; i += 10 {
		fmt.Fprintf(&b, "        %q: %q,\n", name(i), "^"+version(i))
	}
	b.WriteString("      },\n    },\n  },\n  \"packages\": {\n")

	for i := 0; i < numPackages; i++ {
		n := name(i)
		var meta strings.Builder
		meta.WriteString("{")
		// Two ordinary dependency edges per package, plus an optional and a
		// peer edge on a minority — the same edge kinds
		// bunPackageDependencyNames walks.
		lo, hi := 0, half
		if i >= half {
			lo, hi = half, numPackages
		}
		span := hi - lo
		deps := []string{name(lo + (i*7)%span), name(lo + (i*13+1)%span)}
		if i >= half && i%8 == 0 {
			// A devDependency that pulls in a production package: a real edge
			// shape, and the one that makes prodReachable win over dev.
			deps = append(deps, name((i*3)%half))
		}
		meta.WriteString(" \"dependencies\": {")
		for j, d := range deps {
			if j > 0 {
				meta.WriteString(",")
			}
			fmt.Fprintf(&meta, " %q: %q", d, "^"+version(i+j))
		}
		meta.WriteString(" }")
		if i%6 == 0 {
			fmt.Fprintf(&meta, ", \"optionalDependencies\": { %q: %q }", name(lo+(i*3+2)%span), "^"+version(i))
		}
		if i%9 == 0 {
			fmt.Fprintf(&meta, ", \"peerDependencies\": { %q: %q }", name(lo+(i*5+4)%span), "^"+version(i))
		}
		if i%11 == 0 {
			meta.WriteString(", \"os\": \"linux\", \"cpu\": \"x64\"")
		}
		meta.WriteString(" }")

		fmt.Fprintf(&b, "    %q: [%q, \"\", %s, %q],\n\n", n, n+"@"+version(i), meta.String(), integrity)
	}

	// Nested duplicates: the same package name at a different version, keyed
	// by its dependency path. ParseBunLock has to sort every key and prefer
	// the hoisted copy, so these are not free.
	for i := 0; i < numPackages/10; i++ {
		n := name(i * 10)
		key := fmt.Sprintf("pkg-%d/pkg-%d/%s", i, i+1, n)
		fmt.Fprintf(&b, "    %q: [%q, \"\", { }, %q],\n\n", key, n+"@9."+version(i), integrity)
	}

	b.WriteString("  },\n}\n")
	return []byte(b.String())
}

// BenchmarkParseBunLock measures a full parse of a lockfile the size a real
// SvelteKit project produces. 1000 packages is the realistic upper end (the
// repo's own sveltekit-adapter-node fixture has ~250); 100 is included so the
// scaling of the sorted-key hoisting pass and the reachability walk is visible
// rather than assumed.
func BenchmarkParseBunLock(b *testing.B) {
	for _, n := range []int{100, 1000} {
		data := benchBunLock(n)

		// Fixture floor: prove the input actually parses into roughly the
		// package count it claims, and that the reachability walk classified
		// scopes rather than leaving everything ScopeUnknown. A lockfile that
		// silently failed to parse would benchmark an early error return.
		pkgs, err := ParseBunLock(data)
		if err != nil {
			b.Fatalf("synthetic bun.lock (%d packages) failed to parse: %v", n, err)
		}
		if len(pkgs) < n {
			b.Fatalf("synthetic bun.lock (%d packages) parsed to only %d packages", n, len(pkgs))
		}
		var prod, dev, unknown int
		for _, p := range pkgs {
			switch p.Scope {
			case ScopeProduction:
				prod++
			case ScopeDevelopment:
				dev++
			default:
				unknown++
			}
		}
		if prod == 0 || dev == 0 {
			b.Fatalf("degenerate fixture: prod=%d dev=%d unknown=%d — one of the reachability walks is unmeasured", prod, dev, unknown)
		}
		b.Logf("input: %d bytes, %d packages (prod=%d dev=%d unknown=%d)", len(data), len(pkgs), prod, dev, unknown)

		b.Run(fmt.Sprintf("%dpkgs", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, err := ParseBunLock(data)
				if err != nil {
					b.Fatalf("ParseBunLock: %v", err)
				}
				if len(got) == 0 {
					b.Fatal("ParseBunLock returned no packages")
				}
			}
		})
	}
}
