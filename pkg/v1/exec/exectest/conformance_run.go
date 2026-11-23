package exectest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The wire cases of the bounded run: a host runs one command in a target over a real transport, with Client.Run and Client.RunStream,
// as it does a plugin. They share the rig of the interactive cases (conformance_open.go): a fake of the target the author declared
// (its Info, never its Exec) serves a script that the request names with a label, behind the SDK's own server. What the author
// declared decides what the case expects: the exit report and the kill behavior of the kind, the limits of its runs, whether it
// declares a separate standard error, a lossy stall.
//
// A script is described by what it writes and how it ends, so that the case knows every byte that it wrote and where: the oracle of
// a result is computed from that (the head and the tail of a capture, the account of each stream, the exit), and never read from
// what the plugin returned.

const (
	// runCanary is a text no result, no event and no status may carry: the cases put it where the author's local cause and a panic
	// value live.
	runCanary = "CANARY-exectest-run-4b8e91d2-35a7"
	// noRuns is why a case skips for a target that serves no runs.
	noRuns = "the target declares no runs: it serves no Run"
	// runDurationSlack is how much of a run's reported duration the host's own measurement may not account for: the duration is
	// the SDK's, taken inside the call, so it cannot be longer than the call, and the rounding to a millisecond is all that is left.
	runDurationSlack = 5 * time.Millisecond
	// timeoutSlack is how early the end of a run by its timeout may be reported: the duration runs from the accepted request and
	// the timer from just after it.
	timeoutSlack = 50 * time.Millisecond
)

// ---------------------------------------------------------------------------------------------------------------
// the scripts
// ---------------------------------------------------------------------------------------------------------------

// runWrite is one write of a command of the run cases: to standard error or to standard output, either the n next bytes of the
// stream's pattern or the bytes it is given, in writes of at most chunk bytes (zero is one write).
type runWrite struct {
	stderr bool
	n      int
	data   []byte
	chunk  int
}

// runByte is the byte that a command writes at an offset of a stream: the stream's own pattern, so that a byte that moved, or went
// to the other stream, shows.
func runByte(stderr bool, offset uint64) byte {
	b := patternByte(offset)
	if stderr {
		b ^= 0xa5
	}
	return b
}

// runScript is a command of the run cases: what it writes, and how it ends.
type runScript struct {
	writes []runWrite
	// report makes the command call Proc.Started before its first write, as a target that knows its program does.
	report bool
	// exit is how it ends when it returns; err is returned in its place when it is set. block makes it wait for its context to end
	// instead, and return the error of that. panics makes it panic with the canary. gate makes it ignore its context until the gate
	// opens, and then end as it was told.
	exit   exec.Exit
	err    error
	block  bool
	panics bool
	gate   *Gate
	// echo makes the command copy its stdin to its standard output before it writes anything, and stdin is what the request
	// carries: the echo is the first of what it writes to standard output.
	echo  bool
	stdin []byte
	// written is called once all the writes are made: the command is then running, and silent.
	written func()
	// ended is called when the function returns.
	ended func()
}

// effective is the script for a target that declares no separate standard error: its command has one stream, and what it would
// write to standard error it writes to standard output.
func (s runScript) effective(merged bool) runScript {
	if !merged {
		return s
	}
	writes := slices.Clone(s.writes)
	for i := range writes {
		writes[i].stderr = false
	}
	s.writes = writes
	return s
}

// streams returns what the command wrote to standard output and to standard error.
func (s runScript) streams() (stdout, stderr []byte) {
	var out, errb []byte
	if s.echo {
		out = append(out, s.echoed()...)
	}
	for _, w := range s.writes {
		if w.stderr {
			errb = append(errb, w.bytes(uint64(len(errb)))...)
		} else {
			out = append(out, w.bytes(uint64(len(out)))...)
		}
	}
	return out, errb
}

// echoed is what an echoing command writes first: its stdin, or "none" for a run whose request carries none (Proc.Stdin is nil
// then, and only then).
func (s runScript) echoed() []byte {
	if len(s.stdin) > 0 {
		return s.stdin
	}
	return []byte("none")
}

// bytes returns the bytes of a write, given the offset of the stream where it begins.
func (w runWrite) bytes(offset uint64) []byte {
	if w.data != nil {
		return w.data
	}
	data := make([]byte, w.n)
	for i := range data {
		data[i] = runByte(w.stderr, offset+uint64(i))
	}
	return data
}

// layout returns the bytes of the whole output in the order they were written, and whether each was written to standard error: the
// offset of a byte in the output of the command is its index here.
func (s runScript) layout() (data []byte, onStderr []bool) {
	var written [2]uint64
	if s.echo {
		echoed := s.echoed()
		written[0] = uint64(len(echoed))
		data = append(data, echoed...)
		onStderr = make([]bool, len(echoed))
	}
	for _, w := range s.writes {
		chunk := w.bytes(written[boolIndex(w.stderr)])
		written[boolIndex(w.stderr)] += uint64(len(chunk))
		data = append(data, chunk...)
		for range chunk {
			onStderr = append(onStderr, w.stderr)
		}
	}
	return data, onStderr
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

// total is how many bytes the command writes.
func (s runScript) total() int {
	data, _ := s.layout()
	return len(data)
}

// observesStart says that the SDK sees the start of the command by the time it ends: it reported, wrote a byte, or returned the
// status of a process (the end of a command that returns an exit that is not a lost stream, with no error and no panic, that does
// not wait).
func (s runScript) observesStart() bool {
	return s.report || s.total() > 0 || s.returnsStatus()
}

// returnsStatus says that the function returns the status of a process: an exit code or a signal.
func (s runScript) returnsStatus() bool {
	return s.err == nil && !s.panics && !s.block && s.gate == nil && s.exit.Lost == exec.LostUnspecified
}

// fn is the function of the fake for the script.
func (s runScript) fn() exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		if s.ended != nil {
			defer s.ended()
		}
		if s.report {
			name := baseName(p.Request.Argv)
			p.Started(exec.SessionFacts{
				Program: exec.Program{Argv: p.Request.Argv, Name: name, Path: "/usr/bin/" + name},
				PID:     4242, TTY: new(false), Transport: "exectest",
			})
		}
		var written [2]uint64
		if s.echo {
			echoed := s.echoed()
			if p.Stdin == nil {
				if len(s.stdin) > 0 {
					return exec.Exit{}, errors.New("exectest: the request carried stdin and Proc.Stdin is nil")
				}
				if _, err := p.Stdout.Write(echoed); err != nil {
					return exec.Exit{}, err
				}
			} else if _, err := io.Copy(p.Stdout, p.Stdin); err != nil {
				return exec.Exit{}, err
			}
			written[0] = uint64(len(echoed))
		}
		for _, w := range s.writes {
			out := p.Stdout
			if w.stderr && p.Stderr != nil {
				out = p.Stderr
			}
			data := w.bytes(written[boolIndex(w.stderr)])
			written[boolIndex(w.stderr)] += uint64(len(data))
			if err := writeChunks(out, data, w.chunk); err != nil {
				return exec.Exit{}, err
			}
		}
		if s.written != nil {
			s.written()
		}
		if s.gate != nil {
			<-s.gate.Opened()
		}
		switch {
		case s.panics:
			panic(runCanary)
		case s.block:
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		case s.err != nil:
			return exec.Exit{}, s.err
		}
		return s.exit, nil
	}
}

// writeChunks writes data in writes of at most chunk bytes; zero is one write.
func writeChunks(w io.Writer, data []byte, chunk int) error {
	for len(data) > 0 {
		n := len(data)
		if chunk > 0 && chunk < n {
			n = chunk
		}
		if _, err := w.Write(data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// ---------------------------------------------------------------------------------------------------------------
// the oracle: what a run must say of itself
// ---------------------------------------------------------------------------------------------------------------

// streamWant is what a stream of a run wrote and the cap and policy it was run under.
type streamWant struct {
	written []byte
	cap     int
	policy  execv2.TruncatePolicy
}

// kept is what a capture keeps of what a stream wrote, and how the kept bytes split into a head and a tail. Under HEAD_TAIL (the
// default) the head is the first half of the cap, rounded up, and the tail the last half: the bytes of the middle are counted and
// never stored. HEAD keeps the beginning and TAIL the end. Whatever the cap holds is kept whole.
func (w streamWant) kept() (data []byte, head, tail int) {
	n := len(w.written)
	headCap, tailCap := (w.cap+1)/2, w.cap/2
	switch w.policy {
	case execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD:
		headCap, tailCap = w.cap, 0
	case execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL:
		headCap, tailCap = 0, w.cap
	case execv2.TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL:
	}
	if n <= w.cap {
		head = min(n, headCap)
		return w.written, head, n - head
	}
	return slices.Concat(w.written[:headCap], w.written[n-tailCap:]), headCap, tailCap
}

// streamed is what a RunStream delivers of what a stream wrote: the first cap bytes. A stream cannot be cut in its middle, so
// the bytes after the cap are counted as dropped.
func (w streamWant) streamed() []byte { return w.written[:min(len(w.written), w.cap)] }

// account is the account a stream of a run must carry. completeness is what the end allows when no byte was dropped.
func (w streamWant) account(streamed bool, completeness execv2.OutputCompleteness) *execv2.OutputAccount {
	data, head, tail := w.kept()
	if streamed {
		data, head, tail = w.streamed(), len(w.streamed()), 0
	}
	account := &execv2.OutputAccount{
		TotalBytes: uint64(len(w.written)), KeptBytes: uint64(len(data)), DroppedBytes: uint64(len(w.written) - len(data)),
		Truncated: len(w.written) > len(data), HeadBytes: uint32(head), TailBytes: uint32(tail), //nolint:gosec // bounded by the cap
		ValidUtf8: utf8.Valid(data), Completeness: completeness,
	}
	if account.DroppedBytes > 0 {
		account.Completeness = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	}
	return account
}

// runOutcome is the outcome fields that an End becomes in a result: the mapping of the contract. An exit and a signal are the
// outcomes of the same names, with the code and the source as they were given; a timeout and a cancel of the SDK are TIMED_OUT and
// CANCELLED, from the host; an end of the plugin's own that no command asked for (its shutdown, a retired connection) is a failure
// after the hand-over, UNAVAILABLE or CONNECTION; a failure is copied; and a lost stream is LOST, which may be running.
type runOutcome struct {
	outcome execv2.Outcome
	exit    *int64
	source  execv2.ExitSource
	// looseSource says the source of a signal may be left unsaid: the contract says whose an exit code is, and a signal has none.
	looseSource bool
	signal      string
	number      int32
	failed      *execv2.Failed
	survive     *bool
}

func outcomeOf(end *execv2.End) runOutcome {
	out := runOutcome{survive: end.RemoteMaySurvive}
	switch kind := end.GetKind().(type) {
	case *execv2.End_Exited:
		out.outcome, out.exit, out.source = execv2.Outcome_OUTCOME_EXITED, new(kind.Exited.GetExitCode()), kind.Exited.GetSource()
	case *execv2.End_Signaled:
		out.outcome, out.signal, out.number = execv2.Outcome_OUTCOME_SIGNALED, kind.Signaled.GetSignal(), kind.Signaled.GetSignalNumber()
		out.source, out.looseSource = kind.Signaled.GetSource(), true
	case *execv2.End_Closed:
		switch kind.Closed.GetBy() {
		case execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT:
			out.outcome, out.source = execv2.Outcome_OUTCOME_TIMED_OUT, execv2.ExitSource_EXIT_SOURCE_HOST
		case execv2.ClosedBy_CLOSED_BY_CANCELLED, execv2.ClosedBy_CLOSED_BY_REQUEST:
			out.outcome, out.source = execv2.Outcome_OUTCOME_CANCELLED, execv2.ExitSource_EXIT_SOURCE_HOST
		case execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN:
			out.outcome, out.failed = execv2.Outcome_OUTCOME_FAILED, &execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, Stage: execv2.FailureStage_FAILURE_STAGE_RUN}
		case execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED:
			out.outcome, out.failed = execv2.Outcome_OUTCOME_FAILED, &execv2.Failed{
				Class: execv2.ErrorClass_ERROR_CLASS_CONNECTION, Reason: execv2.ErrorReason_ERROR_REASON_CONNECTION_RETIRED, Stage: execv2.FailureStage_FAILURE_STAGE_RUN,
			}
		case execv2.ClosedBy_CLOSED_BY_UNSPECIFIED, execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT, execv2.ClosedBy_CLOSED_BY_IDLE_TIMEOUT:
			out.outcome, out.failed = execv2.Outcome_OUTCOME_FAILED, &execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL, Stage: execv2.FailureStage_FAILURE_STAGE_RUN}
		}
	case *execv2.End_Failed:
		out.outcome, out.failed = execv2.Outcome_OUTCOME_FAILED, kind.Failed
	case *execv2.End_Lost:
		out.outcome, out.survive = execv2.Outcome_OUTCOME_LOST, new(true)
	}
	return out
}

// wantRun is what a run is expected to say.
type wantRun struct {
	// end is the end the command had: the outcome, the exit, the failure and the survival come from it.
	end *execv2.End
	// started is start_observed; reported says the command reported its start, so that its program has the path it reported.
	started, reported bool
	out, err          streamWant
	// timeout is the timeout the run was asked for, when it ended by it.
	timeout time.Duration
}

// facts are the target and the program a run that observed its start must carry: the facts of the declaration, the request's argv,
// and the path that the command reported.
func (r *runRig) facts(req *execv2.RunRequest, reported bool) (*execv2.TargetFacts, *execv2.Program) {
	argv := req.GetArgv()
	program := &execv2.Program{Argv: argv, Name: baseName(argv)}
	if reported {
		program.Path = "/usr/bin/" + program.Name
	}
	return &execv2.TargetFacts{Provider: r.info.Provider, Type: r.info.Type, Id: r.resource, DisplayName: r.info.DisplayName}, program
}

// accountProblems holds the account of a stream to what the command wrote, with the bytes the result carries for it.
func accountProblems(stream string, got *execv2.OutputAccount, payload []byte, want streamWant, streamed bool, completeness execv2.OutputCompleteness) []string {
	var found []string
	add := func(format string, args ...any) {
		found = append(found, fmt.Sprintf("%s: "+format, append([]any{stream}, args...)...))
	}
	wantAccount := want.account(streamed, completeness)
	wantData, _, _ := want.kept()
	if streamed {
		wantData = want.streamed()
	}
	if got == nil {
		return []string{stream + ": the result carries no account"}
	}
	if !proto.Equal(got, wantAccount) {
		add("the account is %v, want %v (the command wrote %d bytes, the cap is %d, the policy %s)", got, wantAccount, len(want.written), want.cap, want.policy)
	}
	switch {
	case streamed && len(payload) != 0:
		add("RunEnded carries %d bytes of the stream: the bytes were streamed, and its payloads are empty", len(payload))
	case !streamed && !bytes.Equal(payload, wantData):
		add("the bytes are %s, want exactly what the cap keeps of what was written, %s: raw and untouched, the middle elided under HEAD_TAIL and never stored",
			clip(payload), clip(wantData))
	}
	return found
}

// clip writes a byte string for a message: its length and a beginning.
func clip(data []byte) string {
	const shown = 24
	if len(data) <= shown {
		return fmt.Sprintf("%d bytes %q", len(data), data)
	}
	return fmt.Sprintf("%d bytes %q...", len(data), data[:shown])
}

// resultProblems holds a result (or the result of a RunEnded, whose payloads are empty) to what the run was expected to say: the
// request it answers, the outcome and everything that goes with it, whether its start was observed and what is said of the target
// and the program, how long it took, and the account and the bytes of each stream.
func (r *runRig) resultProblems(res *execv2.RunResult, req *execv2.RunRequest, streamed bool, want wantRun, took time.Duration) []string {
	if res == nil {
		return []string{"there is no result"}
	}
	var found []string
	add := func(format string, args ...any) { found = append(found, fmt.Sprintf(format, args...)) }
	if res.GetRequestId() != req.GetRequestId() {
		add("request_id is %q, the request's is %q: it is echoed as it was sent, and an id the SDK made is not the host's", res.GetRequestId(), req.GetRequestId())
	}
	outcome := outcomeOf(want.end)
	if arm, ok := endArm(want.end); !ok {
		add("the expectation has no end (%s)", arm)
	}
	if res.GetOutcome() != outcome.outcome {
		add("the outcome is %s, want %s (the command's end is %s)", res.GetOutcome(), outcome.outcome, endText(want.end))
	}
	switch {
	case (res.ExitCode == nil) != (outcome.exit == nil):
		add("exit_code is %s, want %s: an absent code is none, never 0", exitText(res.ExitCode), exitText(outcome.exit))
	case res.ExitCode != nil && *res.ExitCode != *outcome.exit:
		add("exit_code is %d, want %d", *res.ExitCode, *outcome.exit)
	}
	if got := res.GetExitSource(); got != outcome.source && !(outcome.looseSource && got == execv2.ExitSource_EXIT_SOURCE_UNSPECIFIED) {
		add("exit_source is %s, want %s", got, outcome.source)
	}
	if res.GetSignal() != outcome.signal || res.GetSignalNumber() != outcome.number {
		add("the signal is %q (%d), want %q (%d)", res.GetSignal(), res.GetSignalNumber(), outcome.signal, outcome.number)
	}
	switch {
	case (res.Failed == nil) != (outcome.failed == nil):
		add("failed is present: %v, want %v: it is present exactly when the outcome is FAILED", res.Failed != nil, outcome.failed != nil)
	case res.Failed != nil:
		// The failure is copied as the command's end had it: class, reason, facts, stage and the public message, each exactly.
		if gotFailed, wantFailed := res.GetFailed(), outcome.failed; !proto.Equal(gotFailed, wantFailed) {
			add("failed is %v, want %v", gotFailed, wantFailed)
		}
	}
	if (res.RemoteMaySurvive == nil) != (outcome.survive == nil) || (res.RemoteMaySurvive != nil && *res.RemoteMaySurvive != *outcome.survive) {
		add("remote_may_survive is %s, want %s: absent is not known, and a consumer reads it as may", survivalText(res.RemoteMaySurvive), survivalText(outcome.survive))
	}
	if res.GetStartObserved() != want.started {
		add("start_observed is %v, want %v", res.GetStartObserved(), want.started)
	}
	wantTarget, wantProgram := r.facts(req, want.reported)
	wroteAny := len(want.out.written)+len(want.err.written) > 0
	switch {
	case !res.GetStartObserved() && (res.GetTarget() != nil || res.GetProgram() != nil):
		add("the target %v and the program %v are said for a run whose start was not observed: they are never made up", res.GetTarget(), res.GetProgram())
	case want.started && (want.reported || wroteAny):
		// The command reported, or wrote: the facts of its Started are in the result.
		if !proto.Equal(res.GetTarget(), wantTarget) {
			add("the target is %v, want %v", res.GetTarget(), wantTarget)
		}
		if !proto.Equal(res.GetProgram(), wantProgram) {
			add("the program is %v, want %v", res.GetProgram(), wantProgram)
		}
	case res.GetTarget() != nil && !proto.Equal(res.GetTarget(), wantTarget), res.GetProgram() != nil && !proto.Equal(res.GetProgram(), wantProgram):
		// The start was observed by a status alone: the facts are those of the request when they are said at all.
		add("the target is %v and the program %v, want those of the request when they are said at all", res.GetTarget(), res.GetProgram())
	}
	if took > 0 && time.Duration(res.GetDurationMs())*time.Millisecond > took+runDurationSlack {
		add("duration_ms is %d, the call took %v: the duration is taken inside it", res.GetDurationMs(), took)
	}
	if want.timeout > 0 && time.Duration(res.GetDurationMs())*time.Millisecond < want.timeout-timeoutSlack {
		add("duration_ms is %d, the run ended by its timeout of %v", res.GetDurationMs(), want.timeout)
	}
	found = append(found, accountProblems("stdout", res.GetStdoutAccount(), res.GetStdout(), want.out, streamed, completenessFor(want.end, droppedOf(want.out, streamed)))...)
	found = append(found, accountProblems("stderr", res.GetStderrAccount(), res.GetStderr(), want.err, streamed, completenessFor(want.end, droppedOf(want.err, streamed)))...)
	return found
}

// droppedOf is how many bytes of a stream the cap drops.
func droppedOf(w streamWant, streamed bool) uint64 {
	kept, _, _ := w.kept()
	if streamed {
		return uint64(len(w.written) - len(w.streamed()))
	}
	return uint64(len(w.written) - len(kept))
}

func exitText(code *int64) string {
	if code == nil {
		return "absent"
	}
	return fmt.Sprint(*code)
}

// ---------------------------------------------------------------------------------------------------------------
// the rig
// ---------------------------------------------------------------------------------------------------------------

// runRig is a wire rig whose host runs commands in the declared target.
type runRig struct {
	*wireRig
	// limits are the limits of the target's runs as Describe says: effective, and never zero.
	limits   *execv2.RunLimits
	features []execv2.Feature
	// merged says the target declares no separate standard error.
	merged bool
	// lastStream is the stream that the last call of both read.
	lastStream *streamCall
}

// newRunRig serves a fake of the declared target. options, when it is not nil, is the Options callback of the fake: the author's
// own is never called. A target the case gives a lowered limit has it in its Info.
func newRunRig(t checkT, env *wireEnv, target exec.Target, options exec.OptionsFunc) *runRig {
	t.Helper()
	serve := env.serve
	if serve == nil {
		serve = serveReal
	}
	local := *env
	local.serve = func(t checkT, targets []exec.Target, conns *exectestkit.Connections) *wireServer {
		for i := range targets {
			targets[i].Options = options
		}
		return serve(t, targets, conns)
	}
	base := newWireRig(t, &local, target)
	rig := &runRig{wireRig: base, merged: !base.info.Capabilities.SeparateStderr}
	description, err := base.client.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	rig.features = description.GetFeatures()
	rig.limits = base.described().GetRun()
	if rig.limits == nil {
		t.Fatalf("Describe lists no run limits for the target: it declares %+v", base.info.Run)
	}
	return rig
}

// lists says that the build lists a feature.
func (r *runRig) lists(f execv2.Feature) bool { return slices.Contains(r.features, f) }

// runRequest is a RunRequest for the declared target: the argv that names no real program, no stdin, the limits of the target, and
// the label that names the script. The mods change it.
func (r *runRig) runRequest(id, script string, mods ...func(*execv2.RunRequest)) *execv2.RunRequest {
	r.t.Helper()
	target, err := wireTarget(r.conns, r.info.Type, r.resource)
	if err != nil {
		r.t.Fatalf("%v", err)
	}
	req := &execv2.RunRequest{RequestId: id, Target: target, Argv: []string{"exectest", "run"}}
	if script != "" {
		req.Labels = []*execv2.NameValue{{Name: scriptLabel, Value: script}}
	}
	for _, mod := range mods {
		mod(req)
	}
	return req
}

// register registers the function of a script under a name, for the target that the rig serves.
func (r *runRig) register(name string, s runScript) runScript {
	s = s.effective(r.merged)
	r.scriptFunc(name, s.fn())
	return s
}

// runCall is one Run as the host saw it.
type runCall struct {
	req  *execv2.RunRequest
	res  *execv2.RunResult
	err  error
	took time.Duration
}

// callRun runs a request within the bound of a case: a plugin that does not answer fails the case and does not hang it.
func (r *runRig) callRun(ctx context.Context, req *execv2.RunRequest) *runCall {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(ctx, caseBound)
	defer cancel()
	began := time.Now()
	res, err := r.client.Run(ctx, req)
	return &runCall{req: req, res: res, err: err, took: time.Since(began)}
}

// run runs a request over the host's connection.
func (r *runRig) run(req *execv2.RunRequest) *runCall { return r.callRun(r.request(), req) }

// runGrammar holds the events of one RunStream to the order the contract states: RunStarted at most once and before the first
// Output, Output events with offsets that never go back, RunProgress with counters that never go back, and RunEnded last, once,
// with nothing after it.
type runGrammar struct {
	events   int
	started  int
	output   bool
	ended    bool
	next     uint64 // one past the last byte of the last Output event
	elapsed  uint64
	bytesOut uint64
	// layout is what the command wrote, and where: each Output event is held to it.
	data     []byte
	onStderr []bool
	problems []string
}

func (g *runGrammar) problem(format string, args ...any) {
	if len(g.problems) < 20 {
		g.problems = append(g.problems, fmt.Sprintf(format, args...))
	}
}

// eventName names the kind of an event, for a message.
func eventName(e *execv2.RunEvent) string {
	switch e.GetEvent().(type) {
	case *execv2.RunEvent_Started:
		return "RunStarted"
	case *execv2.RunEvent_Output:
		return "an Output event"
	case *execv2.RunEvent_Progress:
		return "RunProgress"
	case *execv2.RunEvent_Ended:
		return "RunEnded"
	case nil:
		return "an event of no kind"
	}
	return "an event of a kind this reader does not know"
}

// take checks one event of the stream.
func (g *runGrammar) take(e *execv2.RunEvent) {
	g.events++
	if g.ended {
		g.problem("%s came after RunEnded: nothing follows it", eventName(e))
	}
	switch event := e.GetEvent().(type) {
	case *execv2.RunEvent_Started:
		if g.started++; g.started > 1 {
			g.problem("a second RunStarted")
		}
		if g.output {
			g.problem("RunStarted after the first Output: it comes before it")
		}
	case *execv2.RunEvent_Output:
		g.output = true
		g.takeOutput(event.Output)
	case *execv2.RunEvent_Progress:
		p := event.Progress
		if p.GetElapsedMs() < g.elapsed || p.GetBytesOut() < g.bytesOut {
			g.problem("RunProgress goes back: %d ms and %d bytes after %d ms and %d bytes", p.GetElapsedMs(), p.GetBytesOut(), g.elapsed, g.bytesOut)
		}
		if g.data != nil && p.GetBytesOut() > uint64(len(g.data)) {
			g.problem("RunProgress says %d bytes were written, the command wrote %d", p.GetBytesOut(), len(g.data))
		}
		g.elapsed, g.bytesOut = p.GetElapsedMs(), p.GetBytesOut()
	case *execv2.RunEvent_Ended:
		g.ended = true
		if event.Ended.GetResult() == nil {
			g.problem("RunEnded carries no result")
		}
	case nil:
		g.problem("an event of no kind: this build sends none")
	}
}

func (g *runGrammar) takeOutput(o *execv2.Output) {
	data, offset := o.GetData(), o.GetOffset()
	switch {
	case len(data) == 0:
		g.problem("an Output event at offset %d carries no bytes", offset)
	case len(data) > maxOutputFrame:
		g.problem("an Output event of %d bytes, over the limit of %d", len(data), maxOutputFrame)
	}
	if o.GetStream() != execv2.Stream_STREAM_STDOUT && o.GetStream() != execv2.Stream_STREAM_STDERR {
		g.problem("an Output event of the stream %s from a command that runs without a terminal", o.GetStream())
	}
	if offset < g.next {
		g.problem("an Output event at offset %d, one past the last byte of the one before it is %d: no byte is delivered twice", offset, g.next)
	}
	g.next = max(g.next, offset+uint64(len(data)))
	if g.data == nil {
		return
	}
	if offset+uint64(len(data)) > uint64(len(g.data)) {
		g.problem("an Output event at offset %d of %d bytes: the command wrote %d", offset, len(data), len(g.data))
		return
	}
	for i, b := range data {
		at := offset + uint64(i)
		if g.data[at] != b || g.onStderr[at] != (o.GetStream() == execv2.Stream_STREAM_STDERR) {
			g.problem("the byte at offset %d of the Output event (stream %s) is %#02x, the command wrote %#02x there to %s", at, o.GetStream(), b, g.data[at], streamText(g.onStderr[at]))
			return
		}
	}
}

func streamText(stderr bool) string {
	if stderr {
		return "standard error"
	}
	return "standard output"
}

// streamCall is one RunStream as the host saw it.
type streamCall struct {
	req     *execv2.RunRequest
	events  []*execv2.RunEvent
	started *execv2.RunStarted
	ended   *execv2.RunEnded
	// stdout and stderr are what the Output events carried, by stream.
	stdout, stderr []byte
	grammar        runGrammar
	// err is the error RunStream returned (a refusal, no events) or the one that ended the stream, which is never io.EOF after RunEnded.
	err  error
	took time.Duration
}

// callStream runs a request with its output as it happens and reads the stream to its end, within the bound of a case. A script
// is given when the events are to be held to what it wrote.
func (r *runRig) callStream(req *execv2.RunRequest, script *runScript) *streamCall {
	r.t.Helper()
	call := &streamCall{req: req}
	if script != nil {
		call.grammar.data, call.grammar.onStderr = script.layout()
	}
	ctx, cancel := context.WithTimeout(r.request(), caseBound)
	defer cancel()
	began := time.Now()
	defer func() { call.took = time.Since(began) }()
	conn, err := r.client.RunStream(ctx, req)
	if err != nil {
		call.err = err
		return call
	}
	defer func() { _ = conn.Close() }()
	for {
		event, err := conn.Recv()
		if errors.Is(err, io.EOF) {
			return call
		}
		if err != nil {
			call.err = err
			return call
		}
		call.events = append(call.events, event)
		call.grammar.take(event)
		switch e := event.GetEvent().(type) {
		case *execv2.RunEvent_Started:
			if call.started == nil {
				call.started = e.Started
			}
		case *execv2.RunEvent_Output:
			if e.Output.GetStream() == execv2.Stream_STREAM_STDERR {
				call.stderr = append(call.stderr, e.Output.GetData()...)
			} else {
				call.stdout = append(call.stdout, e.Output.GetData()...)
			}
		case *execv2.RunEvent_Ended:
			if call.ended == nil {
				call.ended = e.Ended
			}
		}
	}
}

// streamProblems holds a stream that ran to its end to the contract of the events and to what the run was expected to say: the
// grammar; the bytes it delivered, which are the first bytes of each stream up to its cap; RunStarted exactly when the command
// reported or wrote before it ended, never when the start was not observed, and with the facts the result carries; and the result
// of RunEnded, which has no payloads.
func (r *runRig) streamProblems(call *streamCall, want wantRun) []string {
	var found []string
	add := func(format string, args ...any) { found = append(found, fmt.Sprintf(format, args...)) }
	if call.err != nil {
		return []string{fmt.Sprintf("the stream ended with %v, want RunEnded and then io.EOF", call.err)}
	}
	found = append(found, call.grammar.problems...)
	if call.ended == nil {
		return append(found, "the stream ended with status OK and no RunEnded")
	}
	if n := len(call.events); n == 0 || call.events[n-1].GetEnded() == nil {
		add("RunEnded is not the last event")
	}
	if want.started && (want.reported || len(want.out.written)+len(want.err.written) > 0) && call.started == nil {
		add("no RunStarted for a command that reported its start or wrote output")
	}
	if !want.started && call.started != nil {
		add("RunStarted for a run whose start was not observed: a start is never made up")
	}
	if !want.started && len(call.events) != 1 {
		add("a run whose start was not observed sent %d events, want RunEnded alone", len(call.events))
	}
	if call.started != nil && call.ended.GetResult() != nil {
		if !proto.Equal(call.started.GetTarget(), call.ended.GetResult().GetTarget()) || !proto.Equal(call.started.GetProgram(), call.ended.GetResult().GetProgram()) {
			add("RunStarted says the target %v and the program %v, RunEnded %v and %v", call.started.GetTarget(), call.started.GetProgram(),
				call.ended.GetResult().GetTarget(), call.ended.GetResult().GetProgram())
		}
	}
	if !bytes.Equal(call.stdout, want.out.streamed()) {
		add("the Output events delivered %s of standard output, want the first %d bytes: %s", clip(call.stdout), want.out.cap, clip(want.out.streamed()))
	}
	if !bytes.Equal(call.stderr, want.err.streamed()) {
		add("the Output events delivered %s of standard error, want the first %d bytes: %s", clip(call.stderr), want.err.cap, clip(want.err.streamed()))
	}
	return append(found, r.resultProblems(call.ended.GetResult(), call.req, true, want, call.took)...)
}

// expect holds a run that ended to what it was expected to say.
func (r *runRig) expect(name string, call *runCall, want wantRun) {
	r.t.Helper()
	if call.err != nil {
		r.t.Errorf("%s: Run was refused or broke: %v: a command that was handed to the target ends in a result, whatever became of it", name, call.err)
		return
	}
	for _, problem := range r.resultProblems(call.res, call.req, false, want, call.took) {
		r.t.Errorf("%s: %s", name, problem)
	}
}

// expectStream holds a stream that ended to what the run was expected to say.
func (r *runRig) expectStream(name string, call *streamCall, want wantRun) {
	r.t.Helper()
	for _, problem := range r.streamProblems(call, want) {
		r.t.Errorf("%s (RunStream): %s", name, problem)
	}
}

// both runs a request with Run, and with RunStream when the build lists it, and holds each to what the run was expected to say.
// A request id is the id of a run in the engine, so the stream's is the request's with a suffix: the two runs do not share an id.
// It returns the result of Run, and keeps the stream (nil when the build does not list RunStream) in lastStream.
func (r *runRig) both(name string, req *execv2.RunRequest, s runScript, want wantRun) *execv2.RunResult {
	r.t.Helper()
	call := r.run(req)
	r.expect(name, call, want)
	r.lastStream = nil
	if r.lists(execv2.Feature_FEATURE_RUN_STREAM) {
		streamReq := proto.Clone(req).(*execv2.RunRequest)
		if streamReq.RequestId != "" {
			streamReq.RequestId += "-stream"
		}
		r.lastStream = r.callStream(streamReq, &s)
		r.expectStream(name, r.lastStream, want)
	}
	return call.res
}

// settle waits until the plugin holds no function of the case: every function that was called has returned.
func (r *runRig) settle() {
	r.t.Helper()
	r.await("every function the case started to return", func() bool { return r.returned.Load() == r.started.Load() })
}

// ---------------------------------------------------------------------------------------------------------------
// what the declaration gives a case
// ---------------------------------------------------------------------------------------------------------------

// wantFor is what a script that ran under a request is expected to say, for the end the case expects of it. The caps and the
// policy are the request's, with the target's default for a cap the request leaves at zero.
func (r *runRig) wantFor(req *execv2.RunRequest, s runScript, end *execv2.End) wantRun {
	stdout, stderr := s.streams()
	limits := req.GetOutput()
	capOf := func(requested uint32) int {
		if requested == 0 {
			return int(r.limits.GetDefaultOutputBytes())
		}
		return int(requested)
	}
	return wantRun{
		end: end, started: s.observesStart(), reported: s.report,
		out: streamWant{written: stdout, cap: capOf(limits.GetMaxStdoutBytes()), policy: limits.GetPolicy()},
		err: streamWant{written: stderr, cap: capOf(limits.GetMaxStderrBytes()), policy: limits.GetPolicy()},
	}
}

// endFailedWith is a failed end with the facts of the failure.
func endFailedWith(f *execv2.Failed) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Failed{Failed: f}}
}
