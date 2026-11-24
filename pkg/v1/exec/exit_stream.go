package exec

import (
	"errors"
	"io"
	"sync/atomic"
)

// This file holds the two helpers an author needs to write an ExecFunc over a client library that ends a stream with an error and
// reports nothing else: what a stream's end means (ExitOfStreamError), and the one honest moment a quiet command's start can be
// reported at (OnFirstRead). Neither imports a client library: they read the shapes of its errors and readers.

// statusError is the shape of an error that carries a status: client-go's CodeExitError and ssh.ExitError have ExitStatus.
type statusError interface{ ExitStatus() int }

// codeError is the shape of an error that carries a status as os/exec.ExitError does, through its ProcessState: ExitCode.
type codeError interface{ ExitCode() int }

// exitedError is the shape of an error that says whether the process exited: client-go's ExitError and os/exec.ExitError have Exited,
// which is false for a process that a signal ended (its ExitCode is then -1).
type exitedError interface{ Exited() bool }

// ExitOfStreamError says how a command ended from the error that a client library returned when the stream to the target ended, in
// the shapes of client-go's executor, ssh and os/exec, without importing any of them.
//
//   - nil is the transport's normal end: ExitTransport(0). client-go returns nil for a Success status and for a status channel that
//     closed with nothing on it, which it cannot tell apart, and the API server sends the same Success for a command that did not
//     exit 0 (after a kubelet or containerd restart the command is gone and the status says "Success"): the zero is the transport's
//     word for "the stream ended normally", never the process's own status. The end carries the start as observed and leaves
//     remote_may_survive absent (unknown), and a consumer shows "ended normally", never "exit 0".
//   - an error in the chain that has an ExitStatus() int method (client-go's utilexec.CodeExitError, ssh.ExitError) or an ExitCode()
//     int method (os/exec.ExitError) is the status the target relayed: ExitCode(n), whose source is the process. ExitStatus is read
//     before ExitCode. An error that also has Exited() bool and says false (os/exec.ExitError of a process that a signal ended, whose
//     ExitCode is -1) is no status. Nothing is inferred from the number: 137 is a plain 137, and a library that folds a signal into
//     128+n (ssh does) is read as the code it gives; an author who knows more (ssh.ExitError.Signal) builds the Exit itself.
//   - anything else is not a status: ok is false and the Exit is empty. The author classifies the error (return it, and Classify
//     or ErrorFromHTTPStatus types it) or, for a stream that broke while it was read, returns ExitLost.
//
// What the SDK cannot do is read the status channel of client-go's executor: remotecommand.Executor has no hook for it, so a Success
// status and an empty channel are one nil. An author who owns the WebSocket (the v5 channel protocol: a status message on channel 3)
// reads the status itself, and can tell an empty channel (ExitLost) from a Success (this function's nil).
func ExitOfStreamError(err error) (exit Exit, ok bool) {
	if err == nil {
		return ExitTransport(0), true
	}
	var (
		code int
		from any
	)
	if status := new(statusError); errors.As(err, status) {
		code, from = (*status).ExitStatus(), *status
	} else if coded := new(codeError); errors.As(err, coded) {
		code, from = (*coded).ExitCode(), *coded
	} else {
		return Exit{}, false
	}
	if exited, has := from.(exitedError); has && !exited.Exited() {
		return Exit{}, false
	}
	return ExitCode(code), true
}

// OnFirstRead returns a reader that reads what r reads and calls fn once, on the goroutine of the first Read and before that Read
// reaches r. It is the honest trigger of Proc.Started for a command that is quiet.
//
// A command's start is observed in three ways: the first byte it writes (the SDK reports the start itself, from the facts of Opened),
// the status it ends with, and the first Read that the executor makes of the stdin it was handed. client-go reads its stdin only once
// its streams exist, so for a quiet command that is the one moment before the first byte or the status that says "the stream is up":
//
//	stdin := exec.OnFirstRead(p.Stdin, func() { p.Started(exec.SessionFacts{Transport: "k8s-ws-v5"}) })
//	err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: p.Stdout, Stderr: p.Stderr})
//
// Calling Started before the stream exists would make a refusal (a 403, a container that is not running) a failure at stage RUN for a
// command that never ran, so the call belongs to the first Read and no earlier. A failure the runtime reports on the status channel (it
// cannot find the executable) arrives after the stream exists, and is a RUN failure for that reason.
//
// fn is called at most once, however many goroutines read, and a Read never waits for it: a second reader, or one that comes while fn
// is still running, reads r at once. The reads themselves are r's, untouched. If Started loses a race with the first byte of output
// (the executor's output goroutine wrote first) the SDK has sent its own Started already, and the late report is refused and logged.
//
// A nil r (a bounded run whose request carries no stdin) returns nil, and fn is never called: an executor reads a nil Stdin as "no
// input" and opens no stream for it, and such a command has the status or its first byte for its start. A nil fn returns r.
func OnFirstRead(r io.Reader, fn func()) io.Reader {
	if r == nil {
		return nil
	}
	if fn == nil {
		return r
	}
	return &firstRead{r: r, fn: fn}
}

// firstRead is the reader of OnFirstRead.
type firstRead struct {
	r     io.Reader
	fn    func()
	fired atomic.Bool
}

func (f *firstRead) Read(p []byte) (int, error) {
	if f.fired.CompareAndSwap(false, true) {
		f.fn()
	}
	return f.r.Read(p)
}
