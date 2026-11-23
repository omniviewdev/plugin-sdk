package exectest

import (
	"errors"
	"os"
	"testing"
	"time"
)

// A mark written to the terminal comes out of the master after the output injected before it, as it does from
// a real terminal: it is how the manager learns that everything a process wrote has been read.
func TestFakeTerminal_MarkEndComesOutOfTheMasterAfterWhatWasInjected(t *testing.T) {
	terminal, err := NewFakeTerminal()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	if _, err = terminal.InjectOutput([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err = terminal.MarkEnd([]byte("MARK")); err != nil {
		t.Fatalf("MarkEnd: %v", err)
	}
	var got []byte
	buf := make([]byte, 64)
	for len(got) < len("helloMARK") {
		_ = terminal.MasterFd().SetReadDeadline(time.Now().Add(5 * time.Second))
		n, readErr := terminal.MasterFd().Read(buf)
		got = append(got, buf[:n]...)
		if readErr != nil {
			t.Fatalf("reading the master: %v (%q so far)", readErr, got)
		}
	}
	if string(got) != "helloMARK" {
		t.Fatalf("the master gave %q, want the output and then the mark", got)
	}

	if err = terminal.Close(); err != nil {
		t.Fatal(err)
	}
	if err = terminal.MarkEnd([]byte("MARK")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("MarkEnd of a closed terminal: %v, want %v", err, os.ErrClosed)
	}
}

func TestFakeTerminal_CloseReportsErrorsAndClosesRemainingEnds(t *testing.T) {
	terminal, err := NewFakeTerminal()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := terminal.Close(); closeErr != nil {
			t.Errorf("cleanup: %v", closeErr)
		}
	})
	if err = terminal.masterR.Close(); err != nil {
		t.Fatal(err)
	}
	if err = terminal.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Close must report the previously closed descriptor: %v", err)
	}
	for _, writer := range []*os.File{terminal.masterW, terminal.slaveW} {
		if _, writeErr := writer.WriteString("closed"); !errors.Is(writeErr, os.ErrClosed) {
			t.Errorf("writer %s remains open: %v", writer.Name(), writeErr)
		}
	}
	if _, err = terminal.slaveR.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Close must still close the slave reader: %v", err)
	}
	if err = terminal.Close(); err != nil {
		t.Fatalf("repeated Close must be harmless: %v", err)
	}
}
