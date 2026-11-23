package exec

import "unicode/utf8"

// What a bounded run keeps of its output. A run has a cap for each of its streams, and the SDK never holds more than the cap
// of a stream, whatever the command writes: the bytes that do not fit are counted and dropped, and the account says exactly
// how many. A capture keeps a window of one stream; a streamTally is the account of a stream whose bytes are not kept because
// they are delivered as they arrive (RunStream).
//
// Both are plain values with no lock of their own: the reader of a run's output is the one goroutine that writes them, and the
// call reads them once it has stopped that reader (run_server.go).

// truncatePolicy says which part of an output a capture keeps once the output is over its cap.
type truncatePolicy uint8

const (
	// truncateHeadTail keeps the beginning and the end (an error is usually at the end): the head is the first ceil(cap/2)
	// bytes of the stream, the tail the last floor(cap/2). It is the default.
	truncateHeadTail truncatePolicy = iota
	// truncateHead keeps the first cap bytes.
	truncateHead
	// truncateTail keeps the last cap bytes.
	truncateTail
)

// outputAccount is the exact account of one stream of a run: Total is every byte of it, the ones the engine's queue lost
// before the SDK read them included, Kept the bytes returned, Dropped what was not (always Total - Kept). Head and Tail say how
// the kept bytes split: under HEAD_TAIL the head is the first Head of them and the tail the last Tail. ValidUTF8 is about the
// kept bytes as they are returned: a sequence that the cap cut is not repaired.
type outputAccount struct {
	Total     uint64
	Kept      uint64
	Dropped   uint64
	Head      int
	Tail      int
	ValidUTF8 bool
}

// Truncated reports whether any byte was dropped.
func (a outputAccount) Truncated() bool { return a.Dropped > 0 }

// capture keeps the window of one stream: a head and a tail, each one unbroken part of the stream, and counts the rest. Its
// memory grows with the data, up to the cap and never past it: a stream of ten bytes costs about ten bytes. A write at the cap
// costs a copy and no allocation.
//
// A range of the stream that was lost before it could be read (Lose) ends the head where it falls and empties the tail, so that
// the head stays the start of the stream and the tail its end, each in one piece; what is kept is then less than the cap
// allows, and the bytes of the range count as dropped.
type capture struct {
	headCap, tailCap int
	head             []byte // grows with the data, at most headCap
	ring             []byte // the storage of the tail: grows with the data, at most tailCap
	ringStart        int    // index in ring of the oldest byte of the tail
	ringLen          int    // bytes of the tail
	total            uint64
}

// newCapture returns a capture of limit bytes under a policy.
func newCapture(limit int, policy truncatePolicy) *capture {
	c := new(capture)
	switch policy {
	case truncateHead:
		c.headCap = limit
	case truncateTail:
		c.tailCap = limit
	case truncateHeadTail:
		c.headCap, c.tailCap = (limit+1)/2, limit/2
	}
	return c
}

// Write takes p as the next bytes of the stream. It does not keep p.
func (c *capture) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	c.total += uint64(len(p))
	if room := c.headCap - len(c.head); room > 0 {
		n := min(room, len(p))
		c.head = appendGrown(c.head, p[:n], c.headCap)
		p = p[n:]
	}
	if len(p) > 0 && c.tailCap > 0 {
		c.writeTail(p)
	}
}

// Lose takes n bytes of the stream that were lost before the SDK could read them.
func (c *capture) Lose(n uint64) {
	if n == 0 {
		return
	}
	c.total += n
	c.headCap = len(c.head)
	c.ringStart, c.ringLen = 0, 0
}

// Result returns the kept bytes, the head and then the tail, as a slice the caller owns, and their account.
func (c *capture) Result() ([]byte, outputAccount) {
	kept := len(c.head) + c.ringLen
	var data []byte
	if kept > 0 {
		data = make([]byte, kept)
		n := copy(data, c.head)
		c.copyTail(data[n:])
	}
	return data, outputAccount{
		Total: c.total, Kept: uint64(kept), Dropped: c.total - uint64(kept),
		Head: len(c.head), Tail: c.ringLen, ValidUTF8: utf8.Valid(data),
	}
}

// appendGrown appends p to dst, which holds at most limit bytes once p is in it, growing its storage by what it needs and no
// further than limit.
func appendGrown(dst, p []byte, limit int) []byte {
	if need := len(dst) + len(p); need > cap(dst) {
		grown := make([]byte, len(dst), min(limit, max(need, 2*cap(dst))))
		copy(grown, dst)
		dst = grown
	}
	return append(dst, p...)
}

// writeTail takes p as the next bytes of the tail, overwriting the oldest bytes it holds when the cap is reached.
func (c *capture) writeTail(p []byte) {
	if len(p) >= c.tailCap {
		// p alone fills the tail, and everything before it is older.
		p = p[len(p)-c.tailCap:]
		c.growRing(c.tailCap)
		copy(c.ring, p)
		c.ringStart, c.ringLen = 0, len(p)
		return
	}
	c.growRing(min(c.tailCap, c.ringLen+len(p)))
	if over := c.ringLen + len(p) - len(c.ring); over > 0 {
		c.ringStart = (c.ringStart + over) % len(c.ring)
		c.ringLen -= over
	}
	end := (c.ringStart + c.ringLen) % len(c.ring)
	n := copy(c.ring[end:], p)
	copy(c.ring, p[n:])
	c.ringLen += len(p)
}

// growRing makes the storage of the tail hold at least need bytes (need must not exceed tailCap), keeping what it holds, in
// order, at the start of the new storage.
func (c *capture) growRing(need int) {
	if need <= len(c.ring) {
		return
	}
	next := make([]byte, min(c.tailCap, max(need, 2*len(c.ring))))
	c.copyTail(next)
	c.ring, c.ringStart = next, 0
}

// copyTail copies the tail, oldest byte first, into dst, which must hold it.
func (c *capture) copyTail(dst []byte) {
	if c.ringLen == 0 {
		return
	}
	n := copy(dst, c.ring[c.ringStart:min(c.ringStart+c.ringLen, len(c.ring))])
	copy(dst[n:], c.ring[:c.ringLen-n])
}

// streamTally is the account of a stream whose bytes are delivered as they arrive and so are not kept: it lets through the
// first limit bytes of the stream and counts the rest as dropped. The kept bytes are the beginning, whatever policy the request
// named: a stream cannot take back what it has sent.
type streamTally struct {
	limit     int
	total     uint64
	delivered int
	valid     utf8Tracker
}

// newStreamTally returns the account of a stream of limit bytes.
func newStreamTally(limit int) *streamTally { return &streamTally{limit: limit} }

// Take counts p, the next bytes of the stream, and returns the part of it to deliver: all of it while the cap allows, the
// bytes up to the cap when p crosses it and nothing after. The result is a prefix of p.
func (t *streamTally) Take(p []byte) []byte {
	t.total += uint64(len(p))
	n := min(len(p), t.limit-t.delivered)
	if n <= 0 {
		return nil
	}
	t.delivered += n
	t.valid.Write(p[:n])
	return p[:n]
}

// Lose counts n bytes that were lost before the SDK could read them.
func (t *streamTally) Lose(n uint64) { t.total += n }

// Account returns the account of what has been taken so far.
func (t *streamTally) Account() outputAccount {
	return outputAccount{
		Total: t.total, Kept: uint64(t.delivered), Dropped: t.total - uint64(t.delivered),
		Head: t.delivered, ValidUTF8: t.valid.Valid(),
	}
}

// utf8Tracker says whether the bytes it has been given, in as many writes as it takes, are valid UTF-8: what utf8.Valid says of
// all of them at once, without holding them. A sequence that a write leaves open is carried to the next, and one that is still
// open when the bytes end is not valid.
type utf8Tracker struct {
	invalid bool
	carry   [utf8.UTFMax]byte
	n       int // bytes of an open sequence held in carry
}

// Write takes the next bytes.
func (u *utf8Tracker) Write(p []byte) {
	if u.invalid {
		return
	}
	// Close the sequence the last write left open, a byte at a time: it is over as soon as the bytes form a rune, valid or not.
	for u.n > 0 && len(p) > 0 {
		u.carry[u.n] = p[0]
		u.n++
		p = p[1:]
		if utf8.FullRune(u.carry[:u.n]) {
			if r, size := utf8.DecodeRune(u.carry[:u.n]); r == utf8.RuneError && size <= 1 {
				u.invalid = true
				return
			}
			u.n = 0
		}
	}
	if len(p) == 0 {
		return
	}
	// A sequence that p leaves open at its end is held back for the next write; the rest must be valid now.
	open := openSequence(p)
	if !utf8.Valid(p[:len(p)-open]) {
		u.invalid = true
		return
	}
	u.n = copy(u.carry[:], p[len(p)-open:])
}

// Valid reports whether every byte written so far is valid UTF-8 and no sequence is left open.
func (u *utf8Tracker) Valid() bool { return !u.invalid && u.n == 0 }

// openSequence returns how many bytes at the end of p begin a UTF-8 sequence that p does not finish.
func openSequence(p []byte) int {
	for k := 1; k <= min(utf8.UTFMax-1, len(p)); k++ {
		if utf8.RuneStart(p[len(p)-k]) {
			if utf8.FullRune(p[len(p)-k:]) {
				return 0
			}
			return k
		}
	}
	return 0
}
