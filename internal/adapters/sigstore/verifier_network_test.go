package sigstore

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// liveKeylessMaterial resolves ref to a digest, fetches its Cosign signature
// tag, and reads the keyless material off the signature layer descriptor's
// annotations plus the layer blob itself. This mirrors what
// internal/adapters/baseimage.Resolver does for the static-key path, and is
// what M4's resolver integration will do for the keyless path.
func liveKeylessMaterial(t *testing.T, ref string) (ports.KeylessVerifyRequest, string) {
	t.Helper()

	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatalf("parse reference %s: %v", ref, err)
	}

	desc, err := remote.Get(parsed)
	if err != nil {
		t.Fatalf("resolve %s: %v", ref, err)
	}
	digest := desc.Digest

	sigRefStr := parsed.Context().Name() + ":" + digest.Algorithm + "-" + digest.Hex + ".sig"
	sigRef, err := name.ParseReference(sigRefStr)
	if err != nil {
		t.Fatalf("parse signature reference %s: %v", sigRefStr, err)
	}

	sigImg, err := remote.Image(sigRef)
	if err != nil {
		t.Fatalf("fetch signature image %s: %v", sigRefStr, err)
	}

	layers, err := sigImg.Layers()
	if err != nil || len(layers) == 0 {
		t.Fatalf("read signature layers in %s: %v (%d layers)", sigRefStr, err, len(layers))
	}
	rc, err := layers[0].Compressed()
	if err != nil {
		t.Fatalf("open signature layer in %s: %v", sigRefStr, err)
	}
	defer rc.Close()
	payload, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read signature payload in %s: %v", sigRefStr, err)
	}

	manifest, err := sigImg.Manifest()
	if err != nil {
		t.Fatalf("read signature manifest in %s: %v", sigRefStr, err)
	}
	if len(manifest.Layers) == 0 || manifest.Layers[0].Annotations == nil {
		t.Fatalf("signature manifest %s has no layer annotations", sigRefStr)
	}
	ann := manifest.Layers[0].Annotations

	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ann[CosignSignatureAnnotation]))
	if err != nil {
		t.Fatalf("decode %s from %s: %v", CosignSignatureAnnotation, sigRefStr, err)
	}

	return ports.KeylessVerifyRequest{
		PayloadBytes:    payload,
		SignatureBytes:  sigBytes,
		CertificatePEM:  []byte(ann[CosignCertificateAnnotation]),
		ChainPEM:        []byte(ann[CosignChainAnnotation]),
		RekorBundleJSON: []byte(ann[CosignBundleAnnotation]),
	}, sigRefStr
}

// TestVerify_LiveDistrolessKeylessSignature fetches the current keyless
// signature for the stock distroless base image from gcr.io and verifies it
// end to end against the embedded trust root, then confirms a single flipped
// signature byte fails closed.
//
// This is the only place this package touches the network; it is skipped under
// -short so the default `go test ./...` stays hermetic. Run explicitly with:
//
//	go test ./internal/adapters/sigstore/... -run LiveDistroless -v
func TestVerify_LiveDistrolessKeylessSignature(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent keyless verification in -short mode")
	}

	req, sigRefStr := liveKeylessMaterial(t, ports.DistrolessBaseRef)
	req.Identity = ports.KeylessIdentity{
		Issuer: ports.DistrolessKeylessIssuer,
		SAN:    ports.DistrolessKeylessSAN,
	}

	v := NewVerifier(nil)

	t.Run("positive", func(t *testing.T) {
		got, err := v.Verify(context.Background(), req)
		if err != nil {
			t.Fatalf("Verify() on live signature %s failed: %v", sigRefStr, err)
		}
		if got.Issuer != ports.DistrolessKeylessIssuer {
			t.Errorf("Issuer = %q, want %q", got.Issuer, ports.DistrolessKeylessIssuer)
		}
		if got.SAN != ports.DistrolessKeylessSAN {
			t.Errorf("SAN = %q, want %q", got.SAN, ports.DistrolessKeylessSAN)
		}
		if got.RekorLogID == "" || got.RekorLogIndex <= 0 || got.IntegratedTime.IsZero() {
			t.Errorf("incomplete Rekor provenance: %+v", got)
		}
		t.Logf("verified %s: %+v", sigRefStr, got)
	})

	t.Run("tampered signature fails closed", func(t *testing.T) {
		bad := req
		bad.SignatureBytes = append([]byte(nil), req.SignatureBytes...)
		bad.SignatureBytes[len(bad.SignatureBytes)-1] ^= 0x01

		if _, err := v.Verify(context.Background(), bad); err == nil {
			t.Fatal("Verify() accepted a tampered signature")
		} else if !errors.Is(err, ErrTlogInvalid) && !errors.Is(err, ErrChainInvalid) {
			t.Fatalf("Verify() error = %v, want one wrapping ErrTlogInvalid or ErrChainInvalid", err)
		} else {
			t.Logf("rejected as expected: %v", err)
		}
	})
}
