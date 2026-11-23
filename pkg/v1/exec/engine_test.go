package exec_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// ---------------------------------------------------------------------------------------------------------------
// the Proc the engine builds
// ---------------------------------------------------------------------------------------------------------------

// procView is what a function saw of its Proc when it started.
type procView struct {
	request    exec.Request
	caps       exec.Capabilities
	hasStdin   bool
	hasStderr  bool
	hasStarted bool
	hasLink    bool
	firstSize  exec.Size
	sizeOpen   bool
	connection string
	runningCtx bool
}

// viewProc records what a function sees of its Proc, and the first size on Proc.Resize if one arrives.
func viewProc(ctx context.Context, p *exec.Proc, waitSize bool) procView {
	v := procView{
		request: p.Request, caps: p.Capabilities,
		hasStdin: p.Stdin != nil, hasStderr: p.Stderr != nil, hasStarted: p.Started != nil, hasLink: p.Link != nil,
		runningCtx: ctx.Err() == nil,
	}
	if p.Context != nil && p.Context.Connection != nil {
		v.connection = p.Context.Connection.ID
	}
	if waitSize {
		select {
		case size, ok := <-p.Resize:
			v.firstSize, v.sizeOpen = size, ok
		case <-ctx.Done():
		}
	}
	return v
}

func TestTheFunctionRunsWithAProcBuiltFromTheValidatedRequest(t *testing.T) {
	seen := make(chan procView, 1)
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- viewProc(ctx, p, true)
		return exec.ExitCode(0), nil
	}))
	cmd := rig.mustOpen("s1")
	view := <-seen
	awaitDone(t, cmd)

	if !reflect.DeepEqual(view.request.Argv, []string{"/bin/sh"}) {
		t.Errorf("argv %v, want the target's default", view.request.Argv)
	}
	if !view.request.TTY || view.request.Size != (exec.Size{Cols: 80, Rows: 24}) {
		t.Errorf("request tty %v size %v, want a terminal of 80 x 24", view.request.TTY, view.request.Size)
	}
	if view.request.Ref.Address.Type != "thing" || view.request.Ref.Address.ID != "thing-1" {
		t.Errorf("request reference %+v", view.request.Ref.Address)
	}
	if !view.hasStdin || view.hasStderr || !view.hasStarted || !view.hasLink {
		t.Errorf("stdin %v stderr %v started %v link %v: a terminal has an input and no stderr, and Started and Link are never nil",
			view.hasStdin, view.hasStderr, view.hasStarted, view.hasLink)
	}
	if !view.sizeOpen || view.firstSize != (exec.Size{Cols: 80, Rows: 24}) {
		t.Errorf("the first value of Proc.Resize is %v (open %v), want Request.Size", view.firstSize, view.sizeOpen)
	}
	if !reflect.DeepEqual(view.caps, exectest.NewFakeTarget().Info.Capabilities) {
		t.Errorf("Proc.Capabilities %+v, want the capabilities the session was opened with", view.caps)
	}
	if view.connection != "conn-1" || !view.runningCtx {
		t.Errorf("connection %q, running context %v", view.connection, view.runningCtx)
	}
}

func TestACommandWithoutATerminalHasStderrAndNoInitialSize(t *testing.T) {
	seen := make(chan procView, 1)
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- viewProc(ctx, p, false)
		return exec.ExitCode(0), nil
	}))
	cmd := rig.mustOpen("s1", withoutTTY)
	view := <-seen
	awaitDone(t, cmd)
	if view.request.TTY || !view.request.Size.IsZero() || !view.hasStderr {
		t.Errorf("tty %v size %v stderr %v: a command without a terminal has a stderr and no size", view.request.TTY, view.request.Size, view.hasStderr)
	}
}

func TestARequestedSizeIsTheFirstSizeTheFunctionReads(t *testing.T) {
	seen := make(chan procView, 1)
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- viewProc(ctx, p, true)
		return exec.ExitCode(0), nil
	}))
	cmd := rig.mustOpen("s1", withSize(132, 43))
	view := <-seen
	awaitDone(t, cmd)
	if view.firstSize != (exec.Size{Cols: 132, Rows: 43}) || view.request.Size != view.firstSize {
		t.Errorf("first size %v, request size %v, want 132 x 43", view.firstSize, view.request.Size)
	}
}

// A run's Proc has no input unless the request carries stdin, and no terminal ever.
func TestARunHasAnInputOnlyWhenItsRequestCarriesStdin(t *testing.T) {
	seen := make(chan procView, 2)
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- viewProc(ctx, p, false)
		return exec.ExitCode(0), nil
	}))
	run := func(id string, stdin []byte) procView {
		cmd, err := rig.eng.Run(rig.request(), &execv2.RunRequest{
			RequestId: id, Target: rig.conns.WireTarget(t, "thing", "thing-1"), Argv: []string{"true"}, Stdin: stdin,
			Output: &execv2.OutputLimits{},
		})
		if err != nil {
			t.Fatalf("run %q: %v", id, err)
		}
		awaitDone(t, cmd)
		return <-seen
	}
	if v := run("r-none", nil); v.hasStdin || v.request.TTY || !v.hasStderr {
		t.Errorf("a run without stdin: input %v tty %v stderr %v", v.hasStdin, v.request.TTY, v.hasStderr)
	}
	if v := run("r-stdin", []byte("input")); !v.hasStdin {
		t.Error("a run with stdin has no input")
	}
}

// ---------------------------------------------------------------------------------------------------------------
// refusals: nothing ran
// ---------------------------------------------------------------------------------------------------------------

// refusalOf returns the *exec.Error of err, failing the test when it is not one.
func refusalOf(t testing.TB, err error) *exec.Error {
	t.Helper()
	var refusal *exec.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("the error is %T %v, want an *exec.Error", err, err)
	}
	return refusal
}

// Two starts of one id race: exactly one is admitted and the function never runs for the loser, which is the point of
// deciding the admission before anything native happens.
func TestTwoStartsOfOneIdAdmitExactlyOneAndNeverRunTheFunctionForTheLoser(t *testing.T) {
	const racers = 32
	var calls atomic.Int32
	gate := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		select {
		case <-gate.Opened():
		case <-ctx.Done():
		}
		return exec.ExitCode(0), nil
	}))
	type result struct {
		cmd interface{ ID() string }
		err error
	}
	results := make(chan result, racers)
	start := make(chan struct{})
	for range racers {
		go func() {
			<-start
			cmd, err := rig.open("same")
			results <- result{cmd, err}
		}()
	}
	close(start)
	wins := 0
	for range racers {
		r := <-results
		if r.err == nil {
			wins++
			continue
		}
		if refusal := refusalOf(t, r.err); refusal.Class != exec.ClassConflict || refusal.Reason != exec.ReasonSessionExists {
			t.Errorf("a losing start was refused with %v/%v, want CONFLICT/SESSION_EXISTS", refusal.Class, refusal.Reason)
		}
	}
	gate.Open()
	if wins != 1 {
		t.Fatalf("%d starts of one id were admitted, want exactly 1", wins)
	}
	eventually(t, "the winner to end and be removed", func() bool { return len(rig.eng.Live()) == 0 })
	if got := calls.Load(); got != 1 {
		t.Fatalf("the function ran %d times for %d racers, want once: a refused start must run nothing", got, racers)
	}
}

func TestAFullTargetRefusesAnotherSessionUntilOneEnds(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) {
		i.Session = exec.SessionLimits{MaxSessions: 2}
	}))
	a, b := rig.mustOpen("a"), rig.mustOpen("b")
	_, err := rig.open("c")
	if refusal := refusalOf(t, err); refusal.Class != exec.ClassLimit || refusal.Reason != exec.ReasonTooManySessions {
		t.Fatalf("a third session was refused with %v/%v, want LIMIT/TOO_MANY_SESSIONS", refusal.Class, refusal.Reason)
	}
	a.Close(false)
	awaitDone(t, a)
	eventually(t, "the ended session to be removed", func() bool { return len(rig.eng.Live()) == 1 })
	c := rig.mustOpen("c")
	b.Close(false)
	c.Close(false)
	awaitDone(t, b)
	awaitDone(t, c)
}

func TestARunAndASessionAreCountedApart(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) {
		i.Session = exec.SessionLimits{MaxSessions: 1}
		i.Run = exec.RunLimits{MaxConcurrent: 1}
	}))
	runRequest := func(id string) *execv2.RunRequest {
		return &execv2.RunRequest{RequestId: id, Target: rig.conns.WireTarget(t, "thing", "thing-1"), Argv: []string{"x"}}
	}
	ctx, cancel := context.WithCancel(rig.request())
	defer cancel()
	run, err := rig.eng.Run(ctx, runRequest("r1"))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	session := rig.mustOpen("s1") // the run does not take the session's place
	if _, err := rig.eng.Run(rig.request(), runRequest("r2")); err == nil {
		t.Fatal("a second concurrent run was admitted")
	} else if refusal := refusalOf(t, err); refusal.Class != exec.ClassLimit || refusal.Reason != exec.ReasonTooManyRuns {
		t.Fatalf("a second run was refused with %v/%v, want LIMIT/TOO_MANY_RUNS", refusal.Class, refusal.Reason)
	}
	if _, err := rig.open("s2"); err == nil {
		t.Fatal("a second session was admitted")
	}
	session.Close(false)
	cancel()
	awaitDone(t, session)
	awaitDone(t, run)
}

func TestAStartAfterTheEngineClosedIsRefusedAsUnavailable(t *testing.T) {
	var calls atomic.Int32
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		return exec.ExitCode(0), nil
	}))
	if err := rig.eng.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := rig.eng.Close(context.Background()); err != nil {
		t.Fatalf("a second close: %v", err)
	}
	_, err := rig.open("late")
	if refusal := refusalOf(t, err); refusal.Class != exec.ClassUnavailable {
		t.Fatalf("a start after close was refused with %v, want UNAVAILABLE", refusal.Class)
	}
	if calls.Load() != 0 {
		t.Fatal("the function ran for a start that was refused")
	}
}

func TestAStartWithoutAConnectionRegistryIsRefusedByTheResourceLayer(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{noConnections: true}, engTarget(blockUntilCancelled))
	_, err := rig.open("s1")
	var resourceErr *resourcev2.ResourceOperationError
	if !errors.As(err, &resourceErr) || resourceErr.Code != "UNSUPPORTED" {
		t.Fatalf("open without a connection registry: %v, want the resource lane's UNSUPPORTED", err)
	}
	if live := rig.eng.Live(); len(live) != 0 {
		t.Fatalf("a refused start left %v", live)
	}
}

func TestAStartForATargetThatServesNoSessionsIsRefusedWithNothingRunning(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) {
		i.Session = exec.SessionLimits{}
		i.Run = exec.DefaultRunLimits()
	}))
	_, err := rig.open("s1")
	if refusal := refusalOf(t, err); refusal.Class != exec.ClassUnsupported {
		t.Fatalf("a session on a target that serves only runs: %v, want UNSUPPORTED", refusal.Class)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// options: the selection and the mode
// ---------------------------------------------------------------------------------------------------------------

// engOptions are the options of the thing: a container that is required and has a default, a mode whose choices declare
// the capabilities of the session that takes them (an exec cannot be reached again after a host restart, an attach
// reports no status), and an image that is required and has no default.
func engOptions(withImage bool) []exec.Option {
	caps := exectest.NewFakeTarget().Info.Capabilities
	caps.HostRestart = exec.SurvivalTargetProcess
	execMode, attachMode := caps, caps
	execMode.HostRestart = exec.SurvivalNone
	attachMode.ExitReport = exec.ExitReportNone
	options := []exec.Option{
		{Name: "container", Required: true, Choices: []exec.Choice{
			{Value: "app", State: exec.ChoiceStateReady, IsDefault: true},
			{Value: "sidecar", State: exec.ChoiceStateReady},
		}},
		{Name: "mode", Choices: []exec.Choice{
			{Value: "exec", State: exec.ChoiceStateReady, IsDefault: true, Capabilities: &execMode},
			{Value: "attach", State: exec.ChoiceStateReady, Capabilities: &attachMode},
		}},
	}
	if withImage {
		options = append(options, exec.Option{Name: "image", Required: true, Choices: []exec.Choice{{Value: "debian", State: exec.ChoiceStateReady}}})
	}
	return options
}

func targetWithOptions(fn exec.ExecFunc, options []exec.Option, mods ...func(*exec.TargetInfo)) exec.Target {
	target := engTarget(fn, mods...)
	target.Info.Capabilities.HostRestart = exec.SurvivalTargetProcess
	target.Options = exectest.NewFakeTarget(exectest.FakeStaticOptions(options...)).Options
	return target
}

func TestASelectionThatTheOptionsDoNotOfferIsRefusedAndTheFunctionNeverRuns(t *testing.T) {
	var calls atomic.Int32
	rig := newEngineRig(t, engineRigConfig{}, targetWithOptions(func(context.Context, *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		return exec.ExitCode(0), nil
	}, engOptions(false)))
	for _, tc := range []struct {
		name  string
		mods  []func(*execv2.Start)
		facts exec.ErrorFacts
	}{
		{"an option the resource does not offer", []func(*execv2.Start){withSelection("nope", "x")}, exec.ErrorFacts{SelectionName: "nope"}},
		{"a value the option does not offer", []func(*execv2.Start){withSelection("container", "nope")}, exec.ErrorFacts{SelectionName: "container", SelectionValue: "nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rig.open("s1", tc.mods...)
			refusal := refusalOf(t, err)
			if refusal.Class != exec.ClassInvalidRequest || refusal.Facts != tc.facts {
				t.Fatalf("refused with %v facts %+v, want INVALID_REQUEST with %+v", refusal.Class, refusal.Facts, tc.facts)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("the function ran for a refused selection")
	}
}

func TestARequiredOptionWithNoDefaultMustBeSelected(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, targetWithOptions(blockUntilCancelled, engOptions(true)))
	_, err := rig.open("s1")
	if refusal := refusalOf(t, err); refusal.Class != exec.ClassInvalidRequest || refusal.Facts.SelectionName != "image" {
		t.Fatalf("a request that omits the required image: %+v, want INVALID_REQUEST naming it", refusal)
	}
	cmd := rig.mustOpen("s2", withSelection("image", "debian"))
	cmd.Close(false)
	awaitDone(t, cmd)
}

func TestATargetWithNoOptionsRefusesAnySelection(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	_, err := rig.open("s1", withSelection("container", "app"))
	if refusal := refusalOf(t, err); refusal.Class != exec.ClassInvalidRequest {
		t.Fatalf("a selection on a target with no options: %v, want INVALID_REQUEST", refusal.Class)
	}
}

// The mode a selection names is in the capabilities the session was opened with, and in the ones the function starts from.
func TestTheSessionIsOpenedWithTheCapabilitiesOfTheChoiceTheSelectionNames(t *testing.T) {
	seen := make(chan exec.Capabilities, 4)
	rig := newEngineRig(t, engineRigConfig{}, targetWithOptions(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- p.Capabilities
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, engOptions(false)))
	for i, tc := range []struct {
		name        string
		mods        []func(*execv2.Start)
		exitReport  execv2.ExitReport
		hostRestart execv2.Survival
	}{
		{"exec, named", []func(*execv2.Start){withSelection("mode", "exec")}, execv2.ExitReport_EXIT_REPORT_CODE_AND_SIGNAL, execv2.Survival_SURVIVAL_NONE},
		{"attach, named", []func(*execv2.Start){withSelection("mode", "attach")}, execv2.ExitReport_EXIT_REPORT_NONE, execv2.Survival_SURVIVAL_TARGET_PROCESS},
		{"the default choice of an option the selection omits", nil, execv2.ExitReport_EXIT_REPORT_CODE_AND_SIGNAL, execv2.Survival_SURVIVAL_NONE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := rig.mustOpen("mode-"+strconv.Itoa(i), tc.mods...)
			defer func() { cmd.Close(false); awaitDone(t, cmd) }()
			opened := cmd.Opened()
			if opened.GetExitReport() != tc.exitReport || opened.GetHostRestart() != tc.hostRestart {
				t.Errorf("opened with exit report %v and host restart %v, want %v and %v",
					opened.GetExitReport(), opened.GetHostRestart(), tc.exitReport, tc.hostRestart)
			}
			fn := <-seen
			if fn.ExitReport.String() != tc.exitReport.String()[len("EXIT_REPORT_"):] {
				t.Errorf("the function starts from exit report %v, want %v", fn.ExitReport, tc.exitReport)
			}
		})
	}
}

func TestAnOptionsCallbackThatFailsOrPanicsIsARefusalAndNeverTakesThePluginDown(t *testing.T) {
	canary := "CANARY-options-secret-77e1"
	cases := []struct {
		name    string
		options exec.OptionsFunc
		want    exec.ErrorClass
		reason  exec.ErrorReason
	}{
		{"an error the target classifies", func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
			return nil, errors.New(canary + " pod not found")
		}, exec.ClassNotFound, exec.ReasonTargetGone},
		{"a panic", func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
			panic(canary)
		}, exec.ClassInternal, exec.ReasonUnspecified},
		{"options that break their contract", func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
			return []exec.Option{{Name: "Not A Closed Code"}}, nil
		}, exec.ClassInternal, exec.ReasonUnspecified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			target := engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
				calls.Add(1)
				return exec.ExitCode(0), nil
			})
			target.Options = tc.options
			target.Classify = func(err error) (*exec.Error, bool) {
				if err != nil && engContainsCanary("pod not found", err.Error()) {
					return exec.NewError(exec.ClassNotFound, exec.ReasonTargetGone, err), true
				}
				return nil, false
			}
			rig := newEngineRig(t, engineRigConfig{}, target)
			_, err := rig.open("s1")
			refusal := refusalOf(t, err)
			if refusal.Class != tc.want || refusal.Reason != tc.reason {
				t.Fatalf("refused with %v/%v, want %v/%v", refusal.Class, refusal.Reason, tc.want, tc.reason)
			}
			if calls.Load() != 0 {
				t.Fatal("the function ran for a refused start")
			}
			if live := rig.eng.Live(); len(live) != 0 {
				t.Fatalf("a refused start left %v", live)
			}
		})
	}
}
