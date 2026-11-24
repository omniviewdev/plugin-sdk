package exec_test

import (
	"bytes"
	"context"
	"io"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Signals on the wire: ClientFrame.signal is an explicit action ("Stop", "Kill"), delivered to the function through Proc.Signals, in
// the order of the host's frames, and only for the signals the session's effective Capabilities.Signals lists, which the host read in
// Opened (and in Started, when it narrowed them). Another is the host breaking that contract: UNSUPPORTED, ending the stream and the
// session. Ctrl+C in a terminal is the byte 0x03 as input and is none of this.

// signalReader is a function that hands every signal of Proc.Signals to got until its session ends. It reads until the channel is
// closed, which is the end of the session, and not until its context ends: what the SDK accepted before the end is in the closed channel
// to the last signal, and a select over the channel and the context chooses at random when both are ready, so that a function that has
// not reached its select yet when a signal and the end of the session come (the scheduler ran the host's frames first, as it did on
// Windows) loses the signal to its context half of the time. That loss is the function's choice and never the SDK's, and this reader
// makes the other (see open_control_end_test.go, which holds the function before its first read to force that order).
func signalReader(got chan<- exec.Signal) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for sig := range p.Signals {
			got <- sig
		}
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}
}

// drainSignals returns the signals that were handed over and nobody has read, and does not wait for more.
func drainSignals(got <-chan exec.Signal) []exec.Signal {
	var out []exec.Signal
	for {
		select {
		case sig := <-got:
			out = append(out, sig)
		default:
			return out
		}
	}
}

// signalsDropped reads the count of signals the engine dropped because the function did not read its queue.
func signalsDropped() float64 {
	metrics, _ := sdkdiag.Default().Snapshot()
	for _, m := range metrics {
		if m.GetName() != "omniview.plugin.self.sdk.exec.signals.dropped" {
			continue
		}
		var total float64
		for _, p := range m.GetPoints() {
			total += p.GetValue()
		}
		return total
	}
	return 0
}

// A signal that the session declares reaches the function, in the order the host sent it, and a repeated one is delivered each time.
func TestADeclaredSignalReachesTheFunctionInTheOrderTheHostSentIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got := make(chan exec.Signal, 16)
		rig := newMemoryRig(t, engTarget(signalReader(got)))
		before := framesIgnored("signal")
		host := rig.ctlPlainSession("s1")
		host.ctlSend(
			ctlSignal(execv2.Signal_SIGNAL_INT), ctlSignal(execv2.Signal_SIGNAL_TERM), ctlSignal(execv2.Signal_SIGNAL_INT),
			ctlSignal(execv2.Signal_SIGNAL_KILL), ctlSignal(execv2.Signal_SIGNAL_TERM),
		)
		synctest.Wait()
		want := []exec.Signal{exec.SignalInt, exec.SignalTerm, exec.SignalInt, exec.SignalKill, exec.SignalTerm}
		if signals := drainSignals(got); !slices.Equal(signals, want) {
			t.Errorf("the function was handed %v, want %v", signals, want)
		}
		if returned, err := host.ctlStatus(); returned {
			t.Errorf("the stream ended (%v) for signals the session declares", err)
		}
		if n := framesIgnored("signal"); n != before {
			t.Errorf("%v signal frames counted as ignored: the frame is served", n-before)
		}
	})
}

// A signal sent after input does not overtake it: the host's frames are applied in the order they were sent, so a signal waits behind
// an input frame that waits for the function to read, and is delivered once the function has read that input.
func TestASignalSentAfterInputWaitsBehindItAsEveryFrameDoes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const frame, frames = 60 << 10, 5 // 300 KiB: more than the 256 KiB the command's input queue holds
		const input = frame * frames
		gate, got, readAll := ctlGate(t), make(chan exec.Signal, 4), make(chan int, 1)
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			<-gate.Opened()
			n, err := io.CopyN(io.Discard, p.Stdin, input)
			if err != nil {
				return exec.Exit{}, err
			}
			readAll <- int(n)
			return signalReader(got)(ctx, p)
		}))
		host := rig.ctlPlainSession("s1")
		for range frames {
			host.ctlSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: bytes.Repeat([]byte{'x'}, frame)}})
		}
		host.ctlSend(ctlSignal(execv2.Signal_SIGNAL_INT))
		synctest.Wait() // the receiver is held in the last input frame, and the signal is behind it
		if signals := drainSignals(got); len(signals) != 0 {
			t.Fatalf("the function was handed %v before it had read the input that was sent before the signal", signals)
		}
		gate.Open()
		synctest.Wait()
		if n := <-readAll; n != input {
			t.Errorf("the function read %d bytes of the input", n)
		}
		if signals := drainSignals(got); !slices.Equal(signals, []exec.Signal{exec.SignalInt}) {
			t.Errorf("the function was handed %v after the input, want the signal", signals)
		}
	})
}

// A signal the session does not declare is the host breaking what Opened told it: UNSUPPORTED, the stream ends, the session with it, and
// the signal reaches nobody; a declared signal sent before it was accepted, and is delivered: it is in the function's queue before the
// session ends and stays there, and the function reads until its channel is closed (signalReader), so that it finds the signal whether it
// had reached its read when the frames came or not. An unspecified signal and a number this build does not know are not declared either,
// and a target that declares no signals declares none.
func TestASignalTheSessionDoesNotDeclareEndsTheStreamUnsupportedAndTheSession(t *testing.T) {
	cases := []struct {
		name  string
		sig   execv2.Signal
		caps  func(*exec.Capabilities)
		first bool // a declared signal is sent first
	}{
		{"a signal of the set that is not declared", execv2.Signal_SIGNAL_HUP, func(*exec.Capabilities) {}, true},
		{"an unspecified signal", execv2.Signal_SIGNAL_UNSPECIFIED, func(*exec.Capabilities) {}, true},
		{"a number this build does not know", execv2.Signal(99), func(*exec.Capabilities) {}, true},
		{"a signal for a target that declares none", execv2.Signal_SIGNAL_INT, func(c *exec.Capabilities) { c.Signals = nil }, false},
		{"a signal for a target that declares an empty set", execv2.Signal_SIGNAL_TERM, func(c *exec.Capabilities) { c.Signals = []exec.Signal{} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				got, cancelled := make(chan exec.Signal, 4), make(chan struct{})
				rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
					defer close(cancelled)
					return signalReader(got)(ctx, p)
				}, tc.caps))
				host := rig.ctlPlainSession("s1")
				if tc.first {
					host.ctlSend(ctlSignal(execv2.Signal_SIGNAL_INT))
				}
				host.ctlSend(ctlSignal(tc.sig))
				synctest.Wait()
				returned, err := host.ctlStatus()
				if !returned {
					t.Fatal("the stream did not end for a signal the session does not declare")
				}
				if class, _ := ctlClass(err); class != "UNSUPPORTED" {
					t.Errorf("the stream ended with %v (class %q), want UNSUPPORTED", err, class)
				}
				select {
				case <-cancelled:
				default:
					t.Error("the session did not end with its stream")
				}
				want := []exec.Signal(nil)
				if tc.first {
					want = []exec.Signal{exec.SignalInt}
				}
				if signals := drainSignals(got); !slices.Equal(signals, want) {
					t.Errorf("the function was handed %v, want %v: a signal that is not declared reaches nobody", signals, want)
				}
				if ended := ctlEnded(host.ctlFrames()); ended != nil {
					t.Errorf("a stream that ended with a status was sent an Ended: %v", ended)
				}
			})
		})
	}
}

// What the session narrows its signals to is what the host may send from then on: the effective set decides.
func TestASignalAfterTheSessionNarrowedItsSignalsFollowsTheEffectiveSet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got := make(chan exec.Signal, 4)
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			narrowed := p.Capabilities
			narrowed.Signals = []exec.Signal{exec.SignalInt}
			p.Started(exec.SessionFacts{Capabilities: &narrowed})
			return signalReader(got)(ctx, p)
		}))
		host := rig.ctlPlainSession("s1")
		synctest.Wait()
		host.ctlSend(ctlSignal(execv2.Signal_SIGNAL_INT))
		synctest.Wait()
		if signals := drainSignals(got); !slices.Equal(signals, []exec.Signal{exec.SignalInt}) {
			t.Fatalf("the function was handed %v for the one signal that is still declared", signals)
		}
		host.ctlSend(ctlSignal(execv2.Signal_SIGNAL_TERM)) // declared by the kind and by Opened, narrowed away at Started
		synctest.Wait()
		returned, err := host.ctlStatus()
		if !returned {
			t.Fatal("the stream did not end for a signal the session narrowed away")
		}
		if class, _ := ctlClass(err); class != "UNSUPPORTED" {
			t.Errorf("the stream ended with %v (class %q), want UNSUPPORTED", err, class)
		}
	})
}

// A function that never reads Proc.Signals blocks nobody: the receiver goes on applying the frames behind the signals (an input frame
// is echoed), the queue holds the newest eight and the rest are counted as dropped, and what the function reads, when it reads at
// last, is the newest signals in the order they were sent, the last of them the last one sent.
func TestAFunctionThatNeverReadsSignalsBlocksNobodyAndKeepsTheNewestEight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const sent = 100
		gate, got := ctlGate(t), make(chan exec.Signal, 32)
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			go func() { _, _ = io.Copy(p.Stdout, p.Stdin) }() // an echo, to show that the receiver is free
			<-gate.Opened()
			return signalReader(got)(ctx, p)
		}))
		before := signalsDropped()
		host := rig.ctlPlainSession("s1")
		var order []exec.Signal
		for i := range sent {
			sig, wire := exec.SignalInt, execv2.Signal_SIGNAL_INT
			switch {
			case i == sent-1:
				sig, wire = exec.SignalKill, execv2.Signal_SIGNAL_KILL
			case i%2 == 1:
				sig, wire = exec.SignalTerm, execv2.Signal_SIGNAL_TERM
			}
			order = append(order, sig)
			host.ctlSend(ctlSignal(wire))
			if i%8 == 7 {
				synctest.Wait()
			}
		}
		host.ctlSend(ctlInput("ping"))
		synctest.Wait()
		var echoed []byte
		for _, f := range host.ctlFrames() {
			echoed = append(echoed, f.GetOutput().GetData()...)
		}
		if string(echoed) != "ping" {
			t.Fatalf("the echo of the input behind %d signals is %q: a function that does not read its signals held the receiver", sent, echoed)
		}
		if returned, err := host.ctlStatus(); returned {
			t.Fatalf("the stream ended (%v) for signals the session declares", err)
		}
		gate.Open()
		synctest.Wait()
		if signals, want := drainSignals(got), order[sent-8:]; !slices.Equal(signals, want) {
			t.Errorf("the function was handed %v, want the newest eight, %v", signals, want)
		}
		if dropped := signalsDropped() - before; dropped != sent-8 {
			t.Errorf("%v signals counted as dropped, want %d", dropped, sent-8)
		}
	})
}

// Proc.Signals is never nil, and it is closed when the session ends, so that a goroutine that ranges over it returns with the rest:
// the end of a graceful close is not held for the function's grace by a function that waits for the channel.
func TestProcSignalsIsNeverNilAndClosesWhenTheSessionEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		giveUp := ctlGate(t) // ends the reader of a build whose channel does not close, so that its failure is not a deadlock
		nilChannel, closed := make(chan bool, 1), make(chan struct{})
		rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			nilChannel <- p.Signals == nil
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				for {
					select {
					case _, open := <-p.Signals:
						if !open {
							close(closed)
							return
						}
					case <-giveUp.Opened():
						return
					}
				}
			}()
			<-ctx.Done()
			<-readerDone // a function that waits for its goroutines, as a function should
			return exec.Exit{}, ctx.Err()
		}, func(c *exec.Capabilities) { c.Signals = nil }))
		host := rig.ctlPlainSession("s1")
		synctest.Wait()
		if isNil := <-nilChannel; isNil {
			t.Error("Proc.Signals is nil for a target that declares no signals: it is never nil")
		}
		start := time.Now()
		host.ctlSend(ctlClose(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		synctest.Wait()
		select {
		case <-closed:
		default:
			t.Fatal("Proc.Signals was not closed when the session ended")
		}
		if took := time.Since(start); took != 0 {
			t.Errorf("the graceful close took %v: the function waited for its grace for a channel that closes with the command", took)
		}
		if ended := ctlEnded(host.ctlFrames()); ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Errorf("Ended %v, want closed by request", ended)
		}
	})
}
