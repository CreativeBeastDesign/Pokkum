package provenance

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

const testHex = "aaaabbbbccccddddeeeeffff00001111222233334444555566667777888899990"

func testDigest(t *testing.T) v1.Hash {
	t.Helper()
	h, err := v1.NewHash("sha256:" + testHex[:64])
	if err != nil {
		t.Fatalf("build test digest: %v", err)
	}
	return h
}

// TestValidateExpectedKeylessIdentity covers the up-front refusal of a
// half-specified identity, and confirms a fully empty one is *not* rejected
// here — it is a legitimate "nothing configured" state that ResolveProvenance
// handles by failing closed only if the image actually carries keyless material.
func TestValidateExpectedKeylessIdentity(t *testing.T) {
	tests := []struct {
		name    string
		id      ports.KeylessIdentity
		wantErr bool
	}{
		{"empty is deferred, not rejected here", ports.KeylessIdentity{}, false},
		{"both exact", ports.KeylessIdentity{Issuer: "https://i", SAN: "s"}, false},
		{"both regex", ports.KeylessIdentity{IssuerRegex: "^https://i$", SANRegex: "^s$"}, false},
		{"mixed exact/regex", ports.KeylessIdentity{Issuer: "https://i", SANRegex: "^s$"}, false},
		{"SAN only", ports.KeylessIdentity{SAN: "s"}, true},
		{"SAN regex only", ports.KeylessIdentity{SANRegex: "^s$"}, true},
		{"issuer only", ports.KeylessIdentity{Issuer: "https://i"}, true},
		{"issuer regex only", ports.KeylessIdentity{IssuerRegex: "^https://i$"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExpectedKeylessIdentity(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !errors.Is(err, ErrKeylessIdentityIncomplete) {
					t.Errorf("got %v, want ErrKeylessIdentityIncomplete", err)
				}
				if !errors.Is(err, core.ErrInvalidRequest) {
					t.Errorf("got %v, want it to also wrap core.ErrInvalidRequest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestCheckSimpleSigningClaims_PayloadTypeConfusion is the regression test for
// the missing Critical.Type check. This copy of the function used to accept a
// payload of any cosign type as long as the repo and digest lined up, unlike its
// two siblings in internal/adapters/baseimage and internal/adapters/remotecacheutils.
func TestCheckSimpleSigningClaims_PayloadTypeConfusion(t *testing.T) {
	digest := testDigest(t)
	const repo = "ghcr.io/acme/app"

	build := func(typ, ref, dgst string) []byte {
		b, err := json.Marshal(ports.CosignSimpleSigningPayload{
			Critical: ports.CosignCritical{
				Identity: ports.CosignCriticalIdentity{DockerReference: ref},
				Image:    ports.CosignCriticalImage{DockerManifestDigest: dgst},
				Type:     typ,
			},
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		return b
	}

	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"pokkum simple-signing type accepted", build(ports.CosignSimpleSigningType, repo, digest.String()), ""},
		{"upstream cosign type accepted", build(ports.CosignContainerImageSignatureType, repo, digest.String()), ""},
		{"empty type rejected", build("", repo, digest.String()), "payload type"},
		{"foreign type rejected", build("cosign attestation", repo, digest.String()), "payload type"},
		{"wrong repo rejected", build(ports.CosignSimpleSigningType, "ghcr.io/evil/app", digest.String()), "docker-reference"},
		{"repo prefix does not count", build(ports.CosignSimpleSigningType, "evil.com/"+repo, digest.String()), "docker-reference"},
		{"repo suffix does not count", build(ports.CosignSimpleSigningType, repo+".evil.com", digest.String()), "docker-reference"},
		{"wrong digest rejected", build(ports.CosignSimpleSigningType, repo, "sha256:"+strings.Repeat("0", 64)), "docker-manifest-digest"},
		{"not JSON rejected", []byte("not json"), "Simple Signing JSON"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSimpleSigningClaims(tc.payload, repo, digest)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestCheckSimpleSigningClaims_EmptyExpectationIsNotAWildcard proves the
// unconditional comparisons: an empty expected repo or digest used to skip the
// corresponding check entirely, silently downgrading to "accept anything".
func TestCheckSimpleSigningClaims_EmptyExpectationIsNotAWildcard(t *testing.T) {
	digest := testDigest(t)
	payload, err := json.Marshal(ports.CosignSimpleSigningPayload{
		Critical: ports.CosignCritical{
			Identity: ports.CosignCriticalIdentity{DockerReference: "ghcr.io/whatever/app"},
			Image:    ports.CosignCriticalImage{DockerManifestDigest: digest.String()},
			Type:     ports.CosignSimpleSigningType,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := checkSimpleSigningClaims(payload, "  ", digest); err == nil {
		t.Error("an empty expected repo must not be treated as a wildcard")
	}
	if err := checkSimpleSigningClaims(payload, "ghcr.io/whatever/app", v1.Hash{}); err == nil {
		t.Error("an empty expected digest must not be treated as a wildcard")
	}
}

// TestStatementMatchesDigest_URISubstringConfusion is the regression test for
// the strings.Contains(sub.URI, digest.Hex) subject match. A repository name may
// legitimately be all lowercase hex, so a substring match let an attestation
// whose structured subject digest names a *different* image be accepted as this
// image's provenance.
func TestStatementMatchesDigest_URISubstringConfusion(t *testing.T) {
	digest := testDigest(t)
	other := "sha256:" + strings.Repeat("9", 64)

	stmt := func(uri string, digestMap map[string]string) ports.SLSAStatement {
		return ports.SLSAStatement{Subject: []ports.ResourceDescriptor{{URI: uri, Digest: digestMap}}}
	}

	tests := []struct {
		name string
		in   ports.SLSAStatement
		want bool
	}{
		{
			name: "structured digest match is authoritative",
			in:   stmt("ghcr.io/acme/app@"+digest.String(), map[string]string{"sha256": digest.Hex}),
			want: true,
		},
		{
			name: "URI suffix match accepted (the shape slsa.Generator emits)",
			in:   stmt("ghcr.io/acme/app@"+digest.String(), nil),
			want: true,
		},
		{
			name: "hex embedded in an attacker-chosen repository name is NOT a match",
			in:   stmt("ghcr.io/evil/"+digest.Hex+"@"+other, map[string]string{"sha256": strings.Repeat("9", 64)}),
			want: false,
		},
		{
			name: "hex as a bare substring anywhere is NOT a match",
			in:   stmt("ghcr.io/evil/app@sha256:"+digest.Hex+"deadbeef", nil),
			want: false,
		},
		{
			name: "unrelated subject is not a match",
			in:   stmt("ghcr.io/acme/app@"+other, map[string]string{"sha256": strings.Repeat("9", 64)}),
			want: false,
		},
		{
			name: "no subject at all is not a match",
			in:   ports.SLSAStatement{},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := statementMatchesDigest(tc.in, digest); got != tc.want {
				t.Errorf("statementMatchesDigest = %v, want %v", got, tc.want)
			}
		})
	}

	// A zero expected digest must never match anything.
	if statementMatchesDigest(stmt("ghcr.io/acme/app@"+digest.String(), map[string]string{"sha256": digest.Hex}), v1.Hash{}) {
		t.Error("a zero expected digest must not match")
	}
}

// TestValidateSourceMatch_RepoSubstringConfusion is the regression test for the
// strings.Contains repository comparison behind --expect-source.
func TestValidateSourceMatch_RepoSubstringConfusion(t *testing.T) {
	const commit = "c0ffee1234567890abcdef1234567890abcdef12"

	tests := []struct {
		name         string
		resolvedRepo string
		resolvedComm string
		expectSource string
		wantErr      bool
	}{
		{"exact repo@commit matches", "github.com/acme/app", commit, "github.com/acme/app@c0ffee12", false},
		{"https:// scheme normalized away", "https://github.com/acme/app", commit, "github.com/acme/app@c0ffee12", false},
		{".git suffix normalized away", "github.com/acme/app.git", commit, "github.com/acme/app@c0ffee12", false},
		{"trailing slash normalized away", "https://github.com/acme/app/", commit, "github.com/acme/app@c0ffee12", false},
		{"expectation may itself carry a scheme", "github.com/acme/app", commit, "https://github.com/acme/app@c0ffee12", false},
		{"repo only, no @", "github.com/acme/app", commit, "github.com/acme/app", false},
		{"commit prefix only, no @", "github.com/acme/app", commit, "c0ffee12", false},

		// The attack the substring match allowed.
		{"nested attacker repo rejected", "github.com/evil/github.com/acme/app", commit, "github.com/acme/app@c0ffee12", true},
		{"suffixed attacker repo rejected", "github.com/acme/app-evil", commit, "github.com/acme/app@c0ffee12", true},
		{"prefixed attacker host rejected", "evil.com/github.com/acme/app", commit, "github.com/acme/app@c0ffee12", true},
		{"nested attacker repo rejected in the no-@ form", "github.com/evil/github.com/acme/app", commit, "github.com/acme/app", true},

		{"wrong commit rejected", "github.com/acme/app", commit, "github.com/acme/app@deadbeef", true},
		{"wrong repo rejected", "github.com/other/app", commit, "github.com/acme/app@c0ffee12", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSourceMatch(tc.resolvedRepo, tc.resolvedComm, tc.expectSource)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected %q vs %q to be rejected", tc.expectSource, tc.resolvedRepo)
				}
				if !errors.Is(err, core.ErrInvalidRequest) {
					t.Errorf("got %v, want it to wrap core.ErrInvalidRequest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestReadCappedTo covers the decompression-bomb guard applied to every
// registry-supplied blob and tar entry this package reads before any signature
// has been verified.
func TestReadCappedTo(t *testing.T) {
	t.Run("under the cap", func(t *testing.T) {
		got, err := readCappedTo(strings.NewReader("hello"), "blob", 16)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("exactly at the cap is allowed", func(t *testing.T) {
		got, err := readCappedTo(strings.NewReader("0123456789"), "blob", 10)
		if err != nil {
			t.Fatalf("content exactly filling the cap must be accepted, got %v", err)
		}
		if len(got) != 10 {
			t.Errorf("got %d bytes, want 10", len(got))
		}
	})

	t.Run("over the cap is refused", func(t *testing.T) {
		_, err := readCappedTo(strings.NewReader("01234567890"), "blob", 10)
		if err == nil {
			t.Fatal("expected an error for content over the cap")
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error %q should mention the limit", err)
		}
	})

	t.Run("an endless stream is bounded, not read forever", func(t *testing.T) {
		// zeroReader never returns io.EOF; without the LimitReader this call
		// would not terminate.
		_, err := readCappedTo(zeroReader{}, "blob", 1024)
		if err == nil {
			t.Fatal("expected an error for an endless stream")
		}
	})

	t.Run("the production cap is the one wired in", func(t *testing.T) {
		if maxSignatureBlobBytes != 64<<20 {
			t.Errorf("maxSignatureBlobBytes = %d, want 64MiB", maxSignatureBlobBytes)
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

var _ io.Reader = zeroReader{}
