//go:build unix

package exec_test

import (
	"os"
	"syscall"
	"testing"
	"time"

	"golang.org/x/term"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// A handler that reads the TTY until it closes, as a stdin copy does, must
// not keep CloseSession waiting for its goroutines.
func TestRealTerminal_CloseSessionInterruptsHandlerRead(t *testing.T) {
	handlerReadStarted := make(chan struct{})
	handlerReadReturned := make(chan struct{})
	handler := exec.Handler{
		Plugin:   "test",
		Resource: "pod",
		TTYHandler: func(
			_ *types.PluginContext,
			_ exec.SessionOptions,
			tty *os.File,
			_ chan error,
			_ <-chan exec.SessionResizeInput,
		) error {
			go func() {
				defer close(handlerReadReturned)
				close(handlerReadStarted)
				buf := make([]byte, 64)
				for {
					if _, err := tty.Read(buf); err != nil {
						return
					}
				}
			}()
			return nil
		},
	}
	h := exectest.Mount(t,
		exectest.WithHandler(handler),
		exectest.WithTerminalFactory(exec.NewRealTerminalFactory()),
	)

	sess, err := h.CreateSession(exec.SessionOptions{ResourcePlugin: "test", ResourceKey: "pod"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	<-handlerReadStarted
	time.Sleep(50 * time.Millisecond) // let the Read block

	start := time.Now()
	if err := h.CloseSession(sess.ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("CloseSession took %v", elapsed)
	}
	select {
	case <-handlerReadReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("handler read on the TTY did not return after CloseSession")
	}
}

// Close must interrupt a pending master Read even while another holder (for
// example a child process) keeps the TTY open, also after a Resize.
func TestRealTerminal_CloseInterruptsMasterReadWhileTTYHeldElsewhere(t *testing.T) {
	terminal, err := exec.NewRealTerminalFactory()()
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}

	if err := terminal.Resize(40, 120); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	assertWinsize(t, terminal.MasterFd(), 40, 120)

	otherTTYHolder := duplicateFd(t, terminal.SlaveFd())
	defer syscall.Close(otherTTYHolder)

	masterReadReturned := make(chan error, 1)
	go func() {
		_, err := terminal.MasterFd().Read(make([]byte, 64))
		masterReadReturned <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the Read block

	if err := terminal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-masterReadReturned:
		if err == nil {
			t.Fatal("master Read returned without an error after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("master Read did not return after Close")
	}
}

func assertWinsize(t *testing.T, file *os.File, wantRows, wantCols int) {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var cols, rows int
	var sizeErr error
	if err := raw.Control(func(fd uintptr) { cols, rows, sizeErr = term.GetSize(int(fd)) }); err != nil {
		t.Fatalf("Control: %v", err)
	}
	if sizeErr != nil {
		t.Fatalf("GetSize: %v", sizeErr)
	}
	if rows != wantRows || cols != wantCols {
		t.Fatalf("winsize = %dx%d, want %dx%d", rows, cols, wantRows, wantCols)
	}
}

func duplicateFd(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	duplicate, dupErr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) { duplicate, dupErr = syscall.Dup(int(fd)) }); err != nil {
		t.Fatalf("Control: %v", err)
	}
	if dupErr != nil {
		t.Fatalf("dup: %v", dupErr)
	}
	return duplicate
}
