package exec_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"testing/synctest"

	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Stdin EOF on the wire: ClientFrame.close_input half-closes the command's input, after every byte the host sent before it (the
// frames of a session are ordered), for a target whose effective Capabilities.StdinEOF is set. A target that cannot, and an input
// frame after the EOF, are the host breaking the contract it was told in Opened: the stream ends with a typed status and the
// session with it, as every violation does.

// readAllThenHold is a function that reads its input to EOF, hands what it read to read, and waits for its session to end.
func readAllThenHold(read chan<- []byte) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		data, _ := io.ReadAll(p.Stdin)
		read <- data
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}
}

// framesIgnored reads the count of frames of a kind the engine ignored, from the process's diagnostics.
func framesIgnored(kind string) float64 {
	metrics, _ := sdkdiag.Default().Snapshot()
	for _, m := range metrics {
		if m.GetName() != "omniview.plugin.self.sdk.exec.frames.ignored" {
			continue
		}
		for _, p := range m.GetPoints() {
			for _, a := range p.GetAttrs() {
				if a.GetValue() == kind {
					return p.GetValue()
				}
			}
		}
	}
	return 0
}

// The function's input ends after the bytes that were sent before close_input, and the frame is served, not ignored: the session
// carries on, and ends the way the function ends it.
func TestCloseInputEndsTheFunctionsInputAfterEveryByteSentBeforeIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		read := make(chan []byte, 1)
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			data, _ := io.ReadAll(p.Stdin)
			read <- data
			return exec.ExitCode(0), nil
		}))
		before := framesIgnored("close_input")
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlInput("abc"), ctlInput("def"), ctlCloseInput())
		synctest.Wait()
		select {
		case data := <-read:
			if string(data) != "abcdef" {
				t.Errorf("the function read %q before EOF, want everything the host sent before close_input", data)
			}
		default:
			t.Fatal("the function's input did not end: close_input was not served")
		}
		if returned, err := host.ctlStatus(); !returned || err != nil {
			t.Errorf("the stream ended with %v (returned %v), want a clean end after Ended", err, returned)
		}
		ended := ctlEnded(host.ctlFrames())
		if ended.GetEnd().GetExited().GetExitCode() != 0 || ended.GetBytesIn() != 6 {
			t.Errorf("Ended %v, want the function's own exit 0 after 6 bytes in", ended)
		}
		if got := framesIgnored("close_input"); got != before {
			t.Errorf("%v close_input frames counted as ignored: the frame is served", got-before)
		}
	})
}

// A close_input that comes behind more input than the queue holds waits for the function to read it, as every frame waits behind the
// one before it, and the EOF the function then reads is after all of it: 300 KiB are sent before the EOF and 300 KiB are read before it.
func TestCloseInputBehindMoreInputThanTheQueueHoldsEndsTheInputAfterAllOfIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const frame, frames = 60 << 10, 5
		gate, read := ctlGate(t), make(chan []byte, 1)
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			<-gate.Opened()
			return readAllThenHold(read)(ctx, p)
		}))
		host := rig.ctlPlainSession("s1")
		var sent []byte
		for i := range frames {
			chunk := bytes.Repeat([]byte{byte('a' + i)}, frame)
			sent = append(sent, chunk...)
			host.ctlSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: chunk}})
		}
		host.ctlSend(ctlCloseInput())
		synctest.Wait() // the function reads nothing: the receiver is held in the fifth frame, and the EOF is behind it
		select {
		case <-read:
			t.Fatal("the function read to EOF before it had been given the input")
		default:
		}
		gate.Open()
		synctest.Wait()
		select {
		case data := <-read:
			if !bytes.Equal(data, sent) {
				t.Errorf("the function read %d bytes before EOF, want the %d the host sent before close_input, in order", len(data), len(sent))
			}
		default:
			t.Fatal("the function's input did not end after it read everything")
		}
	})
}

// close_input is idempotent: a second one is the same EOF, and not a violation.
func TestCloseInputTwiceIsHarmless(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		read := make(chan []byte, 1)
		rig := newMemoryRig(t, engTarget(readAllThenHold(read)))
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlInput("x"), ctlCloseInput(), ctlCloseInput())
		synctest.Wait()
		select {
		case data := <-read:
			if string(data) != "x" {
				t.Errorf("the function read %q, want %q", data, "x")
			}
		default:
			t.Fatal("the function's input did not end")
		}
		if returned, err := host.ctlStatus(); returned {
			t.Errorf("the stream ended (%v) for a second close_input: it is the same EOF", err)
		}
		host.ctlSend(ctlClose(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		synctest.Wait()
		if ended := ctlEnded(host.ctlFrames()); ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Errorf("Ended %v after a close, want closed by request: the session was alive", ended)
		}
	})
}

// An input frame after close_input, empty or not, ends the stream INVALID_REQUEST and the session with it; the bytes of the frame never
// reach the function, and what it read before the EOF is what the host sent before it.
func TestAnInputFrameAfterCloseInputEndsTheStreamInvalidRequestAndTheSession(t *testing.T) {
	for name, data := range map[string]string{"with bytes": "late", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				read := make(chan []byte, 1)
				rig := newMemoryRig(t, engTarget(readAllThenHold(read)))
				host := rig.ctlPlainSession("s1")
				host.ctlSend(ctlInput("early"), ctlCloseInput(), ctlInput(data))
				synctest.Wait()
				returned, err := host.ctlStatus()
				if !returned {
					t.Fatal("the stream did not end for an input frame after close_input")
				}
				if class, _ := ctlClass(err); class != "INVALID_REQUEST" {
					t.Errorf("the stream ended with %v (class %q), want INVALID_REQUEST", err, class)
				}
				select {
				case got := <-read:
					// A violation ends the session, and what it had queued and the function had not read goes with it: the function
					// reads some of what was sent before close_input, or all of it, and nothing of the frame that came after.
					if !strings.HasPrefix("early", string(got)) {
						t.Errorf("the function read %q, want at most what was sent before close_input", got)
					}
				default:
					t.Error("the function's input did not end")
				}
				if ended := ctlEnded(host.ctlFrames()); ended != nil {
					t.Errorf("a stream that ended with a status was sent an Ended: %v", ended)
				}
			})
		})
	}
}

// close_input for a target that declared no StdinEOF is a refusal the host was told of in Opened: UNSUPPORTED, ending the stream and the
// session, and the function's input ends with the session and not before.
func TestCloseInputOnATargetWithoutStdinEOFEndsTheStreamUnsupportedAndTheSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cancelled, read := make(chan struct{}), make(chan []byte, 1)
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			data, _ := io.ReadAll(p.Stdin)
			read <- data
			<-ctx.Done()
			close(cancelled)
			return exec.Exit{}, ctx.Err()
		}, func(c *exec.Capabilities) { c.StdinEOF = false }))
		host := rig.ctlPlainSession("s1")
		host.ctlSend(ctlInput("abc"), ctlCloseInput())
		synctest.Wait()
		returned, err := host.ctlStatus()
		if !returned {
			t.Fatal("the stream did not end for a close_input the target cannot honor")
		}
		if class, _ := ctlClass(err); class != "UNSUPPORTED" {
			t.Errorf("the stream ended with %v (class %q), want UNSUPPORTED", err, class)
		}
		select {
		case <-cancelled:
		default:
			t.Error("the session did not end with its stream")
		}
	})
}

// A session that narrows its capabilities to no StdinEOF is held to it from then on: the effective set decides.
func TestCloseInputAfterTheSessionNarrowedItsStdinEOFEndsTheStreamUnsupported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			narrowed := p.Capabilities
			narrowed.StdinEOF = false
			p.Started(exec.SessionFacts{Capabilities: &narrowed})
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}))
		host := rig.ctlPlainSession("s1")
		synctest.Wait()
		host.ctlSend(ctlCloseInput())
		synctest.Wait()
		returned, err := host.ctlStatus()
		if !returned {
			t.Fatal("the stream did not end for a close_input the narrowed session cannot honor")
		}
		if class, _ := ctlClass(err); class != "UNSUPPORTED" {
			t.Errorf("the stream ended with %v (class %q), want UNSUPPORTED", err, class)
		}
	})
}
