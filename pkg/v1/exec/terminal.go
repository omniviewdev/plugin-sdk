package exec

import "os"

// Terminal abstracts a PTY/TTY pair so tests can inject fakes.
type Terminal interface {
	// MasterFd returns the PTY (master) side — the manager reads output from here.
	MasterFd() *os.File
	// SlaveFd returns the TTY (slave) side — passed to the handler.
	SlaveFd() *os.File
	// Resize sets the terminal dimensions.
	Resize(rows, cols uint16) error
	// Close releases both file descriptors.
	Close() error
}

// EndMarker is implemented by a Terminal that can carry an end mark, which the manager uses to know that its
// reader has everything a process wrote. When the process behind a session is over, the manager has MarkEnd write
// a mark of its own after the output, and keeps the terminal open until its reader has met the mark: the end of the
// output is then delivered before the Close signal, whatever a kernel does to what is queued in a terminal
// that is closed. A Terminal that does not implement it is closed at once, and what its reader has not taken by
// then is lost. The terminals of the SDK and exectest.FakeTerminal implement it.
type EndMarker interface {
	// MarkEnd makes mark come out of MasterFd after everything that was written to the terminal for it before.
	// It may wait for room until the master is read, or the terminal is closed.
	MarkEnd(mark []byte) error
}

// TerminalFactory creates a new Terminal. The default factory opens a real
// PTY via creack/pty. Tests inject a factory that returns FakeTerminal.
type TerminalFactory func() (Terminal, error)
