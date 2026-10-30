//go:build unix

package exec

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// realTerminal wraps a real PTY/TTY pair from creack/pty.
//
// Both files stay in Go's poller so that Close interrupts a pending Read on
// either side. Code here must never call Fd() on them: Fd() switches the
// descriptor to blocking mode, and Close of a blocking *os.File waits for a
// pending Read to return. Such a Read on the TTY (a handler's stdin copy)
// only returns on input, and one on the PTY only when every TTY holder
// closes, so CloseSession would time out.
type realTerminal struct {
	master *os.File // pty side
	slave  *os.File // tty side
}

var _ Terminal = (*realTerminal)(nil)

func (t *realTerminal) MasterFd() *os.File { return t.master }
func (t *realTerminal) SlaveFd() *os.File  { return t.slave }

func (t *realTerminal) Resize(rows, cols uint16) error {
	return controlFd(t.master, func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	})
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
		// pty.Open's ioctls call Fd() on the master, which leaves it blocking.
		blockingMaster, slave, err := pty.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open pty: %w", err)
		}
		master, err := pollableDuplicate(blockingMaster)
		err = errors.Join(err, blockingMaster.Close())
		if err != nil {
			cleanupErr := slave.Close()
			if master != nil {
				cleanupErr = errors.Join(cleanupErr, master.Close())
			}
			return nil, errors.Join(fmt.Errorf("failed to make pty pollable: %w", err), cleanupErr)
		}
		terminal := &realTerminal{master: master, slave: slave}

		// Set slave TTY to raw mode to disable local echo. This makes the PTY a
		// transparent byte pipe — only the remote side (e.g. bash in a K8s pod)
		// will echo input, preventing the double-echo bug.
		if err = controlFd(slave, func(fd int) error {
			_, err := term.MakeRaw(fd)
			return err
		}); err != nil {
			cleanupErr := errors.Join(slave.Close(), master.Close())
			return nil, errors.Join(fmt.Errorf("failed to set tty to raw mode: %w", err), cleanupErr)
		}

		if err = terminal.Resize(InitialRows, InitialCols); err != nil {
			cleanupErr := errors.Join(slave.Close(), master.Close())
			return nil, errors.Join(fmt.Errorf("failed to set initial pty size: %w", err), cleanupErr)
		}

		return terminal, nil
	}
}

// controlFd runs operation on the file's descriptor without Fd(), so the
// file keeps its poller registration and non-blocking mode.
func controlFd(file *os.File, operation func(fd int) error) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var operationErr error
	if err := raw.Control(func(fd uintptr) { operationErr = operation(int(fd)) }); err != nil {
		return err
	}
	return operationErr
}

// pollableDuplicate returns a non-blocking, close-on-exec duplicate of file
// that lives in Go's poller. O_NONBLOCK belongs to the open file
// description, so it also applies to file itself.
func pollableDuplicate(file *os.File) (*os.File, error) {
	duplicate := -1
	err := controlFd(file, func(fd int) error {
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		var dupErr error
		if duplicate, dupErr = syscall.Dup(fd); dupErr != nil {
			return dupErr
		}
		syscall.CloseOnExec(duplicate)
		return nil
	})
	if err != nil {
		if duplicate >= 0 {
			err = errors.Join(err, syscall.Close(duplicate))
		}
		return nil, err
	}
	if err := syscall.SetNonblock(duplicate, true); err != nil {
		return nil, errors.Join(err, syscall.Close(duplicate))
	}
	return os.NewFile(uintptr(duplicate), file.Name()), nil
}
