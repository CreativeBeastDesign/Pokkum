package poolutils

import (
	"bytes"
	"io"
	"sync"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// CopyBufferSize is the size of each pooled byte slice for io.CopyBuffer (64 KiB).
const CopyBufferSize = 64 * 1024

// MaxPooledBufferCapacity prevents excessively large buffers from remaining in sync.Pool.
const MaxPooledBufferCapacity = 1024 * 1024 // 1 MiB

var copyBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, CopyBufferSize)
		return &b
	},
}

var byteBufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// GetCopyBuffer retrieves a 64 KiB byte slice pointer from the pool.
func GetCopyBuffer() *[]byte {
	return copyBufferPool.Get().(*[]byte)
}

// PutCopyBuffer returns a 64 KiB byte slice pointer to the pool.
func PutCopyBuffer(b *[]byte) {
	if b == nil || len(*b) != CopyBufferSize {
		return
	}
	copyBufferPool.Put(b)
}

// Copy copies from src to dst using a recycled 64 KiB buffer from the pool.
func Copy(dst io.Writer, src io.Reader) (int64, error) {
	bufPtr := GetCopyBuffer()
	defer PutCopyBuffer(bufPtr)
	return io.CopyBuffer(dst, src, *bufPtr)
}

// GetByteBuffer retrieves a reset bytes.Buffer from the pool.
func GetByteBuffer() *bytes.Buffer {
	buf := byteBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// PutByteBuffer returns a bytes.Buffer to the pool if its capacity does not exceed 1 MiB.
func PutByteBuffer(buf *bytes.Buffer) {
	if buf == nil || buf.Cap() > MaxPooledBufferCapacity {
		return
	}
	buf.Reset()
	byteBufferPool.Put(buf)
}
