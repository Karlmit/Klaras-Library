package logcapture

import "testing"

func TestBufferKeepsNewestBytesAndCopiesSnapshot(t *testing.T) {
	b := New(12)
	_, _ = b.Write([]byte("old\n"))
	_, _ = b.Write([]byte("middle\n"))
	_, _ = b.Write([]byte("new\n"))
	got := b.Snapshot()
	if string(got) != "middle\nnew\n" {
		t.Fatalf("snapshot = %q, want %q", got, "middle\\nnew\\n")
	}
	got[0] = 'X'
	if string(b.Snapshot()) != "middle\nnew\n" {
		t.Fatal("Snapshot returned storage owned by the buffer")
	}
}

func TestBufferAcceptsWriteLargerThanCapacity(t *testing.T) {
	b := New(4)
	p := []byte("abcdef")
	n, err := b.Write(p)
	if err != nil || n != len(p) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := string(b.Snapshot()); got != "cdef" {
		t.Fatalf("snapshot = %q, want cdef", got)
	}
}
