package exectest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The suite checks the engine, so the tests of the suite need an engine that is wrong, to see the suite fail. The real engine is
// correct, so a wrong one is the real one with a single fault added at its edge: a wrapper that changes what a result says, what a
// read returns, or how Close and Live behave. It is given to a case or to the stress through the engineFactory seam, and each fault
// is meant to be found by one check.

// engineFault is the one thing that a faulty engine does wrong.
type engineFault int

const (
	faultEndIsLost         engineFault = iota + 1 // every end is Lost, whatever the command did
	faultNoSurvival                               // remote_may_survive is absent from every end
	faultCauseRequested                           // every cause that is not "finished" is reported as "requested"
	faultSecondEnd                                // the second time a result is asked for, it is another end
	faultOutputAfterEnd                           // a Read after the end returns a byte
	faultCompleteWithDrops                        // every output is COMPLETE and says bytes were dropped
	faultHole                                     // the first chunk of output skips a byte without saying so
	faultWrongByte                                // the first chunk of output has a byte that was not written
	faultNoArm                                    // the end has no arm
	faultBytesOut                                 // the result counts a byte that was never written
	faultLive                                     // Live holds a command after Close returned
	faultAbandoned                                // Abandoned never returns to zero
	faultSlowClose                                // Close does not return within its context
	faultLeak                                     // Close leaves a goroutine behind
	faultRefuseWrongClass                         // a start after Close is refused as INTERNAL and not UNAVAILABLE
	faultNeverDone                                // Done is never closed
	faultNoViolations                             // the result hides the ways the function broke the contract
	faultInventedViolation                        // the result reports an exit violation that the function did not make
)

// faultyEngine is the real engine with one fault.
type faultyEngine struct {
	exechook.Engine
	fault   engineFault
	closed  atomic.Bool
	release chan struct{} // closed by the test: ends what faultLeak leaves behind
}

// faultyFactory returns an engineFactory whose engines have the fault. What a fault leaves behind is released when the test ends.
func faultyFactory(t testing.TB, fault engineFault) engineFactory {
	return func(targets []exec.Target, cfg exechook.EngineConfig) (exechook.Engine, error) {
		real, err := realEngine(targets, cfg)
		if err != nil {
			return nil, err
		}
		e := &faultyEngine{Engine: real, fault: fault, release: make(chan struct{})}
		t.Cleanup(func() { close(e.release) })
		return e, nil
	}
}

func (e *faultyEngine) wrap(cmd exechook.Command, err error) (exechook.Command, error) {
	if err != nil {
		return nil, err
	}
	return &faultyCommand{Command: cmd, e: e, never: make(chan struct{})}, nil
}

func (e *faultyEngine) Open(ctx context.Context, req *execv2.OpenRequest) (exechook.Command, error) {
	if e.fault == faultRefuseWrongClass && e.closed.Load() {
		return nil, &exec.Error{Class: exec.ErrorClassInternal}
	}
	return e.wrap(e.Engine.Open(ctx, req))
}

func (e *faultyEngine) Run(ctx context.Context, req *execv2.RunRequest) (exechook.Command, error) {
	if e.fault == faultRefuseWrongClass && e.closed.Load() {
		return nil, &exec.Error{Class: exec.ErrorClassInternal}
	}
	return e.wrap(e.Engine.Run(ctx, req))
}

func (e *faultyEngine) Close(ctx context.Context) error {
	err := e.Engine.Close(ctx)
	e.closed.Store(true)
	switch e.fault {
	case faultSlowClose:
		<-ctx.Done()
		return ctx.Err()
	case faultLeak:
		go func() { <-e.release }()
	}
	return err
}

func (e *faultyEngine) Live() []string {
	if e.fault == faultLive && e.closed.Load() {
		return []string{"ghost-1"}
	}
	return e.Engine.Live()
}

func (e *faultyEngine) Abandoned() int64 {
	if e.fault == faultAbandoned {
		return 1
	}
	return e.Engine.Abandoned()
}

// faultyCommand is a command of the real engine with the fault of the engine that made it.
type faultyCommand struct {
	exechook.Command
	e       *faultyEngine
	never   chan struct{}
	results atomic.Int32
	reads   atomic.Int32
	ended   atomic.Bool
}

func (c *faultyCommand) Done() <-chan struct{} {
	if c.e.fault == faultNeverDone {
		return c.never
	}
	return c.Command.Done()
}

func (c *faultyCommand) Read(ctx context.Context, buf []byte) (exechook.Chunk, error) {
	if c.e.fault == faultOutputAfterEnd && c.ended.Load() {
		buf[0] = 'x'
		return exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, N: 1}, nil
	}
	chunk, err := c.Command.Read(ctx, buf)
	if errors.Is(err, io.EOF) {
		c.ended.Store(true)
	}
	if chunk.N > 0 && c.reads.Add(1) == 1 {
		switch c.e.fault {
		case faultHole:
			chunk.Offset++
		case faultWrongByte:
			buf[0] ^= 0xff
		}
	}
	return chunk, err
}

func (c *faultyCommand) Result() exechook.Result {
	res := c.Command.Result()
	calls := c.results.Add(1)
	switch c.e.fault {
	case faultEndIsLost:
		res.End = endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS)
	case faultNoSurvival:
		res.End = proto.Clone(res.End).(*execv2.End)
		res.End.RemoteMaySurvive = nil
	case faultCauseRequested:
		if res.Cause != "finished" {
			res.Cause = "requested"
		}
	case faultSecondEnd:
		if calls > 1 {
			res.End = endFailed(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED, execv2.FailureStage_FAILURE_STAGE_RUN)
		}
	case faultCompleteWithDrops:
		res.Output, res.DroppedBytes = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE, 5
	case faultNoArm:
		res.End = &execv2.End{}
	case faultBytesOut:
		res.BytesOut++
	case faultNoViolations:
		res.Violations = nil
	case faultInventedViolation:
		res.Violations = append(append([]string(nil), res.Violations...), "exit_shape: a violation the function did not make")
	}
	return res
}

// ---------------------------------------------------------------------------------------------------------------
// a recorder for testing.T
// ---------------------------------------------------------------------------------------------------------------

// recordT records what a case or the stress says instead of ending the test, so that a case can be run against a wrong engine and
// be seen to fail. Fatalf and Skipf end the goroutine they are called on, as the real ones do, so a body is run with run.
type recordT struct {
	mu       sync.Mutex
	failures []string
	skips    []string
	cleanups []func()
}

func (r *recordT) Helper()                        {}
func (r *recordT) Logf(string, ...any)            {}
func (r *recordT) Errorf(format string, a ...any) { r.record(format, a) }

func (r *recordT) record(format string, args []any) {
	r.mu.Lock()
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (r *recordT) Fatalf(format string, args ...any) {
	r.record(format, args)
	runtime.Goexit()
}

func (r *recordT) Skipf(format string, args ...any) {
	r.mu.Lock()
	r.skips = append(r.skips, fmt.Sprintf(format, args...))
	r.mu.Unlock()
	runtime.Goexit()
}

func (r *recordT) Cleanup(f func()) {
	r.mu.Lock()
	r.cleanups = append(r.cleanups, f)
	r.mu.Unlock()
}

// run calls body on a goroutine of its own and returns once it has ended, by returning, by Fatalf or by Skipf, and the cleanups it
// registered have run, last first.
func (r *recordT) run(body func(t checkT)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		body(r)
	}()
	<-done
	r.finish()
}

// finish runs the cleanups, last first.
func (r *recordT) finish() {
	r.mu.Lock()
	cleanups := r.cleanups
	r.cleanups = nil
	r.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// failed returns what was reported as a failure.
func (r *recordT) failed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.failures...)
}

// said reports whether a failure holds every one of the phrases.
func (r *recordT) said(phrases ...string) bool {
	for _, f := range r.failed() {
		found := true
		for _, phrase := range phrases {
			found = found && strings.Contains(f, phrase)
		}
		if found {
			return true
		}
	}
	return false
}
