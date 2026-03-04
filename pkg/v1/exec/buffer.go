package exec

import "sync"

// OutputBuffer stores terminal output bytes, providing a fixed-size cyclic buffer.
type OutputBuffer struct {
	buf      []byte
	capacity int
	lock     sync.Mutex
}

// NewOutputBuffer initializes an OutputBuffer with a specified capacity.
// If capacity is <= 0, a minimum capacity of 0 is used (all appends are discarded).
func NewOutputBuffer(capacity int) *OutputBuffer {
	if capacity < 0 {
		capacity = 0
	}
	return &OutputBuffer{
		buf:      make([]byte, 0, capacity),
		capacity: capacity,
	}
}

// NewDefaultOutputBuffer creates an OutputBuffer with DefaultOutputBufferSize.
func NewDefaultOutputBuffer() *OutputBuffer {
	return NewOutputBuffer(DefaultOutputBufferSize)
}

// Append adds data to the buffer, evicting the oldest bytes when capacity is exceeded.
func (b *OutputBuffer) Append(data []byte) {
	b.lock.Lock()
	defer b.lock.Unlock()

	b.buf = append(b.buf, data...)

	// Cyclic eviction: if we've exceeded capacity, trim from the front.
	if len(b.buf) > b.capacity {
		b.buf = b.buf[len(b.buf)-b.capacity:]
	}
}

// GetAll retrieves a copy of all stored bytes in the buffer.
func (b *OutputBuffer) GetAll() []byte {
	b.lock.Lock()
	defer b.lock.Unlock()
	return append([]byte(nil), b.buf...)
}

// Len returns the current number of bytes in the buffer.
func (b *OutputBuffer) Len() int {
	b.lock.Lock()
	defer b.lock.Unlock()
	return len(b.buf)
}
