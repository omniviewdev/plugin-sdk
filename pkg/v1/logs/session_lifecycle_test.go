package logs

// A session's sources can be started from outside its own goroutines: an options
// update that re-enables a source restarts it. These tests pin when that is allowed
// and what an update gets when it is not, against the sessions' real manager.
//
// The hammer is the reviewer's targeted test of PR #82 (UpdateSessionOptions racing
// the end of a session), made small enough to run hundreds of times.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

const lifecycleWait = 5 * time.Second

// lifecycleSink keeps what a session sends in the order it was sent.
type lifecycleSink struct {
	mu      sync.Mutex
	items   []lifecycleItem
	changed chan struct{}
	ended   chan struct{} // closed when STREAM_ENDED arrives
}

type lifecycleItem struct {
	line  *LogLine
	event *LogStreamEvent
}

func newLifecycleSink() *lifecycleSink {
	return &lifecycleSink{changed: make(chan struct{}), ended: make(chan struct{})}
}

func (s *lifecycleSink) add(it lifecycleItem) {
	s.mu.Lock()
	s.items = append(s.items, it)
	close(s.changed)
	s.changed = make(chan struct{})
	if it.event != nil && it.event.Type == StreamEventStreamEnded {
		select {
		case <-s.ended:
		default:
			close(s.ended)
		}
	}
	s.mu.Unlock()
}

func (s *lifecycleSink) OnLine(l LogLine)                   { s.add(lifecycleItem{line: &l}) }
func (s *lifecycleSink) OnEvent(_ string, e LogStreamEvent) { s.add(lifecycleItem{event: &e}) }

func (s *lifecycleSink) snapshot() []lifecycleItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]lifecycleItem(nil), s.items...)
}

// until waits for cond to hold of what the session has sent so far.
func (s *lifecycleSink) until(t testing.TB, what string, cond func([]lifecycleItem) bool) []lifecycleItem {
	t.Helper()
	deadline := time.After(lifecycleWait)
	for {
		s.mu.Lock()
		items, changed := append([]lifecycleItem(nil), s.items...), s.changed
		s.mu.Unlock()
		if cond(items) {
			return items
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("timed out waiting for %s; the session sent: %s", what, describeLifecycle(items))
			return nil
		}
	}
}

func describeLifecycle(items []lifecycleItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		if it.line != nil {
			parts[i] = "L:" + it.line.Content
		} else {
			parts[i] = "E:" + it.event.Type.String() + "/" + it.event.SourceID
		}
	}
	return strings.Join(parts, " ")
}

func lifecycleLines(items []lifecycleItem) (n int) {
	for _, it := range items {
		if it.line != nil {
			n++
		}
	}
	return n
}

func lifecycleHasEvent(items []lifecycleItem, kind LogStreamEventType) bool {
	for _, it := range items {
		if it.event != nil && it.event.Type == kind {
			return true
		}
	}
	return false
}

// linesAfterEnd counts the lines that follow STREAM_ENDED, which must be none.
func linesAfterEnd(items []lifecycleItem) (n int) {
	ended := false
	for _, it := range items {
		switch {
		case it.event != nil && it.event.Type == StreamEventStreamEnded:
			ended = true
		case it.line != nil && ended:
			n++
		}
	}
	return n
}

type lifecycleRig struct {
	t    *testing.T
	mgr  *Manager
	sink *lifecycleSink
	ctx  *types.PluginContext
}

func newLifecycleRig(t *testing.T, cfg ManagerConfig) *lifecycleRig {
	t.Helper()
	sink := newLifecycleSink()
	cfg.Sink = sink
	mgr := NewManager(cfg)
	t.Cleanup(mgr.Close)
	return &lifecycleRig{t: t, mgr: mgr, sink: sink, ctx: types.NewPluginContext(context.Background(), "test", nil, nil, nil)}
}

func (r *lifecycleRig) create(key string, o LogSessionOptions) *LogSession {
	r.t.Helper()
	s, err := r.mgr.CreateResourceSession(r.ctx, CreateSessionOptions{ResourceKey: key, ResourceID: "x", Options: o})
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *lifecycleRig) state(id string) *sessionState {
	r.t.Helper()
	r.mgr.mu.RLock()
	defer r.mgr.mu.RUnlock()
	ss := r.mgr.sessions[id]
	if ss == nil {
		r.t.Fatalf("session %s is not registered", id)
	}
	return ss
}

// waitEnded waits until the session has sent STREAM_ENDED and published completion.
func (r *lifecycleRig) waitEnded(id string) {
	r.t.Helper()
	select {
	case <-r.sink.ended:
	case <-time.After(lifecycleWait):
		r.t.Fatalf("the session never sent STREAM_ENDED; it sent: %s", describeLifecycle(r.sink.snapshot()))
	}
	select {
	case <-r.state(id).done:
	case <-time.After(lifecycleWait):
		r.t.Fatal("the session never published completion")
	}
}

// waitNoSourceRunning waits until no source of the session holds a registration: the
// initial sources have all ended, or been disabled.
func (r *lifecycleRig) waitNoSourceRunning(id string) {
	r.t.Helper()
	ss := r.state(id)
	deadline := time.Now().Add(lifecycleWait)
	for time.Now().Before(deadline) {
		ss.sourceMu.Lock()
		n := len(ss.sourceCtxs)
		ss.sourceMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	r.t.Fatal("sources are still registered")
}

func (r *lifecycleRig) update(id string, o LogSessionOptions) (*LogSession, error) {
	return r.mgr.UpdateSessionOptions(nil, id, o)
}

func selection(sources string) LogSessionOptions {
	return LogSessionOptions{Params: map[string]string{"enabled_sources": sources}}
}

// assertRefused checks that err is the typed refusal of an update the session can no
// longer act on: a CONFLICT the caller can tell from a failure.
func assertRefused(t testing.TB, err error) {
	t.Helper()
	var typed *resourcev2.ResourceOperationError
	if err == nil || !errors.As(err, &typed) || typed.Code != "CONFLICT" || resourcev2.ErrorCode(err) != "CONFLICT" {
		t.Fatalf("the update was not refused with a CONFLICT error: %v", err)
	}
}

// finiteSources is a handler whose session has two sources, each a finite read of two
// lines named after it: a-1 a-2 and b-1 b-2.
func finiteSources() Handler {
	return Handler{
		Plugin: "test", Resource: "pod",
		SourceBuilder: func(string, *resourcev2.Value, LogSessionOptions) []LogSource {
			return []LogSource{{ID: "a"}, {ID: "b"}}
		},
		Handler: func(_ *types.PluginContext, req LogStreamRequest) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(req.SourceID + "-1\n" + req.SourceID + "-2\n")), nil
		},
	}
}

// gatedReader blocks every Read until its gate is closed, the reader is closed or the
// context ends, and then reads to its end.
type gatedReader struct {
	gate <-chan struct{}
	ctx  context.Context
	done chan struct{}
	once sync.Once
}

func newGatedReader(ctx context.Context, gate <-chan struct{}) *gatedReader {
	return &gatedReader{gate: gate, ctx: ctx, done: make(chan struct{})}
}

func (g *gatedReader) Read([]byte) (int, error) {
	select {
	case <-g.gate:
	case <-g.done:
	case <-g.ctx.Done():
	}
	return 0, io.EOF
}

func (g *gatedReader) Close() error {
	g.once.Do(func() { close(g.done) })
	return nil
}

// An update that would start a source of a session that has ended is refused: the
// session has published completion, and a source started now would stream after
// STREAM_ENDED to a reader that has stopped listening. The refusal changes nothing.
func TestUpdateOfAnEndedSessionIsRefusedAndStartsNothing(t *testing.T) {
	r := newLifecycleRig(t, ManagerConfig{Handlers: map[string]Handler{"test/pod": finiteSources()}})
	session := r.create("pod", LogSessionOptions{Follow: false, TailLines: 3})
	r.waitEnded(session.ID)
	linesAtEnd := lifecycleLines(r.sink.snapshot())

	got, err := r.update(session.ID, LogSessionOptions{TailLines: 9, Params: map[string]string{"enabled_sources": ""}})
	assertRefused(t, err)
	if got != nil {
		t.Errorf("a refused update returned a session: %+v", got)
	}

	time.Sleep(50 * time.Millisecond)
	items := r.sink.snapshot()
	if n := linesAfterEnd(items); n != 0 {
		t.Fatalf("%d lines after STREAM_ENDED: %s", n, describeLifecycle(items))
	}
	if n := lifecycleLines(items); n != linesAtEnd {
		t.Errorf("the refused update started sources: %d lines, %d at the end", n, linesAtEnd)
	}
	kept, err := r.mgr.GetSession(nil, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Options.TailLines != 3 {
		t.Errorf("a refused update changed the options: tail lines %d, want 3", kept.Options.TailLines)
	}
}

// An update that starts nothing has nothing to refuse, so it is applied to an ended
// session as it always was.
func TestAnUpdateThatStartsNoSourceIsAppliedToAnEndedSession(t *testing.T) {
	cases := map[string]LogSessionOptions{
		"no source selection":          {TailLines: 9},
		"a selection naming no source": {TailLines: 9, Params: map[string]string{"enabled_sources": "nope"}},
	}
	for name, update := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLifecycleRig(t, ManagerConfig{Handlers: map[string]Handler{"test/pod": finiteSources()}})
			session := r.create("pod", LogSessionOptions{Follow: false, TailLines: 3})
			r.waitEnded(session.ID)

			got, err := r.update(session.ID, update)
			if err != nil {
				t.Fatalf("an update that starts no source was refused: %v", err)
			}
			if got.Options.TailLines != 9 {
				t.Errorf("returned options: tail lines %d, want 9", got.Options.TailLines)
			}
			kept, err := r.mgr.GetSession(nil, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if kept.Options.TailLines != 9 {
				t.Errorf("stored options: tail lines %d, want 9", kept.Options.TailLines)
			}
		})
	}
}

// While one source still runs, a session is alive and an update may restart a source
// that ended: the restarted source streams before the session ends.
func TestUpdateRestartsAnEndedSourceWhileAnotherStillRuns(t *testing.T) {
	gate := make(chan struct{})
	h := finiteSources()
	h.Handler = func(pctx *types.PluginContext, req LogStreamRequest) (io.ReadCloser, error) {
		if req.SourceID == "b" {
			return newGatedReader(pctx.Context, gate), nil
		}
		return io.NopCloser(strings.NewReader("a-1\n")), nil
	}
	r := newLifecycleRig(t, ManagerConfig{Handlers: map[string]Handler{"test/pod": h}})
	session := r.create("pod", LogSessionOptions{Follow: false})
	r.sink.until(t, "the first line of a", func(items []lifecycleItem) bool { return lifecycleLines(items) == 1 })
	ss := r.state(session.ID)
	deadline := time.Now().Add(lifecycleWait)
	for {
		ss.sourceMu.Lock()
		_, running := ss.sourceCtxs["a"]
		ss.sourceMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source a never ended")
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := r.update(session.ID, selection("")); err != nil {
		t.Fatalf("an update that restarts a source of a live session was refused: %v", err)
	}
	items := r.sink.until(t, "a to stream again", func(items []lifecycleItem) bool { return lifecycleLines(items) == 2 })
	if lifecycleHasEvent(items, StreamEventStreamEnded) {
		t.Fatalf("the session ended while source b was still running: %s", describeLifecycle(items))
	}
	// The event that announces the source comes before its first line.
	reEnabled, secondLine := -1, -1
	for i, it := range items {
		if it.event != nil && it.event.Type == StreamEventSourceAdded && it.event.SourceID == "a" && strings.HasPrefix(it.event.Message, "Source re-enabled") {
			reEnabled = i
		}
		if it.line != nil && secondLine < 0 && i > 0 && lifecycleLines(items[:i]) == 1 {
			secondLine = i
		}
	}
	if reEnabled < 0 || reEnabled > secondLine {
		t.Errorf("the re-enabled event must come before the restarted source's line: %s", describeLifecycle(items))
	}

	close(gate)
	r.waitEnded(session.ID)
	if n := linesAfterEnd(r.sink.snapshot()); n != 0 {
		t.Errorf("%d lines after STREAM_ENDED", n)
	}
}

// A session whose initial sources have all ended stays alive for as long as its
// source watcher runs, and an update may restart a source for as long. When the
// watcher and the restarted sources have finished the session has ended, and the
// next update that would start a source is refused.
func TestUpdateRestartsSourcesOfASessionKeptOpenByItsWatcher(t *testing.T) {
	events := make(chan SourceEvent)
	r := newLifecycleRig(t, ManagerConfig{
		Handlers: map[string]Handler{"test/pod": finiteSources()},
		Resolvers: map[string]SourceResolver{
			"deployment": func(*types.PluginContext, *resourcev2.Value, SourceResolverOptions) (*SourceResolverResult, error) {
				return &SourceResolverResult{Sources: []LogSource{{ID: "a"}, {ID: "b"}}, Events: events}, nil
			},
		},
	})
	session := r.create("deployment", LogSessionOptions{Follow: true})
	r.sink.until(t, "both sources to stream", func(items []lifecycleItem) bool { return lifecycleLines(items) == 4 })
	r.waitNoSourceRunning(session.ID)
	time.Sleep(20 * time.Millisecond)
	if lifecycleHasEvent(r.sink.snapshot(), StreamEventStreamEnded) {
		t.Fatal("the session ended while its watcher was still running")
	}

	if _, err := r.update(session.ID, selection("")); err != nil {
		t.Fatalf("an update that restarts the sources of a live session was refused: %v", err)
	}
	r.sink.until(t, "both sources to stream again", func(items []lifecycleItem) bool { return lifecycleLines(items) == 8 })

	close(events) // the watcher ends; the restarted sources have already finished
	r.waitEnded(session.ID)
	if n := linesAfterEnd(r.sink.snapshot()); n != 0 {
		t.Errorf("%d lines after STREAM_ENDED", n)
	}
	_, err := r.update(session.ID, selection(""))
	assertRefused(t, err)
}

// A session that is being closed has cancelled its sources: an update that would
// start one is refused, whether the last producer has returned yet or not.
func TestUpdateOfAClosingSessionIsRefused(t *testing.T) {
	var opened atomic.Int32
	h := finiteSources()
	h.Handler = func(pctx *types.PluginContext, _ LogStreamRequest) (io.ReadCloser, error) {
		opened.Add(1)
		return newGatedReader(pctx.Context, nil), nil
	}
	r := newLifecycleRig(t, ManagerConfig{Handlers: map[string]Handler{"test/pod": h}})
	session := r.create("pod", LogSessionOptions{Follow: true})
	deadline := time.Now().Add(lifecycleWait)
	for opened.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the sources never opened")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := r.update(session.ID, selection("a")); err != nil { // b is disabled
		t.Fatal(err)
	}

	r.state(session.ID).cancel() // the session starts to close

	_, err := r.update(session.ID, selection(""))
	assertRefused(t, err)
}

// An update that lands while a session ends either restarts a source, which then
// finishes before the session does, or is refused. It never crashes the process and
// never lets a line follow STREAM_ENDED. This is the reviewer's targeted test: before
// the fix sync.WaitGroup was asked to gain a member while Wait could be returning, which
// is a data race under -race and a panic ("WaitGroup is reused before previous Wait
// has returned") otherwise.
func TestUpdateSessionOptionsRacingTheEndNeverCrashes(t *testing.T) {
	const sessions, hammers = 24, 3
	selections := []string{"", "a,b", "a", "b", "a,b"}
	for range sessions {
		r := newLifecycleRig(t, ManagerConfig{Handlers: map[string]Handler{"test/pod": finiteSources()}})
		session := r.create("pod", LogSessionOptions{Follow: false})

		stop := make(chan struct{})
		var wg sync.WaitGroup
		for g := range hammers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := g; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					if _, err := r.update(session.ID, selection(selections[i%len(selections)])); err != nil {
						var typed *resourcev2.ResourceOperationError
						if !errors.As(err, &typed) || typed.Code != "CONFLICT" {
							t.Errorf("an update failed with something other than the refusal: %v", err)
							return
						}
					}
				}
			}()
		}
		r.waitEnded(session.ID)
		time.Sleep(time.Millisecond) // keep updating the ended session
		close(stop)
		wg.Wait()

		if items := r.sink.snapshot(); linesAfterEnd(items) != 0 {
			t.Fatalf("lines after STREAM_ENDED: %s", describeLifecycle(items))
		}
		r.mgr.Close()
	}
}

// The refusal is a classified error, and it keeps its classification across the
// plugin boundary, where every update failure used to arrive as an unclassified
// Internal status. A failure that is not a refusal still arrives as it did.
func TestRefusedUpdateKeepsItsClassificationOverGRPC(t *testing.T) {
	r := newLifecycleRig(t, ManagerConfig{Handlers: map[string]Handler{"test/pod": finiteSources()}})
	session := r.create("pod", LogSessionOptions{Follow: false})
	r.waitEnded(session.ID)

	connection := logsConnection(t, func(s *grpc.Server) { RegisterLogPluginServer(s, &PluginServer{Impl: r.mgr}) })
	client := &PluginClient{client: logspb.NewLogPluginClient(connection)}

	_, err := client.UpdateSessionOptions(r.ctx, session.ID, selection(""))
	assertRefused(t, err)
	var typed *resourcev2.ResourceOperationError
	if errors.As(err, &typed) && status.Code(typed.Err) != codes.Aborted {
		t.Errorf("transport code = %v, want Aborted", status.Code(typed.Err))
	}

	_, err = client.UpdateSessionOptions(r.ctx, "no-such-session", selection(""))
	if err == nil || status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "not found") {
		t.Errorf("an unknown session must fail as it did, with an Internal status that says so: %v", err)
	}
}
