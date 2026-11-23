package exec

import (
	"bytes"
	"crypto/rand"
)

// newEndMark returns the end mark of a session: a string that nothing else has, which the manager writes to
// a terminal after everything the process behind it wrote, so that the reader knows that it has read
// all of it (see Manager.drainTerminal). It is made when the session ends, and nobody sees it but the manager.
// Its characters are upper-case letters and digits, which no setting of a terminal changes on its way.
func newEndMark() []byte { return []byte(rand.Text()) }

// markScanner finds an end mark in the output of a terminal that comes in chunks of any size. A mark can
// be cut between two chunks, so the end of a chunk that could be the beginning of the mark is held back
// until the next chunk says what it is. If the output ends first, what is held back is dropped: the mark is
// written whole, so it is the beginning of the mark, which the close of the terminal cut off, and not output
// of the process. (Output that ends like the beginning of the mark, in that case alone, loses fewer bytes than
// the mark has.)
type markScanner struct {
	held []byte
}

// feed takes the next chunk of the output and returns what can be delivered now. Without a mark that is the
// chunk. With one it is everything but the longest end of the output that is also a beginning of the mark,
// or, when the mark has come, everything before it (found is then true, and the output ends there). The
// returned bytes may be the chunk's own: a caller that keeps them copies them.
func (s *markScanner) feed(mark, chunk []byte) (out []byte, found bool) {
	if mark == nil && len(s.held) == 0 {
		return chunk, false
	}
	buf := chunk
	if len(s.held) > 0 {
		buf = append(s.held, chunk...)
		s.held = nil
	}
	if mark == nil {
		return buf, false
	}
	if at := bytes.Index(buf, mark); at >= 0 {
		return buf[:at], true
	}
	hold := 0
	for n := min(len(mark)-1, len(buf)); n > 0; n-- {
		if bytes.HasSuffix(buf, mark[:n]) {
			hold = n
			break
		}
	}
	s.held = bytes.Clone(buf[len(buf)-hold:])
	return buf[:len(buf)-hold], false
}
