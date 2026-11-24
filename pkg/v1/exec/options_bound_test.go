package exec_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/log/logtest"
	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// The unary Options RPC runs the author's callback inside the connection's lease, and a callback that ignores its context must
// hold neither the lease nor the engine for ever (the independent review of the engine, SHOULD 1): the callback is bounded as
// the one of a start is. Its context ends at the engine's deadline and when the caller gives up, the plugin stops or the
// connection is retired; it then has the grace to return, and one that has not is given up on: the call is refused, the lease is
// released, violations{reason=options_hung} is counted, and the abandoned gauge shows it until it returns. A unary call has no
// session to hold a place with, so the callbacks of one target that are in flight, the ones given up on included, are capped,
// and a call over the cap is refused UNAVAILABLE (TARGET_WEDGED when a callback in flight was given up on) and counted in
// omniview.plugin.self.sdk.exec.options.refused.

const metricOptionsRefused = "omniview.plugin.self.sdk.exec.options.refused"

// optionsRig serves targets over a real transport, as exectestkit.Serve does, with the engine behind the Options RPC set up for
// the test, and with that engine's logs and diagnostics recorded. It is an engineRig without the in-process engine, so the
// helpers that read the logs and the metrics apply to it.
type optionsRig struct {
	*engineRig
	server *exectestkit.Server
	client exec.Client
}

func newOptionsRig(t *testing.T, cfg exechook.EngineConfig, targets ...exec.Target) *optionsRig {
	t.Helper()
	logs := &logtest.RecordingSink{}
	diag := sdkdiag.NewRegistry()
	conns := exectestkit.NewConnectionsT(t, "fake")
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: targets, Connections: conns})
	cfg.Logger = logging.New(logging.Config{Backend: logging.NewSinkBackend(logs), Level: logging.NewLevelController(logging.LevelDebug)})
	cfg.Diagnostics = diag
	exec.ConfigurePluginEngine(server.Plugin, cfg)
	return &optionsRig{
		engineRig: &engineRig{t: t, conns: conns, logs: logs, diag: diag},
		server:    server,
		client:    server.V2(t),
	}
}

// options calls the Options RPC for a resource of the fake target "thing".
func (r *optionsRig) options(ctx context.Context, resource string) (*exec.OptionsResult, error) {
	return r.client.Options(r.conns.Context(ctx), r.conns.TargetRef("thing", resource))
}

// callAsync calls the Options RPC on a goroutine and returns the channel its error arrives on.
func (r *optionsRig) callAsync(ctx context.Context, resource string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := r.options(ctx, resource)
		done <- err
	}()
	return done
}

// gaugeIs reports whether the abandoned gauge reads v.
func (r *optionsRig) gaugeIs(v float64) func() bool {
	return func() bool {
		got, ok := metricValue(r.engineRig, metricAbandoned, map[string]string{})
		return ok && got == v
	}
}

// retiresWhile retires the connection on a goroutine, which returns when every callback that holds a lease has returned or
// has been given up on, and returns the channel that closes when it has.
func (r *optionsRig) retire() <-chan struct{} {
	retired := make(chan struct{})
	go func() {
		r.conns.Retire()
		close(retired)
	}()
	return retired
}

// optionsCallbackTarget is a target of the fake's routing and defaults with an Options callback.
func optionsCallbackTarget(fn exec.OptionsFunc, mods ...func(*exec.TargetInfo)) exec.Target {
	target := engTarget(blockUntilCancelled, mods...)
	target.Options = fn
	return target
}

// hangingOptions is an Options callback that counts the calls that entered it, how many are inside it at once, tells the test,
// and then ignores its context until the gate opens.
type hangingOptions struct {
	gate                      *exectest.Gate
	entered                   chan struct{} // one value for each call that entered
	calls, inside, mostInside atomic.Int32
	// passes, when set, makes the callback answer at once instead of hanging.
	passes atomic.Bool
}

func newHangingOptions() *hangingOptions {
	return &hangingOptions{gate: exectest.NewGate(), entered: make(chan struct{}, 1024)}
}

func (h *hangingOptions) fn(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
	h.calls.Add(1)
	if h.passes.Load() {
		return nil, nil
	}
	n := h.inside.Add(1)
	defer h.inside.Add(-1)
	for {
		most := h.mostInside.Load()
		if n <= most || h.mostInside.CompareAndSwap(most, n) {
			break
		}
	}
	h.entered <- struct{}{}
	<-h.gate.Opened()
	return nil, nil
}

// awaitValue waits for a value and returns it. The wait ends a hang and decides nothing.
func awaitValue[T any](t testing.TB, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(engineWait):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// The case of the independent review: a callback that ignores its context, a caller that gives up, and the retirement of the
// connection that follows. Before the bound, the retirement waited for the callback for as long as it chose to run.
func TestAnOptionsCallbackThatIgnoresItsContextDoesNotHoldTheLeaseOnceTheCallerHasGivenUp(t *testing.T) {
	h := newHangingOptions()
	rig := newOptionsRig(t, exechook.EngineConfig{CloseGrace: 20 * time.Millisecond}, optionsCallbackTarget(h.fn))
	t.Cleanup(h.gate.Open)

	ctx, cancel := context.WithCancel(context.Background())
	done := rig.callAsync(ctx, "thing-1")
	awaitValue(t, h.entered, "the callback to be entered")
	cancel() // the caller gives up
	if err := awaitValue(t, done, "the caller's call to return"); err == nil {
		t.Fatal("a call that was cancelled succeeded")
	}

	select {
	case <-rig.retire():
	case <-time.After(10 * time.Second):
		t.Fatal("the connection's retirement is held by the callback that ignores its context: the lease must be released when the callback is given up on")
	}
	eventually(t, "the callback to be counted as abandoned", rig.gaugeIs(1))
	wantMetric(t, rig.engineRig, metricViolations, map[string]string{"reason": "options_hung"}, 1)

	h.gate.Open()
	eventually(t, "the callback to return and stop being counted", rig.gaugeIs(0))
}

// The same with the caller still waiting: a retirement ends the lease under the callback, which ignores it; the retirement
// does not wait for the callback, and the caller is told that the connection was retired.
func TestARetirementDoesNotWaitForAnOptionsCallbackThatHangsWhileTheCallerWaits(t *testing.T) {
	h := newHangingOptions()
	rig := newOptionsRig(t, exechook.EngineConfig{CloseGrace: 20 * time.Millisecond}, optionsCallbackTarget(h.fn))
	t.Cleanup(h.gate.Open)

	done := rig.callAsync(context.Background(), "thing-1")
	awaitValue(t, h.entered, "the callback to be entered")
	select {
	case <-rig.retire():
	case <-time.After(10 * time.Second):
		t.Fatal("the retirement is held by a callback that ignores the cancellation of its lease")
	}
	err := awaitValue(t, done, "the caller's call to return")
	var typed *exec.Error
	if !errors.As(err, &typed) || typed.Class != exec.ErrorClassConnection || typed.Reason != exec.ErrorReasonConnectionRetired {
		t.Errorf("the call the retirement found in its callback: %v, want CONNECTION/CONNECTION_RETIRED", err)
	}
}

// The engine's deadline bounds the callback of the RPC as it bounds a start's: a callback that is still running when it passes
// has its context ended, and one that ignores it is given up on after the grace: the caller, who is still waiting, is refused
// with TIMEOUT.
func TestAnOptionsCallbackThatOutlivesTheEnginesDeadlineIsRefusedTimeoutAndGivenUpOn(t *testing.T) {
	h := newHangingOptions()
	rig := newOptionsRig(t, exechook.EngineConfig{OptionsDeadline: 10 * time.Millisecond, CloseGrace: 10 * time.Millisecond}, optionsCallbackTarget(h.fn))
	t.Cleanup(h.gate.Open)

	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	_, err := rig.options(ctx, "thing-1")
	var typed *exec.Error
	if !errors.As(err, &typed) || typed.Class != exec.ErrorClassTimeout {
		t.Fatalf("a call whose callback never returns: %v, want TIMEOUT", err)
	}
	wantMetric(t, rig.engineRig, metricViolations, map[string]string{"reason": "options_hung"}, 1)
	wantMetric(t, rig.engineRig, metricAbandoned, map[string]string{}, 1)
	h.gate.Open()
	eventually(t, "the callback to return and stop being counted", rig.gaugeIs(0))
}

// A callback that notices that its context ended and winds down inside the grace is waited for: it is not given up on, nothing
// is counted, and the grace that the engine waited is the engine's own (the timer it armed is read, not the clock).
func TestAnOptionsCallbackThatHonorsItsContextWithinTheGraceIsNotGivenUpOn(t *testing.T) {
	const grace = 7 * time.Second
	entered, winding, let := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{}, 1)
	rig := newOptionsRig(t, exechook.EngineConfig{CloseGrace: grace}, optionsCallbackTarget(func(pc *types.PluginContext, _ resourcev2.ResourceReference) ([]exec.Option, error) {
		entered <- struct{}{}
		<-pc.Context.Done()
		winding <- struct{}{}
		<-let
		return nil, pc.Context.Err()
	}))
	t.Cleanup(func() { close(let) }) // before the connection's retirement waits for the callback, whatever became of the test
	timers := newFakeTimers()
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{NewTimer: timers.New})

	ctx, cancel := context.WithCancel(context.Background())
	done := rig.callAsync(ctx, "thing-1")
	awaitValue(t, entered, "the callback to be entered")
	cancel()
	if timer := timers.next(t); timer.d != grace {
		t.Fatalf("the engine armed a timer of %v for a callback whose context ended, want the grace of %v", timer.d, grace)
	}
	awaitValue(t, winding, "the callback to see that its context ended")
	let <- struct{}{}
	awaitValue(t, done, "the caller's call to return")
	select {
	case <-rig.retire(): // the lease is released: the call, on the server's side, has returned
	case <-time.After(engineWait):
		t.Fatal("the lease is not released by a callback that returned")
	}
	if v, ok := metricValue(rig.engineRig, metricViolations, map[string]string{"reason": "options_hung"}); ok && v != 0 {
		t.Errorf("options_hung is counted %v times for a callback that returned inside the grace", v)
	}
	if v, ok := metricValue(rig.engineRig, metricAbandoned, map[string]string{}); ok && v != 0 {
		t.Errorf("the abandoned gauge is %v for a callback that returned inside the grace", v)
	}
}

// A callback that is given up on is counted, and said once to the plugin's log: the first time, and at most once a second for
// each target after that (the clock of the limit is the test's), while every one is counted. The grace's timer is the test's:
// the callback is given up on when the test runs it out. The caller waits, and is told TIMEOUT when the engine's deadline and
// the grace have run out, which is after the engine wrote its line: the test reads the log only once the call has returned.
func TestAnOptionsCallbackThatIsGivenUpOnIsCountedAndLoggedAtMostOnceASecondForEachTarget(t *testing.T) {
	const grace = 7 * time.Second
	h := newHangingOptions()
	rig := newOptionsRig(t, exechook.EngineConfig{CloseGrace: grace, OptionsDeadline: 10 * time.Millisecond}, optionsCallbackTarget(h.fn))
	t.Cleanup(h.gate.Open)
	timers := newFakeTimers()
	clock := newFakeClock()
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{NewTimer: timers.New, Now: clock.Now})

	giveUp := func(resource string) {
		t.Helper()
		done := rig.callAsync(context.Background(), resource)
		timers.nextOf(t, grace).fire()
		err := awaitValue(t, done, "the caller's call to return")
		var typed *exec.Error
		if !errors.As(err, &typed) || typed.Class != exec.ErrorClassTimeout {
			t.Fatalf("a call whose callback never returns: %v, want TIMEOUT", err)
		}
	}
	lines := func() []map[string]any { return logged(rig.engineRig, msgViolation) }

	giveUp("thing-1")
	if got := lines(); len(got) != 1 || got[0]["violation"] != "options_hung" || got[0]["type"] != "thing" || got[0]["kind"] != "options" {
		t.Fatalf("after one callback was given up on: %v, want one line naming options_hung, the target and the kind of call", got)
	}
	giveUp("thing-2")
	giveUp("thing-3")
	if n := len(lines()); n != 1 {
		t.Errorf("%d lines for three callbacks given up on in one second, want 1", n)
	}
	clock.advance(time.Second)
	giveUp("thing-4")
	if n := len(lines()); n != 2 {
		t.Errorf("%d lines a second after the first, want 2", n)
	}
	wantMetric(t, rig.engineRig, metricViolations, map[string]string{"reason": "options_hung"}, 4)
	eventually(t, "the four callbacks to be counted as abandoned", rig.gaugeIs(4))
}

// The callbacks of a target that are in flight are capped. A call over the cap is refused UNAVAILABLE with no reason while every
// callback in flight is still inside its bound (the target is busy and may be tried again), runs no callback, and is counted.
func TestTheOptionsCallbacksInFlightOfATargetAreCappedAndACallOverTheCapIsRefusedUnavailable(t *testing.T) {
	const cap = 3
	h := newHangingOptions()
	rig := newOptionsRig(t, exechook.EngineConfig{OptionsInFlight: cap}, optionsCallbackTarget(h.fn))
	t.Cleanup(h.gate.Open)
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{Now: newFakeClock().Now}) // the limit of the log lines is the clock's

	var waiting []<-chan error
	for i := range cap {
		waiting = append(waiting, rig.callAsync(context.Background(), fmt.Sprintf("thing-%d", i)))
		awaitValue(t, h.entered, "the callback to be entered")
	}
	for i := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), engineWait)
		_, err := rig.options(ctx, "thing-over")
		cancel()
		var typed *exec.Error
		if !errors.As(err, &typed) || typed.Class != exec.ErrorClassUnavailable || typed.Reason != exec.ErrorReasonUnspecified {
			t.Fatalf("call %d over the cap, with every callback still inside its bound: %v, want UNAVAILABLE with no reason", i, err)
		}
	}
	if got := h.calls.Load(); got != cap {
		t.Errorf("%d callbacks ran, want %d: a call over the cap runs none", got, cap)
	}
	wantMetric(t, rig.engineRig, metricOptionsRefused, map[string]string{}, 2)
	if n := len(logged(rig.engineRig, "exec options refused: the target has too many Options callbacks in flight")); n != 1 {
		t.Errorf("%d lines for two refusals in one second, want 1", n)
	}

	h.gate.Open() // the callbacks return, the callers are answered, and the places are free
	for i, done := range waiting {
		if err := awaitValue(t, done, "a waiting call to be answered"); err != nil {
			t.Errorf("waiting call %d: %v", i, err)
		}
	}
	if _, err := rig.options(context.Background(), "thing-after"); err != nil {
		t.Errorf("a call after the callbacks returned: %v", err)
	}
	if got := h.calls.Load(); got != cap+1 {
		t.Errorf("%d callbacks ran, want %d", got, cap+1)
	}
}

// When a callback in flight was given up on and has not returned, the target is wedged and the refusal says so, whatever else is
// in flight; the callback that returns frees its place and the target answers again.
func TestACapThatAnAbandonedCallbackHelpsFillIsWedgedAndItsPlaceIsFreeWhenItReturns(t *testing.T) {
	const cap, grace = 3, 7 * time.Second
	h := newHangingOptions()
	rig := newOptionsRig(t, exechook.EngineConfig{OptionsInFlight: cap, CloseGrace: grace}, optionsCallbackTarget(h.fn))
	t.Cleanup(h.gate.Open)
	timers := newFakeTimers()
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{NewTimer: timers.New})

	cancels := make([]context.CancelFunc, cap)
	waiting := make([]<-chan error, cap)
	for i := range cap {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i], waiting[i] = cancel, rig.callAsync(ctx, fmt.Sprintf("thing-%d", i))
		awaitValue(t, h.entered, "the callback to be entered")
	}
	// One caller gives up, and the callback ignores it: it is given up on when its grace runs out. Two callbacks are still
	// inside their bound.
	cancels[0]()
	timers.nextOf(t, grace).fire()
	awaitValue(t, waiting[0], "the caller that gave up to return")
	eventually(t, "one callback to be counted as abandoned", rig.gaugeIs(1))

	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	_, err := rig.options(ctx, "thing-over")
	cancel()
	var typed *exec.Error
	if !errors.As(err, &typed) || typed.Class != exec.ErrorClassUnavailable || typed.Reason != exec.ErrorReasonTargetWedged {
		t.Fatalf("a call over the cap while one callback in flight was given up on: %v, want UNAVAILABLE/TARGET_WEDGED", err)
	}
	wantMetric(t, rig.engineRig, metricOptionsRefused, map[string]string{}, 1)

	h.gate.Open()
	eventually(t, "the abandoned callback to return and stop being counted", rig.gaugeIs(0))
	for i := 1; i < cap; i++ {
		if err := awaitValue(t, waiting[i], "a waiting call to be answered"); err != nil {
			t.Errorf("waiting call %d: %v", i, err)
		}
		cancels[i]()
	}
	if _, err := rig.options(context.Background(), "thing-after"); err != nil {
		t.Errorf("a call after every callback returned: %v: the places were not freed", err)
	}
}

// Each target is capped on its own: a target whose callbacks fill its cap does not refuse the calls of another.
func TestTheOptionsCapIsPerTarget(t *testing.T) {
	const cap = 2
	h := newHangingOptions()
	var otherCalls atomic.Int32
	hung := optionsCallbackTarget(h.fn)
	other := optionsCallbackTarget(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		otherCalls.Add(1)
		return nil, nil
	}, func(i *exec.TargetInfo) { i.Type = "other" })
	rig := newOptionsRig(t, exechook.EngineConfig{OptionsInFlight: cap}, hung, other)
	t.Cleanup(h.gate.Open)

	for i := range cap {
		rig.callAsync(context.Background(), fmt.Sprintf("thing-%d", i))
		awaitValue(t, h.entered, "the callback to be entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	_, err := rig.options(ctx, "thing-over")
	var typed *exec.Error
	if !errors.As(err, &typed) || typed.Class != exec.ErrorClassUnavailable {
		t.Fatalf("a call over the cap of its target: %v, want UNAVAILABLE", err)
	}
	if _, err := rig.client.Options(rig.conns.Context(context.Background()), rig.conns.TargetRef("other", "other-1")); err != nil {
		t.Errorf("a call to a target whose callbacks are not in flight: %v", err)
	}
	if got := otherCalls.Load(); got != 1 {
		t.Errorf("the other target's callback ran %d times, want 1", got)
	}
}

// The case of the independent review: 200 calls at once against a callback that ignores its context and never returns. Before
// the cap, 200 callbacks ran, 205 goroutines were added and nothing was counted or logged; now the target's cap (32) of them
// run, every other call is refused, and when the callers give up and the callbacks return nothing is left.
func TestTwoHundredHungOptionsCallsRunAtMostTheCapsCallbacksAndLeaveNothingBehind(t *testing.T) {
	const calls, cap = 200, 32
	h := newHangingOptions()
	h.passes.Store(true)
	rig := newOptionsRig(t, exechook.EngineConfig{CloseGrace: 10 * time.Millisecond}, optionsCallbackTarget(h.fn))
	t.Cleanup(h.gate.Open)
	if _, err := rig.options(context.Background(), "thing-warm-up"); err != nil { // the transport's goroutines are in the baseline
		t.Fatalf("a call to a callback that answers: %v", err)
	}
	h.passes.Store(false)
	h.calls.Store(0)
	baseline := lifecycletest.TakeBaseline()

	results := make(chan error, calls)
	cancels := make([]context.CancelFunc, calls)
	for i := range calls {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		go func() {
			_, err := rig.options(ctx, fmt.Sprintf("thing-%d", i))
			results <- err
		}()
	}
	for range cap {
		awaitValue(t, h.entered, "a callback to be entered")
	}
	for range calls - cap {
		err := awaitValue(t, results, "a call over the cap to be refused")
		var typed *exec.Error
		if !errors.As(err, &typed) || typed.Class != exec.ErrorClassUnavailable || typed.Reason != exec.ErrorReasonUnspecified {
			t.Fatalf("a call over the cap: %v, want UNAVAILABLE with no reason: every callback in flight is still inside its bound", err)
		}
	}
	if got := h.calls.Load(); got != cap {
		t.Errorf("%d callbacks ran, want %d", got, cap)
	}
	if most := h.mostInside.Load(); most > cap {
		t.Errorf("%d callbacks were running at once, want at most %d", most, cap)
	}
	wantMetric(t, rig.engineRig, metricOptionsRefused, map[string]string{}, calls-cap)

	// The callers that are waiting give up. The callbacks ignore it and are given up on after the grace, which releases the
	// leases: the connection's retirement completes while every callback is still blocked.
	for _, cancel := range cancels {
		cancel()
	}
	for range cap {
		awaitValue(t, results, "a caller that gave up to return")
	}
	select {
	case <-rig.retire():
	case <-time.After(10 * time.Second):
		t.Fatal("the retirement is held by the callbacks that ignore their contexts")
	}
	eventually(t, "every callback to be counted as abandoned", rig.gaugeIs(cap))
	wantMetric(t, rig.engineRig, metricViolations, map[string]string{"reason": "options_hung"}, cap)

	h.gate.Open()
	eventually(t, "every callback to return and stop being counted", rig.gaugeIs(0))
	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines after the callbacks returned: %v", err)
	}
}

// A callback that panics after the engine gave up on it is the author's bug and not the plugin's end, as it is for a start: the
// panic is recovered where it happens, the callback is accounted for, and the plugin serves on.
func TestAnOptionsCallbackThatPanicsAfterTheEngineGaveUpOnItIsContainedOnTheRPC(t *testing.T) {
	gate := exectest.NewGate()
	var panics atomic.Bool
	panics.Store(true)
	entered := make(chan struct{}, 1)
	rig := newOptionsRig(t, exechook.EngineConfig{OptionsDeadline: 10 * time.Millisecond, CloseGrace: 10 * time.Millisecond},
		optionsCallbackTarget(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
			if !panics.Load() {
				return nil, nil
			}
			entered <- struct{}{}
			<-gate.Opened()
			panic("a callback that panics after it was given up on")
		}))
	t.Cleanup(gate.Open)

	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	if _, err := rig.options(ctx, "thing-1"); err == nil {
		t.Fatal("a call whose callback never returns was answered")
	}
	awaitValue(t, entered, "the callback to be entered")
	panics.Store(false)
	gate.Open()
	eventually(t, "the callback to panic and be accounted for", rig.gaugeIs(0))
	// The plugin serves on. The call is made until it is answered and not once: its callback answers at once, but the engine's deadline here is
	// 10 ms, which a scheduler that is slow to run the callback's goroutine can use up, and the call is then refused TIMEOUT, rightly.
	var last error
	if !proofPoll(engineWait, func() bool { _, last = rig.options(context.Background(), "thing-2"); return last == nil }) {
		t.Errorf("the plugin did not serve on after the panic of a callback it had given up on: %v", last)
	}
}

// A start's Options callback whose context ended because the engine is closing has the shutdown grace, and not the grace of an
// ordinary end, to return: whichever of the two came first, the end of its context or the close.
func TestAnOptionsCallbackOfAStartHasTheShutdownGraceWhenTheEngineCloses(t *testing.T) {
	const grace, shutdownGrace = 7 * time.Second, 2 * time.Second
	for _, order := range []string{"the engine closes while the callback runs", "the caller gives up and then the engine closes"} {
		t.Run(order, func(t *testing.T) {
			gate := exectest.NewGate()
			var calls atomic.Int32
			entered := make(chan struct{}, 1)
			rig := newEngineRig(t, engineRigConfig{grace: grace, shutdownGrace: shutdownGrace}, hungOptionsTarget(gate, &calls, entered, 4))
			t.Cleanup(gate.Open)
			timers := newFakeTimers()
			exec.SetEngineSeams(rig.eng, exec.EngineSeams{NewTimer: timers.New})

			ctx, cancel := context.WithCancel(rig.request())
			defer cancel()
			started := make(chan error, 1)
			go func() {
				_, err := rig.eng.Open(ctx, rig.openRequest("s1", withoutTTY))
				started <- err
			}()
			awaitValue(t, entered, "the callback to be entered")
			if order != "the engine closes while the callback runs" {
				cancel()
				if timer := timers.next(t); timer.d != grace {
					t.Fatalf("the engine armed a timer of %v for a callback whose context ended, want the grace of %v", timer.d, grace)
				}
			}
			closed := make(chan error, 1)
			go func() { closed <- rig.eng.Close(context.Background()) }()
			timer := timers.next(t)
			if timer.d != shutdownGrace {
				t.Fatalf("the engine armed a timer of %v once it closed, want the shutdown grace of %v", timer.d, shutdownGrace)
			}
			timer.fire()
			if err := awaitValue(t, started, "the start to be refused"); err == nil {
				t.Error("a start whose callback never returned was admitted")
			}
			if err := awaitValue(t, closed, "the engine to close"); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
}

// A callback that returned just as its grace ran out is not given up on: the engine decides at the same compare-and-swap that
// the callback's own return makes, and the callback has the last word. The order that a real run reaches only by chance is
// forced: the callback is held right after it returned, the grace runs out (this test runs the timer out), and the engine's
// decision is read before the callback is let go.
func TestAnOptionsCallbackThatReturnedWhenItsGraceRanOutIsNotGivenUpOn(t *testing.T) {
	const grace = 7 * time.Second
	entered, held := make(chan struct{}, 1), exectest.NewGate()
	hold := exectest.NewGate()
	decided := make(chan bool, 1)
	rig := newOptionsRig(t, exechook.EngineConfig{CloseGrace: grace}, optionsCallbackTarget(func(pc *types.PluginContext, _ resourcev2.ResourceReference) ([]exec.Option, error) {
		entered <- struct{}{}
		<-pc.Context.Done()
		return nil, pc.Context.Err() // honors its context: returns as soon as the caller has given up
	}))
	timers := newFakeTimers()
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{
		NewTimer:       timers.New,
		CallReturned:   func() { held.Open(); <-hold.Opened() },
		AbandonDecided: func(abandoned bool) { decided <- abandoned },
	})
	t.Cleanup(hold.Open)

	ctx, cancel := context.WithCancel(context.Background())
	done := rig.callAsync(ctx, "thing-1")
	awaitValue(t, entered, "the callback to be entered")
	cancel()
	awaitChannel(t, held.Opened(), "the callback to return")
	timers.nextOf(t, grace).fire()
	if abandoned := awaitValue(t, decided, "the engine to decide about the callback"); abandoned {
		t.Fatal("the engine gave up on a callback that had already returned")
	}
	hold.Open()
	awaitValue(t, done, "the caller's call to return")
	select {
	case <-rig.retire(): // the lease is released: the call, on the server's side, has returned
	case <-time.After(engineWait):
		t.Fatal("the lease is not released by a callback that returned")
	}
	if v, ok := metricValue(rig.engineRig, metricViolations, map[string]string{"reason": "options_hung"}); ok && v != 0 {
		t.Errorf("options_hung is counted %v times for a callback that returned", v)
	}
	if v, ok := metricValue(rig.engineRig, metricAbandoned, map[string]string{}); ok && v != 0 {
		t.Errorf("the abandoned gauge is %v for a callback that returned", v)
	}
}
