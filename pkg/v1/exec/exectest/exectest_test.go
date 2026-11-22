package exectest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

const safety = 60 * time.Second

func TestAScriptedTargetWritesWaitsAndEndsInTheScriptedWay(t *testing.T) {
	gate := exectest.NewGate()
	target := exectest.NewFakeTarget(exectest.FakeScript(
		exectest.ReportStarted(exec.SessionFacts{Transport: "fake"}),
		exectest.WriteChunks([]byte("hello world"), 4),
		exectest.WaitGate(gate),
		exectest.WaitInput(3),
		exectest.EndWith(exec.ExitCode(7)),
	))
	rig := exectest.NewProcRig(t, exec.Request{Argv: []string{"x"}})
	var (
		exit exec.Exit
		err  error
	)
	runCtx, stopRun := context.WithCancel(context.Background())
	// A script that stops reading or waits for a byte that never comes must fail the test, not hang it: whatever
	// the way out, the script is cancelled, its gate opened and its input closed, which releases a blocked write.
	t.Cleanup(func() { stopRun(); gate.Open(); _ = rig.Input().Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		exit, err = rig.Run(runCtx, target.Exec)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), safety)
	defer cancel()
	out, werr := rig.WaitStdout(ctx, len("hello world"))
	if werr != nil || string(out) != "hello world" {
		t.Fatalf("output %q, %v", out, werr)
	}
	if got := rig.Started(); len(got) != 1 || got[0].Transport != "fake" {
		t.Errorf("started = %+v", got)
	}
	select {
	case <-done:
		t.Fatal("the script ended before its gate opened")
	default:
	}
	gate.Open()
	written := make(chan error, 1)
	go func() { _, werr := rig.Input().Write([]byte("abc")); written <- werr }()
	select {
	case werr := <-written:
		if werr != nil {
			t.Fatal(werr)
		}
	case <-ctx.Done():
		t.Fatal("the script did not read its input")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the script did not end after its input")
	}
	if err != nil || exit.Code == nil || *exit.Code != 7 {
		t.Errorf("exit = %+v, err = %v", exit, err)
	}
}

func TestAScriptedTargetEndsWhenItsCommandIsCancelled(t *testing.T) {
	// The marker is written before the script blocks: once the test has seen it the command is active, so the cancel
	// below ends a running command and not one that has not started.
	target := exectest.NewFakeTarget(exectest.FakeScript(exectest.WriteOut("started"), exectest.Block()))
	rig := exectest.NewProcRig(t, exec.Request{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := rig.Run(ctx, target.Exec); done <- err }()

	safetyCtx, stop := context.WithTimeout(context.Background(), safety)
	defer stop()
	if _, err := rig.WaitStdout(safetyCtx, len("started")); err != nil {
		t.Fatalf("the script did not start: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("the script ended before it was cancelled: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want the context's", err)
		}
	case <-safetyCtx.Done():
		t.Fatal("the cancelled command did not end")
	}
}

func TestAFakeTargetWritesItsWholeScriptOnEveryExec(t *testing.T) {
	const runs = 8
	target := exectest.NewFakeTarget(exectest.FakeScript(exectest.WriteChunks([]byte("abcdefgh"), 3)))
	wholeRun := func(rig *exectest.ProcRig) error {
		if exit, err := rig.Run(context.Background(), target.Exec); err != nil || exit.Code == nil || *exit.Code != 0 {
			return fmt.Errorf("exit = %+v, err = %v", exit, err)
		}
		if got := rig.Stdout(); string(got) != "abcdefgh" {
			return fmt.Errorf("wrote %q, want the whole data", got)
		}
		return nil
	}

	for i := range 2 {
		if err := wholeRun(exectest.NewProcRig(t, exec.Request{})); err != nil {
			t.Fatalf("sequential run %d: %v", i+1, err)
		}
	}

	rigs := make([]*exectest.ProcRig, runs)
	for i := range rigs {
		rigs[i] = exectest.NewProcRig(t, exec.Request{})
	}
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := range rigs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = wholeRun(rigs[i]) }()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent run %d: %v", i+1, err)
		}
	}
}

func TestAScriptedFailureAndPanicAreWhatTheAuthorsFunctionReturns(t *testing.T) {
	boom := errors.New("boom")
	failing := exectest.NewFakeTarget(exectest.FakeScript(exectest.Fail(boom)))
	if _, err := exectest.NewProcRig(t, exec.Request{}).Run(context.Background(), failing.Exec); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	panicking := exectest.NewFakeTarget(exectest.FakeScript(exectest.Panic("kaboom")))
	if _, err := exectest.NewProcRig(t, exec.Request{}).Run(context.Background(), panicking.Exec); err == nil {
		t.Error("a panic of the author's function was not reported")
	}
	empty := exectest.NewFakeTarget()
	if exit, err := exectest.NewProcRig(t, exec.Request{}).Run(context.Background(), empty.Exec); err != nil || exit.Code == nil || *exit.Code != 0 {
		t.Errorf("a script that runs out of steps ends with exit 0: %+v %v", exit, err)
	}
}

func TestTheRigsResizeIsPrimedAndLatestWins(t *testing.T) {
	rig := exectest.NewProcRig(t, exec.Request{TTY: true, Size: exec.Size{Cols: 80, Rows: 24}})
	if rig.Proc().Stderr != nil {
		t.Error("a terminal has no separate stderr")
	}
	rig.Resize(exec.Size{Cols: 100, Rows: 30})
	rig.Resize(exec.Size{Cols: 120, Rows: 40})
	if got := <-rig.Proc().Resize; got != (exec.Size{Cols: 120, Rows: 40}) {
		t.Errorf("first size = %+v: the newest pending size replaces older ones, the primed one included", got)
	}
	select {
	case extra := <-rig.Proc().Resize:
		t.Errorf("a second size %+v was queued", extra)
	default:
	}
}

// The routing key of a fake target is its Info.Provider and Info.Type: FakeRouting sets it, a description given with
// FakeInfo may carry its own, and the order of the options does not matter.
func TestAFakeTargetIsRoutedByItsInfo(t *testing.T) {
	routed := func(target exec.Target) [2]string { return [2]string{target.Info.Provider, target.Info.Type} }

	if got := routed(exectest.NewFakeTarget()); got != [2]string{"fake", "thing"} {
		t.Errorf("the default routing key is %v", got)
	}
	if got := routed(exectest.NewFakeTarget(exectest.FakeRouting("k8s", "pods"))); got != [2]string{"k8s", "pods"} {
		t.Errorf("FakeRouting: %v", got)
	}
	info := exec.TargetInfo{Provider: "docker", Type: "containers", Capabilities: exec.Capabilities{Kill: exec.KillTerminates}}
	if got := routed(exectest.NewFakeTarget(exectest.FakeInfo(info))); got != [2]string{"docker", "containers"} {
		t.Errorf("a description with its own routing key: %v", got)
	}
	for name, opts := range map[string][]exectest.FakeTargetOption{
		"routing before the description": {exectest.FakeRouting("k8s", "pods"), exectest.FakeInfo(info)},
		"routing after the description":  {exectest.FakeInfo(info), exectest.FakeRouting("k8s", "pods")},
	} {
		if got := routed(exectest.NewFakeTarget(opts...)); got != [2]string{"k8s", "pods"} {
			t.Errorf("%s: %v: FakeRouting wins over the description, whatever the order", name, got)
		}
	}
	bare := exec.TargetInfo{Capabilities: exec.Capabilities{Kill: exec.KillTerminates}}
	if got := routed(exectest.NewFakeTarget(exectest.FakeInfo(bare))); got != [2]string{"fake", "thing"} {
		t.Errorf("a description with no routing key: %v: the default fills it", got)
	}
	if got := routed(exectest.NewFakeTarget(exectest.FakeInfo(bare), exectest.FakeRouting("k8s", "pods"))); got != [2]string{"k8s", "pods"} {
		t.Errorf("a description with no routing key, routed: %v", got)
	}
}

// The rig builds the Proc.Capabilities the SDK would hand to a command, and each rig's copy is its own: a function that
// narrows it changes nothing for the next rig or for the fake target's declaration.
func TestTheRigGivesTheCommandItsOwnCopyOfTheCapabilities(t *testing.T) {
	declared := exectest.NewFakeTarget().Info.Capabilities
	first := exectest.NewProcRig(t, exec.Request{})
	got := first.Proc().Capabilities
	if !got.TTY || got.Kill != exec.KillTerminates || got.ExitReport != exec.ExitReportCodeAndSignal || len(got.Signals) != 3 {
		t.Errorf("the rig's capabilities are not the default fake target's: %+v", got)
	}
	got.Signals[0] = exec.SignalHup
	got.Kill = exec.KillClosesStreamOnly
	if again := exectest.NewProcRig(t, exec.Request{}).Proc().Capabilities; again.Signals[0] != exec.SignalInt || again.Kill != exec.KillTerminates {
		t.Errorf("a rig shares its capabilities with another: %+v", again)
	}
	if declared.Signals[0] != exec.SignalInt {
		t.Errorf("the rig shares its capabilities with the fake target's declaration: %+v", declared)
	}
}

// What the command reports with Proc.Started, before it writes, is what the host receives in the session's Started: the
// SessionFacts the author builds, as it built them, with the capabilities of the session in them (here the ones it was
// opened with, because the report narrowed nothing).
func TestTheRigRecordsTheSessionFactsTheCommandReported(t *testing.T) {
	granted := true
	target := exectest.NewFakeTarget(exectest.FakeScript(exectest.ReportStarted(exec.SessionFacts{
		TTY: &granted, PID: 7, Transport: "fake",
		Program: exec.Program{Argv: []string{"/bin/sh"}, Name: "sh"},
	})))
	rig := exectest.NewProcRig(t, exec.Request{TTY: true})
	if _, err := rig.Run(context.Background(), target.Exec); err != nil {
		t.Fatal(err)
	}
	got := rig.Started()
	if len(got) != 1 {
		t.Fatalf("%d reports, want 1", len(got))
	}
	if got[0].TTY == nil || !*got[0].TTY || got[0].PID != 7 || got[0].Transport != "fake" || got[0].Program.Name != "sh" {
		t.Errorf("the facts were not recorded as given: %+v", got[0])
	}
	if got[0].Capabilities == nil || !reflect.DeepEqual(*got[0].Capabilities, rig.Opened()) {
		t.Errorf("the host receives the capabilities of the session in its Started: %+v, want the opened ones %+v", got[0].Capabilities, rig.Opened())
	}
}

// recordedT is a testing.TB that keeps what is reported to Errorf instead of failing the test, so that a test of a test
// helper can look at what the helper tells its caller. Everything else goes to the real test.
type recordedT struct {
	testing.TB
	mu     sync.Mutex
	errors []string
	helped int
}

func (r *recordedT) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// told returns what the helper reported to Errorf, in order.
func (r *recordedT) told() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errors)
}

// Helper counts the calls, so that a test can see that a function marked itself as a test helper, and passes it on.
func (r *recordedT) Helper() {
	r.mu.Lock()
	r.helped++
	r.mu.Unlock()
	r.TB.Helper()
}

// helps returns how many times Helper was called.
func (r *recordedT) helps() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.helped
}

// podCapabilities is the kind of the guide's Kubernetes illustration: a Pod, whose exec reports an exit code and whose
// attach reports none.
func podCapabilities() exec.Capabilities {
	return exec.Capabilities{
		TTY: true, SeparateStderr: true, Resize: true, InitialSize: true, StdinEOF: true,
		Signals:     []exec.Signal{exec.SignalInt, exec.SignalTerm},
		Kill:        exec.KillClosesStreamOnly,
		ExitReport:  exec.ExitReportCode,
		Stall:       exec.StallLossy,
		HostRestart: exec.SurvivalTargetProcess,
		Integration: []exec.IntegrationMode{exec.IntegrationArgvWrapper},
	}
}

// An attach narrows what its session can do: no exit status, fewer signals, no integration. The rig takes the report as
// it is and says nothing, and Effective returns it; until a report it is the declared set.
func TestTheRigAcceptsASessionThatNarrowsTheDeclaredCapabilities(t *testing.T) {
	told := &recordedT{TB: t}
	rig := exectest.NewProcRig(told, exec.Request{}, exectest.RigCapabilities(podCapabilities()))
	if got := rig.Effective(); !reflect.DeepEqual(got, podCapabilities()) {
		t.Errorf("before any report the session has the declared capabilities, got %+v", got)
	}
	attach := func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		caps := p.Capabilities
		caps.ExitReport = exec.ExitReportNone
		caps.Signals = []exec.Signal{exec.SignalInt}
		caps.Integration = nil
		caps.HostRestart = exec.SurvivalNone
		p.Started(exec.SessionFacts{Transport: "k8s-ws-v5", Capabilities: &caps})
		return exec.ExitLost(exec.LostNoStatus), nil
	}
	if _, err := rig.Run(context.Background(), attach); err != nil {
		t.Fatal(err)
	}
	want := podCapabilities()
	want.ExitReport, want.Signals, want.Integration, want.HostRestart = exec.ExitReportNone, []exec.Signal{exec.SignalInt}, nil, exec.SurvivalNone
	if got := rig.Effective(); !reflect.DeepEqual(got, want) {
		t.Errorf("effective = %+v, want the narrowed report %+v", got, want)
	}
	if got := told.told(); len(got) > 0 {
		t.Errorf("a report that narrows the declared capabilities was told to the test: %q", got)
	}
}

// An author who widens what the kind declared is told in the test, as the SDK logs it, and the session keeps the declared
// values for every field that was widened and the narrowed ones for the rest.
func TestTheRigTellsTheTestWhenASessionWidensTheDeclaredCapabilities(t *testing.T) {
	told := &recordedT{TB: t}
	rig := exectest.NewProcRig(told, exec.Request{}, exectest.RigCapabilities(podCapabilities()))
	greedy := func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		caps := p.Capabilities
		caps.Signals = append(slices.Clone(caps.Signals), exec.SignalHup) // not the kind's
		caps.Kill = exec.KillTerminates                                   // above the kind's
		caps.Env = true                                                   // the kind has none
		caps.ExitReport = exec.ExitReportNone                             // below the kind's: fine
		p.Started(exec.SessionFacts{Capabilities: &caps})
		return exec.ExitCode(0), nil
	}
	helpedBefore := told.helps()
	if _, err := rig.Run(context.Background(), greedy); err != nil {
		t.Fatal(err)
	}
	if told.helps() == helpedBefore {
		t.Error("the rig does not mark the report of Proc.Started as a test helper: a failure would point into the rig and not at the author's call of Proc.Started")
	}
	want := []string{
		"Signals: reported HUP not among the declared INT, TERM",
		"Kill: reported TERMINATES above the declared CLOSES_STREAM_ONLY",
		"Env: reported true above the declared false",
	}
	got := told.told()
	if len(got) != len(want) {
		t.Fatalf("the test was told %d things, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if !strings.Contains(got[i], want[i]) {
			t.Errorf("the test was told %q, want it to say %q", got[i], want[i])
		}
	}
	wantEffective := podCapabilities()
	wantEffective.ExitReport = exec.ExitReportNone
	if got := rig.Effective(); !reflect.DeepEqual(got, wantEffective) {
		t.Errorf("effective = %+v, want the declared values for the widened fields: %+v", got, wantEffective)
	}
	if t.Failed() {
		t.Error("the rig failed the real test: it reports through the testing.TB it was given")
	}
}

// The command may write to its copy of the capabilities as it likes, an element of a list in place included. The copy is
// its own, and so is the rig's declared set: no write reaches the target's declaration, another rig, the author's own
// value or what the rig holds a report to.
func TestTheRigsCapabilitiesAreADeepCopyOfTheDeclaredSet(t *testing.T) {
	target := exectest.NewFakeTarget(exectest.FakeInfo(exec.TargetInfo{Capabilities: podCapabilities()}))

	told := &recordedT{TB: t}
	first := exectest.NewProcRig(told, exec.Request{}, exectest.RigCapabilities(target.Info.Capabilities))
	proc := first.Proc()
	proc.Capabilities.Signals[0] = exec.SignalHup
	proc.Capabilities.Integration[0] = exec.IntegrationOff
	if got := target.Info.Capabilities; !reflect.DeepEqual(got, podCapabilities()) {
		t.Errorf("a write through Proc.Capabilities reached the target's declaration: %+v", got)
	}
	second := exectest.NewProcRig(t, exec.Request{}, exectest.RigCapabilities(target.Info.Capabilities))
	if got := second.Proc().Capabilities; !reflect.DeepEqual(got, podCapabilities()) {
		t.Errorf("a write through Proc.Capabilities reached the Proc of another rig: %+v", got)
	}
	if got := first.Effective(); !reflect.DeepEqual(got, podCapabilities()) {
		t.Errorf("a write through Proc.Capabilities changed the effective capabilities, which only a report sets: %+v", got)
	}
	// What the rig holds a report to did not move with the copy: reporting the changed copy is a widening.
	proc.Started(exec.SessionFacts{Capabilities: &proc.Capabilities})
	got := told.told()
	if len(got) != 2 || !strings.Contains(got[0], "Signals: reported HUP not among the declared INT, TERM") ||
		!strings.Contains(got[1], "Integration: reported NONE not among the declared ARGV_WRAPPER") {
		t.Errorf("a report of the changed copy was told as %q, want the two lists that were widened", got)
	}

	declared := podCapabilities()
	toldAgain := &recordedT{TB: t}
	third := exectest.NewProcRig(toldAgain, exec.Request{}, exectest.RigCapabilities(declared))
	declared.Signals[0] = exec.SignalQuit
	declared.Integration[0] = exec.IntegrationOff
	unchanged := third.Proc().Capabilities
	third.Proc().Started(exec.SessionFacts{Capabilities: &unchanged})
	if got := toldAgain.told(); len(got) > 0 {
		t.Errorf("a write to the author's value after the rig was built moved what the rig holds a report to: %q", got)
	}
}

// Only one report is taken, the first, and it counts only when it comes before the first output. A second is told to the
// test, as the SDK logs it, and changes nothing: the host receives the first, the effective capabilities are the first's
// narrowing of the opened ones, and what the second said about them is ignored.
func TestASecondReportIsToldToTheTestAndChangesNothing(t *testing.T) {
	told := &recordedT{TB: t}
	rig := exectest.NewProcRig(told, exec.Request{}, exectest.RigCapabilities(podCapabilities()))
	narrowed := func(edit func(*exec.Capabilities)) *exec.Capabilities {
		caps := rig.Proc().Capabilities
		edit(&caps)
		return &caps
	}
	rig.Proc().Started(exec.SessionFacts{Transport: "k8s-ws-v5", Capabilities: narrowed(func(c *exec.Capabilities) { c.ExitReport = exec.ExitReportNone })})
	if got := told.told(); len(got) > 0 {
		t.Fatalf("a report that narrows was told to the test: %q", got)
	}
	rig.Proc().Started(exec.SessionFacts{Transport: "second", Capabilities: narrowed(func(c *exec.Capabilities) { c.HostRestart = exec.SurvivalNone })})

	if got := told.told(); len(got) != 1 || !strings.Contains(got[0], "twice") {
		t.Errorf("the test was told %q, want one thing: the report was made twice", got)
	}
	if got := rig.Started(); len(got) != 1 || got[0].Transport != "k8s-ws-v5" {
		t.Errorf("the host receives %+v, want only the first report", got)
	}
	want := podCapabilities()
	want.ExitReport = exec.ExitReportNone
	if got := rig.Effective(); !reflect.DeepEqual(got, want) {
		t.Errorf("effective = %+v, want the first report's narrowing %+v", got, want)
	}
	own := rig.Effective()
	own.Signals[0], own.Integration[0] = exec.SignalHup, exec.IntegrationOff
	if again := rig.Effective(); !reflect.DeepEqual(again, want) {
		t.Errorf("a write to what Effective returned changed the effective capabilities: %+v", again)
	}
}

// What the command reported is a snapshot: the rig keeps its own copy of the pointer and the lists of the facts, so that
// a later write by the author changes nothing, and Started hands out copies, so that a test's write changes nothing
// either.
func TestTheRigsRecordedFactsAreASnapshotAtBothBoundaries(t *testing.T) {
	want := func() exec.SessionFacts {
		granted := true
		caps := podCapabilities()
		return exec.SessionFacts{
			TTY: &granted, Transport: "fake", Capabilities: &caps,
			Program: exec.Program{Argv: []string{"/bin/sh", "-l"}, Name: "sh"},
		}
	}
	rig := exectest.NewProcRig(t, exec.Request{}, exectest.RigCapabilities(podCapabilities()))
	reported := want()
	rig.Proc().Started(reported)

	// The author goes on using what it reported.
	*reported.TTY = false
	reported.Capabilities.Signals[0] = exec.SignalHup
	reported.Capabilities.Integration[0] = exec.IntegrationOff
	reported.Program.Argv[0] = "changed"
	if got := rig.Started(); len(got) != 1 || !reflect.DeepEqual(got[0], want()) {
		t.Errorf("a write to a fact after it was reported changed the record: %+v", got)
	}
	if got := rig.Effective(); !reflect.DeepEqual(got, podCapabilities()) {
		t.Errorf("a write to a reported fact changed the effective capabilities: %+v", got)
	}

	// The test goes on using what Started returned.
	first := rig.Started()
	*first[0].TTY = false
	first[0].Capabilities.Signals[0] = exec.SignalHup
	first[0].Capabilities.Integration[0] = exec.IntegrationOff
	first[0].Program.Argv[0] = "changed"
	if got := rig.Started(); len(got) != 1 || !reflect.DeepEqual(got[0], want()) {
		t.Errorf("a write to what Started returned changed the next Started: %+v", got)
	}
}

// Proc.Stdin reports io.EOF when the session ends, however the command ends: the rig closes the input when Run's
// function returns, and what the test wrote while it ran was delivered first.
func TestTheRigsStdinEndsWhenTheCommandEnds(t *testing.T) {
	ends := map[string]exec.ExecFunc{
		"returns": func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil },
		"fails":   func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.Exit{}, errors.New("boom") },
		"panics":  func(context.Context, *exec.Proc) (exec.Exit, error) { panic("kaboom") },
	}
	for name, fn := range ends {
		t.Run(name, func(t *testing.T) {
			rig := exectest.NewProcRig(t, exec.Request{})
			go func() { _, _ = rig.Input().Write([]byte("typed")) }()
			var got []byte
			read := func(context.Context, *exec.Proc) (exec.Exit, error) {
				buf := make([]byte, 5)
				n, err := io.ReadFull(rig.Proc().Stdin, buf)
				got = buf[:n]
				if err != nil {
					return exec.Exit{}, err
				}
				return fn(context.Background(), rig.Proc())
			}
			_, _ = rig.Run(context.Background(), read)
			if string(got) != "typed" {
				t.Fatalf("input written while the command ran was %q, want %q", got, "typed")
			}
			eof := make(chan error, 1)
			go func() {
				_, err := rig.Proc().Stdin.Read(make([]byte, 1))
				eof <- err
			}()
			select {
			case err := <-eof:
				if err != io.EOF {
					t.Errorf("Stdin after the command ended returned %v, want io.EOF", err)
				}
			case <-time.After(safety):
				t.Fatal("Stdin still blocks after Run returned")
			}
		})
	}
}

func TestARetainedOutputWriterFailsAfterTheCommandEnded(t *testing.T) {
	for _, tty := range []bool{false, true} {
		rig := exectest.NewProcRig(t, exec.Request{TTY: tty})
		var kept io.Writer
		if _, err := rig.Run(context.Background(), func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			kept = p.Stdout
			if _, err := kept.Write([]byte("during")); err != nil {
				return exec.Exit{}, err
			}
			return exec.ExitCode(0), nil
		}); err != nil {
			t.Fatal(err)
		}
		if n, err := kept.Write([]byte("after")); !errors.Is(err, io.ErrClosedPipe) || n != 0 {
			t.Errorf("tty %v: write after the end = %d, %v; want 0, io.ErrClosedPipe", tty, n, err)
		}
		if got := string(rig.Stdout()); got != "during" {
			t.Errorf("tty %v: stdout = %q, want only what was written during the command", tty, got)
		}
	}
	// Stderr too.
	rig := exectest.NewProcRig(t, exec.Request{})
	var kept io.Writer
	_, _ = rig.Run(context.Background(), func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		kept = p.Stderr
		return exec.ExitCode(0), nil
	})
	if _, err := kept.Write([]byte("after")); !errors.Is(err, io.ErrClosedPipe) || len(rig.Stderr()) != 0 {
		t.Errorf("stderr write after the end: err = %v, recorded %q", err, rig.Stderr())
	}
}

// podOptions are the options of a Pod as the guide declares them: its containers do not narrow, and its mode does, in
// the choice, before anything opens: an exec has an exit code and cannot be reached after a host restart, an attach has no
// status and can.
func podOptions() []exec.Option {
	kind := podCapabilities()
	narrow := func(edit func(*exec.Capabilities)) *exec.Capabilities {
		caps := kind
		caps.Signals, caps.Integration = slices.Clone(kind.Signals), slices.Clone(kind.Integration)
		edit(&caps)
		return &caps
	}
	return []exec.Option{
		{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", IsDefault: true}, {Value: "sidecar"}}},
		{Name: "mode", Choices: []exec.Choice{
			{Value: "exec", IsDefault: true, Capabilities: narrow(func(c *exec.Capabilities) { c.HostRestart = exec.SurvivalNone })},
			{Value: "attach", Capabilities: narrow(func(c *exec.Capabilities) { c.ExitReport = exec.ExitReportNone })},
		}},
	}
}

// podRig is a rig of a Pod command in a mode, against the options above.
func podRig(t testing.TB, selection map[string]string, argv ...string) *exectest.ProcRig {
	t.Helper()
	return exectest.NewProcRig(t, exec.Request{Argv: argv, Selection: selection},
		exectest.RigCapabilities(podCapabilities()), exectest.RigOptions(podOptions()...))
}

// The rig opens a command as the SDK does: with the kind's capabilities narrowed by the choice that each option takes in
// the request's selection, the default choice of an option the selection omits. Opened is what the host reads, Proc's
// copy is what the function starts from, and until the function reports something narrower the session's effective
// capabilities are the same.
func TestTheRigOpensACommandWithTheCapabilitiesTheSelectionNames(t *testing.T) {
	execMode := func(c *exec.Capabilities) { c.HostRestart = exec.SurvivalNone }
	attachMode := func(c *exec.Capabilities) { c.ExitReport = exec.ExitReportNone }
	for name, tc := range map[string]struct {
		selection map[string]string
		narrow    func(*exec.Capabilities)
	}{
		"exec":            {map[string]string{"mode": "exec"}, execMode},
		"attach":          {map[string]string{"mode": "attach"}, attachMode},
		"the default":     {map[string]string{"container": "sidecar"}, execMode},
		"no selection":    {nil, execMode},
		"an unknown mode": {map[string]string{"mode": "nope"}, func(*exec.Capabilities) {}},
	} {
		t.Run(name, func(t *testing.T) {
			want := podCapabilities()
			tc.narrow(&want)
			rig := podRig(t, tc.selection)
			if got := rig.Opened(); !reflect.DeepEqual(got, want) {
				t.Errorf("Opened = %+v, want %+v", got, want)
			}
			if got := rig.Proc().Capabilities; !reflect.DeepEqual(got, want) {
				t.Errorf("Proc.Capabilities = %+v, want the opened set %+v", got, want)
			}
			if got := rig.Effective(); !reflect.DeepEqual(got, want) {
				t.Errorf("Effective = %+v, want the opened set before any report %+v", got, want)
			}
		})
	}
}

// Two commands of one Pod kind are opened with different capabilities, and differ in exactly the two fields the guide
// says: an exec has no HostRestart, an attach has no ExitReport. The kind itself is the same for both.
func TestAnExecAndAnAttachOfOneKindAreOpenedDifferentlyInTheFieldsTheirChoicesNarrow(t *testing.T) {
	execOpened := podRig(t, map[string]string{"mode": "exec"}).Opened()
	attachOpened := podRig(t, map[string]string{"mode": "attach"}).Opened()
	var differ []string
	execValue, attachValue := reflect.ValueOf(execOpened), reflect.ValueOf(attachOpened)
	for i := range execValue.NumField() {
		if !reflect.DeepEqual(execValue.Field(i).Interface(), attachValue.Field(i).Interface()) {
			differ = append(differ, execValue.Type().Field(i).Name)
		}
	}
	slices.Sort(differ)
	if !slices.Equal(differ, []string{"ExitReport", "HostRestart"}) {
		t.Errorf("exec and attach are opened with different %v, want exactly ExitReport and HostRestart", differ)
	}
	if execOpened.HostRestart != exec.SurvivalNone || execOpened.ExitReport != exec.ExitReportCode {
		t.Errorf("exec is opened with HostRestart %v and ExitReport %v, want none and code", execOpened.HostRestart, execOpened.ExitReport)
	}
	if attachOpened.HostRestart != exec.SurvivalTargetProcess || attachOpened.ExitReport != exec.ExitReportNone {
		t.Errorf("attach is opened with HostRestart %v and ExitReport %v, want the kind's and none", attachOpened.HostRestart, attachOpened.ExitReport)
	}
}

// What Opened returns and what the function narrows are the caller's own copies: a write to either reaches neither the
// rig, nor the options the test gave it, nor the kind.
func TestTheRigsOpenedCapabilitiesAreItsOwnCopy(t *testing.T) {
	options := podOptions()
	rig := exectest.NewProcRig(t, exec.Request{Selection: map[string]string{"mode": "attach"}},
		exectest.RigCapabilities(podCapabilities()), exectest.RigOptions(options...))
	want := podCapabilities()
	want.ExitReport = exec.ExitReportNone

	opened := rig.Opened()
	opened.Signals[0], opened.Integration[0] = exec.SignalHup, exec.IntegrationOff
	proc := rig.Proc().Capabilities
	proc.Signals[1] = exec.SignalHup
	if got := rig.Opened(); !reflect.DeepEqual(got, want) {
		t.Errorf("a write to what Opened returned or to Proc.Capabilities changed Opened: %+v", got)
	}
	if got := rig.Effective(); !reflect.DeepEqual(got, want) {
		t.Errorf("a write to Opened's copy changed the effective capabilities: %+v", got)
	}
	if !reflect.DeepEqual(options, podOptions()) {
		t.Errorf("the rig changed the options the test gave it: %+v", options)
	}
}

// A report is held to what the session was opened with, which is already its mode: narrowing it further is taken, and a
// field above the opened value, even one the kind allows, is told to the test and clamped to the opened value.
func TestAReportIsHeldToWhatTheSessionWasOpenedWith(t *testing.T) {
	told := &recordedT{TB: t}
	rig := exectest.NewProcRig(told, exec.Request{Selection: map[string]string{"mode": "exec"}},
		exectest.RigCapabilities(podCapabilities()), exectest.RigOptions(podOptions()...))
	report := podCapabilities()                    // the kind's: HostRestart TARGET_PROCESS is above an exec's NONE
	report.Stall = exec.StallLossy                 // equal
	report.Signals = []exec.Signal{exec.SignalInt} // narrower
	rig.Proc().Started(exec.SessionFacts{Capabilities: &report})

	if got := told.told(); len(got) != 1 || !strings.Contains(got[0], "HostRestart: reported TARGET_PROCESS above the declared NONE") {
		t.Errorf("the test was told %q, want the one field that is above what was opened", got)
	}
	want := podCapabilities()
	want.HostRestart, want.Signals = exec.SurvivalNone, []exec.Signal{exec.SignalInt}
	if got := rig.Effective(); !reflect.DeepEqual(got, want) {
		t.Errorf("effective = %+v, want the opened HostRestart and the narrower signals %+v", got, want)
	}
	if got := rig.Started(); len(got) != 1 || got[0].Capabilities == nil || !reflect.DeepEqual(*got[0].Capabilities, want) {
		t.Errorf("the host receives %+v, want the clamped set %+v", got, want)
	}
}

// A choice that declares a field above its kind is the author's bug in Options, and the SDK serves the kind's value and
// logs it. The rig tells the test the same, at the call that builds the rig, and opens the command with the kind's value.
func TestTheRigTellsTheTestWhenAChoiceIsWiderThanTheKind(t *testing.T) {
	told := &recordedT{TB: t}
	options := podOptions()
	options[1].Choices[1].Capabilities.ExitReport = exec.ExitReportCodeAndSignal // above the kind's CODE
	options[1].Choices[1].Capabilities.Kill = exec.KillTerminates                // above the kind's CLOSES_STREAM_ONLY
	rig := exectest.NewProcRig(told, exec.Request{Selection: map[string]string{"mode": "attach"}},
		exectest.RigCapabilities(podCapabilities()), exectest.RigOptions(options...))

	got := told.told()
	if len(got) != 2 {
		t.Fatalf("the test was told %d things, want the two fields: %q", len(got), got)
	}
	for i, field := range []string{"Kill", "ExitReport"} {
		for _, want := range []string{`option "mode"`, `choice "attach"`, field + ": reported"} {
			if !strings.Contains(got[i], want) {
				t.Errorf("the test was told %q, want it to say %q", got[i], want)
			}
		}
	}
	if opened := rig.Opened(); !reflect.DeepEqual(opened, podCapabilities()) {
		t.Errorf("opened = %+v, want the kind's values for the fields the choice widened", opened)
	}
}

// The first output of a command that has not reported makes the Started the host receives, from the facts of Opened: the
// requested program and the opened capabilities, once, on either stream. A write of no bytes is not output, and a command
// that never writes has no Started.
func TestTheFirstOutputMakesTheStartedTheHostReceives(t *testing.T) {
	for name, write := range map[string]func(*exec.Proc){
		"stdout": func(p *exec.Proc) { _, _ = p.Stdout.Write([]byte("a")) },
		"stderr": func(p *exec.Proc) { _, _ = p.Stderr.Write([]byte("a")) },
		"both streams, twice": func(p *exec.Proc) {
			_, _ = p.Stderr.Write([]byte("a"))
			_, _ = p.Stdout.Write([]byte("b"))
			_, _ = p.Stdout.Write([]byte("c"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rig := podRig(t, map[string]string{"mode": "attach"}, "/bin/sh", "-l")
			if got := rig.Started(); len(got) != 0 {
				t.Fatalf("a command that has written nothing has a Started: %+v", got)
			}
			_, _ = rig.Proc().Stdout.Write(nil) // no byte, no output
			if got := rig.Started(); len(got) != 0 {
				t.Fatalf("a write of no bytes made a Started: %+v", got)
			}
			write(rig.Proc())
			got := rig.Started()
			if len(got) != 1 {
				t.Fatalf("the host receives %d Started frames, want one: %+v", len(got), got)
			}
			if !slices.Equal(got[0].Program.Argv, []string{"/bin/sh", "-l"}) || got[0].PID != 0 || got[0].TTY != nil || got[0].Transport != "" {
				t.Errorf("the Started claims more than the facts of Opened: %+v", got[0])
			}
			if got[0].Capabilities == nil || !reflect.DeepEqual(*got[0].Capabilities, rig.Opened()) {
				t.Errorf("the Started carries %+v, want the opened capabilities %+v", got[0].Capabilities, rig.Opened())
			}
			if rig.Effective().ExitReport != exec.ExitReportNone {
				t.Errorf("the attach mode is lost: %+v", rig.Effective())
			}
		})
	}

	// A command that is quiet has no Started, whatever its mode says.
	for _, mode := range []string{"exec", "attach"} {
		rig := podRig(t, map[string]string{"mode": mode})
		if _, err := rig.Run(context.Background(), func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }); err != nil {
			t.Fatal(err)
		}
		if got := rig.Started(); len(got) != 0 {
			t.Errorf("%s: a quiet command has a Started: %+v", mode, got)
		}
	}
}

// A report before the first output is the Started the host receives, and the output after it makes no second one.
func TestAReportBeforeTheOutputIsTheStartedTheHostReceives(t *testing.T) {
	rig := podRig(t, map[string]string{"mode": "exec"}, "/bin/sh")
	rig.Proc().Started(exec.SessionFacts{PID: 42, Program: exec.Program{Argv: []string{"/bin/bash"}, Name: "bash"}})
	_, _ = rig.Proc().Stdout.Write([]byte("a"))
	_, _ = rig.Proc().Stderr.Write([]byte("b"))
	got := rig.Started()
	if len(got) != 1 || got[0].PID != 42 || got[0].Program.Name != "bash" {
		t.Fatalf("the host receives %+v, want the report alone", got)
	}
	if got[0].Capabilities == nil || !reflect.DeepEqual(*got[0].Capabilities, rig.Opened()) {
		t.Errorf("the Started carries %+v, want the opened capabilities", got[0].Capabilities)
	}
}

// A report after the first output is told to the test, as the SDK logs it, and changes nothing: the host received the
// Started the SDK made, which says nothing the author did not, and the capabilities are the opened ones whatever the late
// report claims to narrow.
func TestAReportAfterTheFirstOutputIsToldToTheTestAndChangesNothing(t *testing.T) {
	told := &recordedT{TB: t}
	rig := exectest.NewProcRig(told, exec.Request{Argv: []string{"/bin/sh"}, Selection: map[string]string{"mode": "attach"}},
		exectest.RigCapabilities(podCapabilities()), exectest.RigOptions(podOptions()...))
	_, _ = rig.Proc().Stdout.Write([]byte("a"))
	narrower := rig.Proc().Capabilities
	narrower.Signals = nil
	rig.Proc().Started(exec.SessionFacts{PID: 9, Capabilities: &narrower})

	if got := told.told(); len(got) != 1 || !strings.Contains(got[0], "after the first output") {
		t.Errorf("the test was told %q, want one thing: the report came after the first output", got)
	}
	if got := rig.Started(); len(got) != 1 || got[0].PID != 0 {
		t.Errorf("the host receives %+v, want only the Started the SDK made", got)
	}
	if got := rig.Effective(); !reflect.DeepEqual(got, rig.Opened()) {
		t.Errorf("a late report changed the effective capabilities: %+v", got)
	}
}

// The author reports from one goroutine while the library it copies output with writes from its own, and which comes first
// is not the author's to say. Either way the host receives exactly one Started: the author's if it came first, and else
// the one the SDK made, with the author's report told to the test as late.
func TestAReportRacingTheFirstOutputIsEitherTakenOrToldAsLateAndTheHostGetsOneStarted(t *testing.T) {
	for round := range 200 {
		told := &recordedT{TB: t}
		rig := exectest.NewProcRig(told, exec.Request{})
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; rig.Proc().Started(exec.SessionFacts{PID: 5}) }()
		go func() { defer wg.Done(); <-start; _, _ = rig.Proc().Stdout.Write([]byte("a")) }()
		close(start)
		wg.Wait()

		got, late := rig.Started(), told.told()
		if len(got) != 1 {
			t.Fatalf("round %d: the host receives %d Started frames: %+v", round, len(got), got)
		}
		switch {
		case got[0].PID == 5 && len(late) == 0:
		case got[0].PID == 0 && len(late) == 1 && strings.Contains(late[0], "after the first output"):
		default:
			t.Fatalf("round %d: the host receives %+v and the test was told %q", round, got, late)
		}
	}
}
