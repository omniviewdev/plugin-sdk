package exectest

import (
	"os"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// ResizeRecord captures a resize call.
type ResizeRecord struct {
	Rows uint16
	Cols uint16
}

// FakeTerminal implements exec.Terminal using os.Pipe pairs.
// This allows tests to inject data into the master side and read from the
// slave side without requiring real PTY support.
type FakeTerminal struct {
	masterR *os.File // read end of master pipe (manager reads output from here)
	masterW *os.File // write end of master pipe (test writes data to simulate PTY output)
	slaveR  *os.File // read end of slave pipe (handler reads from here)
	slaveW  *os.File // write end of slave pipe (test writes to simulate input to handler)

	mu      sync.Mutex
	resizes []ResizeRecord
	closed  bool
}

var _ exec.Terminal = (*FakeTerminal)(nil)

// NewFakeTerminal creates a FakeTerminal backed by two os.Pipe pairs.
func NewFakeTerminal() (*FakeTerminal, error) {
	masterR, masterW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	slaveR, slaveW, err := os.Pipe()
	if err != nil {
		masterR.Close()
		masterW.Close()
		return nil, err
	}
	return &FakeTerminal{
		masterR: masterR,
		masterW: masterW,
		slaveR:  slaveR,
		slaveW:  slaveW,
	}, nil
}

func (t *FakeTerminal) MasterFd() *os.File { return t.masterR }
func (t *FakeTerminal) SlaveFd() *os.File  { return t.slaveR }

func (t *FakeTerminal) Resize(rows, cols uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resizes = append(t.resizes, ResizeRecord{Rows: rows, Cols: cols})
	return nil
}

func (t *FakeTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	t.masterR.Close()
	t.masterW.Close()
	t.slaveR.Close()
	t.slaveW.Close()
	return nil
}

// InjectOutput writes data to the master write end, simulating PTY output
// that the manager will read from MasterFd().
func (t *FakeTerminal) InjectOutput(data []byte) (int, error) {
	return t.masterW.Write(data)
}

// Resizes returns a copy of all resize records.
func (t *FakeTerminal) Resizes() []ResizeRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]ResizeRecord, len(t.resizes))
	copy(cp, t.resizes)
	return cp
}

// FakeTerminalFactory returns an exec.TerminalFactory that creates the given
// FakeTerminal on the first call and returns an error on subsequent calls.
// If ft is nil, the factory always returns (nil, os.ErrClosed).
func FakeTerminalFactory(ft *FakeTerminal) exec.TerminalFactory {
	if ft == nil {
		return func() (exec.Terminal, error) {
			return nil, os.ErrClosed
		}
	}
	var once sync.Once
	return func() (exec.Terminal, error) {
		var used bool
		once.Do(func() { used = true })
		if !used {
			return nil, os.ErrClosed
		}
		return ft, nil
	}
}

// NewFakeTerminalFactory returns a factory that creates a fresh FakeTerminal
// each time it is called.
func NewFakeTerminalFactory() exec.TerminalFactory {
	return func() (exec.Terminal, error) {
		return NewFakeTerminal()
	}
}
