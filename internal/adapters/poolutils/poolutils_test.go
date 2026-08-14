package poolutils_test

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/poolutils"
)

func TestCopyBufferPool(t *testing.T) {
	bufPtr := poolutils.GetCopyBuffer()
	if bufPtr == nil || len(*bufPtr) != poolutils.CopyBufferSize {
		t.Fatalf("expected buffer of size %d, got %v", poolutils.CopyBufferSize, bufPtr)
	}

	// Write dummy data
	(*bufPtr)[0] = 0x42
	poolutils.PutCopyBuffer(bufPtr)

	// Nil or wrong sized slice should not panic
	poolutils.PutCopyBuffer(nil)
	wrong := make([]byte, 10)
	poolutils.PutCopyBuffer(&wrong)
}

func TestCopy(t *testing.T) {
	data := make([]byte, 256*1024) // 256 KiB
	_, _ = rand.Read(data)

	src := bytes.NewReader(data)
	var dst bytes.Buffer

	n, err := poolutils.Copy(&dst, src)
	if err != nil {
		t.Fatalf("poolutils.Copy failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("expected copied %d bytes, got %d", len(data), n)
	}
	if !bytes.Equal(dst.Bytes(), data) {
		t.Errorf("destination bytes do not match source data")
	}
}

func TestByteBufferPool(t *testing.T) {
	buf := poolutils.GetByteBuffer()
	if buf == nil {
		t.Fatalf("expected non-nil buffer")
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer, got len %d", buf.Len())
	}

	buf.WriteString("hello pool")
	if buf.String() != "hello pool" {
		t.Errorf("unexpected content: %s", buf.String())
	}

	poolutils.PutByteBuffer(buf)

	// Large buffer exceeding max capacity
	oversized := bytes.NewBuffer(make([]byte, poolutils.MaxPooledBufferCapacity+1024))
	poolutils.PutByteBuffer(oversized) // Should be discarded safely

	// Nil buffer should not panic
	poolutils.PutByteBuffer(nil)
}
