package exectest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The in-process cases of the conformance suite. Each runs the real engine in this process, with no transport: the engine is built
// over a fake target that has the description the author declared and a script the case chooses, behind a fake connection
// registry, so that what the author declared (the Kill and ExitReport of the kind, whether it serves sessions or runs, its limits)
// decides what the case expects, and the author's own Exec is never called. The author's Options and Classify are not given to the
// fake either: they are the author's code, and no case here is about them.

// caseBound is how long a case waits for something that has to happen. It turns a hang into a failure and decides no outcome.
const caseBound = 10 * time.Second

// checkT is the part of testing.T that an in-process case uses. *testing.T has all of it; the tests of the suite give a recorder, so
// that a case can be run against a wrong engine and be seen to fail.
type checkT interface {
	Helper()
	Logf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
	Cleanup(func())
}

// inProcessEnv is what an in-process case is given besides the author's target.
type inProcessEnv struct {
	// resource is the id of the thing that the requests address (ConformanceResource).
	resource string
	// hooks carry the engine under test and the bounds of the stress: the real engine and the stated bounds when they are zero. The
	// tests of the suite give a wrong engine here.
	hooks stressHooks
	// stress is what the stress entry runs with; the zero value is the default.
	stress StressConfig
}

func (e *inProcessEnv) engine() engineFactory {
	if e.hooks.engine != nil {
		return e.hooks.engine
	}
	return realEngine
}

// servesCommands reports whether the declared target serves sessions or runs: a target that serves neither has nothing to run.
func servesCommands(info exec.TargetInfo) bool { return !info.Session.IsZero() || !info.Run.IsZero() }

// ---------------------------------------------------------------------------------------------------------------
// the rig: one engine over a fake of the declared target
// ---------------------------------------------------------------------------------------------------------------

// caseRig is one engine over a fake target that has the author's declared description and the script of a case.
type caseRig struct {
	t        checkT
	info     exec.TargetInfo
	resource string
	conns    *exectestkit.Connections
	eng      exechook.Engine
	returned atomic.Int64 // the functions that returned
	started  int
}

// newCaseRig builds the engine for the declared target with a script, and closes it when the case ends.
func newCaseRig(t checkT, env *inProcessEnv, target exec.Target, steps ...Step) *caseRig {
	t.Helper()
	info := target.Info
	info.Capabilities = cloneCapabilities(info.Capabilities)
	fake := NewFakeTarget(FakeInfo(info), FakeScript(steps...))
	r := &caseRig{t: t, info: info, resource: env.resource}
	inner := fake.Exec
	fake.Exec = func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		defer r.returned.Add(1)
		return inner(ctx, p)
	}
	r.conns = exectestkit.NewConnections(info.Provider)
	eng, err := env.engine()([]exec.Target{fake}, exechook.EngineConfig{Connections: r.conns})
	if err != nil {
		t.Fatalf("build the engine for the declared target: %v", err)
	}
	r.eng = eng
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), caseBound)
		defer cancel()
		_ = eng.Close(ctx)
		r.conns.Retire()
	})
	return r
}

// start asks for a command: a session when the target declares sessions, a run otherwise. A run carries stdin; a session is sent it
// by mustStart.
func (r *caseRig) start(stdin []byte) (exechook.Command, error) {
	r.t.Helper()
	r.started++
	id := fmt.Sprintf("case-%d", r.started)
	target, err := wireTarget(r.conns, r.info.Type, r.resource)
	if err != nil {
		r.t.Fatalf("%v", err)
	}
	ctx := r.conns.Context(context.Background())
	if !r.info.Session.IsZero() {
		start := &execv2.Start{Target: target, Tty: r.info.Capabilities.TTY}
		if len(r.info.DefaultArgv) == 0 {
			start.Argv = []string{"x"}
		}
		return r.eng.Open(ctx, &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}})
	}
	argv := r.info.DefaultArgv
	if len(argv) == 0 {
		argv = []string{"x"}
	}
	return r.eng.Run(ctx, &execv2.RunRequest{RequestId: id, Target: target, Argv: argv, Stdin: stdin})
}

// mustStart starts a command that has to be admitted, and sends it its input.
func (r *caseRig) mustStart(stdin []byte) exechook.Command {
	r.t.Helper()
	cmd, err := r.start(stdin)
	if err != nil {
		r.t.Fatalf("start a command on the declared target: %v", err)
	}
	if len(stdin) > 0 && !r.info.Session.IsZero() {
		ctx, cancel := context.WithTimeout(context.Background(), caseBound)
		defer cancel()
		if n, err := cmd.Write(ctx, stdin); err != nil || n != len(stdin) {
			r.t.Fatalf("send %d bytes of input: took %d, %v", len(stdin), n, err)
		}
	}
	return cmd
}

// await waits for the command to end and returns its result.
func (r *caseRig) await(cmd exechook.Command) exechook.Result {
	r.t.Helper()
	select {
	case <-cmd.Done():
	case <-time.After(caseBound):
		r.t.Fatalf("command %q did not end within %v", cmd.ID(), caseBound)
	}
	return cmd.Result()
}

// settle waits until the engine holds no command: an ended command leaves it just after it ends.
func (r *caseRig) settle() {
	r.t.Helper()
	if !poll(caseBound, func() bool { return len(r.eng.Live()) == 0 }) {
		r.t.Fatalf("the engine still holds %v after every command ended", r.eng.Live())
	}
}

// caseReader reads the output of a command, as the one reader it has.
type caseReader struct {
	r    *caseRig
	cmd  exechook.Command
	data []byte
}

func (r *caseRig) reader(cmd exechook.Command) *caseReader { return &caseReader{r: r, cmd: cmd} }

// until reads until n bytes have arrived: a command that wrote its first bytes is running.
func (c *caseReader) until(n int) {
	c.r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	buf := make([]byte, 256)
	for len(c.data) < n {
		chunk, err := c.cmd.Read(ctx, buf)
		c.data = append(c.data, buf[:chunk.N]...)
		if err != nil {
			c.r.t.Fatalf("command %q: reading its first %d bytes: got %q, %v", c.cmd.ID(), n, c.data, err)
		}
	}
}

// toEnd reads the output to its end and returns all of it. Nothing comes after the end: a read once more gives the end again.
func (c *caseReader) toEnd() []byte {
	c.r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	buf := make([]byte, 256)
	for {
		chunk, err := c.cmd.Read(ctx, buf)
		if len(chunk.Dropped) != 0 {
			c.r.t.Errorf("command %q: output was dropped (%v) from a command that wrote a few bytes", c.cmd.ID(), chunk.Dropped)
		}
		c.data = append(c.data, buf[:chunk.N]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			c.r.t.Fatalf("command %q: reading its output: %v", c.cmd.ID(), err)
		}
	}
	chunk, err := c.cmd.Read(ctx, buf)
	if !errors.Is(err, io.EOF) || chunk.N != 0 || len(chunk.Dropped) != 0 {
		c.r.t.Errorf("command %q: a read after the end of the output gave %d bytes, %d dropped ranges and %v: nothing comes after the end",
			c.cmd.ID(), chunk.N, len(chunk.Dropped), err)
	}
	return c.data
}

// ---------------------------------------------------------------------------------------------------------------
// the ends the declaration promises
// ---------------------------------------------------------------------------------------------------------------

// closeSurvival is remote_may_survive of a Closed end by what the kind declares ending a session does: false only when it
// terminates the process, true when it may not, absent when the author did not say. Absent is never read as no.
func closeSurvival(kill exec.KillBehavior) *bool {
	switch kill {
	case exec.KillTerminates:
		return new(false)
	case exec.KillBestEffort, exec.KillClosesStreamOnly:
		return new(true)
	case exec.KillUnspecified:
	}
	return nil
}

func endExited(code int64, source execv2.ExitSource, survive *bool) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Exited{Exited: &execv2.Exited{ExitCode: code, Source: source}}, RemoteMaySurvive: survive}
}

func endSignaled(name string, number int32, survive *bool) *execv2.End {
	return &execv2.End{
		Kind:             &execv2.End_Signaled{Signaled: &execv2.Signaled{Signal: name, SignalNumber: number, Source: execv2.ExitSource_EXIT_SOURCE_PROCESS}},
		RemoteMaySurvive: survive,
	}
}

func endClosed(by execv2.ClosedBy, kill exec.KillBehavior) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Closed{Closed: &execv2.Closed{By: by}}, RemoteMaySurvive: closeSurvival(kill)}
}

func endLost(cause execv2.LostCause) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Lost{Lost: &execv2.Lost{Cause: cause}}, RemoteMaySurvive: new(true)}
}

func endFailed(class execv2.ErrorClass, reason execv2.ErrorReason, stage execv2.FailureStage) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Failed{Failed: &execv2.Failed{Class: class, Reason: reason, Stage: stage}}}
}

// processExit is what a command of the declared kind returns for the status of its process, and the end it becomes. A kind that
// declares no exit report relays no status of the process: its code is the transport's word and the survival is not known.
func processExit(caps exec.Capabilities, code int64) (exec.Exit, *execv2.End) {
	if caps.ExitReport == exec.ExitReportNone {
		return exec.ExitTransport(code), endExited(code, execv2.ExitSource_EXIT_SOURCE_TRANSPORT, nil)
	}
	return exec.ExitCode(code), endExited(code, execv2.ExitSource_EXIT_SOURCE_PROCESS, new(false))
}

// reportsSignals says that the declared kind can report that a signal killed the process.
func reportsSignals(caps exec.Capabilities) bool {
	return caps.ExitReport == exec.ExitReportCodeAndSignal
}

// startsGate is a step that opens the gate when the function begins: the test then knows the function has started and has written
// and reported nothing.
func opensGate(g *Gate) Step {
	return stepFunc(func(context.Context, *exec.Proc) (bool, exec.Exit, error) {
		g.Open()
		return false, exec.Exit{}, nil
	})
}

// endText writes an end on one line, for the message of a failure.
func endText(end *execv2.End) string { return prototext.MarshalOptions{}.Format(end) }

// wantResult is what the result of a command must be.
type wantResult struct {
	end     *execv2.End
	cause   string
	started bool
	output  string // what the function wrote: the result counts exactly these bytes, and the output is exactly this
	bytesIn uint64
}

// check holds the result of an ended command, and what follows its end, to the contract: exactly one end of the arm that was
// expected, the cause, the start as the contract defines it, the counts exact, no byte dropped or refused, the output exactly what
// was written and nothing after it, and an end that does not change when it is asked for again or when the command is ended again.
func (r *caseRig) check(name string, cmd exechook.Command, rd *caseReader, want wantResult) exechook.Result {
	r.t.Helper()
	res := r.await(cmd)
	if _, ok := endArm(res.End); !ok {
		r.t.Errorf("%s: the end has no arm: a command has exactly one end", name)
	} else if !proto.Equal(res.End, want.end) {
		r.t.Errorf("%s: the end is %s, want %s", name, endText(res.End), endText(want.end))
	}
	if res.Cause != want.cause {
		r.t.Errorf("%s: the cause is %q, want %q", name, res.Cause, want.cause)
	}
	if res.StartObserved != want.started {
		r.t.Errorf("%s: start observed is %v, want %v: the start is observed once the function reported, output arrived or the command ended with a status", name, res.StartObserved, want.started)
	}
	if res.BytesOut != uint64(len(want.output)) || res.BytesIn != want.bytesIn {
		r.t.Errorf("%s: %d bytes out and %d in, want %d and %d", name, res.BytesOut, res.BytesIn, len(want.output), want.bytesIn)
	}
	if res.DroppedBytes != 0 || res.LateWrites != 0 || res.Abandoned {
		r.t.Errorf("%s: dropped %d, late writes %d, abandoned %v: a command that behaved loses and refuses nothing", name, res.DroppedBytes, res.LateWrites, res.Abandoned)
	}
	if len(res.Violations) != 0 {
		r.t.Errorf("%s: the engine reports violations of the contract: %v", name, res.Violations)
	}
	if got := string(rd.toEnd()); got != want.output {
		r.t.Errorf("%s: the output is %q, want exactly what was written, %q", name, got, want.output)
	}
	if diff := sameResult(res, cmd.Result()); diff != "" {
		r.t.Errorf("%s: asked for again, the result differs: %s: a command has one end", name, diff)
	}
	cmd.Close(false)
	cmd.Close(true)
	if diff := sameResult(res, cmd.Result()); diff != "" {
		r.t.Errorf("%s: ended again, the result differs: %s: nothing follows the end", name, diff)
	}
	r.settle()
	return res
}

// ---------------------------------------------------------------------------------------------------------------
// case 9: every kind of end is typed, a command has one, and nothing follows it
// ---------------------------------------------------------------------------------------------------------------

// caseEveryEndIsTyped: a command ends in exactly one of the five ways the contract names, each typed as the declaration says, and
// nothing follows its end. The kinds are an exit with and without output, a signal when the kind reports signals, a failure at the
// start and one in the middle, a close by request, the plugin's shutdown, a retired connection and a lost stream; the counts are
// exact, and the start is observed as the contract defines it.
func caseEveryEndIsTyped(t checkT, target exec.Target, env *inProcessEnv) {
	t.Helper()
	info := target.Info
	if !servesCommands(info) {
		t.Skipf("the target declares neither sessions nor runs: no command runs in it")
	}
	caps := info.Capabilities
	exit0, end0 := processExit(caps, 0)
	exit3, end3 := processExit(caps, 3)
	running := []Step{WriteOut("up"), Block()}
	closed := func(by execv2.ClosedBy) *execv2.End { return endClosed(by, caps.Kill) }

	type scenario struct {
		name  string
		steps []Step
		input []byte
		drive func(*caseRig, exechook.Command, *caseReader) // what ends the command; nil: it ends by itself
		want  wantResult
	}
	drivers := map[string]func(*caseRig, exechook.Command, *caseReader){
		"close": func(_ *caseRig, cmd exechook.Command, rd *caseReader) {
			rd.until(2)
			cmd.Close(false)
		},
		"shutdown": func(r *caseRig, _ exechook.Command, rd *caseReader) {
			rd.until(2)
			ctx, cancel := context.WithTimeout(context.Background(), caseBound)
			defer cancel()
			if err := r.eng.Close(ctx); err != nil {
				r.t.Fatalf("close the engine: %v", err)
			}
		},
		"retire": func(r *caseRig, _ exechook.Command, rd *caseReader) {
			rd.until(2)
			r.conns.Retire()
		},
	}
	scenarios := []scenario{
		{name: "exit 0", steps: []Step{EndWith(exit0)}, want: wantResult{end: end0, cause: "finished", started: true}},
		{name: "exit n with output", steps: []Step{WriteOut("out"), EndWith(exit3)},
			want: wantResult{end: end3, cause: "finished", started: true, output: "out"}},
		{name: "input and output counted exactly", steps: []Step{WaitInput(5), WriteOut("hello"), EndWith(exit0)}, input: []byte("12345"),
			want: wantResult{end: end0, cause: "finished", started: true, output: "hello", bytesIn: 5}},
		{name: "a stream that ended with no status", steps: []Step{EndWith(exec.ExitLost(exec.LostNoStatus))},
			want: wantResult{end: endLost(execv2.LostCause_LOST_CAUSE_NO_STATUS), cause: "finished"}},
		{name: "a link that died after output", steps: []Step{WriteOut("tail"), EndWith(exec.ExitLost(exec.LostLinkDead))},
			want: wantResult{end: endLost(execv2.LostCause_LOST_CAUSE_LINK_DEAD), cause: "finished", started: true, output: "tail"}},
		{name: "a failure at the start", steps: []Step{Fail(&exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable})},
			want: wantResult{end: endFailed(execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE, execv2.FailureStage_FAILURE_STAGE_START), cause: "finished"}},
		{name: "a failure in the middle of the run", steps: []Step{WriteOut("x"), Fail(&exec.Error{Class: exec.ErrorClassUnavailable, Reason: exec.ErrorReasonNotRunning})},
			want: wantResult{end: endFailed(execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE, execv2.ErrorReason_ERROR_REASON_NOT_RUNNING, execv2.FailureStage_FAILURE_STAGE_RUN), cause: "finished", started: true, output: "x"}},
		{name: "a close by request", steps: running, drive: drivers["close"],
			want: wantResult{end: closed(execv2.ClosedBy_CLOSED_BY_REQUEST), cause: "requested", started: true, output: "up"}},
		{name: "the plugin's shutdown", steps: running, drive: drivers["shutdown"],
			want: wantResult{end: closed(execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN), cause: "shutdown", started: true, output: "up"}},
		{name: "a retired connection", steps: running, drive: drivers["retire"],
			want: wantResult{end: closed(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED), cause: "retired", started: true, output: "up"}},
	}
	if reportsSignals(caps) {
		scenarios = append(scenarios, scenario{name: "a signal", steps: []Step{EndWith(exec.ExitSignal("SIGKILL", 9))},
			want: wantResult{end: endSignaled("SIGKILL", 9, new(false)), cause: "finished", started: true}})
	} else {
		t.Logf("no signal scenario: the target declares ExitReport %s, and a signal is reported only by a target that declares CODE_AND_SIGNAL", caps.ExitReport)
	}
	for _, sc := range scenarios {
		rig := newCaseRig(t, env, target, sc.steps...)
		cmd := rig.mustStart(sc.input)
		rd := rig.reader(cmd)
		if sc.drive != nil {
			sc.drive(rig, cmd, rd)
		}
		rig.check(sc.name, cmd, rd, sc.want)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// case 14: a retired connection ends its sessions
// ---------------------------------------------------------------------------------------------------------------

// effectiveLimit is how many commands the declared target serves at once: what it declared, or the SDK's default for a limit it left
// at zero.
func effectiveLimit(info exec.TargetInfo) int {
	if !info.Session.IsZero() {
		if info.Session.MaxSessions > 0 {
			return info.Session.MaxSessions
		}
		return exec.DefaultSessionLimits().MaxSessions
	}
	if info.Run.MaxConcurrent > 0 {
		return info.Run.MaxConcurrent
	}
	return exec.DefaultRunLimits().MaxConcurrent
}

// caseRetiredConnection: retiring the connection ends every command that runs through it with Closed by CONNECTION_RETIRED, the
// function has returned by the time the retirement has, and a start on the retired connection is refused.
func caseRetiredConnection(t checkT, target exec.Target, env *inProcessEnv) {
	t.Helper()
	info := target.Info
	if !servesCommands(info) {
		t.Skipf("the target declares neither sessions nor runs: no command runs in it")
	}
	rig := newCaseRig(t, env, target, WriteOut("up"), Block())
	n := min(2, effectiveLimit(info))
	var commands []exechook.Command
	for range n {
		cmd := rig.mustStart(nil)
		rig.reader(cmd).until(2) // the function runs
		commands = append(commands, cmd)
	}
	rig.conns.Retire()
	for i, cmd := range commands {
		res := rig.await(cmd)
		name := fmt.Sprintf("command %d of %d", i+1, n)
		if want := endClosed(execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, info.Capabilities.Kill); !proto.Equal(res.End, want) {
			t.Errorf("%s: the end is %s, want %s", name, endText(res.End), endText(want))
		}
		if res.Cause != "retired" {
			t.Errorf("%s: the cause is %q, want \"retired\"", name, res.Cause)
		}
	}
	if got := rig.returned.Load(); got != int64(n) {
		t.Errorf("%d of %d functions had returned when the retirement and every end were done: the function must have returned", got, n)
	}
	if cmd, err := rig.start(nil); err == nil {
		cmd.Close(true)
		t.Errorf("a command was started on a retired connection")
	}
	rig.settle()
}

// ---------------------------------------------------------------------------------------------------------------
// case 28: remote_may_survive per end kind
// ---------------------------------------------------------------------------------------------------------------

// caseSurvival: remote_may_survive says whether the process may still run, per kind of end: true for a lost stream; false for the
// status of the process itself, an exit or a signal, and for a code the target folded from a death by signal; absent for a code that
// only the transport relayed and for a failure; and for a Closed end false only when the declared Kill is TERMINATES, true for
// BEST_EFFORT and CLOSES_STREAM_ONLY. Absent is a value of its own, and a consumer never reads it as no.
func caseSurvival(t checkT, target exec.Target, env *inProcessEnv) {
	t.Helper()
	info := target.Info
	if !servesCommands(info) {
		t.Skipf("the target declares neither sessions nor runs: no command runs in it")
	}
	caps := info.Capabilities
	type scenario struct {
		name  string
		steps []Step
		close bool // the command is ended by a request after it runs
		arm   string
		want  *bool
	}
	scenarios := []scenario{
		{"a lost stream", []Step{EndWith(exec.ExitLost(exec.LostNoStatus))}, false, "lost", new(true)},
		{"a code only the transport relayed", []Step{EndWith(exec.ExitTransport(0))}, false, "exited", nil},
		{"a failure", []Step{Fail(&exec.Error{Class: exec.ErrorClassUnavailable, Reason: exec.ErrorReasonNotRunning})}, false, "failed", nil},
		{"a failure the SDK classifies", []Step{Fail(errors.New("a failure of the target"))}, false, "failed", nil},
		{"a close by request", []Step{WriteOut("up"), Block()}, true, "closed", closeSurvival(caps.Kill)},
	}
	if caps.ExitReport != exec.ExitReportNone {
		scenarios = append(scenarios,
			scenario{"the process's own exit", []Step{EndWith(exec.ExitCode(0))}, false, "exited", new(false)},
			scenario{"a code folded from a death by signal", []Step{EndWith(exec.ExitFolded(137))}, false, "exited", new(false)})
	} else {
		t.Logf("no process-status scenario: the target declares ExitReport NONE, so any code it returns is the transport's word")
	}
	if reportsSignals(caps) {
		scenarios = append(scenarios, scenario{"a signal", []Step{EndWith(exec.ExitSignal("SIGTERM", 15))}, false, "signaled", new(false)})
	}
	for _, sc := range scenarios {
		rig := newCaseRig(t, env, target, sc.steps...)
		cmd := rig.mustStart(nil)
		if sc.close {
			rig.reader(cmd).until(2)
			cmd.Close(false)
		}
		res := rig.await(cmd)
		if arm, _ := endArm(res.End); arm != sc.arm {
			t.Errorf("%s: the end is %s, want %s", sc.name, armText(res.End), sc.arm)
		}
		if got := res.End.RemoteMaySurvive; (got == nil) != (sc.want == nil) || (got != nil && *got != *sc.want) {
			t.Errorf("%s: remote_may_survive is %s, want %s (the declared Kill is %s, ExitReport %s)",
				sc.name, survivalText(got), survivalText(sc.want), caps.Kill, caps.ExitReport)
		}
		rig.settle()
	}
}

// ---------------------------------------------------------------------------------------------------------------
// pair P5: ending twice, after the exit and during the start give the same final end
// ---------------------------------------------------------------------------------------------------------------

// casePairEnding: a command ended once, ended twice (gracefully or with a kill, in either order), ended after it exited and ended
// while it was starting has one final end each, and the same one as the end that was not repeated: ending again changes nothing.
func casePairEnding(t checkT, target exec.Target, env *inProcessEnv) {
	t.Helper()
	info := target.Info
	if !servesCommands(info) {
		t.Skipf("the target declares neither sessions nor runs: no command runs in it")
	}
	caps := info.Capabilities
	wantClosed := endClosed(execv2.ClosedBy_CLOSED_BY_REQUEST, caps.Kill)

	// Ended once is the end that every other way must give.
	once := newCaseRig(t, env, target, WriteOut("up"), Block())
	cmd := once.mustStart(nil)
	once.reader(cmd).until(2)
	cmd.Close(false)
	want := once.await(cmd)
	if !proto.Equal(want.End, wantClosed) || want.Cause != "requested" {
		t.Errorf("a command ended once: the end is %s cause %q, want %s cause \"requested\"", endText(want.End), want.Cause, endText(wantClosed))
	}

	for _, kills := range [][]bool{{false, false}, {true, true}, {false, true}, {true, false}} {
		rig := newCaseRig(t, env, target, WriteOut("up"), Block())
		cmd := rig.mustStart(nil)
		rig.reader(cmd).until(2)
		for _, kill := range kills {
			cmd.Close(kill)
		}
		got := rig.await(cmd)
		if !proto.Equal(got.End, want.End) || got.Cause != want.Cause || got.StartObserved != want.StartObserved || got.BytesOut != want.BytesOut {
			t.Errorf("ended twice (kill %v): the result is %+v, want the same as ended once: %+v", kills, got, want)
		}
		rig.settle()
	}

	// Ended after it exited: the exit is the end, and no later ending changes it.
	exit7, end7 := processExit(caps, 7)
	after := newCaseRig(t, env, target, WriteOut("done"), EndWith(exit7))
	exited := after.mustStart(nil)
	before := after.await(exited)
	exited.Close(false)
	exited.Close(true)
	later := exited.Result()
	if !proto.Equal(later.End, end7) {
		t.Errorf("ended after its exit: the end is %s, want %s", endText(later.End), endText(end7))
	}
	if diff := sameResult(before, later); diff != "" {
		t.Errorf("ended after its exit, the result changed: %s", diff)
	}
	after.settle()

	// Ended during the start: the function has begun and has reported and written nothing.
	for _, kills := range [][]bool{{false}, {true}, {false, false}, {true, true}, {false, true}} {
		begun := NewGate()
		t.Cleanup(begun.Open)
		rig := newCaseRig(t, env, target, opensGate(begun), Block())
		cmd := rig.mustStart(nil)
		select {
		case <-begun.Opened():
		case <-time.After(caseBound):
			t.Fatalf("the function did not begin within %v", caseBound)
		}
		for _, kill := range kills {
			cmd.Close(kill)
		}
		got := rig.await(cmd)
		if !proto.Equal(got.End, want.End) {
			t.Errorf("ended during the start (kill %v): the end is %s, want the same as ended once, %s", kills, endText(got.End), endText(want.End))
		}
		if got.StartObserved || got.Cause != "requested" {
			t.Errorf("ended during the start (kill %v): start observed %v cause %q, want an unobserved start and cause \"requested\"", kills, got.StartObserved, got.Cause)
		}
		rig.settle()
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the stress entry
// ---------------------------------------------------------------------------------------------------------------

// caseStressEntry runs the stress harness: workers loop open, input, resize, signal, detach, attach, supersede, end, close-stream, run, cancel,
// EndSession and ListSessions against scripted targets on the real engine, read through windows that prove, by offset, that every byte of the
// output was read once or told lost; Close lands at a random moment, and the end-state invariant holds at rest (the registry, the goroutines,
// the last frame of every stream, the cause of every end, the tombstones and their ids). It runs for what Stress runs for by default (one
// second under go test -short, two otherwise, or what OMNIVIEW_LIFECYCLE_HAMMER says), on every platform. It does not use the author's
// target: the harness has the targets it needs, one of each way to behave and to misbehave.
func caseStressEntry(t checkT, _ exec.Target, env *inProcessEnv) {
	t.Helper()
	runStress(t, env.stress, env.hooks)
}
