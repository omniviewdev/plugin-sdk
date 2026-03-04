package exec

import (
	"fmt"
	"os"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// realTerminal wraps a real PTY/TTY pair from creack/pty.
type realTerminal struct {
	master *os.File // pty side
	slave  *os.File // tty side
}

var _ Terminal = (*realTerminal)(nil)

func (t *realTerminal) MasterFd() *os.File { return t.master }
func (t *realTerminal) SlaveFd() *os.File  { return t.slave }

func (t *realTerminal) Resize(rows, cols uint16) error {
	return pty.Setsize(t.master, &pty.Winsize{Rows: rows, Cols: cols})
}

func (t *realTerminal) Close() error {
	var firstErr error
	if err := t.slave.Close(); err != nil {
		firstErr = err
	}
	if err := t.master.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// NewRealTerminalFactory returns a TerminalFactory that creates real PTY terminals.
// The slave is set to raw mode and the terminal is sized to the given initial dimensions.
func NewRealTerminalFactory() TerminalFactory {
	return func() (Terminal, error) {
		master, slave, err := pty.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open pty: %w", err)
		}

		// Set slave TTY to raw mode to disable local echo. This makes the PTY a
		// transparent byte pipe — only the remote side (e.g. bash in a K8s pod)
		// will echo input, preventing the double-echo bug.
		if _, err = term.MakeRaw(int(slave.Fd())); err != nil {
			slave.Close()
			master.Close()
			return nil, fmt.Errorf("failed to set tty to raw mode: %w", err)
		}

		if err = pty.Setsize(master, &pty.Winsize{Rows: InitialRows, Cols: InitialCols}); err != nil {
			slave.Close()
			master.Close()
			return nil, fmt.Errorf("failed to set initial pty size: %w", err)
		}

		return &realTerminal{master: master, slave: slave}, nil
	}
}
