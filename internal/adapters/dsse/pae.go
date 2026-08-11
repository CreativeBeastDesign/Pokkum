package dsse

import (
	"fmt"
)

// PAE computes the Pre-Authentication Encoding per the DSSE specification:
// PAE(type, payload) = "DSSEv1 " + len(type) + " " + type + " " + len(payload) + " " + payload
//
// Where len(x) is the ASCII decimal representation of the byte length of x.
func PAE(payloadType string, payload []byte) []byte {
	prefix := fmt.Sprintf("DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	pae := make([]byte, 0, len(prefix)+len(payload))
	pae = append(pae, []byte(prefix)...)
	pae = append(pae, payload...)
	return pae
}
