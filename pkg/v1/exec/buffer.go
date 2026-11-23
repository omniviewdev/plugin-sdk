// Package exec manages plugin terminal sessions, multiplexed input and output,
// and bounded output history for reconnecting clients.
package exec

import "sync"

// OutputBuffer keeps the last bytes a terminal wrote, up to a fixed capacity, in a ring: one array,
// allocated when the buffer is made, in which Append overwrites the oldest bytes. Appending
// costs the size of the chunk and allocates nothing, however long the terminal has been talking.
type OutputBuffer struct {
	lock     sync.Mutex
	buf      []byte // the ring: len(buf) == capacity
	capacity int
	start    int    // where the oldest byte kept is in buf
	size     int    // the number of bytes kept
	total    uint64 // the number of bytes appended, kept or not
}

// NewOutputBuffer initializes an OutputBuffer with a specified capacity.
// If capacity is <= 0, a minimum capacity of 0 is used (all appends are discarded).
func NewOutputBuffer(capacity int) *OutputBuffer {
	if capacity < 0 {
		capacity = 0
	}
	return &OutputBuffer{
		buf:      make([]byte, capacity),
		capacity: capacity,
	}
}

// NewDefaultOutputBuffer creates an OutputBuffer with DefaultOutputBufferSize.
func NewDefaultOutputBuffer() *OutputBuffer {
	return NewOutputBuffer(DefaultOutputBufferSize)
}

// Append adds data to the buffer, evicting the oldest bytes when capacity is exceeded.
// It copies data and keeps no reference to it.
func (b *OutputBuffer) Append(data []byte) {
	b.lock.Lock()
	defer b.lock.Unlock()

	b.total += uint64(len(data))
	if b.capacity == 0 || len(data) == 0 {
		return
	}

	if len(data) >= b.capacity {
		// Data alone fills the buffer: keep only its last capacity bytes.
		copy(b.buf, data[len(data)-b.capacity:])
		b.start, b.size = 0, b.capacity
		return
	}

	// Write after the newest byte, to the end of the array and on from its beginning. What the write
	// covers beyond the free room is the oldest bytes, which it replaces.
	end := b.start + b.size
	if end >= b.capacity {
		end -= b.capacity
	}
	n := copy(b.buf[end:], data)
	copy(b.buf, data[n:])

	b.size += len(data)
	if b.size > b.capacity {
		b.start += b.size - b.capacity
		if b.start >= b.capacity {
			b.start -= b.capacity
		}
		b.size = b.capacity
	}
}

// GetAll retrieves a copy of all stored bytes in the buffer, oldest first.
func (b *OutputBuffer) GetAll() []byte {
	b.lock.Lock()
	defer b.lock.Unlock()

	out := make([]byte, b.size)
	n := copy(out, b.buf[b.start:min(b.start+b.size, b.capacity)])
	copy(out[n:], b.buf)
	return out
}

// Len returns the current number of bytes in the buffer.
func (b *OutputBuffer) Len() int {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.size
}

// Total returns the number of bytes appended since the buffer was made, whether it kept them or
// evicted them since: the position, in the terminal's output, of the byte that the next Append adds.
func (b *OutputBuffer) Total() uint64 {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.total
}
