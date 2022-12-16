package state

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

// BufferStringer is an interface for mocking standard streams.
type BufferStringer interface {
	io.ReadWriter
	fmt.Stringer
	Bytes() []byte
}

// SafeBuffer implements a thread-safe read/write buffer, that allows reading
// its current data. It's typically useful for tests.
type SafeBuffer struct {
	b bytes.Buffer
	m sync.RWMutex
}

var _ BufferStringer = &SafeBuffer{}

func (b *SafeBuffer) Read(p []byte) (n int, err error) {
	b.m.RLock()
	defer b.m.RUnlock()
	return b.b.Read(p)
}

func (b *SafeBuffer) Write(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Write(p)
}

func (b *SafeBuffer) String() string {
	b.m.RLock()
	defer b.m.RUnlock()
	return b.b.String()
}

// Bytes returns the unread bytes contained in the buffer.
func (b *SafeBuffer) Bytes() []byte {
	b.m.RLock()
	defer b.m.RUnlock()
	return b.b.Bytes()
}
