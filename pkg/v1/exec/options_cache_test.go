package exec_test

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The admission of an Open or a Run asks the target's Options callback for the resource, to hold the selection to its choices and to
// narrow the capabilities by the choice's mode. A host that opens a session has usually just called the Options RPC for the same
// resource, so the engine keeps the result of a callback for a short time and the admission reuses it: the callback runs once for
// the picker and the start, not twice. The RPC itself always runs the callback and refreshes what is kept. A stale choice only
// affects what is advisory: the target refuses late and Classify types it.

// cacheRig serves one target whose Options callback counts its calls, on a clock the test moves.
type cacheRig struct {
	*optionsRig
	calls atomic.Int32
	clock *fakeClock
	// hold makes the callback wait for the context of its call before it answers, as a callback that honors its context does when
	// the work it is asked to do takes its time; entered tells the test that a call is inside.
	hold    atomic.Bool
	entered chan struct{}
}

// versionedOptions is the callback's answer for its n-th call: a required container whose one choice is named for the call, so that a
// selection of "c<n>" is admitted only against the answer of that call.
func versionedOptions(n int32) []exec.Option {
	return []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "c" + strconv.Itoa(int(n)), State: exec.ChoiceStateReady}}}}
}

// endsAtOnce is the function of a session that has nothing to do: it ends with exit 0.
func endsAtOnce(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }

// newCacheRig serves a target whose callback is answer, called with the number of the call, and whose function ends at once with exit
// 0. cfg configures the engine behind the RPC: a TTL of zero is the server's default.
func newCacheRig(t *testing.T, cfg exechook.EngineConfig, answer func(call int32) ([]exec.Option, error)) *cacheRig {
	t.Helper()
	return newCacheRigRunning(t, cfg, endsAtOnce, answer)
}

// newCacheRigRunning is newCacheRig with the function of the sessions.
func newCacheRigRunning(t *testing.T, cfg exechook.EngineConfig, fn exec.ExecFunc, answer func(call int32) ([]exec.Option, error)) *cacheRig {
	t.Helper()
	r := &cacheRig{clock: newFakeClock(), entered: make(chan struct{}, 16)}
	target := engTarget(fn)
	target.Options = func(pc *types.PluginContext, _ resourcev2.ResourceReference) ([]exec.Option, error) {
		n := r.calls.Add(1)
		if r.hold.Load() {
			r.entered <- struct{}{}
			<-pc.Context.Done()
			return nil, pc.Context.Err()
		}
		return answer(n)
	}
	r.optionsRig = newOptionsRig(t, cfg, target)
	exec.SetPluginEngineSeams(r.server.Plugin, exec.EngineSeams{Now: r.clock.Now})
	return r
}

// versioned is the callback of most tests: every call answers with its own version.
func versioned(call int32) ([]exec.Option, error) { return versionedOptions(call), nil }

// cacheStart is the Start of a session in the resource, selecting the container.
func (r *cacheRig) start(resource, container string, incarnation *string) *execv2.Start {
	target := r.conns.WireTarget(r.t, "thing", resource, &execv2.NameValue{Name: "container", Value: container})
	if incarnation != nil {
		target.Reference.Incarnation = incarnation
	}
	return &execv2.Start{Target: target}
}

// open opens a session of the resource that selects the container, and reads it to its end, which is the function's at once.
func (r *cacheRig) open(id, resource, container string, incarnation *string) error {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	conn, err := r.client.Open(r.conns.Context(ctx), &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: r.start(resource, container, incarnation)}})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	readUntilEnded(r.t, conn)
	return nil
}

// run runs a command in the resource that selects the container.
func (r *cacheRig) run(resource, container string) error {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	target := r.start(resource, container, nil).Target
	res, err := r.client.Run(r.conns.Context(ctx), &execv2.RunRequest{Target: target, Argv: []string{"prog"}})
	if err == nil && res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
		r.t.Fatalf("the run ended %v", res.GetOutcome())
	}
	return err
}

func (r *cacheRig) mustOptions(resource string) {
	r.t.Helper()
	if _, err := r.options(context.Background(), resource); err != nil {
		r.t.Fatalf("Options(%s): %v", resource, err)
	}
}

func (r *cacheRig) mustOpen(id, resource, container string) {
	r.t.Helper()
	if err := r.open(id, resource, container, nil); err != nil {
		r.t.Fatalf("open %s in %s selecting %s: %v", id, resource, container, err)
	}
}

func (r *cacheRig) wantCalls(want int32, why string) {
	r.t.Helper()
	if got := r.calls.Load(); got != want {
		r.t.Fatalf("the callback ran %d times, want %d: %s", got, want, why)
	}
}

// Options for a resource, and then an Open of a session in it, run the callback once in all: the admission of the Open reuses the
// answer the picker got. So does the admission of a Run, and a second Open.
func TestAnOpenRightAfterOptionsRunsTheCallbackOnce(t *testing.T) {
	t.Run("an Open", func(t *testing.T) {
		rig := newCacheRig(t, exechook.EngineConfig{}, versioned)
		rig.mustOptions("thing-1")
		rig.wantCalls(1, "the RPC runs the callback")
		rig.mustOpen("s1", "thing-1", "c1")
		rig.wantCalls(1, "the Open right after it reuses the answer")
		rig.mustOpen("s2", "thing-1", "c1")
		rig.wantCalls(1, "so does the next")
	})
	t.Run("a Run", func(t *testing.T) {
		rig := newCacheRig(t, exechook.EngineConfig{}, versioned)
		rig.mustOptions("thing-1")
		if err := rig.run("thing-1", "c1"); err != nil {
			t.Fatal(err)
		}
		rig.wantCalls(1, "a Run right after Options reuses the answer")
	})
	t.Run("an Open that came first fills it for the next", func(t *testing.T) {
		rig := newCacheRig(t, exechook.EngineConfig{}, versioned)
		rig.mustOpen("s1", "thing-1", "c1")
		rig.wantCalls(1, "a cold Open runs the callback")
		rig.mustOpen("s2", "thing-1", "c1")
		if err := rig.run("thing-1", "c1"); err != nil {
			t.Fatal(err)
		}
		rig.wantCalls(1, "the cold Open filled the cache for what came after")
	})
}

// An answer lives for 5 s from the moment the callback returned: an Open one instant before that reuses it, and one at it runs the
// callback again, which fills the cache for the 5 s after.
func TestAnOpenAfterTheTTLRunsTheCallbackAgain(t *testing.T) {
	const ttl = 5 * time.Second
	rig := newCacheRig(t, exechook.EngineConfig{}, func(call int32) ([]exec.Option, error) {
		return []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", State: exec.ChoiceStateReady}}}}, nil
	})
	rig.mustOptions("thing-1")
	rig.wantCalls(1, "the RPC")
	rig.clock.advance(ttl - time.Millisecond)
	rig.mustOpen("s1", "thing-1", "app")
	rig.wantCalls(1, "an Open a millisecond before the TTL ends")
	rig.clock.advance(time.Millisecond)
	rig.mustOpen("s2", "thing-1", "app")
	rig.wantCalls(2, "an Open at the TTL")
	rig.clock.advance(ttl - time.Millisecond)
	rig.mustOpen("s3", "thing-1", "app")
	rig.wantCalls(2, "the Open at the TTL filled the cache for 5 s more, counted from its own callback")
	rig.clock.advance(time.Millisecond)
	rig.mustOpen("s4", "thing-1", "app")
	rig.wantCalls(3, "an Open 5 s after the callback of the Open that filled the cache")
}

// A request is its whole identity: another resource, another incarnation of the same one and one that claims none do not share an
// answer. (Two connections do not either: the key of the cache holds the route, which options_cache_internal_test.go holds.)
func TestRequestsOfAnotherResourceOrIncarnationDoNotShareACachedAnswer(t *testing.T) {
	rig := newCacheRig(t, exechook.EngineConfig{}, func(int32) ([]exec.Option, error) {
		return []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", State: exec.ChoiceStateReady}}}}, nil
	})
	rig.mustOptions("thing-1")
	rig.wantCalls(1, "the RPC")
	rig.mustOpen("s1", "thing-2", "app")
	rig.wantCalls(2, "another resource")
	for i, incarnation := range []string{"a", "b"} {
		if err := rig.open("i"+strconv.Itoa(i), "thing-1", "app", &incarnation); err != nil {
			t.Fatal(err)
		}
		rig.wantCalls(int32(3+i), "an incarnation the cache has not seen: "+strconv.Quote(incarnation))
	}
	for i, incarnation := range []string{"a", "b"} {
		if err := rig.open("j"+strconv.Itoa(i), "thing-1", "app", &incarnation); err != nil {
			t.Fatal(err)
		}
		rig.wantCalls(4, "an incarnation the cache has seen: "+strconv.Quote(incarnation))
	}
}

// The unary Options RPC never reads the cache: it runs the callback every time, and what it got is what the next Open is admitted
// against. A picker that refreshes sees the answer of its own call.
func TestTheOptionsRPCRunsTheCallbackFreshAndRefreshesWhatTheOpenReuses(t *testing.T) {
	rig := newCacheRig(t, exechook.EngineConfig{}, versioned)
	rig.mustOptions("thing-1")
	rig.mustOptions("thing-1")
	rig.wantCalls(2, "the RPC runs the callback every time, even right after it ran")
	if err := rig.open("s1", "thing-1", "c1", nil); err == nil {
		t.Fatal("an Open selecting the first answer's choice was admitted: the cache kept the first answer and the second refreshed nothing")
	}
	rig.wantCalls(2, "an Open that is refused for its selection ran the callback")
	rig.mustOpen("s2", "thing-1", "c2")
	rig.wantCalls(2, "the Open is admitted against the answer of the second call")
}

// A cache that is turned off asks the callback at every admission, as the engine did before it had one.
func TestAnOptionsCacheOfANegativeTTLIsOff(t *testing.T) {
	rig := newCacheRig(t, exechook.EngineConfig{OptionsCacheTTL: -1}, func(int32) ([]exec.Option, error) {
		return []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", State: exec.ChoiceStateReady}}}}, nil
	})
	rig.mustOptions("thing-1")
	rig.mustOpen("s1", "thing-1", "app")
	rig.mustOpen("s2", "thing-1", "app")
	rig.wantCalls(3, "the RPC and each Open ask the callback")
	if got := exec.OptionsCacheLen(rig.server.Plugin); got != 0 {
		t.Errorf("a cache that is off holds %d entries", got)
	}
}

// What is kept is an answer: a callback that failed, panicked or broke its contract is asked again by the next admission, and a
// refusal made of a failure that the cache kept would outlive the failure.
func TestACallbackThatFailedOrBrokeItsContractIsNeverCached(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first func() ([]exec.Option, error)
	}{
		{"an error", func() ([]exec.Option, error) { return nil, errors.New("the API is down") }},
		{"a panic", func() ([]exec.Option, error) { panic("boom") }},
		{"options that break the contract", func() ([]exec.Option, error) { return []exec.Option{{Name: "Not A Closed Code"}}, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newCacheRig(t, exechook.EngineConfig{}, func(call int32) ([]exec.Option, error) {
				if call == 1 {
					return tc.first()
				}
				return []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", State: exec.ChoiceStateReady}}}}, nil
			})
			if err := rig.open("s1", "thing-1", "app", nil); err == nil {
				t.Fatal("an Open was admitted against a callback that failed")
			}
			rig.wantCalls(1, "the first Open")
			if got := exec.OptionsCacheLen(rig.server.Plugin); got != 0 {
				t.Errorf("the cache holds %d entries after a callback that failed", got)
			}
			rig.mustOpen("s2", "thing-1", "app")
			rig.wantCalls(2, "the failure was kept and served to the next Open instead of asking again")
			rig.mustOpen("s3", "thing-1", "app")
			rig.wantCalls(2, "the answer of the second call is kept")
		})
	}
}

// The callback of the author may return one slice again and again and change it between calls: what the cache keeps is its own
// copy, so the change reaches the next call and not the answer that was kept (and the race detector sees no write under a reader).
func TestTheCacheKeepsACopyOfWhatTheCallbackReturned(t *testing.T) {
	shared := []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", State: exec.ChoiceStateReady}}}}
	rig := newCacheRig(t, exechook.EngineConfig{}, func(int32) ([]exec.Option, error) { return shared, nil })
	rig.mustOptions("thing-1")
	shared[0].Choices[0].Value = "changed" // the author changes what it returned, after it returned
	rig.mustOpen("s1", "thing-1", "app")
	rig.wantCalls(1, "the Open is admitted against the answer that was kept: the author's later change is not in it")
}

// Many Opens of one resource at a cold cache all succeed, ask the callback no more than once each, and leave the cache holding the one
// answer, which the next Open reuses; the Options RPC may run in the middle of them.
func TestConcurrentOpensAndOptionsCallsShareOneCacheSafely(t *testing.T) {
	const opens = 16
	rig := newCacheRig(t, exechook.EngineConfig{}, func(int32) ([]exec.Option, error) {
		return []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", State: exec.ChoiceStateReady}}}}, nil
	})
	errs := make(chan error, opens+2)
	start := make(chan struct{})
	for i := range opens {
		go func() {
			<-start
			errs <- rig.open("c"+strconv.Itoa(i), "thing-1", "app", nil)
		}()
	}
	for range 2 {
		go func() {
			<-start
			_, err := rig.options(context.Background(), "thing-1")
			errs <- err
		}()
	}
	close(start)
	for range opens + 2 {
		if err := awaitValue(t, errs, "an Open or an Options call to return"); err != nil {
			t.Error(err)
		}
	}
	if got := rig.calls.Load(); got < 1 || got > opens+2 {
		t.Errorf("the callback ran %d times for %d calls", got, opens+2)
	}
	if got := exec.OptionsCacheLen(rig.server.Plugin); got != 1 {
		t.Errorf("the cache holds %d entries for one resource, want 1", got)
	}
	before := rig.calls.Load()
	rig.mustOpen("after", "thing-1", "app")
	rig.wantCalls(before, "an Open after the burst reuses what it left")
}

// The cache is bounded: Opens of more resources than it holds leave it at its bound, whatever the order they came in.
func TestTheOptionsCacheNeverHoldsMoreThanItsBound(t *testing.T) {
	rig := newCacheRig(t, exechook.EngineConfig{}, func(int32) ([]exec.Option, error) {
		return []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", State: exec.ChoiceStateReady}}}}, nil
	})
	const resources = 80 // more than the 64 the engine keeps
	for i := range resources {
		rig.mustOpen("s"+strconv.Itoa(i), "thing-"+strconv.Itoa(i), "app")
		if got := exec.OptionsCacheLen(rig.server.Plugin); got > 64 {
			t.Fatalf("after %d resources the cache holds %d entries, its bound is 64", i+1, got)
		}
	}
	if got := exec.OptionsCacheLen(rig.server.Plugin); got != 64 {
		t.Errorf("the cache holds %d entries after %d resources, want its bound 64", got, resources)
	}
}

// A connection that was retired takes its cached answers with it. The resource lane has no hook for a retirement, so the engine
// sees it in the two places a lease is cancelled under its work: a session that the retirement ended, and an Options call that it
// cancelled. Each drops what the connection's requests cached, so that a connection that is started again under the same id is asked
// again and not answered from what it was before.
func TestARetiredConnectionDropsItsCachedOptions(t *testing.T) {
	t.Run("a session it ended", func(t *testing.T) {
		rig := newCacheRigRunning(t, exechook.EngineConfig{}, blockUntilCancelled, func(int32) ([]exec.Option, error) {
			return []exec.Option{{Name: "container", Required: true, Choices: []exec.Choice{{Value: "app", State: exec.ChoiceStateReady}}}}, nil
		})
		conn, err := rig.client.Open(rig.conns.Context(context.Background()),
			&execv2.OpenRequest{SessionId: "s1", Kind: &execv2.OpenRequest_Start{Start: rig.start("thing-1", "app", nil)}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if got := exec.OptionsCacheLen(rig.server.Plugin); got != 1 {
			t.Fatalf("the cache holds %d entries after an Open, want 1", got)
		}
		retired := rig.retire()
		_, ended := readUntilEnded(t, conn)
		if by := ended.GetEnd().GetClosed().GetBy(); by != execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED {
			t.Fatalf("the session ended %v, want closed by the retirement of its connection", ended.GetEnd())
		}
		// The end of the session is told after the engine saw the retirement: what the connection cached is gone by then.
		if got := exec.OptionsCacheLen(rig.server.Plugin); got != 0 {
			t.Errorf("the cache holds %d entries after the session was ended by the retirement, want none", got)
		}
		awaitChannel(t, retired, "the retirement to finish")
	})
	t.Run("an Options call it cancelled", func(t *testing.T) {
		rig := newCacheRig(t, exechook.EngineConfig{}, func(int32) ([]exec.Option, error) { return nil, nil })
		rig.mustOptions("thing-1")
		if got := exec.OptionsCacheLen(rig.server.Plugin); got != 1 {
			t.Fatalf("the cache holds %d entries after an Options call, want 1", got)
		}
		rig.hold.Store(true) // the next call waits for its context, which the retirement cancels
		call := rig.callAsync(context.Background(), "thing-1")
		awaitValue(t, rig.entered, "the callback to be inside")
		retired := rig.retire()
		if err := awaitValue(t, call, "the Options call to return"); err == nil {
			t.Error("an Options call whose connection was retired succeeded")
		}
		awaitChannel(t, retired, "the retirement to finish")
		if got := exec.OptionsCacheLen(rig.server.Plugin); got != 0 {
			t.Errorf("the cache holds %d entries after the retirement cancelled an Options call, want none", got)
		}
	})
}
