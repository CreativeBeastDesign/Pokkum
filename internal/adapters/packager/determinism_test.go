package packager

import (
	"context"
	"io"
	"reflect"
	"runtime"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestBuildIsReproducible is the headline test of this package.
//
// Two Build calls, from two Packager instances, over two separate copies of the
// same inputs written to two different temporary directories, must produce
// byte-identical images: the same manifest digest, the same config digest, the
// same compressed layer digests and the same raw JSON. The compressed digests
// are asserted alongside the diffIDs on purpose — a gzip header that embedded a
// name or an mtime would leave the diffIDs identical and change only the
// digests that a registry actually stores.
func TestBuildIsReproducible(t *testing.T) {
	ctx := context.Background()

	first, err := NewPackager(testLogger()).Build(ctx, newRequest(t, ports.LinuxAMD64))
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	second, err := NewPackager(testLogger()).Build(ctx, newRequest(t, ports.LinuxAMD64))
	if err != nil {
		t.Fatalf("second build: %v", err)
	}

	a, b := fingerprint(t, first), fingerprint(t, second)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("builds differ:\n first: %+v\nsecond: %+v", a, b)
	}
	t.Logf("image manifest digest: %s", a.Manifest)
	t.Logf("image config digest:   %s", a.Config)
	for i := range a.LayerDigests {
		t.Logf("layer[%d] digest=%s diffID=%s", i, a.LayerDigests[i], a.LayerDiffIDs[i])
	}
}

// TestBuildIsReproducibleAcrossPlatforms checks the same property for arm64,
// and additionally that the two platforms do not accidentally collide — a
// packager that ignored the base image would produce identical images for both.
func TestBuildIsReproducibleAcrossPlatforms(t *testing.T) {
	ctx := context.Background()
	p := NewPackager(testLogger())

	amd, err := p.Build(ctx, newRequest(t, ports.LinuxAMD64))
	if err != nil {
		t.Fatalf("amd64 build: %v", err)
	}
	arm, err := p.Build(ctx, newRequest(t, ports.LinuxARM64))
	if err != nil {
		t.Fatalf("arm64 build: %v", err)
	}
	armAgain, err := p.Build(ctx, newRequest(t, ports.LinuxARM64))
	if err != nil {
		t.Fatalf("arm64 rebuild: %v", err)
	}

	if !reflect.DeepEqual(fingerprint(t, arm), fingerprint(t, armAgain)) {
		t.Error("two arm64 builds from identical inputs differ")
	}
	if fingerprint(t, amd).Manifest == fingerprint(t, arm).Manifest {
		t.Error("amd64 and arm64 images share a digest; the base image is being ignored")
	}
	// Both pokkum-added layers are platform-independent in this fixture, so
	// both must be shared: that is what makes a multi-arch push cheap when only
	// the base differs, and it is also the cross-image deduplication property
	// that motivates keeping the supervisor in its own layer at all.
	amdLayers := fingerprint(t, amd).LayerDigests
	armLayers := fingerprint(t, arm).LayerDigests
	if len(amdLayers) != 3 || len(armLayers) != 3 {
		t.Fatalf("got %d/%d layers, want base layer plus exactly two pokkum layers", len(amdLayers), len(armLayers))
	}
	if amdLayers[len(amdLayers)-1] != armLayers[len(armLayers)-1] {
		t.Error("application layer digest differs between platforms for identical inputs")
	}
	if amdLayers[len(amdLayers)-2] != armLayers[len(armLayers)-2] {
		t.Error("supervisor layer digest differs between platforms for identical inputs")
	}
}

// TestIndexIsReproducible builds the same two-platform index twice and asserts
// the digests and raw JSON match.
func TestIndexIsReproducible(t *testing.T) {
	ctx := context.Background()
	p := NewPackager(testLogger())

	build := func() v1.ImageIndex {
		t.Helper()
		images := map[ports.Platform]v1.Image{}
		for _, plat := range []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64} {
			img, err := p.Build(ctx, newRequest(t, plat))
			if err != nil {
				t.Fatalf("build %s: %v", plat, err)
			}
			images[plat] = img
		}
		idx, err := p.Index(ctx, ports.IndexRequest{
			Images:      images,
			CreatedAt:   buildEpoch,
			Annotations: map[string]string{ports.LabelSource: "https://github.com/example/app"},
		})
		if err != nil {
			t.Fatalf("index: %v", err)
		}
		return idx
	}

	first, second := build(), build()

	fd, err := first.Digest()
	if err != nil {
		t.Fatalf("first digest: %v", err)
	}
	sd, err := second.Digest()
	if err != nil {
		t.Fatalf("second digest: %v", err)
	}
	if fd != sd {
		t.Errorf("index digests differ: %s vs %s", fd, sd)
	}

	fraw, err := first.RawManifest()
	if err != nil {
		t.Fatalf("first raw: %v", err)
	}
	sraw, err := second.RawManifest()
	if err != nil {
		t.Fatalf("second raw: %v", err)
	}
	if string(fraw) != string(sraw) {
		t.Errorf("index manifests differ:\n%s\n%s", fraw, sraw)
	}
	t.Logf("index digest: %s", fd)
	t.Logf("index manifest: %s", fraw)
}

// TestIndexDigestSurvivesMapOrder is the guard against the specific failure W1
// flagged: Go randomises map iteration order per range statement, so a packager
// that drained IndexRequest.Images directly would produce a different index
// digest on most runs while passing any single-pass test roughly half the time.
//
// The loop also carries a five-key RuntimeConfig.Env, which is the second map
// whose order reaches serialized output — the image config's Env is an ordered
// JSON array, so an unsorted drain changes the config digest, which changes the
// manifest digest, which changes the index digest. One constant asserted at the
// end covers both hazards.
func TestIndexDigestSurvivesMapOrder(t *testing.T) {
	ctx := context.Background()
	p := NewPackager(testLogger())

	env := map[string]string{
		"ZULU":         "z",
		"ALPHA":        "a",
		"MIKE":         "m",
		"BRAVO":        "b",
		"OSCAR":        "o",
		ports.EnvPort:  "4000",
		"NODE_ENV":     "production",
		"SSL_CERT_DIR": "/etc/ssl/certs",
	}

	var want string
	const iterations = 20
	for i := range iterations {
		images := map[ports.Platform]v1.Image{}
		for _, plat := range []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64} {
			req := newRequest(t, plat)
			req.Runtime.Env = env
			img, err := p.Build(ctx, req)
			if err != nil {
				t.Fatalf("iteration %d: build %s: %v", i, plat, err)
			}
			images[plat] = img
		}

		idx, err := p.Index(ctx, ports.IndexRequest{
			Images:      images,
			CreatedAt:   buildEpoch,
			Annotations: map[string]string{ports.LabelVersion: "1.2.3"},
		})
		if err != nil {
			t.Fatalf("iteration %d: index: %v", i, err)
		}
		d, err := idx.Digest()
		if err != nil {
			t.Fatalf("iteration %d: digest: %v", i, err)
		}
		if i == 0 {
			want = d.String()
			continue
		}
		if d.String() != want {
			t.Fatalf("iteration %d: index digest %s, want %s (map iteration order is leaking)", i, d, want)
		}
	}
	t.Logf("index digest stable over %d iterations: %s", iterations, want)
}

// TestLayerOpenerIsReinvocable checks the property tarball.LayerFromOpener
// depends on and that a buffered-once implementation would quietly break: the
// tar stream can be produced any number of times and is identical each time.
// LayerFromOpener already calls it three times internally, but a push calls it
// again later, so the guarantee has to hold beyond the constructor.
func TestLayerOpenerIsReinvocable(t *testing.T) {
	ctx := context.Background()
	img, err := NewPackager(testLogger()).Build(ctx, newRequest(t, ports.LinuxAMD64))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	appLayer := layers[len(layers)-1]

	first := readLayer(t, appLayer)
	for i := range 3 {
		got := readLayer(t, appLayer)
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("re-read %d differs:\n%+v\n%+v", i, first, got)
		}
	}
}

// TestLayerOpenerDoesNotLeakGoroutines covers the cost of streaming the tar
// instead of buffering it. Each call to the opener starts a goroutine writing
// into a pipe, and a reader that stops early — which is exactly what
// go-containerregistry's compression sniffer does, after four bytes — must
// unblock that goroutine rather than park it forever. Two goroutines per layer
// per platform leaked would be invisible in a CLI run and fatal in anything
// long-lived.
func TestLayerOpenerDoesNotLeakGoroutines(t *testing.T) {
	entries, err := tarEntries([]layerFile{
		{path: ports.AppBinaryPath, size: int64(len(fakeAppBytes)), open: bytesOpener(fakeAppBytes)},
		{path: ports.SupervisorPath, size: int64(len(fakeSupervisorBytes)), open: bytesOpener(fakeSupervisorBytes)},
	})
	if err != nil {
		t.Fatalf("tarEntries: %v", err)
	}
	opener := tarOpener(entries, buildEpoch)

	runtime.GC()
	before := runtime.NumGoroutine()

	for i := range 50 {
		rc, err := opener()
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		// Read a few bytes and abandon the rest, the sniffer's access pattern.
		var buf [4]byte
		if _, err := io.ReadFull(rc, buf[:]); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}

	// The writers exit asynchronously once the pipe is closed, so allow a
	// bounded settle window rather than sampling immediately.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines: %d before, %d after 50 abandoned readers", before, after)
	}
}
