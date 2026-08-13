package sigstore

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
)

func TestDefaultTrustedRootJSON_NonEmpty(t *testing.T) {
	data := DefaultTrustedRootJSON()
	if len(data) == 0 {
		t.Fatal("DefaultTrustedRootJSON() returned empty bytes")
	}
}

func TestDefaultTrustedRootJSON_ReturnsDefensiveCopy(t *testing.T) {
	a := DefaultTrustedRootJSON()
	b := DefaultTrustedRootJSON()
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("expected non-empty trust root bytes")
	}

	a[0] ^= 0xFF // mutate the caller's copy
	if a[0] == b[0] {
		t.Fatal("mutating one returned slice affected another; DefaultTrustedRootJSON is not returning defensive copies")
	}
}

func TestDefaultTrustedRootJSON_ParsesAsTrustedRoot(t *testing.T) {
	data := DefaultTrustedRootJSON()

	tr, err := root.NewTrustedRootFromJSON(data)
	if err != nil {
		t.Fatalf("root.NewTrustedRootFromJSON returned error: %v", err)
	}
	if tr == nil {
		t.Fatal("root.NewTrustedRootFromJSON returned nil TrustedRoot with no error")
	}
}
