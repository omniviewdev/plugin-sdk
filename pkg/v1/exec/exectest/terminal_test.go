package exectest

import (
	"errors"
	"os"
	"testing"
)

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
		if _, writeErr := writer.Write([]byte("closed")); !errors.Is(writeErr, os.ErrClosed) {
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
