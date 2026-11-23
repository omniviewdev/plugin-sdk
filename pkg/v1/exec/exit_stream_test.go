package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	osexec "os/exec"
	"strconv"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// What an author does with the error a client library returns when the stream to the target ends: client-go's executor returns
// nil for a Success status (and for a status channel that closed empty, which it cannot tell apart from one) and a
// utilexec.CodeExitError for a non-zero status; ssh and os/exec have an error of their own with a status. ExitOfStreamError
// reads those shapes without importing any of the libraries.

// sshShaped has the shape of ssh.ExitError: a status, and nothing else.
type sshShaped struct{ status int }

func (e sshShaped) Error() string { return "Process exited with status " + strconv.Itoa(e.status) }

func (e sshShaped) ExitStatus() int { return e.status }

// osShaped has the shape of os/exec.ExitError (through its ProcessState): ExitCode, which is -1 for a process that a signal ended,
// and Exited.
type osShaped struct {
	code   int
	exited bool
}

func (e osShaped) Error() string { return "exit status " + strconv.Itoa(e.code) }

func (e osShaped) ExitCode() int { return e.code }

func (e osShaped) Exited() bool { return e.exited }

// bothShaped has the two methods, which disagree: ExitStatus is read first.
type bothShaped struct{}

func (bothShaped) Error() string { return "both" }

func (bothShaped) ExitStatus() int { return 5 }

func (bothShaped) ExitCode() int { return 6 }

// wantExit checks that exit is a status of source with the code.
func wantExit(t *testing.T, exit exec.Exit, ok bool, code int64, source exec.ExitSource) {
	t.Helper()
	if !ok {
		t.Fatalf("ExitOfStreamError says the error is no status; want %v with code %d", source, code)
	}
	if exit.Code == nil || *exit.Code != code || exit.Source != source {
		t.Errorf("exit = %+v, want the code %d from %v", exit, code, source)
	}
	if exit.Signal != "" || exit.SignalNumber != 0 || exit.Lost != exec.LostUnspecified {
		t.Errorf("exit = %+v claims a signal or a loss", exit)
	}
}

// A stream that ended with no error is what the transport says: it ended normally. It is no status of the process, because the
// API server sends the same Success for a command that was killed with its stream.
func TestExitOfStreamErrorNilIsTheTransportsNormalEndNeverTheProcessesExit0(t *testing.T) {
	exit, ok := exec.ExitOfStreamError(nil)
	wantExit(t, exit, ok, 0, exec.SourceTransport)
}

// A status that the error carries is the process's own, as the target relayed it, and no signal is read into its range.
func TestExitOfStreamErrorReadsTheStatusOfTheShapesOfClientLibraries(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int64
	}{
		{"client-go's CodeExitError", exectest.CodeExitError{Code: 3}, 3},
		{"client-go's CodeExitError of a killed command is a plain 137", exectest.CodeExitError{Code: 137}, 137},
		{"wrapped", fmt.Errorf("stream: %w", exectest.CodeExitError{Code: 2}), 2},
		{"wrapped twice", fmt.Errorf("a: %w", fmt.Errorf("b: %w", exectest.CodeExitError{Code: 126})), 126},
		{"joined", errors.Join(errors.New("first"), exectest.CodeExitError{Code: 9}), 9},
		{"ssh's ExitError", sshShaped{status: 255}, 255},
		{"os/exec's ExitError", osShaped{code: 4, exited: true}, 4},
		{"a Windows status, which stays as it is", exectest.CodeExitError{Code: -1073741819}, -1073741819},
		{"ExitStatus is read before ExitCode", bothShaped{}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exit, ok := exec.ExitOfStreamError(tc.err)
			wantExit(t, exit, ok, tc.code, exec.SourceProcess)
		})
	}
}

// An error that is no status of the process is not turned into one: the author classifies it, or says the stream broke.
func TestExitOfStreamErrorLeavesWhatIsNoStatusToTheAuthor(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a plain error", errors.New("boom")},
		{"a cancelled context", context.Canceled},
		{"an unexpected end of the stream", io.ErrUnexpectedEOF},
		{"a network error", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}},
		{"the upgrade's refusal, which client-go flattens into text", errors.New(`unable to upgrade connection: pods "x" is forbidden: User "u" cannot create resource "pods/exec"`)},
		{"os/exec's ExitError of a process that a signal ended (ExitCode is -1)", osShaped{code: -1, exited: false}},
		{"wrapped, a process that a signal ended", fmt.Errorf("run: %w", osShaped{code: -1, exited: false})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exit, ok := exec.ExitOfStreamError(tc.err)
			if ok {
				t.Errorf("ExitOfStreamError(%v) = %+v, true; want false: it is no status", tc.err, exit)
			}
			if exit.Code != nil || exit.Lost != exec.LostUnspecified || exit.Signal != "" {
				t.Errorf("exit = %+v with ok false, want the empty Exit", exit)
			}
		})
	}
}

// exitHelperEnv names the status the helper process exits with.
const exitHelperEnv = "EXEC_SDK_EXIT_STREAM_HELPER"

// TestExitOfStreamErrorHelperProcess is the process of the test below: it exits with the status its environment names, and is
// no test of its own.
func TestExitOfStreamErrorHelperProcess(t *testing.T) {
	if code := os.Getenv(exitHelperEnv); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			os.Exit(125)
		}
		os.Exit(n)
	}
}

// The real error of os/exec, which is what a plugin that runs a local process has, has the shape: its ExitCode comes from its
// ProcessState, which the shape test above can only imitate.
func TestExitOfStreamErrorReadsARealOSExecExitError(t *testing.T) {
	cmd := osexec.Command(os.Args[0], "-test.run=^TestExitOfStreamErrorHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), exitHelperEnv+"=3")
	err := cmd.Run()
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the helper process ended with %v, want an *os/exec.ExitError", err)
	}
	exit, ok := exec.ExitOfStreamError(err)
	wantExit(t, exit, ok, 3, exec.SourceProcess)
	exit, ok = exec.ExitOfStreamError(fmt.Errorf("wait: %w", err))
	wantExit(t, exit, ok, 3, exec.SourceProcess)
}

// The test double has client-go's shape and its message, so that an author unit-tests the mapping of the error of their executor.
func TestCodeExitErrorHasTheShapeAndTheMessageOfClientGosCodeExitError(t *testing.T) {
	var err error = exectest.CodeExitError{Code: 7}
	if got, want := err.Error(), "command terminated with exit code 7"; got != want {
		t.Errorf("Error() = %q, want %q: the message client-go gives a command that exited with a status", got, want)
	}
	shape, ok := err.(interface {
		String() string
		Exited() bool
		ExitStatus() int
	})
	if !ok {
		t.Fatal("CodeExitError lacks String, Exited or ExitStatus: client-go's utilexec.ExitError has all three")
	}
	if shape.ExitStatus() != 7 || !shape.Exited() || shape.String() != err.Error() {
		t.Errorf("ExitStatus %d, Exited %v, String %q; want 7, true and the message", shape.ExitStatus(), shape.Exited(), shape.String())
	}
	var target exectest.CodeExitError
	if !errors.As(fmt.Errorf("wrapped: %w", err), &target) || target.Code != 7 {
		t.Errorf("errors.As finds %+v, want the CodeExitError with code 7", target)
	}
	exit, ok := exec.ExitOfStreamError(err)
	wantExit(t, exit, ok, 7, exec.SourceProcess)
}
