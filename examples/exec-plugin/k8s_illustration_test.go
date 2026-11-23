package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// This file is the Kubernetes illustration of docs/exec.md, kept compiling and tested. It is written against a tiny
// stand-in for the parts of client-go it uses, so that the example needs no Kubernetes dependency. Each stand-in is
// the client-go type of the same role: streamOptions is remotecommand.StreamOptions, terminalSize is
// remotecommand.TerminalSize, sizeSource is remotecommand.TerminalSizeQueue, executor is remotecommand.Executor and
// codeExitError is k8s.io/client-go/util/exec.CodeExitError.

type terminalSize struct{ Width, Height uint16 }

type sizeSource interface{ Next() *terminalSize }

type streamOptions struct {
	Stdin             io.Reader
	Stdout, Stderr    io.Writer
	Tty               bool
	TerminalSizeQueue sizeSource
}

type codeExitError struct{ Code int }

func (e codeExitError) Error() string {
	return fmt.Sprintf("command terminated with exit code %d", e.Code)
}

type executor interface {
	StreamWithContext(ctx context.Context, opts streamOptions) error
}

// executorFor builds the pods/exec or the pods/attach request of the Pod the request names, with the client of the
// connection the plugin leased (p.Context), and wraps it in an executor. Here it fails until a test scripts it.
var executorFor = func(*exec.Proc, bool) (executor, error) { return nil, errors.New("no cluster") }

// guide:begin podExec
// podCapabilities is what a Pod can do at most: the upper bound of every session of the kind, which the two modes of a
// Pod narrow in different fields (see podOptions).
func podCapabilities() exec.Capabilities {
	return exec.Capabilities{
		TTY: true, SeparateStderr: true, Resize: true, InitialSize: true, StdinEOF: true,
		Kill: exec.KillClosesStreamOnly, ExitReport: exec.ExitReportCode, Stall: exec.StallLossy,
		HostRestart: exec.SurvivalTargetProcess, // attach reaches the container's own process again
		Integration: []exec.IntegrationMode{exec.IntegrationArgvWrapper},
	}
}

// podTarget is a Pod: one kind that serves two modes, exec (a new process) and attach (the container's own).
func podTarget() exec.Target {
	return exec.Target{
		Info: exec.TargetInfo{
			Provider: "k8s", Type: "pods", DisplayName: "Pod", DefaultArgv: []string{"/bin/sh"},
			Capabilities: podCapabilities(), // the upper bound: the most any session of this kind can do
			Session:      exec.DefaultSessionLimits(),
		},
		Exec: podExec,
	}
}

func podExec(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	// p.Capabilities is what the host read in Opened: the kind's upper bound narrowed by the mode this request chose
	// (the Capabilities of the choice, see podOptions). The mode is known before this function runs, so it is not
	// reported here, and nothing else is learned at run time, so Started is not called. client-go has no callback for
	// "the command is running"; the start this adapter observes is the first byte the command writes, and the SDK sends
	// the session's Started before it, made from the facts of Opened. A command that stays silent has none.
	attach := p.Request.Selection["mode"] == "attach" // the "mode" option of the target's Options
	run, err := executorFor(p, attach)
	if err != nil {
		return exec.Exit{}, err // Target.Classify maps "container not running", RBAC 403, "no such file"
	}
	opts := streamOptions{Stdin: p.Stdin, Stdout: p.Stdout, Stderr: p.Stderr, Tty: p.Request.TTY,
		TerminalSizeQueue: sizeQueue{p.Resize}} // a terminal has no Stderr: it is nil, which client-go reads as none
	err = run.StreamWithContext(ctx, opts)
	var code codeExitError
	switch {
	case !attach && errors.As(err, &code):
		// The API reports a code and nothing about where it came from: 137 can be a SIGKILL or a plain exit 137, so
		// no signal is inferred from the range. ExitFolded is only for a target that says a code is a folded signal.
		return exec.ExitCode(code.Code), nil
	case err == nil:
		// client-go returns nil both for a Success status and for a status channel that closed empty, so nil says
		// nothing about the exit: the stream ended and the command's status is not known. It is never exit 0.
		// (A plugin that reads the status channel itself can return ExitCode(0) for a Success status.)
		return exec.ExitLost(exec.LostNoStatus), nil
	default:
		return exec.Exit{}, err
	}
}

// sizeQueue hands Proc.Resize to client-go: Next waits for the next size and returns nil when the command has ended.
type sizeQueue struct{ resize <-chan exec.Size }

func (q sizeQueue) Next() *terminalSize {
	size, ok := <-q.resize
	if !ok {
		return nil
	}
	return &terminalSize{Width: size.Cols, Height: size.Rows}
}

// guide:end podExec

// safety bounds a wait that the test expects an event to end: it never decides an outcome, it only turns a hang into a
// failure.
const safety = 60 * time.Second

// script is an executor whose stream ends as the test says.
type script func(context.Context, streamOptions) error

func (s script) StreamWithContext(ctx context.Context, opts streamOptions) error { return s(ctx, opts) }

// podRig is a rig of a Pod command in a mode: the kind's declared capabilities, and the options that podOptions returns
// for the Pod, which the rig resolves the request's selection against, as the SDK does when it admits the request.
func podRig(t testing.TB, mode string) *exectest.ProcRig {
	t.Helper()
	options, err := podOptions(nil, resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{ID: "web-0"}})
	if err != nil {
		t.Fatal(err)
	}
	return exectest.NewProcRig(t,
		exec.Request{Argv: []string{"/bin/sh"}, Selection: map[string]string{"mode": mode}},
		exectest.RigCapabilities(podCapabilities()), exectest.RigOptions(options...))
}

// podRun is what a function did on one command in the rig: how it ended, the Started the host would have received, and
// the capabilities the session was opened with and ended up with.
type podRun struct {
	exit      exec.Exit
	err       error
	facts     []exec.SessionFacts
	opened    exec.Capabilities
	effective exec.Capabilities
}

// runIn runs fn in a rig of the Pod kind: the Proc it receives carries the capabilities of the session as it was opened,
// and the rig holds what fn reports with Proc.Started to the SDK's rule: a report that widens them, or that comes late or
// twice, is told to t.
func runIn(t testing.TB, mode string, fn exec.ExecFunc) podRun {
	t.Helper()
	rig := podRig(t, mode)
	_ = rig.Input().Close() // the host sent no input: a read of Proc.Stdin returns io.EOF at once
	var run podRun
	run.exit, run.err = rig.Run(context.Background(), fn)
	run.facts, run.opened, run.effective = rig.Started(), rig.Opened(), rig.Effective()
	return run
}

// runStream runs podExec against a cluster whose stream is stream.
func runStream(t testing.TB, mode string, stream script) podRun {
	t.Helper()
	previous := executorFor
	defer func() { executorFor = previous }()
	executorFor = func(*exec.Proc, bool) (executor, error) { return stream, nil }
	return runIn(t, mode, podExec)
}

// runPod runs podExec against a cluster whose command writes a byte and then its stream ends with streamErr.
func runPod(t testing.TB, mode string, streamErr error) podRun {
	t.Helper()
	return runStream(t, mode, func(_ context.Context, opts streamOptions) error {
		_, _ = opts.Stdout.Write([]byte("x")) // the command ran: its first output is what the adapter observes
		return streamErr
	})
}

// A real exit code is reported as it is, from the process, and no signal is inferred from its range.
func TestPodExecReportsTheCodeTheAPIGave(t *testing.T) {
	for _, code := range []int{1, 3, 137} {
		run := runPod(t, "exec", codeExitError{Code: code})
		if run.err != nil || run.exit.Code == nil || *run.exit.Code != int64(code) || run.exit.Source != exec.SourceProcess {
			t.Errorf("code %d: exit = %+v, err = %v; want ExitCode(%d)", code, run.exit, run.err, code)
		}
		if run.exit.Signal != "" || run.exit.SignalNumber != 0 || run.exit.Lost != exec.LostUnspecified {
			t.Errorf("code %d: exit = %+v claims a signal or a loss", code, run.exit)
		}
	}
}

// A stream that ends with no status is Lost: never ExitTransport(0), never ExitCode(0).
func TestPodExecNeverTurnsASilentEndIntoAnExit(t *testing.T) {
	for _, mode := range []string{"exec", "attach"} {
		run := runPod(t, mode, nil)
		if run.err != nil || run.exit.Code != nil || run.exit.Lost != exec.LostNoStatus || run.exit.Signal != "" {
			t.Errorf("%s: exit = %+v, err = %v; want ExitLost(LostNoStatus) and nothing else", mode, run.exit, run.err)
		}
	}
}

// A failure of the API is an error for the SDK to classify, not an exit.
func TestPodExecFailureIsAnError(t *testing.T) {
	boom := errors.New("pods/exec forbidden")
	run := runPod(t, "exec", boom)
	if !errors.Is(run.err, boom) || run.exit.Code != nil || run.exit.Lost != exec.LostUnspecified {
		t.Errorf("exit = %+v, err = %v; want the error and an empty Exit", run.exit, run.err)
	}
}

// What the guide says of the two modes: an exec has an exit code and cannot be reached again after a host restart, an
// attach has no status and can. Each is declared on its choice, so the session is opened with it, and the two are
// opened differently in exactly those two fields. The kind itself, the upper bound of both, is not changed by either.
func TestPodModesAreOpenedWithTheCapabilitiesOfTheirChoices(t *testing.T) {
	kind := podCapabilities()
	execOpened, attachOpened := podRig(t, "exec").Opened(), podRig(t, "attach").Opened()

	wantExec := kind
	wantExec.HostRestart = exec.SurvivalNone
	wantAttach := kind
	wantAttach.ExitReport = exec.ExitReportNone
	if !reflect.DeepEqual(execOpened, wantExec) {
		t.Errorf("exec is opened with %+v, want the kind's with HostRestart none %+v", execOpened, wantExec)
	}
	if !reflect.DeepEqual(attachOpened, wantAttach) {
		t.Errorf("attach is opened with %+v, want the kind's with ExitReport none %+v", attachOpened, wantAttach)
	}
	if execOpened.ExitReport != exec.ExitReportCode || attachOpened.HostRestart != exec.SurvivalTargetProcess {
		t.Errorf("each mode keeps what the other narrows: exec ExitReport %v, attach HostRestart %v", execOpened.ExitReport, attachOpened.HostRestart)
	}
	if after := podTarget().Info.Capabilities; !reflect.DeepEqual(after, kind) {
		t.Errorf("the kind's capabilities changed: %+v", after)
	}
}

// A Pod whose container is quiet, run in each mode: it writes nothing and calls no Started, so the host receives no
// Started at all, yet the session was opened with its mode. The mode does not wait for the command to be observed: it is
// in Opened, which is sent before the function runs, and it is what the function started from.
func TestAQuietPodIsOpenedWithItsModeAndReportsNothing(t *testing.T) {
	opened := map[string]exec.Capabilities{}
	for _, mode := range []string{"exec", "attach"} {
		rig := podRig(t, mode)
		_ = rig.Input().Close()
		started := rig.Proc().Capabilities // what the function starts from
		previous := executorFor
		executorFor = func(*exec.Proc, bool) (executor, error) {
			return script(func(context.Context, streamOptions) error { return nil }), nil // a quiet container, which ends
		}
		_, err := rig.Run(context.Background(), podExec)
		executorFor = previous
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if got := rig.Started(); len(got) != 0 {
			t.Errorf("%s: a quiet command has a Started: %+v", mode, got)
		}
		opened[mode] = rig.Opened()
		if !reflect.DeepEqual(started, opened[mode]) || !reflect.DeepEqual(rig.Effective(), opened[mode]) {
			t.Errorf("%s: the function started from %+v and ended with %+v, want the opened set %+v", mode, started, rig.Effective(), opened[mode])
		}
	}

	var differ []string
	execValue, attachValue := reflect.ValueOf(opened["exec"]), reflect.ValueOf(opened["attach"])
	for i := range execValue.NumField() {
		if !reflect.DeepEqual(execValue.Field(i).Interface(), attachValue.Field(i).Interface()) {
			differ = append(differ, execValue.Type().Field(i).Name)
		}
	}
	if !reflect.DeepEqual(differ, []string{"HostRestart", "ExitReport"}) {
		t.Errorf("a quiet exec and a quiet attach are opened with different %v, want HostRestart and ExitReport", differ)
	}
	if opened["exec"].HostRestart != exec.SurvivalNone || opened["exec"].ExitReport != exec.ExitReportCode ||
		opened["attach"].HostRestart != exec.SurvivalTargetProcess || opened["attach"].ExitReport != exec.ExitReportNone {
		t.Errorf("exec is %+v and attach is %+v, want what the guide says of them", opened["exec"], opened["attach"])
	}
}

// A Pod whose container writes: the host receives exactly one Started, and it is there when the first byte is, on
// whichever stream the byte is. It carries the capabilities of the session as it was opened (the mode included), the
// requested program, and nothing the adapter did not observe. The output that follows makes no second one.
func TestAPodThatWritesIsGivenExactlyOneStartedBeforeItsFirstByte(t *testing.T) {
	for _, mode := range []string{"exec", "attach"} {
		for _, stream := range []string{"stdout", "stderr"} {
			t.Run(mode+" "+stream, func(t *testing.T) {
				rig := podRig(t, mode)
				_ = rig.Input().Close()
				written, proceed := make(chan struct{}), exectest.NewGate()
				t.Cleanup(proceed.Open)
				previous := executorFor
				t.Cleanup(func() { executorFor = previous })
				executorFor = func(*exec.Proc, bool) (executor, error) {
					return script(func(_ context.Context, opts streamOptions) error {
						w := opts.Stdout
						if stream == "stderr" {
							w = opts.Stderr
						}
						_, _ = w.Write([]byte("a")) // the first output, from client-go's own goroutine in the real thing
						close(written)
						<-proceed.Opened()
						_, _ = w.Write([]byte("b"))
						return nil // the stream ends: client-go reports no status for an attach, and an empty one for an exec
					}), nil
				}
				ended := make(chan error, 1)
				go func() { _, err := rig.Run(context.Background(), podExec); ended <- err }()

				select {
				case <-written:
				case err := <-ended:
					t.Fatalf("the command ended before it wrote: %v", err)
				case <-time.After(safety):
					t.Fatal("the command did not write")
				}
				got := rig.Started()
				if len(got) != 1 {
					t.Fatalf("the host receives %d Started frames when the first byte is there, want one: %+v", len(got), got)
				}
				if got[0].Capabilities == nil || !reflect.DeepEqual(*got[0].Capabilities, rig.Opened()) {
					t.Errorf("the Started carries %+v, want the opened capabilities %+v", got[0].Capabilities, rig.Opened())
				}
				if !reflect.DeepEqual(got[0].Program.Argv, []string{"/bin/sh"}) || got[0].PID != 0 || got[0].TTY != nil || got[0].Transport != "" {
					t.Errorf("the Started claims more than the facts of Opened: %+v", got[0])
				}

				proceed.Open()
				if err := <-ended; err != nil {
					t.Fatal(err)
				}
				if again := rig.Started(); len(again) != 1 {
					t.Errorf("later output made a Started: %d in all", len(again))
				}
			})
		}
	}
}

// An author who still reports a Started is held to the rule the SDK enforces, so the illustration needs no gate of its
// own: a report that comes after the first byte, and a second one, are refused and told to the test, and the host
// receives one Started either way. The late one leaves the session's capabilities as they were opened; the second leaves
// the first's.
func TestALateAndASecondStartedAreRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		fn        exec.ExecFunc
		says      string
		transport string // of the Started the host receives
	}{
		"late": {func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			_, _ = p.Stdout.Write([]byte("x"))                   // the command ran: the SDK sent the session's Started with this byte
			p.Started(exec.SessionFacts{Transport: "k8s-ws-v5"}) // too late
			return exec.ExitLost(exec.LostNoStatus), nil
		}, "after the first output", ""},
		"second": {func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			p.Started(exec.SessionFacts{Transport: "k8s-ws-v5"})
			p.Started(exec.SessionFacts{Transport: "again"})
			return exec.ExitLost(exec.LostNoStatus), nil
		}, "twice", "k8s-ws-v5"},
	} {
		t.Run(name, func(t *testing.T) {
			told := &recordedT{TB: t}
			run := runIn(told, "attach", tc.fn)
			if got := told.told(); len(got) != 1 || !strings.Contains(got[0], tc.says) {
				t.Errorf("the test was told %q, want one thing: %s", got, tc.says)
			}
			if len(run.facts) != 1 || run.facts[0].Transport != tc.transport {
				t.Errorf("the host receives %+v, want one Started, with transport %q", run.facts, tc.transport)
			}
			if !reflect.DeepEqual(run.effective, run.opened) {
				t.Errorf("a refused report changed the session's capabilities: %+v, opened with %+v", run.effective, run.opened)
			}
		})
	}
}

// recordedT is a testing.TB that keeps what is reported to Errorf instead of failing the test, so that a test of a
// session that breaks the contract can look at what the rig told it. Everything else goes to the real test.
type recordedT struct {
	testing.TB
	mu     sync.Mutex
	errors []string
}

func (r *recordedT) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recordedT) told() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errors...)
}

// A session that WIDENS what it was opened with is told to the test, as the SDK will log it, and keeps the opened values.
// The mode is not the author's to change at run time: an exec that reports the kind's own HostRestart, which only an
// attach has, is widening its mode, and so is a session that claims more than the kind itself declares.
func TestPodSessionThatWidensItsModeIsReported(t *testing.T) {
	greedy := func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		caps := p.Capabilities
		caps.ExitReport = exec.ExitReportCodeAndSignal // above the kind's Code
		caps.HostRestart = exec.SurvivalTargetProcess  // the kind's, and above an exec's none
		caps.Kill = exec.KillBestEffort                // above the kind's ClosesStreamOnly
		caps.Stall = exec.StallPropagates              // above the kind's Lossy
		p.Started(exec.SessionFacts{Capabilities: &caps})
		return exec.ExitLost(exec.LostNoStatus), nil
	}
	told := &recordedT{TB: t}
	run := runIn(told, "exec", greedy)

	got := told.told()
	for _, want := range []string{"ExitReport", "HostRestart", "Kill", "Stall"} {
		found := false
		for _, message := range got {
			found = found || strings.Contains(message, want+": reported")
		}
		if !found {
			t.Errorf("the rig did not tell the test that %s was widened: %q", want, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("the rig told the test %d things, want exactly the four widened fields: %q", len(got), got)
	}
	if !reflect.DeepEqual(run.effective, run.opened) {
		t.Errorf("a widening was not clamped to what the session was opened with: %+v, opened with %+v", run.effective, run.opened)
	}
	if run.effective.HostRestart != exec.SurvivalNone {
		t.Errorf("an exec session is effectively HostRestart %v, want none: the mode was reported back up", run.effective.HostRestart)
	}
}

// Next waits for a size and ends with the command.
func TestPodSizeQueue(t *testing.T) {
	resize := make(chan exec.Size, 1)
	queue := sizeQueue{resize}
	resize <- exec.Size{Cols: 80, Rows: 24}
	if got := queue.Next(); got == nil || *got != (terminalSize{Width: 80, Height: 24}) {
		t.Errorf("Next = %+v, want 80x24", got)
	}
	close(resize)
	if got := queue.Next(); got != nil {
		t.Errorf("Next after the command ended = %+v, want nil", got)
	}
}

// The illustration registers: the declared kind passes the SDK's checks and the conformance suite.
func TestPodTargetConforms(t *testing.T) {
	if _, err := exec.NewTargetPlugin([]exec.Target{podTarget()}); err != nil {
		t.Fatal(err)
	}
	exectest.Conformance(t, podTarget())
}

// The guide prints the illustration as it is in this file.
func TestTheGuideShowsTheKubernetesIllustration(t *testing.T) {
	assertGuideShows(t, "k8s_illustration_test.go", "podExec")
}
