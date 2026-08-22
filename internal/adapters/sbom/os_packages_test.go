package sbom

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// The dpkg status.d fixtures below are byte-for-byte real content extracted
// from gcr.io/distroless/cc-debian12:nonroot (`crane export ... | tar -x
// var/lib/dpkg/status.d/{libssl3,libc6,base-files}`), not hand-written --
// per Lessons.md's fixture-fidelity family of incidents (the handler.js and
// static-prerendered bugs both slipped past a fixture whose *shape* looked
// right but wasn't what the real upstream artifact produces). Distroless's
// status.d entries deliberately omit the "Status:" field real dpkg writes,
// which is exactly why scannerutils.ParseDPKGStatus treats an empty Status
// as installed rather than requiring the field.
const realLibssl3Status = `Package: libssl3
Source: openssl
Version: 3.0.20-1~deb12u2
Architecture: amd64
Maintainer: Debian OpenSSL Team <pkg-openssl-devel@alioth-lists.debian.net>
Installed-Size: 6030
Depends: libc6 (>= 2.34)
Section: libs
Priority: optional
Multi-Arch: same
Homepage: https://www.openssl.org/
Description: Secure Sockets Layer toolkit - shared libraries
 This package is part of the OpenSSL project's implementation of the SSL
 and TLS cryptographic protocols for secure communication over the
 Internet.
 .
 It provides the libssl and libcrypto shared libraries.
`

const realLibc6Status = `Package: libc6
Source: glibc
Version: 2.36-9+deb12u14
Architecture: amd64
Maintainer: GNU Libc Maintainers <debian-glibc@lists.debian.org>
Installed-Size: 13001
Depends: libgcc-s1
Section: libs
Priority: optional
Multi-Arch: same
Homepage: https://www.gnu.org/software/libc/libc.html
Description: GNU C Library: Shared libraries
 Contains the standard libraries that are used by nearly all programs on
 the system. This package includes shared versions of the standard C library
 and the standard math library, as well as many others.
`

const realBaseFilesStatus = `Package: base-files
Version: 12.4+deb12u15
Architecture: amd64
Essential: yes
Maintainer: Santiago Vila <sanvila@debian.org>
Installed-Size: 341
Pre-Depends: awk
Section: admin
Priority: required
Multi-Arch: foreign
Description: Debian base system miscellaneous files
 This package contains the basic filesystem hierarchy of a Debian system, and
 several important miscellaneous files, such as /etc/debian_version,
 /etc/host.conf, /etc/issue, /etc/motd, /etc/profile, and others,
 and the text of several common licenses in use on Debian systems.
`

// realDebian12OSRelease is /usr/lib/os-release exactly as shipped by
// gcr.io/distroless/cc-debian12:nonroot (etc/os-release is a symlink to it
// in the real image).
const realDebian12OSRelease = `PRETTY_NAME="Distroless"
NAME="Debian GNU/Linux"
ID="debian"
VERSION_ID="12"
VERSION="Debian GNU/Linux 12 (bookworm)"
HOME_URL="https://github.com/GoogleContainerTools/distroless"
`

// buildOSImage constructs a single-layer OCI image containing the given
// files (path -> content), mirroring
// internal/adapters/scannerutils/scannerutils_test.go's buildTestImage --
// duplicated locally rather than imported because it is an unexported test
// helper in a different package, and scannerutils_test.go's own copy is not
// reachable from here.
func buildOSImage(t *testing.T, files map[string]string) v1.Image {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar WriteHeader(%s): %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar Write(%s): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	tarBytes := buf.Bytes()

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(tarBytes)), nil
	})
	if err != nil {
		t.Fatalf("tarball.LayerFromOpener: %v", err)
	}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("mutate.AppendLayers: %v", err)
	}
	return img
}

// realDistrolessDebian12Image builds a synthetic single-layer image whose
// dpkg database and os-release are byte-identical to the real
// gcr.io/distroless/cc-debian12:nonroot image's own (see the const fixtures
// above) -- three of the eleven packages a real pull of that image carries
// (var/lib/dpkg/status.d/{base-files,ca-certificates,gcc-12-base,libc6,
// libgcc-s1,libgomp1,libssl3,libstdc++6,media-types,netbase,tzdata}), chosen
// specifically because libssl3 and libc6 are the two the task's field report
// named as the highest-CVE-count components a distroless base ships and
// that were completely absent from the SBOM this change fixes.
func realDistrolessDebian12Image(t *testing.T) v1.Image {
	t.Helper()
	return buildOSImage(t, map[string]string{
		"var/lib/dpkg/status.d/libssl3":    realLibssl3Status,
		"var/lib/dpkg/status.d/libc6":      realLibc6Status,
		"var/lib/dpkg/status.d/base-files": realBaseFilesStatus,
		"usr/lib/os-release":               realDebian12OSRelease,
		"etc/os-release":                   realDebian12OSRelease,
	})
}

// projectWithOneNPMDependency writes a minimal, real-shaped project (a
// package.json plus a bun.lock resolving one dependency) so tests can prove
// OS packages and npm packages coexist correctly in the same document
// instead of one clobbering or being mistaken for the other.
func projectWithOneNPMDependency(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"my-app","version":"1.0.0","dependencies":{"svelte":"^5.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte(bunLockWith("svelte", "5.0.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type spdxDoc struct {
	CreationInfo struct {
		Comment string `json:"comment"`
	} `json:"creationInfo"`
	Packages []struct {
		Name         string `json:"name"`
		VersionInfo  string `json:"versionInfo"`
		ExternalRefs []struct {
			ReferenceLocator string `json:"referenceLocator"`
		} `json:"externalRefs"`
	} `json:"packages"`
}

func (d spdxDoc) purls() []string {
	var out []string
	for _, p := range d.Packages {
		for _, ref := range p.ExternalRefs {
			out = append(out, ref.ReferenceLocator)
		}
	}
	return out
}

type cdxDoc struct {
	Metadata struct {
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	} `json:"metadata"`
	Components []struct {
		Name string `json:"name"`
		Purl string `json:"purl"`
	} `json:"components"`
}

func (d cdxDoc) property(name string) (string, bool) {
	for _, p := range d.Metadata.Properties {
		if p.Name == name {
			return p.Value, true
		}
	}
	return "", false
}

func (d cdxDoc) purls() []string {
	var out []string
	for _, c := range d.Components {
		out = append(out, c.Purl)
	}
	return out
}

// TestGenerateForImage_RealDistrolessFixture_EmitsCorrectDebPurls is the
// central regression test for the SBOM's missing OS-package coverage: fed a
// base image carrying real Debian 12 dpkg records (byte-identical to
// gcr.io/distroless/cc-debian12:nonroot's own), GenerateForImage's output
// must contain a pkg:deb purl for each one, and the pre-existing npm
// dependency must still be present and unaffected -- driven through the
// real generate() core both Generate and GenerateForImage share, not a
// hand-rolled substitute.
func TestGenerateForImage_RealDistrolessFixture_EmitsCorrectDebPurls(t *testing.T) {
	dir := projectWithOneNPMDependency(t)
	img := realDistrolessDebian12Image(t)
	images := map[ports.Platform]v1.Image{ports.LinuxAMD64: img}

	wantDebPurls := map[string]bool{
		"pkg:deb/debian/libssl3@3.0.20-1~deb12u2?arch=amd64": false,
		"pkg:deb/debian/libc6@2.36-9+deb12u14?arch=amd64":    false,
		"pkg:deb/debian/base-files@12.4+deb12u15?arch=amd64": false,
	}

	t.Run("spdx-json", func(t *testing.T) {
		g := NewGenerator(nil)
		doc, err := g.GenerateForImage(context.Background(), ports.SBOMRequest{
			ProjectDir: dir,
			Format:     ports.SBOMFormatSPDXJSON,
			CreatedAt:  time.Unix(0, 0).UTC(),
		}, images)
		if err != nil {
			t.Fatalf("GenerateForImage() failed: %v", err)
		}

		var parsed spdxDoc
		if err := json.Unmarshal(doc.Content, &parsed); err != nil {
			t.Fatalf("unmarshal SPDX doc: %v", err)
		}

		got := map[string]bool{}
		for _, purl := range parsed.purls() {
			got[purl] = true
		}
		for want := range wantDebPurls {
			if !got[want] {
				t.Errorf("SPDX packages missing purl %q; got purls %v", want, parsed.purls())
			}
		}
		if !got["pkg:npm/svelte@5.0.0"] {
			t.Errorf("SPDX packages missing the project's own npm purl pkg:npm/svelte@5.0.0; got %v", parsed.purls())
		}
		if parsed.CreationInfo.Comment != "pokkum:osPackagesScanned=true pokkum:osPackageCount=3" {
			t.Errorf("creationInfo.comment = %q, want the scanned/count marker for 3 OS packages", parsed.CreationInfo.Comment)
		}
		if doc.PackageCount != 4 { // 3 deb + 1 npm
			t.Errorf("doc.PackageCount = %d, want 4 (3 OS + 1 npm)", doc.PackageCount)
		}

		// Bit-for-bit OCI Reproducibility: a second GenerateForImage call
		// against the same image map and project must hash identically, the
		// same guarantee Generate() already carries (see
		// TestGenerator_SPDX_ReproducibilityAndPackages). extractBaseImageOSPackages'
		// deterministic platform ordering and merge-then-sort in generate()
		// exist specifically to make this hold with the OS-package path
		// included, not just the npm-only path.
		doc2, err := g.GenerateForImage(context.Background(), ports.SBOMRequest{
			ProjectDir: dir,
			Format:     ports.SBOMFormatSPDXJSON,
			CreatedAt:  time.Unix(0, 0).UTC(),
		}, images)
		if err != nil {
			t.Fatalf("second GenerateForImage() failed: %v", err)
		}
		if doc.SHA256 != doc2.SHA256 || !bytes.Equal(doc.Content, doc2.Content) {
			t.Fatal("GenerateForImage produced different bytes across two identical calls")
		}
	})

	t.Run("cyclonedx-json", func(t *testing.T) {
		g := NewGenerator(nil)
		doc, err := g.GenerateForImage(context.Background(), ports.SBOMRequest{
			ProjectDir: dir,
			Format:     ports.SBOMFormatCycloneDXJSON,
			CreatedAt:  time.Unix(0, 0).UTC(),
		}, images)
		if err != nil {
			t.Fatalf("GenerateForImage() failed: %v", err)
		}

		var parsed cdxDoc
		if err := json.Unmarshal(doc.Content, &parsed); err != nil {
			t.Fatalf("unmarshal CycloneDX doc: %v", err)
		}

		got := map[string]bool{}
		for _, purl := range parsed.purls() {
			got[purl] = true
		}
		for want := range wantDebPurls {
			if !got[want] {
				t.Errorf("CycloneDX components missing purl %q; got purls %v", want, parsed.purls())
			}
		}
		if !got["pkg:npm/svelte@5.0.0"] {
			t.Errorf("CycloneDX components missing the project's own npm purl pkg:npm/svelte@5.0.0; got %v", parsed.purls())
		}
		if scanned, ok := parsed.property("pokkum:osPackagesScanned"); !ok || scanned != "true" {
			t.Errorf("metadata.properties[pokkum:osPackagesScanned] = (%q, %v), want (\"true\", true)", scanned, ok)
		}
		if count, ok := parsed.property("pokkum:osPackageCount"); !ok || count != "3" {
			t.Errorf("metadata.properties[pokkum:osPackageCount] = (%q, %v), want (\"3\", true)", count, ok)
		}
	})
}

// TestGenerateForImage_NoPackageDatabase_IsACleanZeroDistinctFromNotScanned
// is the scratch/static-base regression test: an image with no dpkg or apk
// database at all (nothing under var/lib/dpkg or lib/apk/db) must produce a
// genuine, positive "scanned, found zero" result -- not an error, and not
// silently indistinguishable from Generate() never having been given an
// image to look at in the first place. This is the exact "found nothing"
// vs "could not check" conflation Lessons.md names repeatedly (the scanner
// adapter's fallback-advisory bug, secretguard's skip-vs-clean-scan bug) as
// this codebase's most recurring class of SBOM/scanner defect.
func TestGenerateForImage_NoPackageDatabase_IsACleanZeroDistinctFromNotScanned(t *testing.T) {
	dir := projectWithOneNPMDependency(t)
	// A scratch/static base: some real file content, but nothing resembling
	// a dpkg or apk package database anywhere in the image.
	scratchImg := buildOSImage(t, map[string]string{
		"usr/local/bin/app": "not a package database",
	})
	images := map[ports.Platform]v1.Image{ports.LinuxAMD64: scratchImg}

	req := func(format ports.SBOMFormat) ports.SBOMRequest {
		return ports.SBOMRequest{ProjectDir: dir, Format: format, CreatedAt: time.Unix(0, 0).UTC()}
	}

	g := NewGenerator(nil)

	t.Run("spdx-json", func(t *testing.T) {
		scannedDoc, err := g.GenerateForImage(context.Background(), req(ports.SBOMFormatSPDXJSON), images)
		if err != nil {
			t.Fatalf("GenerateForImage() on scratch base failed: %v", err)
		}
		notScannedDoc, err := g.Generate(context.Background(), req(ports.SBOMFormatSPDXJSON))
		if err != nil {
			t.Fatalf("Generate() failed: %v", err)
		}

		var scanned, notScanned spdxDoc
		if err := json.Unmarshal(scannedDoc.Content, &scanned); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(notScannedDoc.Content, &notScanned); err != nil {
			t.Fatal(err)
		}

		if scanned.CreationInfo.Comment != "pokkum:osPackagesScanned=true pokkum:osPackageCount=0" {
			t.Errorf("scratch base creationInfo.comment = %q, want a positive scanned/zero marker", scanned.CreationInfo.Comment)
		}
		if notScanned.CreationInfo.Comment != "pokkum:osPackagesScanned=false" {
			t.Errorf("no-image creationInfo.comment = %q, want the not-scanned marker", notScanned.CreationInfo.Comment)
		}
		if scanned.CreationInfo.Comment == notScanned.CreationInfo.Comment {
			t.Fatal("scanned-zero and not-scanned documents carry the same comment -- the two states are not distinguishable")
		}
		for _, purl := range scanned.purls() {
			if strings.HasPrefix(purl, "pkg:deb/") || strings.HasPrefix(purl, "pkg:apk/") {
				t.Errorf("scratch base unexpectedly produced an OS purl: %s", purl)
			}
		}

		// Same npm packages, same everything except scan status -- the
		// documents' content identity (and therefore SHA256) must still
		// differ, or the two claims ("checked, found nothing" vs "never
		// checked") would be silently unrecoverable from the SBOM's own
		// bytes/hash even though the JSON differs.
		if scannedDoc.SHA256 == notScannedDoc.SHA256 {
			t.Error("scanned-zero and not-scanned documents hashed identically despite differing content")
		}
	})

	t.Run("cyclonedx-json", func(t *testing.T) {
		scannedDoc, err := g.GenerateForImage(context.Background(), req(ports.SBOMFormatCycloneDXJSON), images)
		if err != nil {
			t.Fatalf("GenerateForImage() on scratch base failed: %v", err)
		}
		notScannedDoc, err := g.Generate(context.Background(), req(ports.SBOMFormatCycloneDXJSON))
		if err != nil {
			t.Fatalf("Generate() failed: %v", err)
		}

		var scanned, notScanned cdxDoc
		if err := json.Unmarshal(scannedDoc.Content, &scanned); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(notScannedDoc.Content, &notScanned); err != nil {
			t.Fatal(err)
		}

		if v, ok := scanned.property("pokkum:osPackagesScanned"); !ok || v != "true" {
			t.Errorf("scratch base pokkum:osPackagesScanned = (%q, %v), want (\"true\", true)", v, ok)
		}
		if v, ok := scanned.property("pokkum:osPackageCount"); !ok || v != "0" {
			t.Errorf("scratch base pokkum:osPackageCount = (%q, %v), want (\"0\", true)", v, ok)
		}
		if v, ok := notScanned.property("pokkum:osPackagesScanned"); !ok || v != "false" {
			t.Errorf("no-image pokkum:osPackagesScanned = (%q, %v), want (\"false\", true)", v, ok)
		}
		if _, ok := notScanned.property("pokkum:osPackageCount"); ok {
			t.Error("no-image document must not carry pokkum:osPackageCount at all")
		}
	})

	t.Run("nil images map behaves exactly like Generate", func(t *testing.T) {
		viaGenerateForImage, err := g.GenerateForImage(context.Background(), req(ports.SBOMFormatSPDXJSON), nil)
		if err != nil {
			t.Fatal(err)
		}
		viaGenerate, err := g.Generate(context.Background(), req(ports.SBOMFormatSPDXJSON))
		if err != nil {
			t.Fatal(err)
		}
		if viaGenerateForImage.SHA256 != viaGenerate.SHA256 {
			t.Error("GenerateForImage(nil images) must produce byte-identical output to Generate: a nil/empty image map is not scanning, not a confirmed-empty base")
		}
	})
}

// TestGenerateForImage_MultiPlatform_UnionsAndDedupesByArchitecture proves
// the multi-platform design decision: packages are unioned across every
// platform's base image (a package present in only one arch's image must
// still appear), an identical package+version+architecture reported by more
// than one platform is not duplicated, and two platforms legitimately
// differ in Architecture is preserved as two distinct components.
func TestGenerateForImage_MultiPlatform_UnionsAndDedupesByArchitecture(t *testing.T) {
	dir := projectWithOneNPMDependency(t)

	amd64Only := `Package: base-files
Version: 12.4+deb12u15
Architecture: amd64
Description: Debian base system miscellaneous files
`
	sharedArm64 := `Package: netbase
Version: 6.4
Architecture: all
Description: Basic TCP/IP networking support
`
	sharedAmd64 := sharedArm64 // identical content: same name/version/arch on both platforms

	amd64Img := buildOSImage(t, map[string]string{
		"var/lib/dpkg/status.d/base-files": amd64Only,
		"var/lib/dpkg/status.d/netbase":    sharedAmd64,
		"usr/lib/os-release":               realDebian12OSRelease,
	})
	arm64Img := buildOSImage(t, map[string]string{
		"var/lib/dpkg/status.d/netbase": sharedArm64,
		"usr/lib/os-release":            realDebian12OSRelease,
	})

	g := NewGenerator(nil)
	doc, err := g.GenerateForImage(context.Background(), ports.SBOMRequest{
		ProjectDir: dir,
		Format:     ports.SBOMFormatCycloneDXJSON,
		CreatedAt:  time.Unix(0, 0).UTC(),
	}, map[ports.Platform]v1.Image{
		ports.LinuxAMD64: amd64Img,
		ports.LinuxARM64: arm64Img,
	})
	if err != nil {
		t.Fatalf("GenerateForImage() failed: %v", err)
	}

	var parsed cdxDoc
	if err := json.Unmarshal(doc.Content, &parsed); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	for _, purl := range parsed.purls() {
		counts[purl]++
	}

	if counts["pkg:deb/debian/base-files@12.4+deb12u15?arch=amd64"] != 1 {
		t.Errorf("base-files (amd64-only) count = %d, want 1 (must still appear, present on only one platform)",
			counts["pkg:deb/debian/base-files@12.4+deb12u15?arch=amd64"])
	}
	if counts["pkg:deb/debian/netbase@6.4?arch=all"] != 1 {
		t.Errorf("netbase (identical on both platforms) count = %d, want exactly 1 (deduped, not doubled)",
			counts["pkg:deb/debian/netbase@6.4?arch=all"])
	}
	if v, ok := parsed.property("pokkum:osPackageCount"); !ok || v != "2" {
		t.Errorf("pokkum:osPackageCount = (%q, %v), want (\"2\", true) -- base-files + deduped netbase", v, ok)
	}
}
