package cosign

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// FuzzCosignPayloadJSON exercises json.Unmarshal against arbitrary bytes
// decoded into ports.CosignSimpleSigningPayload — the exact type Verify
// (signer.go) unmarshals bundle.PayloadBytes into as its very first step,
// before any cryptographic check runs. bundle.PayloadBytes ultimately
// originates from a signature attached to (or claimed to be attached to) a
// pulled OCI image — untrusted content a verifier has no choice but to
// parse before it can even check whether the signature is valid. The only
// property under test is that parsing (and round-trip re-marshaling of
// whatever successfully parsed) never panics — Verify's own claim checks
// and crypto verification are exercised separately by
// FuzzCosignVerify_MalformedPayloadNeverVerifies below.
func FuzzCosignPayloadJSON(f *testing.F) {
	f.Add(`{"critical":{"identity":{"docker-reference":"ghcr.io/acme/app"},"image":{"docker-manifest-digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"},"type":"cosign container image signature"},"optional":{"creator":"pokkum"}}`)
	f.Add(`{}`)
	f.Add(`null`)
	f.Add(`[]`)
	f.Add(`"just a string"`)
	f.Add(`42`)
	f.Add(`{"critical":null}`)
	f.Add(`{"critical":{"identity":null,"image":null,"type":123}}`)
	f.Add(`{"critical":{"identity":{"docker-reference":123}}}`) // wrong type for a string field
	f.Add(`{"optional":{"extra":"not-a-map"}}`)                 // Extra is map[string]string in ports
	f.Add(`{"optional":{"timestamp":"not-a-number"}}`)
	f.Add("")
	f.Add("\x00\x01\x02")
	f.Add(`{"critical":{"identity":{"docker-reference":"` + string([]byte{0xff, 0xfe}) + `"}}}`)
	f.Add(`{"a":` + string(make([]byte, 0)) + `}`)                                                               // malformed trailing
	f.Add(`{"critical":{"identity":{"docker-reference":"a"}},"critical":{"identity":{"docker-reference":"b"}}}`) // duplicate key

	f.Fuzz(func(t *testing.T, raw string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("json.Unmarshal into CosignSimpleSigningPayload panicked on %q: %v", raw, r)
			}
		}()
		var payload ports.CosignSimpleSigningPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return // malformed/mistyped JSON is an expected, ordinary rejection
		}
		// Whatever successfully parsed must also be safe to re-marshal — this
		// is exactly what marshalPayload does for the signing half, and
		// exercising it here catches any field whose parsed zero-value/edge
		// value can't itself be re-encoded.
		if _, err := json.Marshal(payload); err != nil {
			t.Fatalf("re-marshal of successfully-parsed payload failed for input %q: %v", raw, err)
		}
	})
}

// fuzzECDSAKeyPair is generated once for FuzzCosignVerify_MalformedPayloadNeverVerifies
// (key generation needs real randomness and is not itself part of the
// property being fuzzed) and reused across every fuzz iteration.
type fuzzECDSAKeyPair struct {
	privPEM []byte
	pubPEM  []byte
}

func newFuzzECDSAKeyPair() (fuzzECDSAKeyPair, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fuzzECDSAKeyPair{}, err
	}
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fuzzECDSAKeyPair{}, err
	}
	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return fuzzECDSAKeyPair{}, err
	}
	return fuzzECDSAKeyPair{
		privPEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes}),
		pubPEM:  pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}),
	}, nil
}

// FuzzCosignVerify_MalformedPayloadNeverVerifies asserts the actual security
// property Simple Signing verification exists for: a payload the verifier
// did not itself produce/sign must never be accepted as valid, no matter how
// it's mutated, and Verify must never panic while deciding that.
//
// A genuinely signed bundle is built once (real ECDSA key, real signature
// over a real payload). Each fuzz iteration then swaps in a fuzz-controlled
// byte string as bundle.PayloadBytes while keeping the ORIGINAL signature
// bytes — exactly the shape of an attacker who intercepts a real signature
// and tries to attach it to different, forged content. Since the signature
// is computed over the exact payload bytes (SHA-256, then ECDSA), Verify
// must reject every fuzzed payload except the one byte-identical to the
// original (soundness of ECDSA makes any other "success" a real forgery,
// not a coincidence worth tolerating).
func FuzzCosignVerify_MalformedPayloadNeverVerifies(f *testing.F) {
	keys, err := newFuzzECDSAKeyPair()
	if err != nil {
		f.Fatalf("generate fuzz ecdsa keypair: %v", err)
	}

	digest, err := v1.NewHash("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		f.Fatalf("parse digest: %v", err)
	}
	const repo = "ghcr.io/acme/app"

	signer := NewSigner(nil)
	req := ports.CosignSignRequest{
		Repo:            repo,
		Digest:          digest,
		KeyPEM:          keys.privPEM,
		Creator:         "pokkum-fuzz",
		SourceDateEpoch: time.Unix(1700000000, 0).UTC(),
	}
	bundle, err := signer.Sign(context.Background(), req)
	if err != nil {
		f.Fatalf("Sign failed during fuzz setup: %v", err)
	}
	if err := signer.Verify(context.Background(), bundle, keys.pubPEM, repo, digest); err != nil {
		f.Fatalf("sanity check: genuine bundle failed to verify: %v", err)
	}

	// Real-shaped seed: the genuine, correctly-signed payload — must verify.
	f.Add(string(bundle.PayloadBytes))
	// Hostile seeds: near-miss mutations of the genuine payload.
	f.Add(string(bundle.PayloadBytes) + " ")
	f.Add(string(bundle.PayloadBytes[:len(bundle.PayloadBytes)-1]))
	f.Add(`{"critical":{"identity":{"docker-reference":"ghcr.io/attacker/app"},"image":{"docker-manifest-digest":"` + digest.String() + `"},"type":"cosign container image signature"}}`)
	f.Add(`{}`)
	f.Add(``)
	f.Add(`null`)
	f.Add(string(bundle.PayloadBytes[:len(bundle.PayloadBytes)/2]))

	f.Fuzz(func(t *testing.T, fuzzedPayload string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Verify panicked on fuzzed payload %q: %v", fuzzedPayload, r)
			}
		}()

		forged := ports.CosignSignatureBundle{
			PayloadBytes:   []byte(fuzzedPayload),
			SignatureBytes: bundle.SignatureBytes,
			Repo:           repo,
			Digest:         digest,
		}
		err := signer.Verify(context.Background(), forged, keys.pubPEM, repo, digest)

		if fuzzedPayload == string(bundle.PayloadBytes) {
			if err != nil {
				t.Fatalf("Verify rejected the byte-identical genuine payload: %v", err)
			}
			return
		}
		if err == nil {
			t.Fatalf("Verify ACCEPTED a forged/mutated payload %q with a signature that was never computed over it — this is a real signature-verification bypass", fuzzedPayload)
		}
	})
}
