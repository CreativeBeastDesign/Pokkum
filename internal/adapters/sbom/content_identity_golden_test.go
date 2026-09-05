package sbom

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scannerutils"
)

// contentIdentityUUIDOracle is a verbatim copy of the fmt.Sprintf/
// strings.Builder implementation of contentIdentityUUID that shipped before
// the allocation rewrite.
//
// The SBOM's serialNumber is derived from this UUID and the document's bytes
// are content-addressed downstream, so the rewrite had exactly one hard
// requirement: byte-identical input to uuid.NewSHA1. That is not something a
// "still deterministic" assertion can check — the old and new code are each
// internally consistent — so it is checked against the old formatting
// directly, plus a literal golden UUID below so a future edit to BOTH
// implementations still trips.
//
// Do not modernise this. Its only job is to be the old code.
func contentIdentityUUIDOracle(name, version string, packages []scannerutils.CatalogPackage, bunVersion, bunSHA256 string, distro scannerutils.DistroInfo, osScanned bool, npmDevExcluded int) uuid.UUID {
	ids := make([]string, 0, len(packages))
	for _, p := range packages {
		ids = append(ids, fmt.Sprintf("%s@%s@%s@resolved=%v@scope=%s", p.Name, p.Version, p.Type, p.Resolved, p.Scope))
	}
	sort.Strings(ids)

	var b strings.Builder
	fmt.Fprintf(&b, "pokkum-sbom\n%s@%s\n", name, version)
	for _, id := range ids {
		b.WriteString(id)
		b.WriteByte('\n')
	}
	if bunVersion != "" {
		fmt.Fprintf(&b, "bun@%s@%s\n", bunVersion, bunSHA256)
	}
	fmt.Fprintf(&b, "osScanned=%v@distro=%s:%s\n", osScanned, distro.ID, distro.VersionID)
	fmt.Fprintf(&b, "npmDevExcluded=%d\n", npmDevExcluded)
	return uuid.NewSHA1(pokkumSBOMNamespace, []byte(b.String()))
}

// contentIdentityFixture is a fixed multi-package fixture deliberately
// covering every field the identity string interpolates: both Resolved
// values, all three DependencyScope values, all three PackageType values, an
// architecture-bearing OS package, a scoped npm name, a prerelease version,
// and an entry whose fields contain the '@' separator itself.
func contentIdentityFixture() []scannerutils.CatalogPackage {
	return []scannerutils.CatalogPackage{
		{Name: "@scope/pkg", Version: "1.2.3-beta.1", Type: scannerutils.PkgTypeNpm, Ecosystem: "npm", Resolved: true, Scope: scannerutils.ScopeProduction},
		{Name: "unresolved-range", Version: "^4.17.0", Type: scannerutils.PkgTypeNpm, Ecosystem: "npm", Resolved: false, Scope: scannerutils.ScopeDevelopment},
		{Name: "mystery", Version: "0.0.0", Type: scannerutils.PkgTypeNpm, Ecosystem: "npm", Resolved: true, Scope: scannerutils.ScopeUnknown},
		{Name: "libc6", Version: "2.36-9+deb12u14", Type: scannerutils.PkgTypeDeb, Ecosystem: "Debian:12", Architecture: "amd64", Resolved: true, Scope: scannerutils.ScopeProduction},
		{Name: "musl", Version: "1.2.5-r0", Type: scannerutils.PkgTypeApk, Ecosystem: "Alpine:v3.20", Architecture: "aarch64", Resolved: true, Scope: scannerutils.ScopeProduction},
		{Name: "weird@name", Version: "1@2", Type: scannerutils.PkgTypeNpm, Ecosystem: "npm", Resolved: false, Scope: scannerutils.ScopeUnknown},
		// Two entries differing only in a field folded into the id, to make
		// sure the sort key really is the whole formatted string.
		{Name: "twin", Version: "1.0.0", Type: scannerutils.PkgTypeNpm, Ecosystem: "npm", Resolved: true, Scope: scannerutils.ScopeProduction},
		{Name: "twin", Version: "1.0.0", Type: scannerutils.PkgTypeNpm, Ecosystem: "npm", Resolved: false, Scope: scannerutils.ScopeProduction},
	}
}

func TestContentIdentityUUID_MatchesPreviousImplementation(t *testing.T) {
	pkgs := contentIdentityFixture()
	cases := []struct {
		name           string
		appName        string
		appVersion     string
		packages       []scannerutils.CatalogPackage
		bunVersion     string
		bunSHA256      string
		distro         scannerutils.DistroInfo
		osScanned      bool
		npmDevExcluded int
	}{
		{name: "no packages", appName: "app", appVersion: "1.0.0"},
		{name: "full fixture", appName: "app", appVersion: "1.0.0", packages: pkgs},
		{name: "with bun", appName: "app", appVersion: "1.0.0", packages: pkgs, bunVersion: "1.2.2", bunSHA256: "deadbeef"},
		{name: "bun version empty but sha set", appName: "app", appVersion: "1.0.0", packages: pkgs, bunSHA256: "deadbeef"},
		{name: "os scanned debian", appName: "app", appVersion: "1.0.0", packages: pkgs, distro: scannerutils.DistroInfo{ID: "debian", VersionID: "12"}, osScanned: true},
		{name: "os scanned empty distro", appName: "app", appVersion: "1.0.0", packages: pkgs, osScanned: true},
		{name: "dev excluded", appName: "app", appVersion: "1.0.0", packages: pkgs, npmDevExcluded: 42},
		{name: "negative dev excluded", appName: "app", appVersion: "1.0.0", packages: pkgs, npmDevExcluded: -1},
		{name: "empty name and version", packages: pkgs},
		{name: "everything at once", appName: "my-app", appVersion: "0.0.0-next.1", packages: pkgs, bunVersion: "1.2.2", bunSHA256: "abc", distro: scannerutils.DistroInfo{ID: "wolfi", VersionID: "20230201"}, osScanned: true, npmDevExcluded: 7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := contentIdentityUUIDOracle(tc.appName, tc.appVersion, tc.packages, tc.bunVersion, tc.bunSHA256, tc.distro, tc.osScanned, tc.npmDevExcluded)
			got := contentIdentityUUID(tc.appName, tc.appVersion, tc.packages, tc.bunVersion, tc.bunSHA256, tc.distro, tc.osScanned, tc.npmDevExcluded)
			if got != want {
				t.Errorf("contentIdentityUUID = %s, previous implementation = %s", got, want)
			}
		})
	}
}

// TestContentIdentityUUID_Golden is the literal pin. The oracle test above
// catches a rewrite that diverges from the old code; this catches an edit
// that changes both at once.
func TestContentIdentityUUID_Golden(t *testing.T) {
	const (
		goldenFull = "84bf28d0-4bbb-591b-aa7a-8d2dafc474e1"
		goldenAll  = "40eca2b6-f9b3-5cce-b5a6-5ec67a054599"
	)
	pkgs := contentIdentityFixture()

	got := contentIdentityUUID("app", "1.0.0", pkgs, "", "", scannerutils.DistroInfo{}, false, 0).String()
	if got != goldenFull {
		t.Errorf("contentIdentityUUID(full fixture) = %s, want %s\nA moved value here means every SBOM emitted for these inputs now carries a different serialNumber.", got, goldenFull)
	}

	got = contentIdentityUUID("my-app", "0.0.0-next.1", pkgs, "1.2.2", "abc",
		scannerutils.DistroInfo{ID: "wolfi", VersionID: "20230201"}, true, 7).String()
	if got != goldenAll {
		t.Errorf("contentIdentityUUID(everything at once) = %s, want %s", got, goldenAll)
	}
}

// TestContentIdentityUUID_SortKeyIsTheWholeFormattedString guards the
// specific hazard the rewrite introduced: the sort operates on the assembled
// id, so two packages that differ only in Resolved or Scope must still sort
// (and hash) distinctly.
func TestContentIdentityUUID_SortKeyIsTheWholeFormattedString(t *testing.T) {
	base := []scannerutils.CatalogPackage{
		{Name: "twin", Version: "1.0.0", Type: scannerutils.PkgTypeNpm, Resolved: true, Scope: scannerutils.ScopeProduction},
		{Name: "twin", Version: "1.0.0", Type: scannerutils.PkgTypeNpm, Resolved: false, Scope: scannerutils.ScopeProduction},
	}
	flipped := []scannerutils.CatalogPackage{base[1], base[0]}
	if a, b := contentIdentityUUID("app", "1", base, "", "", scannerutils.DistroInfo{}, false, 0),
		contentIdentityUUID("app", "1", flipped, "", "", scannerutils.DistroInfo{}, false, 0); a != b {
		t.Errorf("identity depends on input slice order: %s != %s", a, b)
	}

	sameScope := []scannerutils.CatalogPackage{
		{Name: "twin", Version: "1.0.0", Type: scannerutils.PkgTypeNpm, Resolved: true, Scope: scannerutils.ScopeProduction},
		{Name: "twin", Version: "1.0.0", Type: scannerutils.PkgTypeNpm, Resolved: true, Scope: scannerutils.ScopeDevelopment},
	}
	if a, b := contentIdentityUUID("app", "1", base, "", "", scannerutils.DistroInfo{}, false, 0),
		contentIdentityUUID("app", "1", sameScope, "", "", scannerutils.DistroInfo{}, false, 0); a == b {
		t.Error("packages differing only in Resolved/Scope hashed identically — a field dropped out of the identity string")
	}
}
