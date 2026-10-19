// Package logcapture keeps a bounded, concurrency-safe copy of application logs.
package logcapture

import (
	"bytes"
	"sync"
)

// Buffer is an io.Writer retaining only the newest max bytes written to it.
// Log records are written in one call by slog, so trimming at write boundaries
// avoids returning a partial record whenever possible.
type Buffer struct {
	mu   sync.RWMutex
	max  int
	data []byte
}

func New(max int) *Buffer {
	if max < 1 {
		max = 1
	}
	return &Buffer{max: max, data: make([]byte, 0, max)}
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := len(p)
	if n >= b.max {
		b.data = append(b.data[:0], p[n-b.max:]...)
		return n, nil
	}
	if overflow := len(b.data) + n - b.max; overflow > 0 {
		// Drop through the end of the oldest affected record so downloads do
		// not begin halfway through a log line.
		drop := overflow
		if end := bytes.IndexByte(b.data[overflow:], '\n'); end >= 0 {
			drop = overflow + end + 1
		}
		copy(b.data, b.data[drop:])
		b.data = b.data[:len(b.data)-drop]
	}
	b.data = append(b.data, p...)
	return n, nil
}

// Snapshot returns a copy safe for the caller to retain or modify.
func (b *Buffer) Snapshot() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]byte, len(b.data))
	copy(out, b.data)
	return out
}
