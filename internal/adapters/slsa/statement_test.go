package slsa

import (
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// statementTypeForPredicate encodes the in-toto Statement version each SLSA
// provenance predicate version is defined against. SLSA v0.2 predicates ride
// in an in-toto Statement v0.1; SLSA v1.0 predicates ride in a Statement v1.
// The two are not interchangeable: cosign tolerates a mismatched pair, but
// slsa-verifier and policy engines that validate `_type` may reject it.
//
// This table is deliberately written out literally rather than derived from
// the ports constants — a test that recomputes the value it is checking
// cannot catch the values drifting apart.
var statementTypeForPredicate = map[string]string{
	"https://slsa.dev/provenance/v0.2": "https://in-toto.io/Statement/v0.1",
	"https://slsa.dev/provenance/v1":   "https://in-toto.io/Statement/v1",
}

// generateMinimalStatement produces a statement from the smallest request
// Generate accepts, since these assertions are about the envelope only.
func generateMinimalStatement(t *testing.T) ports.SLSAStatement {
	t.Helper()
	outHash, err := v1.NewHash("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("parse output hash: %v", err)
	}
	stmt, err := NewGenerator(nil).Generate(context.Background(), ports.SLSAGeneratorRequest{
		Repo:         "ghcr.io/acme/app",
		OutputDigest: outHash,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	return stmt
}

// TestStatementTypeMatchesPredicateType is the drift guard: the emitted
// `_type` and `predicateType` must stay a legal pair. Changing either
// constant alone fails this test.
func TestStatementTypeMatchesPredicateType(t *testing.T) {
	stmt := generateMinimalStatement(t)

	want, known := statementTypeForPredicate[stmt.PredicateType]
	if !known {
		t.Fatalf("predicateType = %q, which this test does not know an in-toto Statement version for; "+
			"add the pairing to statementTypeForPredicate when adopting a new SLSA predicate version", stmt.PredicateType)
	}
	if stmt.Type != want {
		t.Errorf("_type = %q but predicateType = %q, which is defined against Statement %q; "+
			"slsa-verifier and policy engines that validate _type may reject this attestation",
			stmt.Type, stmt.PredicateType, want)
	}
}

// TestStatementTypeIsSLSAv1Pair pins the pair Pokkum currently emits, so an
// unintended downgrade of *both* constants together (which would satisfy
// TestStatementTypeMatchesPredicateType) still fails.
func TestStatementTypeIsSLSAv1Pair(t *testing.T) {
	stmt := generateMinimalStatement(t)

	if stmt.PredicateType != "https://slsa.dev/provenance/v1" {
		t.Errorf("predicateType = %q, want %q", stmt.PredicateType, "https://slsa.dev/provenance/v1")
	}
	if stmt.Type != "https://in-toto.io/Statement/v1" {
		t.Errorf("_type = %q, want %q", stmt.Type, "https://in-toto.io/Statement/v1")
	}
}

// TestStatementTypeSerializesAsUnderscoreType checks the wire form a
// verifier actually reads, not just the Go field: `_type` is what
// slsa-verifier parses out of the DSSE payload.
func TestStatementTypeSerializesAsUnderscoreType(t *testing.T) {
	stmt := generateMinimalStatement(t)

	raw, err := json.Marshal(stmt)
	if err != nil {
		t.Fatalf("marshal statement: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal statement: %v", err)
	}

	gotType, _ := decoded["_type"].(string)
	gotPredicate, _ := decoded["predicateType"].(string)
	want, known := statementTypeForPredicate[gotPredicate]
	if !known {
		t.Fatalf("serialized predicateType = %q, unknown pairing", gotPredicate)
	}
	if gotType != want {
		t.Errorf("serialized _type = %q, want %q for predicateType %q", gotType, want, gotPredicate)
	}
}
