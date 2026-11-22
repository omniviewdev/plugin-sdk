package logs_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs/logtest"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

const (
	replayWait    = 5 * time.Second
	replaySession = "6f1f9a3e-31c8-4f0e-9d44-0a7f5d1c2b10"
)

// replayRig is a real manager over one replay-capable source.
type replayRig struct {
	t   *testing.T
	out *logtest.RecordingOutput
	mgr *logs.Manager
	ctx *types.PluginContext
}

func newReplayRig(t *testing.T, handlers map[string]logs.Handler, tune ...func(*logs.ManagerConfig)) *replayRig {
	t.Helper()
	out := logtest.NewRecordingOutput()
	cfg := logs.ManagerConfig{Handlers: handlers, Sink: out, Clock: timeutil.NewInstantFakeClock()}
	for _, f := range tune {
		f(&cfg)
	}
	mgr := logs.NewManager(cfg)
	t.Cleanup(mgr.Close)
	return &replayRig{t: t, out: out, mgr: mgr, ctx: types.NewPluginContext(context.Background(), "test", nil, nil, nil)}
}

func oneSource(src *logtest.ReplaySource) map[string]logs.Handler {
	return map[string]logs.Handler{"test/pod": src.Handler("pod")}
}

// asking is a first-open request that asks for replay markers.
func asking(o logs.LogSessionOptions) logs.LogSessionOptions {
	params := map[string]string{logs.ParamReplayMarkers: logs.ReplayMarkersOn}
	for k, v := range o.Params {
		params[k] = v
	}
	o.Params = params
	return o
}

func (r *replayRig) open(o logs.LogSessionOptions) *logs.LogSession { return r.openKey("pod", o) }

func (r *replayRig) openKey(key string, o logs.LogSessionOptions) *logs.LogSession {
	r.t.Helper()
	session, err := r.mgr.CreateResourceSession(r.ctx, logs.CreateSessionOptions{
		SessionID: replaySession, ResourceKey: key, ResourceID: "web-1", Options: o,
	})
	if err != nil {
		r.t.Fatalf("CreateResourceSession: %v", err)
	}
	return session
}

func (r *replayRig) marker(source string, n int) logs.LogStreamEvent {
	r.t.Helper()
	deadline := time.Now().Add(replayWait)
	for time.Now().Before(deadline) {
		if found := markersOf(r.out.Events(), source); len(found) >= n {
			return found[n-1]
		}
		time.Sleep(time.Millisecond)
	}
	r.t.Fatalf("source %q: marker #%d never came; events: %v", source, n, eventKinds(r.out.Events()))
	return logs.LogStreamEvent{}
}

func markersOf(events []logs.LogStreamEvent, source string) []logs.LogStreamEvent {
	var found []logs.LogStreamEvent
	for _, e := range events {
		if e.Type == logs.StreamEventSourceCaughtUp && e.SourceID == source {
			found = append(found, e)
		}
	}
	return found
}

func eventKinds(events []logs.LogStreamEvent) []string {
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Type.String() + "/" + e.SourceID
	}
	return kinds
}

func contents(lines []logs.LogLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Content
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(replayWait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func logspbEventType(n int32) logspb.LogStreamEventType { return logspb.LogStreamEventType(n) }

func requireIncreasing(t *testing.T, what string, seqs []uint64) {
	t.Helper()
	for i, s := range seqs {
		if s == 0 {
			t.Fatalf("%s: seq %d is zero (not numbered)", what, i)
		}
		if i > 0 && s <= seqs[i-1] {
			t.Fatalf("%s: seq not increasing: %v", what, seqs)
		}
	}
}

func lineSeqs(lines []logs.LogLine) []uint64 {
	out := make([]uint64, len(lines))
	for i, l := range lines {
		out[i] = l.Seq
	}
	return out
}

// requireUnique fails when two lines or events of a session share a number.
func requireUnique(t *testing.T, out *logtest.RecordingOutput) {
	t.Helper()
	seen := map[uint64]string{}
	note := func(seq uint64, what string) {
		if seq == 0 {
			return
		}
		if prior, dup := seen[seq]; dup {
			t.Fatalf("seq %d used twice: %s and %s", seq, prior, what)
		}
		seen[seq] = what
	}
	for _, l := range out.Lines() {
		note(l.Seq, "line "+l.Content)
	}
	for _, e := range out.Events() {
		note(e.Seq, "event "+e.Type.String())
	}
}

// A replay of five lines, then two live ones: the SDK numbers everything, flags
// the backlog, and says exactly where it ends.
func TestReplayThenLiveNumbersFlagsAndMarks(t *testing.T) {
	src := logtest.NewReplaySource("b1", "b2", "b3", "b4", "b5")
	rig := newReplayRig(t, oneSource(src))
	session := rig.open(asking(logs.LogSessionOptions{Follow: true, TailLines: 100}))
	if !session.ReplayMarkers {
		t.Fatal("the create reply must say the plugin accepted replay markers")
	}

	marker := rig.marker("pod", 1)
	src.Append("l1", "l2")
	lines := rig.out.WaitForLines(7, replayWait)
	if got := contents(lines); !slices.Equal(got, []string{"b1", "b2", "b3", "b4", "b5", "l1", "l2"}) {
		t.Fatalf("lines = %v", got)
	}
	for i, l := range lines {
		if l.Replay != (i < 5) {
			t.Errorf("line %q: Replay = %v, want %v", l.Content, l.Replay, i < 5)
		}
	}
	requireIncreasing(t, "all lines", lineSeqs(lines))
	if marker.AfterSeq != lines[4].Seq {
		t.Errorf("marker.AfterSeq = %d, want the last replay line's seq %d", marker.AfterSeq, lines[4].Seq)
	}
	if marker.Seq == 0 || marker.Seq <= marker.AfterSeq || marker.Seq >= lines[5].Seq {
		t.Errorf("marker.Seq = %d must sit between the last replay line (%d) and the first live one (%d)",
			marker.Seq, lines[4].Seq, lines[5].Seq)
	}
	requireUnique(t, rig.out)
	if n := len(markersOf(rig.out.Events(), "pod")); n != 1 {
		t.Errorf("markers = %d, want exactly one", n)
	}
}

// A source with nothing to replay is caught up at once, and everything it sends
// after is live.
func TestReplayOfAnEmptyBacklogMarksAtOnce(t *testing.T) {
	src := logtest.NewReplaySource()
	rig := newReplayRig(t, oneSource(src))
	rig.open(asking(logs.LogSessionOptions{Follow: true}))

	marker := rig.marker("pod", 1)
	if marker.AfterSeq != 0 {
		t.Errorf("marker.AfterSeq = %d, want 0 for an empty backlog", marker.AfterSeq)
	}
	src.Append("first")
	lines := rig.out.WaitForLines(1, replayWait)
	if lines[0].Replay {
		t.Error("a line produced after the marker is live, not replay")
	}
	if lines[0].Seq <= marker.Seq {
		t.Errorf("live line seq %d must follow the marker's %d", lines[0].Seq, marker.Seq)
	}
}

// Without Follow the replay is the whole session: caught up, then done.
func TestReplayAloneWhenNotFollowing(t *testing.T) {
	src := logtest.NewReplaySource("b1", "b2", "b3")
	rig := newReplayRig(t, oneSource(src))
	rig.open(asking(logs.LogSessionOptions{Follow: false, TailLines: 2}))

	rig.marker("pod", 1)
	if rig.out.WaitForEvent(logs.StreamEventStreamEnded, replayWait) == nil {
		t.Fatal("a finished replay must end the session")
	}
	if got := contents(rig.out.Lines()); !slices.Equal(got, []string{"b2", "b3"}) {
		t.Fatalf("lines = %v, want the last two (TailLines honoured)", got)
	}
	if reqs := src.Requests(); len(reqs) != 1 || reqs[0].Follow {
		t.Fatalf("requests = %+v, want one bounded read and no follow", reqs)
	}
}

// The replay carries the caller's bounds; the follow carries none of them and
// resumes after the last replayed line.
func TestReplayAndFollowRequests(t *testing.T) {
	src := logtest.NewReplaySource("b1", "b2", "b3")
	rig := newReplayRig(t, oneSource(src))
	rig.open(asking(logs.LogSessionOptions{
		Follow: true, TailLines: 2, LimitBytes: 4096, IncludePrevious: true, Target: "app",
	}))
	rig.marker("pod", 1)
	lines := rig.out.WaitForLines(2, replayWait)

	var replay, follow *logs.LogStreamRequest
	waitFor(t, "the follow request", func() bool { return len(src.Requests()) >= 2 })
	for _, r := range src.Requests() {
		if r.Follow {
			follow = &r
		} else {
			replay = &r
		}
	}
	if replay == nil || follow == nil {
		t.Fatalf("want one replay and one follow request, got %+v", src.Requests())
	}
	if replay.Resume || replay.TailLines != 2 || replay.LimitBytes != 4096 || !replay.IncludePrevious || replay.Target != "app" {
		t.Errorf("replay request lost the caller's bounds: %+v", *replay)
	}
	if !follow.Resume || follow.TailLines != 0 || follow.SinceSeconds != 0 || follow.LimitBytes != 0 || follow.IncludePrevious {
		t.Errorf("follow request must resume with no bounds: %+v", *follow)
	}
	if follow.Target != "app" || follow.SourceID != "pod" {
		t.Errorf("follow request lost its identity: %+v", *follow)
	}
	last, _ := time.Parse(time.RFC3339Nano, strings.Fields(src.Lines()[2])[0])
	if follow.SinceTime == nil || !follow.SinceTime.Equal(last) {
		t.Errorf("follow SinceTime = %v, want the last replayed line's timestamp %v", follow.SinceTime, last)
	}
	if got := contents(lines); !slices.Equal(got, []string{"b2", "b3"}) {
		t.Errorf("lines = %v", got)
	}
}

// A replay whose lines carry no timestamp gives the follow no line to continue
// after: it continues after the instant the replay ended.
func TestFollowAfterAnUntimestampedReplayStartsAtItsEnd(t *testing.T) {
	requests := make(chan logs.LogStreamRequest, 4)
	handler := logs.Handler{
		Plugin: "test", Resource: "pod",
		SourceBuilder: logtest.SingleSourceBuilder("pod", nil),
		Replay: func(_ *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			requests <- req
			return io.NopCloser(strings.NewReader("plain one\nplain two\n")), nil
		},
		Handler: func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			requests <- req
			return &blockedReader{ctx: pctx.Context}, nil
		},
	}
	rig := newReplayRig(t, map[string]logs.Handler{"test/pod": handler})
	started := time.Now()
	rig.open(asking(logs.LogSessionOptions{Follow: true, SinceSeconds: 30, TailLines: 5}))
	<-requests
	follow := <-requests
	if !follow.Follow || !follow.Resume || follow.SinceTime == nil || follow.SinceTime.Before(started) ||
		follow.SinceSeconds != 0 || follow.TailLines != 0 {
		t.Fatalf("follow = %+v, want Resume after the instant the replay ended, with no caller bounds", follow)
	}
	if lines := rig.out.WaitForLines(2, replayWait); !lines[0].Replay || !lines[1].Replay {
		t.Fatalf("untimestamped backlog lines must still be flagged replay: %+v", lines)
	}
}

// When nothing was read, nothing is there to continue after: the follow keeps the
// caller's own time window and drops only the tail and limit.
func TestFollowAfterAnEmptyReplayKeepsTheCallersWindow(t *testing.T) {
	src := logtest.NewReplaySource()
	rig := newReplayRig(t, oneSource(src))
	rig.open(asking(logs.LogSessionOptions{Follow: true, SinceSeconds: 30, TailLines: 100, LimitBytes: 4096, IncludePrevious: true}))
	rig.marker("pod", 1)
	waitFor(t, "the follow request", func() bool { return len(src.Requests()) >= 2 })
	follow := src.Requests()[1]
	if !follow.Follow || !follow.Resume || follow.SinceTime != nil || follow.SinceSeconds != 30 ||
		follow.TailLines != 0 || follow.LimitBytes != 0 || follow.IncludePrevious {
		t.Fatalf("follow = %+v, want Resume with only the caller's SinceSeconds", follow)
	}
}

// Lines the source produces between the end of the replay and the opening of the
// follow are not lost, whether or not the replay delivered anything.
func TestNoLineIsLostBetweenReplayAndFollow(t *testing.T) {
	for _, backlog := range [][]string{nil, {"b1", "b2"}} {
		src := logtest.NewReplaySource(backlog...)
		inner := src.Handler("pod")
		replay := inner.Replay
		inner.Replay = func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			reader, err := replay(pctx, req)
			if err == nil && !req.Resume {
				src.Append("seam") // produced after the backlog was read, before the follow opens
			}
			return reader, err
		}
		rig := newReplayRig(t, map[string]logs.Handler{"test/pod": inner})
		rig.open(asking(logs.LogSessionOptions{Follow: true}))
		want := append(slices.Clone(backlog), "seam")
		lines := rig.out.WaitForLines(len(want), replayWait)
		if got := contents(lines); !slices.Equal(got, want) {
			t.Fatalf("backlog %v: lines = %v, want %v", backlog, got, want)
		}
		if lines[len(lines)-1].Replay {
			t.Errorf("backlog %v: the line produced after the replay is live", backlog)
		}
	}
}

type blockedReader struct{ ctx context.Context }

func (r *blockedReader) Read([]byte) (int, error) { <-r.ctx.Done(); return 0, io.EOF }
func (r *blockedReader) Close() error             { return nil }

// Two sources of one session never repeat a number and each gets its own marker.
func TestReplayOfConcurrentSourcesKeepsSeqUniqueAndMarkersApart(t *testing.T) {
	a := logtest.NewReplaySource("a1", "a2", "a3")
	b := logtest.NewReplaySource("b1", "b2")
	byID := map[string]*logtest.ReplaySource{"a": a, "b": b}
	dispatch := func(replay bool) logs.LogHandlerFunc {
		return func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			src := byID[req.SourceID]
			if replay {
				return src.ReplayFunc()(pctx, req)
			}
			return src.FollowFunc()(pctx, req)
		}
	}
	handler := logs.Handler{
		Plugin: "test", Resource: "pod",
		SourceBuilder: logtest.MultiSourceBuilder("a", "b"),
		Replay:        dispatch(true), Handler: dispatch(false),
	}
	rig := newReplayRig(t, map[string]logs.Handler{"test/pod": handler})
	rig.open(asking(logs.LogSessionOptions{Follow: true}))
	ma, mb := rig.marker("a", 1), rig.marker("b", 1)
	a.Append("a4")
	b.Append("b3")
	lines := rig.out.WaitForLines(7, replayWait)

	requireUnique(t, rig.out)
	for id, m := range map[string]logs.LogStreamEvent{"a": ma, "b": mb} {
		var own []logs.LogLine
		for _, l := range lines {
			if l.SourceID == id {
				own = append(own, l)
			}
		}
		requireIncreasing(t, "source "+id, lineSeqs(own))
		for _, l := range own {
			if l.Replay != (l.Seq <= m.AfterSeq) {
				t.Errorf("source %s line %q seq %d: Replay=%v but marker.AfterSeq=%d", id, l.Content, l.Seq, l.Replay, m.AfterSeq)
			}
		}
	}
}

// A reader that never asks gets today's stream: no numbers, no flags, no marker.
func TestWithoutTheRequestTheStreamIsUnchanged(t *testing.T) {
	src := logtest.NewReplaySource("b1", "b2")
	src.End()
	rig := newReplayRig(t, oneSource(src))
	session := rig.open(logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true})
	if session.ReplayMarkers {
		t.Fatal("a session that did not ask must not report markers")
	}
	if rig.out.WaitForEvent(logs.StreamEventStreamEnded, replayWait) == nil {
		t.Fatal("session did not end")
	}
	if got := contents(rig.out.Lines()); !slices.Equal(got, []string{"b1", "b2"}) {
		t.Fatalf("lines = %v", got)
	}
	for _, l := range rig.out.Lines() {
		if l.Seq != 0 || l.Replay {
			t.Errorf("line %q carries replay metadata: seq=%d replay=%v", l.Content, l.Seq, l.Replay)
		}
		if p := l.ToProto(); p.GetSeq() != 0 || p.GetReplay() {
			t.Errorf("line %q puts replay metadata on the wire", l.Content)
		}
	}
	for _, e := range rig.out.Events() {
		if e.Type == logs.StreamEventSourceCaughtUp || e.Seq != 0 || e.AfterSeq != 0 {
			t.Errorf("event %v carries replay metadata: %+v", e.Type, e)
		}
		if p := e.ToProto(); p.GetSeq() != 0 || p.GetAfterSeq() != 0 {
			t.Errorf("event %v puts replay metadata on the wire", e.Type)
		}
	}
}

// What a session that did not ask receives is, line for line and byte for byte on
// the wire, what a handler with no Replay produced before replay markers existed.
func TestWithoutTheRequestTheWireMatchesASinglePhaseHandler(t *testing.T) {
	history := []string{"b1", "b2", "b3"}
	src := logtest.NewReplaySource(history...)
	src.End()
	split := newReplayRig(t, oneSource(src))
	split.open(logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true})
	split.out.WaitForEvent(logs.StreamEventStreamEnded, replayWait)

	whole := logtest.NewReplaySource(history...)
	single := logs.Handler{
		Plugin: "test", Resource: "pod", SourceBuilder: logtest.SingleSourceBuilder("pod", map[string]string{"source": "pod"}),
		Handler: func(*types.PluginContext, logs.LogStreamRequest) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(strings.Join(whole.Lines(), "\n") + "\n")), nil
		},
	}
	legacy := newReplayRig(t, map[string]logs.Handler{"test/pod": single})
	legacy.open(logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true})
	legacy.out.WaitForEvent(logs.StreamEventStreamEnded, replayWait)

	wire := func(lines []logs.LogLine) [][]byte {
		var frames [][]byte
		for _, l := range lines {
			b, err := proto.MarshalOptions{Deterministic: true}.Marshal(l.ToProto())
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, b)
		}
		return frames
	}
	got, want := wire(split.out.Lines()), wire(legacy.out.Lines())
	if len(got) != len(want) {
		t.Fatalf("frames: %d, want %d", len(got), len(want))
	}
	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("line %d differs on the wire:\n got %x\nwant %x", i, got[i], want[i])
		}
	}
	kinds := func(events []logs.LogStreamEvent) []string { return eventKinds(events) }
	if g, w := kinds(split.out.Events()), kinds(legacy.out.Events()); !slices.Equal(g, w) {
		t.Errorf("events: %v, want %v", g, w)
	}
}

// A handler with no Replay cannot mark anything: the reply says so, and the
// stream is today's.
func TestAHandlerWithoutReplayNeverSendsMarkers(t *testing.T) {
	src := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("pod", src))
	session := h.CreateSession("pod", logtest.WithSessionOptions(asking(logs.LogSessionOptions{Follow: true})))
	if session.ReplayMarkers {
		t.Fatal("a handler with no Replay must not accept markers")
	}
	if _, forged := session.Options.Params[logs.ParamReplayMarkersAccepted]; forged {
		t.Fatal("the reply must not carry the acceptance key")
	}
	h.FeedLine("pod", "hello")
	lines := h.WaitForLines(1, replayWait)
	if lines[0].Seq != 0 || lines[0].Replay {
		t.Errorf("legacy line carries replay metadata: %+v", lines[0])
	}
	for _, e := range h.Output().Events() {
		if e.Type == logs.StreamEventSourceCaughtUp {
			t.Fatal("a handler with no Replay sent a marker")
		}
	}
}

// The acceptance is part of the session: every snapshot reports it, a reader
// cannot forge it, and updating options cannot turn it on or off.
func TestReplayAcceptanceIsFixedAtCreation(t *testing.T) {
	src := logtest.NewReplaySource("b1")
	rig := newReplayRig(t, oneSource(src))
	session := rig.open(asking(logs.LogSessionOptions{Follow: true}))
	if !session.ReplayMarkers || session.Options.Params[logs.ParamReplayMarkersAccepted] != logs.ReplayMarkersOn {
		t.Fatalf("create reply: ReplayMarkers=%v params=%v", session.ReplayMarkers, session.Options.Params)
	}
	got, err := rig.mgr.GetSession(rig.ctx, session.ID)
	if err != nil || !got.ReplayMarkers {
		t.Fatalf("GetSession: %v markers=%v", err, got != nil && got.ReplayMarkers)
	}
	listed, _ := rig.mgr.ListSessions(rig.ctx)
	if len(listed) != 1 || !listed[0].ReplayMarkers {
		t.Fatalf("ListSessions lost the acceptance: %+v", listed)
	}
	// Updating options without the request must not switch the session off ...
	updated, err := rig.mgr.UpdateSessionOptions(rig.ctx, session.ID, logs.LogSessionOptions{Follow: true})
	if err != nil || !updated.ReplayMarkers || updated.Options.Params[logs.ParamReplayMarkersAccepted] != logs.ReplayMarkersOn {
		t.Fatalf("UpdateSessionOptions: %v %+v", err, updated)
	}
	// ... and a reader that forges the key on a session that was not accepted gets nothing.
	plain := logtest.NewTestLogSource()
	h := logtest.Mount(t, logtest.WithSource("pod", plain))
	forged := h.CreateSession("pod", logtest.WithSessionOptions(logs.LogSessionOptions{
		Follow: true, Params: map[string]string{logs.ParamReplayMarkersAccepted: logs.ReplayMarkersOn},
	}))
	if forged.ReplayMarkers {
		t.Fatal("a reader forged the acceptance")
	}
	if _, kept := forged.Options.Params[logs.ParamReplayMarkersAccepted]; kept {
		t.Fatal("the forged acceptance key must not be echoed")
	}
}

// A read error after delivery reconnects with a resumed replay of the gap: the
// gap is flagged, delivered once, and a second marker says live resumed.
func TestReplayAfterAFollowFailureFillsTheGapAndMarksAgain(t *testing.T) {
	src := logtest.NewReplaySource("b1", "b2", "b3")
	rig := newReplayRig(t, oneSource(src))
	rig.open(asking(logs.LogSessionOptions{Follow: true}))
	first := rig.marker("pod", 1)
	src.Append("l1", "l2")
	rig.out.WaitForLines(5, replayWait)

	src.AppendAndBreak(errors.New("connection reset"), "gap1", "gap2")
	second := rig.marker("pod", 2)
	src.Append("l3")
	lines := rig.out.WaitForLines(8, replayWait)

	want := []string{"b1", "b2", "b3", "l1", "l2", "gap1", "gap2", "l3"}
	if got := contents(lines); !slices.Equal(got, want) {
		t.Fatalf("lines = %v, want %v (each exactly once)", got, want)
	}
	flags := []bool{true, true, true, false, false, true, true, false}
	for i, l := range lines {
		if l.Replay != flags[i] {
			t.Errorf("line %q: Replay = %v, want %v", l.Content, l.Replay, flags[i])
		}
	}
	if second.AfterSeq != lines[6].Seq || second.Seq <= first.Seq {
		t.Errorf("second marker %+v must follow the first and end at the gap's last line (seq %d)", second, lines[6].Seq)
	}
	requireIncreasing(t, "all lines", lineSeqs(lines))
	requireUnique(t, rig.out)

	reqs := src.Requests()
	var resumed []logs.LogStreamRequest
	for _, r := range reqs {
		if r.Resume && !r.Follow {
			resumed = append(resumed, r)
		}
	}
	if len(resumed) != 1 || resumed[0].SinceTime == nil || resumed[0].TailLines != 0 {
		t.Fatalf("want one resumed replay with a SinceTime and no tail, got %+v", reqs)
	}
	kinds := eventKinds(rig.out.Events())
	if !slices.Contains(kinds, "Reconnecting/pod") || !slices.Contains(kinds, "Reconnected/pod") {
		t.Errorf("reconnection must stay visible: %v", kinds)
	}
}

// A failure inside the replay sends no marker for the broken attempt; the retry
// picks up after the last line delivered and one marker ends the backlog.
func TestReplayFailureMidwayRetriesFromTheLastLine(t *testing.T) {
	src := logtest.NewReplaySource("b1", "b2", "b3", "b4", "b5")
	src.BreakReplayAfter(2, errors.New("stream reset"))
	rig := newReplayRig(t, oneSource(src))
	rig.open(asking(logs.LogSessionOptions{Follow: true, TailLines: 100}))

	marker := rig.marker("pod", 1)
	lines := rig.out.WaitForLines(5, replayWait)
	if got := contents(lines); !slices.Equal(got, []string{"b1", "b2", "b3", "b4", "b5"}) {
		t.Fatalf("lines = %v, want each backlog line exactly once", got)
	}
	for _, l := range lines {
		if !l.Replay {
			t.Errorf("line %q was part of the backlog", l.Content)
		}
	}
	if marker.AfterSeq != lines[4].Seq {
		t.Errorf("marker.AfterSeq = %d, want %d", marker.AfterSeq, lines[4].Seq)
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(markersOf(rig.out.Events(), "pod")); n != 1 {
		t.Errorf("markers = %d, want one (the broken attempt sends none)", n)
	}
}

// A backlog that never ends is reported, not waited for: a reader that was told
// markers are coming must not wait for one forever.
func TestReplayThatNeverEndsIsReported(t *testing.T) {
	src := logtest.NewReplaySource("b1")
	src.HangReplay(true)
	rig := newReplayRig(t, oneSource(src), func(c *logs.ManagerConfig) { c.ReplayIdleTimeout = 50 * time.Millisecond })
	session := rig.open(asking(logs.LogSessionOptions{Follow: true}))
	if !session.ReplayMarkers {
		t.Fatal("the plugin declared replay: the reply must say markers come")
	}
	failed := rig.out.WaitForEvent(logs.StreamEventStreamError, replayWait)
	if failed == nil || failed.SourceID != "pod" {
		t.Fatalf("the stuck source must be reported with its ID, got %+v", failed)
	}
	if failed.Message == "" {
		t.Error("the report must say what went wrong")
	}
	if len(markersOf(rig.out.Events(), "pod")) != 0 {
		t.Fatal("a backlog that never ended must not be marked caught up")
	}
	got, err := rig.mgr.GetSession(rig.ctx, session.ID)
	if err != nil || got.Status != logs.LogSessionStatusError {
		t.Fatalf("session = %+v err=%v, want ERROR", got, err)
	}
	if n := len(src.Requests()); n != 1 {
		t.Errorf("a deterministic failure must not be retried: %d replay requests", n)
	}
}

// A source that is closed before it caught up never sends a marker, and closing
// does not wait on it.
func TestSessionClosedBeforeCatchUpSendsNoMarker(t *testing.T) {
	src := logtest.NewReplaySource("b1")
	src.HangReplay(true)
	rig := newReplayRig(t, oneSource(src), func(c *logs.ManagerConfig) { c.ReplayIdleTimeout = time.Hour })
	session := rig.open(asking(logs.LogSessionOptions{Follow: true}))
	waitFor(t, "the replay to open", func() bool { return len(src.Requests()) > 0 })
	done := make(chan error, 1)
	go func() { done <- rig.mgr.CloseSession(rig.ctx, session.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(replayWait):
		t.Fatal("CloseSession blocked on a replay that never ends")
	}
	if len(markersOf(rig.out.Events(), "pod")) != 0 {
		t.Fatal("a source closed before catching up must not send a marker")
	}
}

// Paused delivery drops lines; the numbers of delivered ones stay unique.
func TestPausedReplayLinesDoNotReuseNumbers(t *testing.T) {
	src := logtest.NewReplaySource("b1")
	rig := newReplayRig(t, oneSource(src))
	session := rig.open(asking(logs.LogSessionOptions{Follow: true}))
	rig.marker("pod", 1)

	in := make(chan logs.StreamInput, 2)
	if _, err := rig.mgr.Stream(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	statusIs := func(want logs.LogSessionStatus) func() bool {
		return func() bool {
			got, err := rig.mgr.GetSession(rig.ctx, session.ID)
			return err == nil && got.Status == want
		}
	}
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandPause}
	waitFor(t, "the session to pause", statusIs(logs.LogSessionStatusPaused))
	src.Append("dropped")
	time.Sleep(50 * time.Millisecond) // the follow reads and drops it
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandResume}
	waitFor(t, "the session to resume", statusIs(logs.LogSessionStatusActive))
	src.Append("kept")
	lines := rig.out.WaitForLines(2, replayWait)
	if got := contents(lines); !slices.Equal(got, []string{"b1", "kept"}) {
		t.Fatalf("lines = %v", got)
	}
	requireUnique(t, rig.out)
	requireIncreasing(t, "delivered lines", lineSeqs(lines))
	// A line dropped while paused was never delivered, so it takes no number: a
	// reader that sees a gap in the sequence can trust it lost something.
	if marker := markersOf(rig.out.Events(), "pod")[0]; lines[1].Seq != marker.Seq+1 {
		t.Errorf("seq after a paused drop = %d, want %d: dropped lines must not consume numbers", lines[1].Seq, marker.Seq+1)
	}
}

// A producer that knows no replay marker must not make a reader see a source join.
func TestEventKindsThisReaderDoesNotKnowAreNotSourceAdded(t *testing.T) {
	unknown := logs.LogStreamEventTypeFromProto(logspbEventType(99))
	if unknown == logs.StreamEventSourceAdded {
		t.Fatal("an unknown event kind decoded as SourceAdded")
	}
	if unknown != logs.StreamEventUnknown {
		t.Fatalf("an unknown event kind decoded as %v, want StreamEventUnknown", unknown)
	}
	if logs.StreamEventSourceCaughtUp.ToProto() != logspbEventType(7) {
		t.Fatalf("StreamEventSourceCaughtUp must be wire value 7, got %v", logs.StreamEventSourceCaughtUp.ToProto())
	}
	if logs.LogStreamEventTypeFromProto(logspbEventType(7)) != logs.StreamEventSourceCaughtUp {
		t.Fatal("wire value 7 must decode as StreamEventSourceCaughtUp")
	}
}

// Disabling a source before its backlog ended cancels it: it never sends a marker
// even though the session lives on.
func TestSourceDisabledBeforeCatchUpSendsNoMarker(t *testing.T) {
	src := logtest.NewReplaySource("b1")
	src.HangReplay(true)
	rig := newReplayRig(t, oneSource(src), func(c *logs.ManagerConfig) { c.ReplayIdleTimeout = time.Hour })
	session := rig.open(asking(logs.LogSessionOptions{Follow: true}))
	waitFor(t, "the replay to open", func() bool { return len(src.Requests()) > 0 })

	if _, err := rig.mgr.UpdateSessionOptions(rig.ctx, session.ID, logs.LogSessionOptions{
		Follow: true, Params: map[string]string{"enabled_sources": "none"},
	}); err != nil {
		t.Fatal(err)
	}
	if rig.out.WaitForEvent(logs.StreamEventSourceRemoved, replayWait) == nil {
		t.Fatal("the source was not disabled")
	}
	time.Sleep(50 * time.Millisecond)
	if len(markersOf(rig.out.Events(), "pod")) != 0 {
		t.Fatal("a source canceled before catching up must not send a marker")
	}
}

// A session for a group resource streams its resolved sources through the
// registry's handler; markers follow that handler's declaration.
func TestReplayMarkersForResolvedSources(t *testing.T) {
	src := logtest.NewReplaySource("b1", "b2")
	rig := newReplayRig(t, oneSource(src), func(c *logs.ManagerConfig) {
		c.Resolvers = map[string]logs.SourceResolver{
			"deployment": func(*types.PluginContext, *resourcev2.Value, logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
				return &logs.SourceResolverResult{Sources: []logs.LogSource{{ID: "pod", Labels: map[string]string{"source": "pod"}}}}, nil
			},
		}
	})
	session := rig.openKey("deployment", asking(logs.LogSessionOptions{Follow: true}))
	if !session.ReplayMarkers {
		t.Fatal("a resolved session must accept markers when the streaming handler declares Replay")
	}
	marker := rig.marker("pod", 1)
	lines := rig.out.WaitForLines(2, replayWait)
	if marker.AfterSeq != lines[1].Seq || !lines[0].Replay || !lines[1].Replay {
		t.Fatalf("marker %+v does not close the resolved source's backlog %+v", marker, lines)
	}
}

// failsAtOnce is a reader whose every Read fails.
type failsAtOnce struct{ err error }

func (r failsAtOnce) Read([]byte) (int, error) { return 0, r.err }
func (r failsAtOnce) Close() error             { return nil }

// With no timestamp to resume from, a reopened source starts over with the
// caller's bounds: the tail is delivered again, flagged replay, and marked again.
func TestReconnectWithoutTimestampsRedeliversTheTailAndMarksAgain(t *testing.T) {
	requests := make(chan logs.LogStreamRequest, 16)
	follows := 0
	handler := logs.Handler{
		Plugin: "test", Resource: "pod",
		SourceBuilder: logtest.SingleSourceBuilder("pod", nil),
		Replay: func(_ *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			requests <- req
			return io.NopCloser(strings.NewReader("a\nb\n")), nil
		},
		Handler: func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			requests <- req
			follows++
			if follows == 1 {
				return failsAtOnce{err: errors.New("connection reset")}, nil
			}
			return &blockedReader{ctx: pctx.Context}, nil
		},
	}
	rig := newReplayRig(t, map[string]logs.Handler{"test/pod": handler})
	rig.open(asking(logs.LogSessionOptions{Follow: true, TailLines: 50}))

	second := rig.marker("pod", 2)
	lines := rig.out.WaitForLines(4, replayWait)
	if got := contents(lines); !slices.Equal(got, []string{"a", "b", "a", "b"}) {
		t.Fatalf("lines = %v, want the tail twice", got)
	}
	for _, l := range lines {
		if !l.Replay {
			t.Errorf("redelivered line %q must be flagged replay", l.Content)
		}
	}
	if second.AfterSeq != lines[3].Seq {
		t.Errorf("second marker AfterSeq = %d, want %d", second.AfterSeq, lines[3].Seq)
	}
	var replays []logs.LogStreamRequest
	for len(requests) > 0 {
		if r := <-requests; !r.Follow {
			replays = append(replays, r)
		}
	}
	if len(replays) != 2 || replays[1].Resume || replays[1].TailLines != 50 {
		t.Fatalf("with nothing to resume from the second replay must repeat the caller's bounds: %+v", replays)
	}
}
