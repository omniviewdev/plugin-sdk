package exectest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// ---------------------------------------------------------------------------------------------------------------
// case 15: a bounded run
// ---------------------------------------------------------------------------------------------------------------

// scenario runs a script under a request over the wire, with Run and with RunStream when the build lists it, and holds each to what
// the run is expected to say for the end the case expects of it. The mods change the request. It returns the result of Run.
func (r *runRig) scenario(label string, s runScript, end *execv2.End, mods ...func(*execv2.RunRequest)) *execv2.RunResult {
	r.t.Helper()
	name := r.newID("script")
	s = r.register(name, s)
	req := r.runRequest("", name, mods...)
	return r.both(label, req, s, r.wantFor(req, s, end))
}

// caseWireRun: a bounded run of the declared target over the wire. The result is exact: the exit as the target reported it (the
// code as it was given, the signal when the kind reports signals), the bytes of each stream raw and untouched, the account of each
// (total, kept, dropped, the split between head and tail, valid_utf8 of the bytes as returned, completeness), the start, the target
// and the program, and the survival. The capture keeps a head and a tail under HEAD_TAIL, the head or the tail under the other
// policies, and counts what it elides; a run is refused when its request breaks a bound of the target, never clamped; a timeout
// ends it, and a cancel; and the limit on runs in flight is the target's.
func caseWireRun(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Run.IsZero() {
		t.Skipf(noRuns)
	}
	rig := newRunRig(t, env, target, nil)
	rig.checkExits()
	rig.checkRefusals()
	rig.checkBoundsAreAccepted()
	rig.checkTimeout()
	rig.checkCancel()
	rig.checkCapture()
	rig.checkValidUTF8()
	rig.checkIDs()
	checkConcurrency(t, env, target)
}

// checkExits: a command that exits is a result of the outcome EXITED (or SIGNALED) with the code as the target reported it, from
// the source its kind declares, and its output on the two streams.
func (r *runRig) checkExits() {
	caps := r.info.Capabilities
	exit0, end0 := processExit(caps, 0)
	exit3, end3 := processExit(caps, 3)
	dword, endDword := processExit(caps, 3221225477) // a Windows status: a code is carried exactly, never narrowed
	r.scenario("exit 0 with no output", runScript{exit: exit0}, end0)
	r.scenario("exit 3 with output on both streams", runScript{
		writes: []runWrite{{n: 17, chunk: 5}, {stderr: true, n: 9, chunk: 4}, {n: 3}}, exit: exit3,
	}, end3)
	r.scenario("the program is reported before the output", runScript{report: true, writes: []runWrite{{n: 40}, {stderr: true, n: 2}}, exit: exit0}, end0)
	r.scenario("an exit code that is above 32 bits", runScript{writes: []runWrite{{n: 4}}, exit: dword}, endDword)
	r.scenario("a request id is echoed", runScript{exit: exit0}, end0, func(req *execv2.RunRequest) { req.RequestId = "audit-echo" })
	if reportsSignals(caps) {
		r.scenario("a signal", runScript{writes: []runWrite{{n: 6}}, exit: exec.ExitSignal("SIGKILL", 9)}, endSignaled("SIGKILL", 9, new(false)))
	} else {
		r.t.Logf("no signal scenario: the target declares ExitReport %s, and a signal is reported only by a target that declares CODE_AND_SIGNAL", caps.ExitReport)
	}
}

// refusal is a request that breaks a bound, and the refusal it must be: a status, and nothing ran.
type refusal struct {
	name string
	mod  func(*execv2.RunRequest)
	want string
}

// checkRefusals: a request that breaks a bound of the request or of the target is refused and never clamped, and what is refused
// never reaches the function: no argv, an argv that is no command, no target, an id that is no id, a timeout, an output cap, a
// stdin or an argv above the target's maximum, an output policy this build does not know, and the env and the working directory
// that the target does not honor.
func (r *runRig) checkRefusals() {
	caps := r.info.Capabilities
	limits := r.limits
	r.register("never", runScript{})
	refusals := []refusal{
		{"no argv", func(req *execv2.RunRequest) { req.Argv = nil }, "INVALID_REQUEST"},
		{"an argv whose first element is empty", func(req *execv2.RunRequest) { req.Argv = []string{"", "x"} }, "INVALID_REQUEST"},
		{"an argv element with a NUL", func(req *execv2.RunRequest) { req.Argv = []string{"a\x00b"} }, "INVALID_REQUEST"},
		{"no target", func(req *execv2.RunRequest) { req.Target = nil }, "INVALID_REQUEST"},
		{"a request id that is not printable ASCII", func(req *execv2.RunRequest) { req.RequestId = "run\n1" }, "INVALID_REQUEST"},
		{"a timeout above the target's maximum", func(req *execv2.RunRequest) { req.TimeoutMs = limits.GetMaxTimeoutMs() + 1 }, "INVALID_REQUEST"},
		{"a standard output cap above the target's maximum", func(req *execv2.RunRequest) {
			req.Output = &execv2.OutputLimits{MaxStdoutBytes: limits.GetMaxOutputBytes() + 1}
		}, "INVALID_REQUEST"},
		{"a standard error cap above the target's maximum", func(req *execv2.RunRequest) {
			req.Output = &execv2.OutputLimits{MaxStderrBytes: limits.GetMaxOutputBytes() + 1}
		}, "INVALID_REQUEST"},
		{"an output policy this build does not know", func(req *execv2.RunRequest) {
			req.Output = &execv2.OutputLimits{Policy: execv2.TruncatePolicy(99)}
		}, "UNSUPPORTED"},
		{"a stdin above the target's maximum", func(req *execv2.RunRequest) {
			req.Stdin = bytes.Repeat([]byte{'s'}, int(limits.GetMaxStdinBytes())+1)
		}, "LIMIT/INPUT_TOO_LARGE"},
		{"an argv above the target's maximum", func(req *execv2.RunRequest) {
			const element = 4096
			req.Argv = []string{"exectest"}
			for total := 0; total <= int(limits.GetMaxArgvBytes()); total += element {
				req.Argv = append(req.Argv, strings.Repeat("a", element))
			}
		}, "INVALID_REQUEST"},
	}
	if !caps.Env {
		refusals = append(refusals, refusal{"an environment for a target that honors none", func(req *execv2.RunRequest) {
			req.Env = []*execv2.NameValue{{Name: "A", Value: "b"}}
		}, "UNSUPPORTED"})
	}
	if !caps.Cwd {
		refusals = append(refusals, refusal{"a working directory for a target that honors none", func(req *execv2.RunRequest) { req.Cwd = "/" }, "UNSUPPORTED"})
	}
	for _, rf := range refusals {
		before := r.started.Load()
		req := r.runRequest("", "never", rf.mod)
		call := r.run(req)
		switch {
		case call.err == nil:
			r.t.Errorf("%s: the run was taken and answered %v: a request that breaks a bound is refused, and never clamped", rf.name, call.res.GetOutcome())
		case call.res != nil:
			r.t.Errorf("%s: a refusal came with a result: %v", rf.name, call.res)
		default:
			if got := refusalName(call.err); got != rf.want {
				r.t.Errorf("%s: the run was refused with %s (%v), want %s", rf.name, got, call.err, rf.want)
			}
		}
		if r.lists(execv2.Feature_FEATURE_RUN_STREAM) {
			stream := r.callStream(req, nil)
			if len(stream.events) != 0 || stream.err == nil || refusalName(stream.err) != rf.want {
				r.t.Errorf("%s (RunStream): %d events and %v, want no event and the refusal %s: a refusal is a status", rf.name, len(stream.events), stream.err, rf.want)
			}
		}
		if got := r.started.Load(); got != before {
			r.t.Errorf("%s: the function was called %d times for a request that was refused: a refusal means nothing ran", rf.name, got-before)
		}
	}
}

// checkBoundsAreAccepted: a request that is exactly at the bounds of the target is taken as it is: the largest timeout, the
// largest caps and the largest stdin, which arrives whole (every byte value) and ends with EOF; and a run without a stdin has none.
func (r *runRig) checkBoundsAreAccepted() {
	caps := r.info.Capabilities
	exit0, end0 := processExit(caps, 0)
	limits := r.limits
	stdin := make([]byte, limits.GetMaxStdinBytes())
	for i := range stdin {
		stdin[i] = byte(i*7 + i>>8)
	}
	maxOut := limits.GetMaxOutputBytes()
	r.scenario("the largest timeout, caps and stdin of the target", runScript{echo: true, stdin: stdin, exit: exit0}, end0, func(req *execv2.RunRequest) {
		req.TimeoutMs = limits.GetMaxTimeoutMs()
		req.Stdin = stdin
		req.Output = &execv2.OutputLimits{MaxStdoutBytes: maxOut, MaxStderrBytes: maxOut}
	})
	r.scenario("a run with no stdin has none: Proc.Stdin is nil", runScript{echo: true, exit: exit0}, end0)
	r.scenario("one byte of stdin", runScript{echo: true, stdin: []byte{0}, exit: exit0}, end0, func(req *execv2.RunRequest) { req.Stdin = []byte{0} })
}

// checkTimeout: a run that does not end by itself ends at its timeout, whatever its function does: the call returns a result of the
// outcome TIMED_OUT, the exit (if one was reported) from the host, the output it had written and the process's survival as the kind's
// kill declares. A function that honors its cancelled context has returned; one that ignores it has not, and the call returned
// without it.
func (r *runRig) checkTimeout() {
	caps := r.info.Capabilities
	exit0, endOK := processExit(caps, 0)
	timeout := min(time.Duration(r.limits.GetMaxTimeoutMs())*time.Millisecond, 250*time.Millisecond)
	timedOut := endClosed(execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT, caps.Kill)
	atTimeout := func(req *execv2.RunRequest) { req.TimeoutMs = uint32(timeout.Milliseconds()) } //nolint:gosec // at most the target's maximum

	// A function that honors its context.
	honors := runScript{writes: []runWrite{{n: 5}, {stderr: true, n: 3}}, block: true}
	name := r.newID("script")
	honors = r.register(name, honors)
	req := r.runRequest("", name, atTimeout)
	want := r.wantFor(req, honors, timedOut)
	want.timeout = timeout
	r.expect("a run that outlasts its timeout", r.run(req), want)
	if r.lists(execv2.Feature_FEATURE_RUN_STREAM) {
		r.expectStream("a run that outlasts its timeout", r.callStream(req, &honors), want)
	}
	r.settle()

	// A function that ignores its context: the call does not wait for it.
	gate := NewGate()
	r.t.Cleanup(gate.Open)
	ignores := runScript{writes: []runWrite{{n: 5}}, gate: gate}
	name = r.newID("script")
	ignores = r.register(name, ignores)
	req = r.runRequest("", name, atTimeout)
	want = r.wantFor(req, ignores, timedOut)
	want.timeout = timeout
	call := r.run(req)
	r.expect("a function that ignores its cancelled context", call, want)
	if outstanding := r.started.Load() - r.returned.Load(); outstanding != 1 {
		r.t.Errorf("a function that ignores its context: %d functions are still running when the call returned, want that one: the call does not wait for it", outstanding)
	}
	gate.Open()
	r.settle()
	// The place it held is free again, and so is its id.
	r.scenario("a run after a timeout", runScript{exit: exit0}, endOK)
}

// checkCancel: a host that cancels its call cancels the run: the call returns the cancel, typed, and the function sees its context
// end and returns, so that the place and the id of the run are free again. Neither Run nor RunStream leaves a command running.
func (r *runRig) checkCancel() {
	caps := r.info.Capabilities
	exit0, end0 := processExit(caps, 0)
	up := make(chan struct{}, 4)
	r.register("hold", runScript{writes: []runWrite{{n: 6}}, block: true, written: func() { up <- struct{}{} }})
	quick := r.register("quick", runScript{exit: exit0})
	for _, method := range []string{"Run", "RunStream"} {
		if method == "RunStream" && !r.lists(execv2.Feature_FEATURE_RUN_STREAM) {
			continue
		}
		id := "cancel-" + strings.ToLower(method)
		req := r.runRequest(id, "hold")
		ctx, cancel := context.WithCancel(r.request())
		done := make(chan error, 1)
		go func() {
			if method == "Run" {
				call := r.callRun(ctx, req)
				if call.err == nil {
					call.err = fmt.Errorf("a cancelled call returned the result %v", call.res.GetOutcome())
				}
				done <- call.err
				return
			}
			conn, err := r.client.RunStream(ctx, req)
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = conn.Close() }()
			for {
				if _, err := conn.Recv(); err != nil {
					done <- err
					return
				}
			}
		}()
		select {
		case <-up:
		case err := <-done:
			cancel()
			r.t.Errorf("%s: the run ended before it was cancelled: %v", method, err)
			continue
		case <-time.After(caseBound):
			cancel()
			r.t.Errorf("%s: the function did not start within %v", method, caseBound)
			continue
		}
		cancel()
		select {
		case err := <-done:
			var typed *exec.Error
			if !errors.As(err, &typed) || typed.Class != exec.ErrorClassCancelled || !errors.Is(err, context.Canceled) {
				r.t.Errorf("%s: a call that was cancelled returned %v, want CANCELLED with the cancel inside", method, err)
			}
		case <-time.After(caseBound):
			r.t.Errorf("%s: cancelling the call did not end it within %v", method, caseBound)
		}
		// The function has been told, and has returned.
		r.settle()
		// The id of the run is free: the same id, run again, is taken and ends.
		var again *runCall
		r.await("the id of the cancelled run to be free", func() bool {
			again = r.run(r.runRequest(id, "quick"))
			return again.err == nil
		})
		r.expect(method+": the id of a cancelled run, run again", again, r.wantFor(again.req, quick, end0))
	}
}

// checkCapture: the capture keeps what the cap and the policy say, exactly, and counts the rest. Each policy is run with caps and
// totals around the boundary (one under, at, one over and well over the cap, so that the cut falls inside a write), for the two streams
// at once, with caps of their own: HEAD_TAIL (and the policy left unsaid) keeps the first half of the cap, rounded up, and the last
// half; HEAD keeps the beginning; TAIL the end. A run with no caps gets the target's default.
func (r *runRig) checkCapture() {
	caps := r.info.Capabilities
	exit0, end0 := processExit(caps, 0)
	maxOut := int(r.limits.GetMaxOutputBytes())
	chunks := []int{1, 3, 7, 64, 1000}
	for _, policy := range []execv2.TruncatePolicy{
		execv2.TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL,
		execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD, execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL,
	} {
		for _, capBytes := range []int{1, 2, 7, 64} {
			if capBytes > maxOut {
				continue
			}
			errCap := min(capBytes+3, maxOut)
			totals := []int{0, capBytes - 1, capBytes, capBytes + 1, 5*capBytes + 2}
			for i, total := range totals {
				errTotal := totals[(i+2)%len(totals)]
				chunk := chunks[i%len(chunks)]
				script := runScript{writes: []runWrite{
					{n: total / 2, chunk: chunk}, {stderr: true, n: errTotal / 2, chunk: chunk},
					{n: total - total/2, chunk: chunk}, {stderr: true, n: errTotal - errTotal/2, chunk: chunk},
				}, exit: exit0}
				label := fmt.Sprintf("policy %s, caps %d and %d, wrote %d and %d", policy, capBytes, errCap, total, errTotal)
				r.scenario(label, script, end0, func(req *execv2.RunRequest) {
					req.Output = &execv2.OutputLimits{MaxStdoutBytes: uint32(capBytes), MaxStderrBytes: uint32(errCap), Policy: policy} //nolint:gosec // at most the target's maximum
				})
			}
		}
	}
	// The default cap: a run that sets none keeps what the target's default holds, and a policy alone changes the cut, not the cap.
	if caps.Stall == exec.StallLossy {
		r.t.Logf("the default cap is not tried: the target stalls lossily, and a command that writes more than the 256 KiB of its queue may lose some of it before the capture sees it")
		return
	}
	def := int(r.limits.GetDefaultOutputBytes())
	script := runScript{writes: []runWrite{{n: def + 1000, chunk: 4096}, {stderr: true, n: 10}}, exit: exit0}
	r.scenario("no caps and no policy: the target's default", script, end0)
	r.scenario("a policy and no caps", script, end0, func(req *execv2.RunRequest) {
		req.Output = &execv2.OutputLimits{Policy: execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL}
	})
}

// checkValidUTF8: valid_utf8 says whether the bytes as returned are UTF-8. The bytes are raw: a multi-byte character that the cap
// cuts is not repaired, and its bytes are returned as they are, so a cut in the middle of a character is not valid and a cut
// between characters is.
func (r *runRig) checkValidUTF8() {
	caps := r.info.Capabilities
	exit0, end0 := processExit(caps, 0)
	euros := []byte("€€€€") // three bytes each
	head, tail, both := execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD, execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL
	for _, tc := range []struct {
		name               string
		out, err           []byte
		policy             execv2.TruncatePolicy
		cap                uint32
		validOut, validErr bool
	}{
		{"plain text", []byte("hello\n"), nil, both, 0, true, true},
		{"a character cut by the cap under HEAD", euros, nil, head, 4, false, true},
		{"a character cut by the cap under TAIL", euros, nil, tail, 4, false, true},
		{"a character cut by the cap in the head and the tail", euros, nil, both, 4, false, true},
		{"a cut between two characters under HEAD", euros, nil, head, 6, true, true},
		{"a cut between characters in the head and the tail", euros, nil, both, 6, true, true},
		{"a byte that is no UTF-8 at all", []byte("ok\xff"), nil, both, 0, false, true},
		{"NUL bytes are UTF-8", []byte("a\x00b"), nil, both, 0, true, true},
		{"an escape sequence", []byte("\x1b[31mred\x1b[0m"), nil, both, 0, true, true},
		{"each stream is judged by its own bytes", []byte("fine"), []byte("\xc3("), both, 0, true, false},
		{"nothing at all is valid", nil, nil, both, 0, true, true},
	} {
		script := runScript{writes: []runWrite{{data: tc.out, chunk: 2}, {stderr: true, data: tc.err, chunk: 2}}, exit: exit0}
		res := r.scenario(tc.name, script, end0, func(req *execv2.RunRequest) {
			req.Output = &execv2.OutputLimits{MaxStdoutBytes: tc.cap, MaxStderrBytes: tc.cap, Policy: tc.policy}
		})
		if res == nil {
			continue
		}
		// What the case says of its own bytes must be what the oracle computes, or the case is wrong and the run is not at fault.
		if !r.merged {
			if got := res.GetStdoutAccount().GetValidUtf8(); got != tc.validOut {
				r.t.Errorf("%s: valid_utf8 of standard output is %v, want %v (the bytes returned are %s)", tc.name, got, tc.validOut, clip(res.GetStdout()))
			}
			if got := res.GetStderrAccount().GetValidUtf8(); got != tc.validErr {
				r.t.Errorf("%s: valid_utf8 of standard error is %v, want %v (the bytes returned are %s)", tc.name, got, tc.validErr, clip(res.GetStderr()))
			}
		}
	}
}

// checkIDs: the id of a run is the request's, and is free again when its result has arrived: the same id, run twice in a row, is
// taken both times.
func (r *runRig) checkIDs() {
	caps := r.info.Capabilities
	exit0, end0 := processExit(caps, 0)
	script := r.register("ids", runScript{writes: []runWrite{{n: 3}}, exit: exit0})
	for i := range 3 {
		req := r.runRequest("reused-id", "ids")
		call := r.run(req)
		r.expect(fmt.Sprintf("the same request id, run %d of 3", i+1), call, r.wantFor(req, script, end0))
	}
}

// runLimit is how many runs the declared target serves at once.
func runLimit(info exec.TargetInfo) int {
	if info.Run.MaxConcurrent > 0 {
		return info.Run.MaxConcurrent
	}
	return exec.DefaultRunLimits().MaxConcurrent
}

// heldRun is a run in flight, whose function waits to be ended.
type heldRun struct {
	cancel context.CancelFunc
	done   chan *runCall
}

// startHolds starts n runs of the script "hold", with the request ids the function gives for their index (an empty id is one the
// engine makes), and returns them once their functions have begun.
func (r *runRig) startHolds(n int, up <-chan struct{}, id func(i int) string) []*heldRun {
	r.t.Helper()
	holds := make([]*heldRun, n)
	for i := range holds {
		ctx, cancel := context.WithCancel(r.request())
		h := &heldRun{cancel: cancel, done: make(chan *runCall, 1)}
		holds[i] = h
		req := r.runRequest(id(i), "hold")
		go func() { h.done <- r.callRun(ctx, req) }()
	}
	for i := range holds {
		select {
		case <-up:
		case <-time.After(caseBound):
			r.t.Fatalf("only %d of %d runs began within %v: the target serves %d at once", i, n, caseBound, n)
		}
	}
	return holds
}

// end cancels the runs and waits for their calls to return.
func (r *runRig) endHolds(holds []*heldRun) {
	r.t.Helper()
	for _, h := range holds {
		h.cancel()
	}
	for _, h := range holds {
		select {
		case <-h.done:
		case <-time.After(caseBound):
			r.t.Errorf("a run that was cancelled did not return within %v", caseBound)
		}
	}
	r.settle()
}

// checkConcurrency: a target serves at most Run.MaxConcurrent runs at once. The one over is refused LIMIT / TOO_MANY_RUNS and never
// reaches the function; a run that ends gives its place back and a new one is taken; two runs with the same request id are refused
// CONFLICT, and the runs that carry no id each have one of their own; and nothing that was refused or cancelled holds a place.
func checkConcurrency(t checkT, env *wireEnv, target exec.Target) {
	t.Helper()
	limit := min(runLimit(target.Info), 3)
	target.Info.Run.MaxConcurrent = limit // the copy of the declaration the rig serves; the author's own is not changed
	rig := newRunRig(t, env, target, nil)
	exit0, end0 := processExit(rig.info.Capabilities, 0)
	up := make(chan struct{}, 4*limit)
	rig.register("hold", runScript{writes: []runWrite{{n: 4}}, block: true, written: func() { up <- struct{}{} }})
	quick := rig.register("quick", runScript{exit: exit0})

	// Fill the target. The first run has an id, and the others have none: each of them is taken, so each has an id of its own.
	holds := rig.startHolds(limit, up, func(i int) string {
		if i == 0 {
			return "held-id"
		}
		return ""
	})
	if got := rig.started.Load(); got != int64(limit) {
		t.Errorf("%d functions began for %d runs", got, limit)
	}

	// The run over the limit is refused, as a status, and never reaches the function.
	over := rig.run(rig.runRequest("", "quick"))
	if got := refusalName(over.err); over.res != nil || got != "LIMIT/TOO_MANY_RUNS" {
		t.Errorf("the run over the limit of %d: %v and %v, want a refusal LIMIT/TOO_MANY_RUNS", limit, over.res.GetOutcome(), over.err)
	}
	if rig.lists(execv2.Feature_FEATURE_RUN_STREAM) {
		if stream := rig.callStream(rig.runRequest("", "quick"), nil); refusalName(stream.err) != "LIMIT/TOO_MANY_RUNS" || len(stream.events) != 0 {
			t.Errorf("RunStream over the limit of %d: %d events and %v, want a refusal LIMIT/TOO_MANY_RUNS", limit, len(stream.events), stream.err)
		}
	}
	if got := rig.started.Load(); got != int64(limit) {
		t.Errorf("%d functions were called, %d runs were taken: a refused run never reaches the function", got, limit)
	}

	// A place is given back when a run ends. While another run holds the id "held-id", a run with that id is refused CONFLICT, once
	// there is a place for it.
	if limit > 1 {
		holds[limit-1].cancel()
		var last string
		rig.await("the cancelled run to give its place back", func() bool {
			last = refusalName(rig.run(rig.runRequest("held-id", "quick")).err)
			return last != "LIMIT/TOO_MANY_RUNS"
		})
		if last != "CONFLICT/SESSION_EXISTS" {
			t.Errorf("a second run with the id of a run that is running was refused with %s, want CONFLICT/SESSION_EXISTS", last)
		}
		var res *runCall
		rig.await("a place for a run without an id", func() bool {
			res = rig.run(rig.runRequest("", "quick"))
			return res.err == nil
		})
		rig.expect("a run that takes the place of one that ended", res, rig.wantFor(res.req, quick, end0))
	}
	rig.endHolds(holds)

	// Nothing that ended or was refused holds a place: the whole limit is taken again, and the id of a run that ended is free.
	holds = rig.startHolds(limit, up, func(int) string { return "" })
	rig.endHolds(holds)
	var reused *runCall
	rig.await("the id of the first run to be free", func() bool {
		reused = rig.run(rig.runRequest("held-id", "quick"))
		return reused.err == nil
	})
	rig.expect("the id of a run that ended, run again", reused, rig.wantFor(reused.req, quick, end0))
	rig.checkSharedIDSpace()
}

// checkSharedIDSpace: a run's id is its request id, in the id space of the sessions too: a run with the id of a session that is
// running is refused CONFLICT, and is taken once the session has ended.
func (r *runRig) checkSharedIDSpace() {
	if r.info.Session.IsZero() || !r.lists(execv2.Feature_FEATURE_SESSION) {
		return
	}
	exit0, end0 := processExit(r.info.Capabilities, 0)
	quick := r.register("quick-shared", runScript{exit: exit0})
	r.register("idle-shared", runScript{block: true})
	session := r.mustOpen("shared-id", "idle-shared", func(st *execv2.Start) {
		st.Tty = false
		st.Argv = []string{"exectest", "session"}
	})
	conflict := r.run(r.runRequest("shared-id", "quick-shared"))
	if got := refusalName(conflict.err); conflict.res != nil || got != "CONFLICT/SESSION_EXISTS" {
		r.t.Errorf("a run with the id of a session that is running: %v and %v, want a refusal CONFLICT/SESSION_EXISTS: the id space is shared", conflict.res.GetOutcome(), conflict.err)
	}
	_ = session.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	session.finish()
	var after *runCall
	r.await("the id of the ended session to be free for a run", func() bool {
		after = r.run(r.runRequest("shared-id", "quick-shared"))
		return after.err == nil
	})
	r.expect("a run with the id of a session that ended", after, r.wantFor(after.req, quick, end0))
}

// ---------------------------------------------------------------------------------------------------------------
// the scripts that end by themselves, in every way a command ends
// ---------------------------------------------------------------------------------------------------------------

// naturalScript is a command of the cases that compare two ways to run the same thing: it ends by itself, the way its name says.
type naturalScript struct {
	name   string
	script runScript
}

// naturalScripts are the ways a command ends by itself: an exit, a signal for a kind that reports signals, a lost stream, and a
// failure at every stage the contract names, a panic and an error that is no exec error. Each writes what it wrote before it ended.
func naturalScripts(caps exec.Capabilities) []naturalScript {
	exit0, _ := processExit(caps, 0)
	exit3, _ := processExit(caps, 3)
	out := []runWrite{{n: 17, chunk: 5}, {stderr: true, n: 9, chunk: 4}, {n: 3}}
	scripts := []naturalScript{
		{"exit 0 with no output", runScript{exit: exit0}},
		{"exit 3 with output on both streams", runScript{writes: out, exit: exit3}},
		{"the program is reported before the output", runScript{report: true, writes: out, exit: exit0}},
		{"a stream that ended with no status, and no output", runScript{exit: exec.ExitLost(exec.LostNoStatus)}},
		{"a link that died after output", runScript{writes: out, exit: exec.ExitLost(exec.LostLinkDead)}},
		{"a failure before anything was written", runScript{err: &exec.Error{
			Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable, Facts: exec.ErrorFacts{Program: "kubectl"}, Message: "no kubectl in the container",
			Err: errors.New(runCanary),
		}}},
		{"a failure after partial output", runScript{writes: out, err: &exec.Error{Class: exec.ErrorClassUnavailable, Reason: exec.ErrorReasonNotRunning, Err: errors.New(runCanary)}}},
		{"a failure of the connection after output", runScript{writes: out, err: &exec.Error{Class: exec.ErrorClassConnection, Err: errors.New(runCanary)}}},
		{"an error that is no exec error", runScript{writes: out, err: errors.New(runCanary)}},
		{"a panic after output", runScript{writes: out, panics: true}},
	}
	if reportsSignals(caps) {
		scripts = append(scripts, naturalScript{"a signal", runScript{writes: out, exit: exec.ExitSignal("SIGTERM", 15)}})
	}
	return scripts
}

// ---------------------------------------------------------------------------------------------------------------
// case 13, the Run part: errors round-trip and a canary appears nowhere
// ---------------------------------------------------------------------------------------------------------------

// The things whose Options callback the case 13's fake answers for: the id of the thing the request addresses chooses what it does.
const (
	denyThing  = "thing-denied"
	panicThing = "thing-panic"
	plainThing = "thing-plain"
)

// canaryProblem looks for the canary in the bytes of a message or a status.
func canaryProblem(what string, raw []byte) []string {
	if bytes.Contains(raw, []byte(runCanary)) {
		return []string{what + " carries the canary: the local cause of an error and the value of a panic never leave the process"}
	}
	return nil
}
