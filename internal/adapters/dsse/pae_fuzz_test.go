package dsse

import (
	"bytes"
	"testing"
)

// FuzzPAE asserts the actual security property Pre-Authentication Encoding
// exists for: PAE must be an UNAMBIGUOUS (injective) encoding of the
// (payloadType, payload) pair it's given. If two different pairs could ever
// produce identical PAE bytes, a signature computed over one pair's PAE
// would also verify for the other — letting an attacker who controls
// (type, payload) shift bytes between the two fields to forge a signature
// over content the signer never actually intended to sign (the classic
// length-prefix-free-encoding confusion attack PAE's explicit length
// prefixes are specifically designed to prevent, e.g. type="ab",
// payload="c" vs. type="a", payload="bc").
//
// This differs from what a single spot-check would find: the property must
// hold for arbitrary fuzz-discovered pairs, not just the one textbook
// example, since a subtly different off-by-one in the length-prefix
// encoding could still leave a narrower class of pairs colliding.
func FuzzPAE(f *testing.F) {
	// The classic ambiguity a naive "type + payload" concatenation (with no
	// length prefix) would suffer from — PAE's length prefixes must keep
	// these four distinct.
	f.Add("ab", []byte("c"), "a", []byte("bc"))
	f.Add("", []byte("abc"), "a", []byte("bc"))
	f.Add("abc", []byte(""), "ab", []byte("c"))
	// Payload/type containing the exact delimiter/space characters PAE uses
	// in its own prefix format ("DSSEv1 <len> <type> <len> <payload>") —
	// the length-prefix mechanism must not be confusable by content that
	// looks like another length prefix.
	f.Add("application/vnd.in-toto+json", []byte("DSSEv1 3 xyz 4 data"), "application/vnd.in-toto+json", []byte("DSSEv1 3 xyz 4 dat"))
	f.Add("1 a", []byte("b"), "1", []byte("a b"))
	f.Add("", []byte(""), "", []byte("\x00"))
	f.Add("t", []byte{0x00, 0x01, 0xff, 0xfe}, "t", []byte{0x00, 0x01, 0xff})
	f.Add("dup", []byte("same"), "dup", []byte("same")) // identical pair: PAE must be equal, not a counterexample

	f.Fuzz(func(t *testing.T, type1 string, payload1 []byte, type2 string, payload2 []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("PAE panicked: type1=%q payload1=%v type2=%q payload2=%v: %v", type1, payload1, type2, payload2, r)
			}
		}()

		pae1 := PAE(type1, payload1)
		pae2 := PAE(type2, payload2)

		samePair := type1 == type2 && bytes.Equal(payload1, payload2)
		sameEncoding := bytes.Equal(pae1, pae2)

		if samePair && !sameEncoding {
			t.Fatalf("PAE is not even deterministic: identical input (type=%q, payload=%v) produced different output %v vs %v", type1, payload1, pae1, pae2)
		}
		if !samePair && sameEncoding {
			t.Fatalf("PAE collision: distinct pairs (type=%q, payload=%v) and (type=%q, payload=%v) both encode to %v — this breaks the unambiguous-encoding property DSSE PAE exists to guarantee", type1, payload1, type2, payload2, pae1)
		}
	})
}
