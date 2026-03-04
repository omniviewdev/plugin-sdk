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

// TerminalFactory creates a new Terminal. The default factory opens a real
// PTY via creack/pty. Tests inject a factory that returns FakeTerminal.
type TerminalFactory func() (Terminal, error)
