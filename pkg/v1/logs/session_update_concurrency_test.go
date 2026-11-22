package logs

// Options updates can overlap: the log viewer syncs its source selection 300 ms after
// the last change, so a slow plugin makes two updates in flight plausible. These tests
// pin that the update that commits last decides which sources run, however they
// interleave, and that the lock that makes it so is held across no send, no plugin call
// and no wait.

import (
	"context"
	"io"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// streamTracker is a handler whose sources stream until their context ends, and that
// counts the streams it has open. A source named "x" is a finite read of one line.
type streamTracker struct {
	mu     sync.Mutex
	open   map[string]int
	opened map[string]int
	hold   map[string]chan struct{} // the next stream of the source closes only once its channel is closed
}

func newStreamTracker() *streamTracker {
	return &streamTracker{open: map[string]int{}, opened: map[string]int{}, hold: map[string]chan struct{}{}}
}

func (t *streamTracker) handler() Handler {
	return Handler{
		Plugin: "test", Resource: "pod",
		Handler: func(pctx *types.PluginContext, req LogStreamRequest) (io.ReadCloser, error) {
			if req.SourceID == "x" {
				return io.NopCloser(strings.NewReader("x\n")), nil
			}
			t.mu.Lock()
			defer t.mu.Unlock()
			t.open[req.SourceID]++
			t.opened[req.SourceID]++
			hold := t.hold[req.SourceID]
			delete(t.hold, req.SourceID)
			return &trackedReader{t: t, id: req.SourceID, ctx: pctx.Context, hold: hold, done: make(chan struct{})}, nil
		},
	}
}

func (t *streamTracker) openAmong(ids []string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for _, id := range ids {
		if t.open[id] > 0 {
			out = append(out, id)
		}
	}
	return out
}

func (t *streamTracker) openedCount(id string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opened[id]
}

type trackedReader struct {
	t    *streamTracker
	id   string
	ctx  context.Context
	hold chan struct{}
	done chan struct{}
	once sync.Once
}

func (r *trackedReader) Read([]byte) (int, error) {
	select {
	case <-r.done:
	case <-r.ctx.Done():
	}
	return 0, io.EOF
}

func (r *trackedReader) Close() error {
	if r.hold != nil {
		<-r.hold
	}
	r.once.Do(func() {
		close(r.done)
		r.t.mu.Lock()
		r.t.open[r.id]--
		r.t.mu.Unlock()
	})
	return nil
}

// updateRig is a session of resolved sources that stays open, because its watcher does.
type updateRig struct {
	*lifecycleRig
	tracker *streamTracker
	session *LogSession
	ids     []string
	events  chan SourceEvent // the resolver's source events; the watcher reads them
}

func newUpdateRig(t *testing.T, sink OutputSink, ids ...string) *updateRig {
	t.Helper()
	tracker := newStreamTracker()
	return newUpdateRigWith(t, tracker, sink, ids...)
}

func newUpdateRigWith(t *testing.T, tracker *streamTracker, sink OutputSink, ids ...string) *updateRig {
	t.Helper()
	events := make(chan SourceEvent)
	t.Cleanup(func() { close(events) })
	var sources []LogSource
	for _, id := range ids {
		sources = append(sources, LogSource{ID: id})
	}
	cfg := ManagerConfig{
		Handlers: map[string]Handler{"test/pod": tracker.handler()},
		Resolvers: map[string]SourceResolver{
			"deployment": func(*types.PluginContext, *resourcev2.Value, SourceResolverOptions) (*SourceResolverResult, error) {
				return &SourceResolverResult{Sources: sources, Events: events}, nil
			},
		},
	}
	var rig *lifecycleRig
	if sink == nil {
		rig = newLifecycleRig(t, cfg)
	} else {
		cfg.Sink = sink
		mgr := NewManager(cfg)
		t.Cleanup(mgr.Close)
		rig = &lifecycleRig{t: t, mgr: mgr, sink: newLifecycleSink(), ctx: types.NewPluginContext(context.Background(), "test", nil, nil, nil)}
	}
	session := rig.create("deployment", LogSessionOptions{Follow: true})
	return &updateRig{lifecycleRig: rig, tracker: tracker, session: session, ids: ids, events: events}
}

// registered lists the sources, among ids, that hold a registration in the session.
func (r *updateRig) registered(ids []string) []string {
	ss := r.state(r.session.ID)
	ss.sourceMu.Lock()
	defer ss.sourceMu.Unlock()
	var out []string
	for _, id := range ids {
		if _, ok := ss.sourceCtxs[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// waitSettled waits until exactly the sources in want, among the rig's long-running
// sources, hold a registration and have a stream open.
func (r *updateRig) waitSettled(want []string, why string) {
	r.t.Helper()
	long := slices.DeleteFunc(slices.Clone(r.ids), func(id string) bool { return id == "x" })
	deadline := time.Now().Add(lifecycleWait)
	for {
		open, reg := r.tracker.openAmong(long), r.registered(long)
		if slices.Equal(open, want) && slices.Equal(reg, want) {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("%s: want exactly %v running; streams open %v, registered %v; stored selection %q",
				why, want, open, reg, r.storedSelection())
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *updateRig) storedSelection() string {
	s, err := r.mgr.GetSession(nil, r.session.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return s.Options.Params["enabled_sources"]
}

// enabledBy is the sources, among ids, that a stored enabled_sources selection enables.
func enabledBy(stored string, ids []string) []string {
	if stored == "" {
		return slices.Clone(ids)
	}
	set := enabledSourceIDs(stored)
	var out []string
	for _, id := range ids {
		if set[id] {
			out = append(out, id)
		}
	}
	return out
}

// awaitBlocked waits until a goroutine is blocked on a mutex inside a function whose name
// contains fn: the point a test froze it at.
func awaitBlocked(t *testing.T, fn string) {
	t.Helper()
	buf := make([]byte, 4<<20)
	deadline := time.Now().Add(lifecycleWait)
	for time.Now().Before(deadline) {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			header, _, _ := strings.Cut(g, "\n")
			if strings.Contains(g, fn) && (strings.Contains(header, "Mutex.Lock") || strings.Contains(header, "semacquire")) {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no goroutine is blocked on a mutex in %s", fn)
}

// Two updates that overlap leave the session as the one that commits last says. The
// first, which brings every source back, restarts the ended source x and is frozen
// there, between settling its plan and committing it; the second, which keeps only b,
// is then free to run to its end. Before, the second updated the sources while the
// first held a plan that did not know it had: the first then stored "all enabled" and
// started nothing, and a stayed stopped.
func TestOverlappingUpdatesLeaveTheSourcesTheLastStoredSelectionEnables(t *testing.T) {
	r := newUpdateRig(t, nil, "a", "b", "x")
	r.waitSettled([]string{"a", "b"}, "the session")
	ss := r.state(r.session.ID)
	deadline := time.Now().Add(lifecycleWait)
	for len(r.registered([]string{"x"})) > 0 { // x is finite: wait until it has ended
		if time.Now().After(deadline) {
			t.Fatal("source x never ended")
		}
		time.Sleep(time.Millisecond)
	}

	ss.producers.mu.Lock() // freezes whichever update has to reserve a producer, at the reservation
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := r.update(r.session.ID, selection("")); err != nil {
			t.Errorf("first update: %v", err)
		}
	})
	awaitBlocked(t, "startProducers")

	secondDone := make(chan struct{})
	wg.Go(func() {
		defer close(secondDone)
		if _, err := r.update(r.session.ID, selection("b")); err != nil {
			t.Errorf("second update: %v", err)
		}
	})
	select { // a serialised second update is waiting for the first; an unserialised one finishes here
	case <-secondDone:
	case <-time.After(150 * time.Millisecond):
	}
	ss.producers.mu.Unlock()
	wg.Wait()

	r.waitSettled(enabledBy(r.storedSelection(), []string{"a", "b"}), "after both updates")
}

// A stream that ends after its source was disabled and enabled again must not take the
// registration of the new run with it: the source would look stopped while it streams,
// be started a second time by the next update, and never be cancelled again.
func TestTheExitOfAnOldStreamKeepsTheRegistrationOfTheNewRun(t *testing.T) {
	tracker := newStreamTracker()
	release := make(chan struct{})
	tracker.hold["a"] = release // the first stream of a closes only when the test says
	r := newUpdateRigWith(t, tracker, nil, "a", "b")
	r.waitSettled([]string{"a", "b"}, "the session")

	if _, err := r.update(r.session.ID, selection("b")); err != nil { // a is disabled; its stream is slow to close
		t.Fatal(err)
	}
	if _, err := r.update(r.session.ID, selection("")); err != nil { // a is enabled again: a second run
		t.Fatal(err)
	}
	deadline := time.Now().Add(lifecycleWait)
	for tracker.openedCount("a") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the second run of a never opened its stream")
		}
		time.Sleep(time.Millisecond)
	}

	close(release) // the old stream ends now
	deadline = time.Now().Add(lifecycleWait)
	for {
		tracker.mu.Lock()
		open := tracker.open["a"]
		tracker.mu.Unlock()
		if open == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the old stream of a never closed: %d open", open)
		}
		time.Sleep(time.Millisecond)
	}
	if got := r.registered([]string{"a"}); !slices.Equal(got, []string{"a"}) {
		t.Fatal("the exit of the old stream of a removed the registration of its new run")
	}
	if _, err := r.update(r.session.ID, selection("b")); err != nil { // and a can still be disabled
		t.Fatal(err)
	}
	r.waitSettled([]string{"b"}, "after a was disabled again")
}

// gateSink holds the first SourceRemoved event until it is released.
type gateSink struct {
	*lifecycleSink
	gate    chan struct{}
	blocked chan struct{}
	once    sync.Once
}

func (s *gateSink) OnEvent(id string, e LogStreamEvent) {
	if e.Type == StreamEventSourceRemoved {
		s.once.Do(func() {
			close(s.blocked)
			<-s.gate
		})
	}
	s.lifecycleSink.OnEvent(id, e)
}

// The lock that serialises updates is not held while an update announces what it
// changed, because the announcement goes through the output, which can block for as
// long as its consumer does: a second update runs to its end while the first is stuck
// in the send.
func TestUpdatesAreNotHeldUpByAnotherUpdatesBlockedAnnouncement(t *testing.T) {
	sink := &gateSink{lifecycleSink: newLifecycleSink(), gate: make(chan struct{}), blocked: make(chan struct{})}
	r := newUpdateRig(t, sink, "a", "b")
	r.waitSettled([]string{"a", "b"}, "the session")

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		if _, err := r.update(r.session.ID, selection("b")); err != nil { // cancels a: its event blocks
			t.Errorf("first update: %v", err)
		}
	}()
	select {
	case <-sink.blocked:
	case <-time.After(lifecycleWait):
		t.Fatal("the first update never announced the source it cancelled")
	}

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		if _, err := r.update(r.session.ID, selection("")); err != nil { // brings a back
			t.Errorf("second update: %v", err)
		}
	}()
	select {
	case <-secondDone:
	case <-time.After(lifecycleWait):
		t.Fatal("an update waited for another update's blocked event send")
	}
	close(sink.gate)
	<-firstDone
	r.waitSettled([]string{"a", "b"}, "after both updates")
}

// Many updates with different selections, overlapping: whenever they stop, the sources
// that run are exactly the ones the stored selection enables, every time.
func TestOverlappingUpdatesWithDifferentSelectionsConverge(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e"}
	r := newUpdateRig(t, nil, ids...)
	r.waitSettled(ids, "the session")

	choose := func(rng *rand.Rand) string {
		switch rng.IntN(6) {
		case 0:
			return "" // all
		case 1:
			return "none" // a name that matches no source: all are disabled
		}
		var picked []string
		for _, id := range ids {
			if rng.IntN(2) == 0 {
				picked = append(picked, id)
			}
		}
		return strings.Join(picked, ",")
	}

	const workers, rounds = 6, 12
	for round := range rounds {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(uint64(round), uint64(w)))
				for {
					select {
					case <-stop:
						return
					default:
					}
					if _, err := r.update(r.session.ID, selection(choose(rng))); err != nil {
						t.Errorf("update: %v", err)
						return
					}
				}
			}()
		}
		time.Sleep(60 * time.Millisecond)
		close(stop)
		wg.Wait()
		r.waitSettled(enabledBy(r.storedSelection(), ids), "round "+string(rune('0'+round%10)))
	}
}

// A source can be listed twice: a watcher adds one again after it ended. An update that
// brings every source back restarts it once (found by the reviewer's stress test, in
// the first version of the change, which registered it twice and panicked).
func TestAnUpdateRestartsASourceOnceWhenItIsListedTwice(t *testing.T) {
	r := newUpdateRig(t, nil, "x")
	r.sink.until(t, "the first run of x", func(items []lifecycleItem) bool { return lifecycleLines(items) == 1 })
	r.waitNoSourceRunning(r.session.ID)

	r.events <- SourceEvent{Type: SourceAdded, Source: LogSource{ID: "x"}} // x is listed again, and runs again
	r.sink.until(t, "the second run of x", func(items []lifecycleItem) bool { return lifecycleLines(items) == 2 })
	r.waitNoSourceRunning(r.session.ID)
	if n := len(r.state(r.session.ID).snapshot().ActiveSources); n != 2 {
		t.Fatalf("x is listed %d times, want 2", n)
	}

	if _, err := r.update(r.session.ID, selection("")); err != nil {
		t.Fatal(err)
	}
	r.sink.until(t, "the restarted x", func(items []lifecycleItem) bool { return lifecycleLines(items) == 3 })
	time.Sleep(30 * time.Millisecond)
	if got := lifecycleLines(r.sink.snapshot()); got != 3 {
		t.Fatalf("x ran %d times for one update, want once", got-2)
	}
}
