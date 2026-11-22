package logs_test

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"

	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs/logtest"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// The counted lines are flagged, the marker follows the last of them, and what the
// source writes afterwards is live.
func TestReplayLinesAreFlaggedAndCaughtUpFollows(t *testing.T) {
	src := newTailLog("a", "b", "c", "d", "e")
	r := newRig(t, src.handlers(true))
	session := r.open(asking(logs.LogSessionOptions{Follow: true, TailLines: 100}))
	if !session.ReplayMarkers {
		t.Fatal("the create reply must say the plugin accepted replay markers")
	}
	r.marker("pod", 1)
	src.Append("f", "g")
	items := r.tl.until(t, "the live lines", func(it []timelineItem) bool { return countLines(it) >= 7 })
	if got, want := signature(items, "pod"), "R:a R:b R:c R:d R:e MARK L:f L:g"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
	if n := countMarkers(items, "pod"); n != 1 {
		t.Fatalf("markers = %d, want exactly one", n)
	}
}

// A tail-limited request counts the tail it asked for, not the whole log.
func TestReplayOfATailLimitedRequestIsTheTail(t *testing.T) {
	src := newTailLog("l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10")
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{Follow: true, TailLines: 3}))
	r.marker("pod", 1)
	items := r.linesUntilItems(3)
	if got, want := signature(items, "pod"), "R:l8 R:l9 R:l10 MARK"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
}

// A source with nothing yet is caught up at once, and what it writes is live.
func TestAnEmptyReplayIsCaughtUpAtOnce(t *testing.T) {
	src := newTailLog()
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{Follow: true}))
	r.marker("pod", 1)
	src.Append("first")
	items := r.tl.until(t, "the first line", func(it []timelineItem) bool { return countLines(it) >= 1 })
	if got, want := signature(items, "pod"), "MARK L:first"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
}

// Lines that reach the source between the count and the open, when nothing limits
// the tail, come after the counted ones and are live.
func TestLinesArrivingBetweenTheCountAndTheOpenAreLive(t *testing.T) {
	src := newTailLog("a", "b", "c", "d", "e")
	src.afterCount = func() { src.Append("f", "g") }
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{Follow: true}))
	r.marker("pod", 1)
	items := r.linesUntilItems(7)
	if got, want := signature(items, "pod"), "R:a R:b R:c R:d R:e MARK L:f L:g"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
}

// A tail-limited stream's window moves forward with the newest lines: the number
// flagged is the number counted, whichever lines they are.
func TestATailWindowThatMovesKeepsTheCountedNumber(t *testing.T) {
	src := newTailLog("l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10")
	src.afterCount = func() { src.Append("l11", "l12") }
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{Follow: true, TailLines: 3}))
	r.marker("pod", 1)
	items := r.linesUntilItems(3)
	if got, want := signature(items, "pod"), "R:l10 R:l11 R:l12 MARK"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
}

// A source that lost lines between the count and the open (a log rotation) opens
// with fewer than counted: nothing is lost, the next lines are flagged as replay
// until the count is reached, and the caught-up event follows the counted line.
func TestFewerLinesThanCountedFlagsLaterLinesUntilTheCountIsReached(t *testing.T) {
	src := newTailLog("a", "b", "c", "d", "e")
	src.afterCount = src.Rotate
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{Follow: true, TailLines: 100}))
	waitOpen(t, src, 1)
	src.Append("f", "g", "h")
	items := r.linesUntilItems(3)
	if n := countMarkers(items, "pod"); n != 0 {
		t.Fatalf("a marker came after 3 of the 5 counted lines: %s", describe(items))
	}
	src.Append("i", "j", "k")
	items = r.linesUntilItems(6)
	if got, want := signature(items, "pod"), "R:f R:g R:h R:i R:j MARK L:k"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
}

func waitOpen(t *testing.T, src *tailLog, n int) {
	t.Helper()
	deadline := time.Now().Add(replayWait)
	for len(src.Opens()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("the source was opened %d times, want %d", len(src.Opens()), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *rig) linesUntilItems(n int) []timelineItem {
	r.t.Helper()
	return r.tl.until(r.t, fmt.Sprintf("%d lines", n), func(items []timelineItem) bool { return countLines(items) >= n })
}

// What a session delivers is what the handler's one stream delivers: a source that
// rotates its log after the open loses nothing a plain handler's stream keeps. The
// same script runs against a handler with ReplayCount and an asking reader, and
// against the handler as it was before replay markers existed.
func TestSessionsDeliverWhatTheHandlersStreamDelivers(t *testing.T) {
	script := func(t *testing.T, withReplayCount, ask bool) []string {
		var first []string
		for i := 1; i <= 100; i++ {
			first = append(first, fmt.Sprintf("n%03d", i))
		}
		src := newTailLog(first...)
		r := newRig(t, src.handlers(withReplayCount))
		o := logs.LogSessionOptions{Follow: true, TailLines: 200}
		if ask {
			o = asking(o)
		}
		r.open(o)
		r.linesUntil(100)
		var more []string
		for i := 101; i <= 150; i++ {
			more = append(more, fmt.Sprintf("n%03d", i))
		}
		src.Append(more...) // written to the file the stream is on, then the file rotates
		src.Rotate()
		src.Append("after-rotation-1", "after-rotation-2")
		r.linesUntil(150)
		time.Sleep(30 * time.Millisecond)
		return contents(r.tl.lines())
	}
	want := script(t, false, false)
	if len(want) != 150 || want[149] != "n150" {
		t.Fatalf("the handler's own stream delivered %d lines ending %q", len(want), want[len(want)-1])
	}
	if got := script(t, true, true); !slices.Equal(got, want) {
		t.Fatalf("with markers the session delivered %d lines, the handler's stream %d:\n got %v\nwant %v", len(got), len(want), got, want)
	}
	if got := script(t, true, false); !slices.Equal(got, want) {
		t.Fatalf("without asking the session delivered %d lines, the handler's stream %d", len(got), len(want))
	}
}

// A reader that did not ask gets the handler called exactly as before: one request,
// the original one, ReplayCount never called, and frames identical on the wire to a
// handler that has no ReplayCount.
func TestWithoutTheRequestTheHandlerIsCalledAsBefore(t *testing.T) {
	run := func(withReplayCount bool) (*tailLog, *timeline) {
		src := newTailLog("a", "b", "c")
		src.End()
		r := newRig(t, src.handlers(withReplayCount))
		session := r.open(logs.LogSessionOptions{Follow: true, TailLines: 100, IncludeSourceEvents: true})
		if session.ReplayMarkers {
			t.Fatal("a session that did not ask must not report markers")
		}
		r.tl.until(t, "the session to end", func(items []timelineItem) bool {
			return slices.ContainsFunc(items, func(it timelineItem) bool {
				return it.event != nil && it.event.Type == logs.StreamEventStreamEnded
			})
		})
		return src, r.tl
	}
	srcWith, with := run(true)
	_, without := run(false)

	if n := len(srcWith.Counts()); n != 0 {
		t.Fatalf("ReplayCount was called %d times for a reader that did not ask", n)
	}
	opens := srcWith.Opens()
	if len(opens) != 1 {
		t.Fatalf("Handler was called %d times, want once: %+v", len(opens), opens)
	}
	if o := opens[0]; !o.Follow || o.TailLines != 100 || o.SourceID != "pod" {
		t.Fatalf("Handler did not get the original request: %+v", o)
	}
	wire := func(tl *timeline) [][]byte {
		var frames [][]byte
		for _, l := range tl.lines() {
			if l.Replay {
				t.Errorf("line %q is flagged on a session that did not ask", l.Content)
			}
			b, err := proto.MarshalOptions{Deterministic: true}.Marshal(l.ToProto())
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, b)
		}
		return frames
	}
	got, want := wire(with), wire(without)
	if len(got) != len(want) {
		t.Fatalf("%d frames, want %d", len(got), len(want))
	}
	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("line %d differs on the wire:\n got %x\nwant %x", i, got[i], want[i])
		}
	}
	kinds := func(tl *timeline) (out []string, frames [][]byte) {
		for _, e := range tl.events() {
			out = append(out, e.Type.String()+"/"+e.SourceID+"/"+e.Message)
			e.Timestamp = time.Time{} // the clock is not what is compared
			b, err := proto.MarshalOptions{Deterministic: true}.Marshal(e.ToProto())
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, b)
		}
		return out, frames
	}
	gotKinds, gotFrames := kinds(with)
	wantKinds, wantFrames := kinds(without)
	if !slices.Equal(gotKinds, wantKinds) {
		t.Errorf("events: %v, want %v", gotKinds, wantKinds)
	}
	if len(gotFrames) != len(wantFrames) {
		t.Fatalf("%d event frames, want %d", len(gotFrames), len(wantFrames))
	}
	for i := range gotFrames {
		if !slices.Equal(gotFrames[i], wantFrames[i]) {
			t.Errorf("event %d differs on the wire:\n got %x\nwant %x", i, gotFrames[i], wantFrames[i])
		}
	}
}

// IncludePrevious, Target and every other bound reach ReplayCount and Handler
// unchanged: the stream is still the one stream the request describes.
func TestReplayCountAndHandlerGetTheSameRequest(t *testing.T) {
	src := newTailLog("a", "b")
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{
		Follow: true, IncludePrevious: true, IncludeTimestamps: true, Target: "app",
		TailLines: 50, SinceSeconds: 30, LimitBytes: 4096,
	}))
	r.marker("pod", 1)
	counts, opens := src.Counts(), src.Opens()
	if len(counts) != 1 || len(opens) != 1 {
		t.Fatalf("ReplayCount %d times, Handler %d times, want once each", len(counts), len(opens))
	}
	if !reflect.DeepEqual(counts[0], opens[0]) {
		t.Fatalf("the requests differ:\nReplayCount: %+v\nHandler: %+v", counts[0], opens[0])
	}
	if o := opens[0]; !o.IncludePrevious || o.TailLines != 50 || o.SinceSeconds != 30 || o.LimitBytes != 4096 || o.Target != "app" {
		t.Fatalf("a bound was lost: %+v", o)
	}
}

// A handler without ReplayCount cannot be caught up: the reply says so and the stream
// is the one it always was.
func TestAHandlerWithoutReplayCountNeverGetsReplayMarkers(t *testing.T) {
	src := newTailLog("a", "b")
	r := newRig(t, src.handlers(false))
	session := r.open(asking(logs.LogSessionOptions{Follow: true}))
	if session.ReplayMarkers {
		t.Fatal("a handler with no ReplayCount must not accept replay markers")
	}
	if _, forged := session.Options.Params[logs.ParamReplayMarkersAccepted]; forged {
		t.Fatal("the reply must not carry the acceptance key")
	}
	items := r.linesUntilItems(2)
	if got, want := signature(items, "pod"), "L:a L:b"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
	time.Sleep(20 * time.Millisecond)
	if n := countMarkers(r.tl.snapshot(), "pod"); n != 0 {
		t.Fatal("a handler with no ReplayCount sent a caught-up event")
	}
}

// The acceptance is part of the session: every snapshot reports it, a reader cannot
// forge it, and updating options cannot turn it on or off.
func TestAcceptanceIsFixedAtCreation(t *testing.T) {
	src := newTailLog("a")
	r := newRig(t, src.handlers(true))
	session := r.open(asking(logs.LogSessionOptions{Follow: true}))
	if !session.ReplayMarkers || session.Options.Params[logs.ParamReplayMarkersAccepted] != logs.ReplayMarkersOn {
		t.Fatalf("create reply: ReplayMarkers=%v params=%v", session.ReplayMarkers, session.Options.Params)
	}
	got, err := r.mgr.GetSession(r.ctx, session.ID)
	if err != nil || !got.ReplayMarkers {
		t.Fatalf("GetSession: %v markers=%v", err, got != nil && got.ReplayMarkers)
	}
	listed, _ := r.mgr.ListSessions(r.ctx)
	if len(listed) != 1 || !listed[0].ReplayMarkers {
		t.Fatalf("ListSessions lost the acceptance: %+v", listed)
	}
	updated, err := r.mgr.UpdateSessionOptions(r.ctx, session.ID, logs.LogSessionOptions{Follow: true})
	if err != nil || !updated.ReplayMarkers || updated.Options.Params[logs.ParamReplayMarkersAccepted] != logs.ReplayMarkersOn {
		t.Fatalf("UpdateSessionOptions: %v %+v", err, updated)
	}

	plain := newRig(t, newTailLog("a").handlers(false))
	forged := plain.open(logs.LogSessionOptions{
		Follow: true, Params: map[string]string{logs.ParamReplayMarkersAccepted: logs.ReplayMarkersOn},
	})
	if forged.ReplayMarkers {
		t.Fatal("a reader forged the acceptance")
	}
	if _, kept := forged.Options.Params[logs.ParamReplayMarkersAccepted]; kept {
		t.Fatal("the forged acceptance key must not be echoed")
	}
}

// A request with Follow false describes a stream that ends, and its end is the
// boundary: every line is replay, the caught-up event follows the last, ReplayCount is not asked.
func TestAFiniteSessionFlagsEveryLineAndMarksItsEnd(t *testing.T) {
	src := newTailLog("a", "b", "c")
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{Follow: false, TailLines: 2}))
	items := r.tl.until(t, "the session to end", func(items []timelineItem) bool {
		return slices.ContainsFunc(items, func(it timelineItem) bool {
			return it.event != nil && it.event.Type == logs.StreamEventStreamEnded
		})
	})
	if got, want := signature(items, "pod"), "R:b R:c MARK"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
	if n := len(src.Counts()); n != 0 {
		t.Fatalf("ReplayCount was called %d times for a request with Follow false", n)
	}
	last := items[len(items)-1]
	if last.event == nil || last.event.Type != logs.StreamEventStreamEnded {
		t.Fatalf("the session must end after the marker: %s", describe(items))
	}
}

// After a read error the source reopens as it always did: the same request, so its
// tail comes again. The SDK counts that tail again too: flagged and marked again.
func TestAReopenedSourceIsCountedAndMarkedAgain(t *testing.T) {
	src := newTailLog("a", "b", "c")
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{Follow: true, TailLines: 100}))
	r.marker("pod", 1)
	src.Break(errBoom)
	r.marker("pod", 2)
	src.Append("d")
	items := r.linesUntilItems(7)
	if got, want := signature(items, "pod"), "R:a R:b R:c MARK R:a R:b R:c MARK L:d"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
	if n := len(src.Counts()); n != 2 {
		t.Fatalf("ReplayCount was called %d times for two opens", n)
	}
	kinds := ""
	for _, e := range r.tl.events() {
		kinds += e.Type.String() + " "
	}
	if !strings.Contains(kinds, "Reconnecting") || !strings.Contains(kinds, "Reconnected") {
		t.Errorf("the reconnection must stay visible: %s", kinds)
	}
}

// A count that cannot be given fails the attempt as a failed open does: no stream is
// opened for it, the source is retried, and the retry marks.
func TestAReplayCountErrorFailsTheAttemptLikeAFailedOpen(t *testing.T) {
	src := newTailLog("a", "b")
	src.countErr = []error{errBoom}
	r := newRig(t, src.handlers(true))
	r.open(asking(logs.LogSessionOptions{Follow: true}))
	r.marker("pod", 1)
	if n := len(src.Opens()); n != 1 {
		t.Fatalf("Handler was called %d times: the failed count must not open a stream", n)
	}
	if n := len(src.Counts()); n != 2 {
		t.Fatalf("ReplayCount was called %d times, want the failed one and the retry", n)
	}
	items := r.linesUntilItems(2)
	if got, want := signature(items, "pod"), "R:a R:b MARK"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
	kinds := ""
	for _, e := range r.tl.events() {
		kinds += e.Type.String() + " "
	}
	if !strings.Contains(kinds, "Reconnecting") {
		t.Errorf("the retry must be visible: %s", kinds)
	}
}

// A count that never works is reported like a source that never opens.
func TestAReplayCountThatAlwaysFailsEndsInAStreamError(t *testing.T) {
	src := newTailLog("a")
	for range logs.ReconnectMaxAttempts + 1 {
		src.countErr = append(src.countErr, errBoom)
	}
	r := newRig(t, src.handlers(true))
	session := r.open(asking(logs.LogSessionOptions{Follow: true}))
	r.tl.until(t, "the stream error", func(items []timelineItem) bool {
		return slices.ContainsFunc(items, func(it timelineItem) bool {
			return it.event != nil && it.event.Type == logs.StreamEventStreamError && it.event.SourceID == "pod"
		})
	})
	if n := len(src.Opens()); n != 0 {
		t.Fatalf("Handler was called %d times though no count was ever given", n)
	}
	got, err := r.mgr.GetSession(r.ctx, session.ID)
	if err != nil || got.Status != logs.LogSessionStatusError {
		t.Fatalf("session = %+v err=%v, want ERROR", got, err)
	}
}

// A negative count is not a count.
func TestANegativeReplayCountIsAnError(t *testing.T) {
	src := newTailLog("a", "b")
	calls := 0
	h := src.handlers(true)["test/pod"]
	h.ReplayCount = func(pctx *types.PluginContext, req logs.LogStreamRequest) (int64, error) {
		calls++
		if calls == 1 {
			return -1, nil
		}
		return src.count(pctx, req)
	}
	r := newRig(t, map[string]logs.Handler{"test/pod": h})
	r.open(asking(logs.LogSessionOptions{Follow: true}))
	r.marker("pod", 1)
	if n := len(src.Opens()); n != 1 || calls != 2 {
		t.Fatalf("opens=%d replay count calls=%d: a negative count must fail its attempt", n, calls)
	}
}

// A paused session drops its lines, but the replay's position still counts: the
// caught-up event is sent, and what comes after it is live.
func TestAPausedSessionIsStillCaughtUp(t *testing.T) {
	src := newTailLog("a", "b", "c")
	release := src.Hold()
	r := newRig(t, src.handlers(true))
	session := r.open(asking(logs.LogSessionOptions{Follow: true}))
	waitOpen(t, src, 1)
	in := make(chan logs.StreamInput, 2)
	if _, err := r.mgr.Stream(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandPause}
	waitStatus(t, r, session.ID, logs.LogSessionStatusPaused)
	release()
	r.marker("pod", 1)
	in <- logs.StreamInput{SessionID: session.ID, Command: logs.StreamCommandResume}
	waitStatus(t, r, session.ID, logs.LogSessionStatusActive)
	src.Append("x")
	items := r.linesUntilItems(1)
	if got, want := signature(items, "pod"), "MARK L:x"; got != want {
		t.Fatalf("timeline = %s, want %s (the paused replay lines are dropped, as paused lines always were)", got, want)
	}
}

func waitStatus(t *testing.T, r *rig, id string, want logs.LogSessionStatus) {
	t.Helper()
	deadline := time.Now().Add(replayWait)
	for {
		got, err := r.mgr.GetSession(r.ctx, id)
		if err == nil && got.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session did not reach status %v", want)
		}
		time.Sleep(time.Millisecond)
	}
}

// A source that is closed or disabled before its replay was delivered is never caught
// up: no caught-up event is sent.
func TestASourceEndedBeforeItsReplayWasDeliveredIsNeverCaughtUp(t *testing.T) {
	t.Run("session closed", func(t *testing.T) {
		src := newTailLog("a", "b", "c")
		release := src.Hold()
		defer release()
		r := newRig(t, src.handlers(true))
		session := r.open(asking(logs.LogSessionOptions{Follow: true}))
		waitOpen(t, src, 1)
		if err := r.mgr.CloseSession(r.ctx, session.ID); err != nil {
			t.Fatal(err)
		}
		release()
		time.Sleep(30 * time.Millisecond)
		if n := countMarkers(r.tl.snapshot(), "pod"); n != 0 {
			t.Fatal("a closed session sent a marker")
		}
	})
	t.Run("finite session closed before its end", func(t *testing.T) {
		src := newTailLog("a", "b", "c")
		release := src.Hold()
		defer release()
		r := newRig(t, src.handlers(true))
		session := r.open(asking(logs.LogSessionOptions{Follow: false}))
		waitOpen(t, src, 1)
		if err := r.mgr.CloseSession(r.ctx, session.ID); err != nil {
			t.Fatal(err)
		}
		release()
		time.Sleep(30 * time.Millisecond)
		if n := countMarkers(r.tl.snapshot(), "pod"); n != 0 {
			t.Fatal("a finite session closed before its end sent a marker")
		}
	})
	t.Run("source disabled", func(t *testing.T) {
		src := newTailLog("a", "b", "c")
		release := src.Hold()
		defer release()
		r := newRig(t, src.handlers(true))
		session := r.open(asking(logs.LogSessionOptions{Follow: true}))
		waitOpen(t, src, 1)
		if _, err := r.mgr.UpdateSessionOptions(r.ctx, session.ID, logs.LogSessionOptions{
			Follow: true, Params: map[string]string{"enabled_sources": "none"},
		}); err != nil {
			t.Fatal(err)
		}
		r.tl.until(t, "the source to be removed", func(items []timelineItem) bool {
			return slices.ContainsFunc(items, func(it timelineItem) bool {
				return it.event != nil && it.event.Type == logs.StreamEventSourceRemoved
			})
		})
		release()
		time.Sleep(30 * time.Millisecond)
		if n := countMarkers(r.tl.snapshot(), "pod"); n != 0 {
			t.Fatal("a source disabled before its replay was delivered sent a caught-up event")
		}
	})
}

// Sources of one session are counted and marked each on its own, in order within
// each source, whichever way they interleave.
func TestSourcesOfOneSessionEachGetTheirOwnMarker(t *testing.T) {
	logsBySource := map[string]*tailLog{
		"a": newTailLog("a1", "a2", "a3"),
		"b": newTailLog("b1"),
		"c": newTailLog(),
	}
	handler := logs.Handler{
		Plugin: "test", Resource: "pod",
		Handler: func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			return logsBySource[req.SourceID].open(pctx, req)
		},
		ReplayCount: func(pctx *types.PluginContext, req logs.LogStreamRequest) (int64, error) {
			return logsBySource[req.SourceID].count(pctx, req)
		},
	}
	r := newRig(t, map[string]logs.Handler{"test/pod": handler}, func(c *logs.ManagerConfig) {
		c.Resolvers = map[string]logs.SourceResolver{
			"deployment": func(*types.PluginContext, *resourcev2.Value, logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
				return &logs.SourceResolverResult{Sources: []logs.LogSource{{ID: "a"}, {ID: "b"}, {ID: "c"}}}, nil
			},
		}
	})
	session := r.openKey("deployment", asking(logs.LogSessionOptions{Follow: true}))
	if !session.ReplayMarkers {
		t.Fatal("a session for a group resource must accept replay markers when the streaming handler declares ReplayCount")
	}
	for _, id := range []string{"a", "b", "c"} {
		r.marker(id, 1)
	}
	logsBySource["a"].Append("a4")
	logsBySource["b"].Append("b2")
	logsBySource["c"].Append("c1")
	items := r.tl.until(t, "the live lines", func(it []timelineItem) bool { return countLines(it) >= 7 })
	for id, want := range map[string]string{
		"a": "R:a1 R:a2 R:a3 MARK L:a4",
		"b": "R:b1 MARK L:b2",
		"c": "MARK L:c1",
	} {
		if got := signature(items, id); got != want {
			t.Errorf("source %s: %s, want %s", id, got, want)
		}
	}
}

// Sources past MaxConcurrentStreamsPerSession wait for a slot in a following
// session, as they always did: they send nothing, a marker included, until they
// start, and then they are counted and marked like any other.
func TestSourcesWaitingForASlotSendNothingUntilTheyStart(t *testing.T) {
	const sources = logs.MaxConcurrentStreamsPerSession + 3
	all := map[string]*tailLog{}
	var ids []logs.LogSource
	for i := range sources {
		id := fmt.Sprintf("s%02d", i)
		all[id] = newTailLog(id + "-1")
		ids = append(ids, logs.LogSource{ID: id})
	}
	handler := logs.Handler{
		Plugin: "test", Resource: "pod",
		Handler: func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			return all[req.SourceID].open(pctx, req)
		},
		ReplayCount: func(pctx *types.PluginContext, req logs.LogStreamRequest) (int64, error) {
			return all[req.SourceID].count(pctx, req)
		},
	}
	r := newRig(t, map[string]logs.Handler{"test/pod": handler}, func(c *logs.ManagerConfig) {
		c.Resolvers = map[string]logs.SourceResolver{
			"deployment": func(*types.PluginContext, *resourcev2.Value, logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
				return &logs.SourceResolverResult{Sources: ids}, nil
			},
		}
	})
	r.openKey("deployment", asking(logs.LogSessionOptions{Follow: true, IncludeSourceEvents: true}))
	markedSources := func(items []timelineItem) map[string]bool {
		out := map[string]bool{}
		for _, it := range items {
			if it.event != nil && it.event.Type == logs.StreamEventSourceCaughtUp {
				out[it.event.SourceID] = true
			}
		}
		return out
	}
	r.tl.until(t, "the first slots to be marked", func(items []timelineItem) bool {
		return len(markedSources(items)) >= logs.MaxConcurrentStreamsPerSession
	})
	time.Sleep(30 * time.Millisecond)
	items := r.tl.snapshot()
	if n := len(markedSources(items)); n != logs.MaxConcurrentStreamsPerSession {
		t.Fatalf("%d sources marked, want exactly the %d that hold a slot", n, logs.MaxConcurrentStreamsPerSession)
	}
	heard := map[string]bool{}
	for _, it := range items {
		switch {
		case it.line != nil:
			heard[it.line.SourceID] = true
		case it.event != nil && it.event.SourceID != "":
			heard[it.event.SourceID] = true
		}
	}
	if len(heard) != logs.MaxConcurrentStreamsPerSession {
		t.Fatalf("%d sources were heard from, want the %d that hold a slot", len(heard), logs.MaxConcurrentStreamsPerSession)
	}
	// One started source ends; a waiting one takes its slot and is marked.
	for id, l := range all {
		if heard[id] {
			l.End()
			break
		}
	}
	r.tl.until(t, "a waiting source to start", func(items []timelineItem) bool {
		return len(markedSources(items)) > logs.MaxConcurrentStreamsPerSession
	})
}

// A reader whose SDK has no such event kind must not see a source join.
func TestEventKindsThisReaderDoesNotKnowAreNotSourceAdded(t *testing.T) {
	unknown := logs.LogStreamEventTypeFromProto(logspb.LogStreamEventType(99))
	if unknown != logs.StreamEventUnknown {
		t.Fatalf("an unknown event kind decoded as %v, want StreamEventUnknown", unknown)
	}
	if logs.StreamEventSourceCaughtUp.ToProto() != logspb.LogStreamEventType(7) {
		t.Fatalf("StreamEventSourceCaughtUp must be wire value 7, got %v", logs.StreamEventSourceCaughtUp.ToProto())
	}
	if logs.LogStreamEventTypeFromProto(logspb.LogStreamEventType(7)) != logs.StreamEventSourceCaughtUp {
		t.Fatal("wire value 7 must decode as StreamEventSourceCaughtUp")
	}
	if got := logs.StreamEventSourceCaughtUp.String(); got != "SourceCaughtUp" {
		t.Fatalf("String() = %q", got)
	}
}

// The flag travels on the wire and in JSON only when it is true.
func TestTheReplayFlagTravelsOnlyWhenSet(t *testing.T) {
	line := logs.LogLine{SessionID: "s", SourceID: "pod", Content: "x", Replay: true}
	back := logs.LogLineFromProto(line.ToProto())
	if !back.Replay || line.ToProto().GetReplay() != true {
		t.Fatalf("the flag did not round-trip: %+v", back)
	}
	plain := logs.LogLine{SessionID: "s", SourceID: "pod", Content: "x"}
	if proto.Size(plain.ToProto()) >= proto.Size(line.ToProto()) {
		t.Fatal("a line that is not replay must not pay for the flag")
	}
	b, err := json.Marshal(plain)
	if err != nil || strings.Contains(string(b), "replay") {
		t.Fatalf("JSON of an unflagged line carries the flag: %s %v", b, err)
	}
	b, _ = json.Marshal(line)
	if !strings.Contains(string(b), `"replay":true`) {
		t.Fatalf("JSON of a flagged line lacks the flag: %s", b)
	}
}

// Options carried over from an accepted session must not make a plugin that
// predates markers look as though it accepted: the request never carries the key.
func TestRequestsNeverCarryTheAcceptanceKey(t *testing.T) {
	params := map[string]string{logs.ParamReplayMarkersAccepted: logs.ReplayMarkersOn, "keep": "me"}
	request, err := logs.CreateSessionOptions{
		ResourceKey: "pod", ResourceID: "web-1", Options: logs.LogSessionOptions{Params: params},
	}.ToProto()
	if err != nil {
		t.Fatal(err)
	}
	if _, sent := request.GetOptions().GetParams()[logs.ParamReplayMarkersAccepted]; sent {
		t.Error("the create request carries the acceptance key")
	}
	if request.GetOptions().GetParams()["keep"] != "me" {
		t.Error("the other parameters must travel")
	}
	if params[logs.ParamReplayMarkersAccepted] != logs.ReplayMarkersOn {
		t.Error("the caller's map was changed")
	}
}

// A source that is disabled and enabled again is a new stream: its tail comes
// again, counted again, flagged and marked again. The source that stayed enabled is
// not touched.
func TestAReenabledSourceIsCountedAndMarkedAgain(t *testing.T) {
	logsBySource := map[string]*tailLog{"a": newTailLog("a1", "a2"), "b": newTailLog("b1")}
	handler := logs.Handler{
		Plugin: "test", Resource: "pod",
		Handler: func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			return logsBySource[req.SourceID].open(pctx, req)
		},
		ReplayCount: func(pctx *types.PluginContext, req logs.LogStreamRequest) (int64, error) {
			return logsBySource[req.SourceID].count(pctx, req)
		},
	}
	r := newRig(t, map[string]logs.Handler{"test/pod": handler}, func(c *logs.ManagerConfig) {
		c.Resolvers = map[string]logs.SourceResolver{
			"deployment": func(*types.PluginContext, *resourcev2.Value, logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
				return &logs.SourceResolverResult{Sources: []logs.LogSource{{ID: "a"}, {ID: "b"}}}, nil
			},
		}
	})
	session := r.openKey("deployment", asking(logs.LogSessionOptions{Follow: true}))
	r.marker("a", 1)
	r.marker("b", 1)
	update := func(enabled string) {
		t.Helper()
		if _, err := r.mgr.UpdateSessionOptions(r.ctx, session.ID, logs.LogSessionOptions{
			Follow: true, Params: map[string]string{"enabled_sources": enabled},
		}); err != nil {
			t.Fatal(err)
		}
	}
	update("a")
	r.tl.until(t, "source b to be removed", func(items []timelineItem) bool {
		return slices.ContainsFunc(items, func(it timelineItem) bool {
			return it.event != nil && it.event.Type == logs.StreamEventSourceRemoved && it.event.SourceID == "b"
		})
	})
	update("")
	r.marker("b", 2)
	items := r.tl.snapshot()
	if got, want := signature(items, "b"), "R:b1 MARK R:b1 MARK"; got != want {
		t.Errorf("source b: %s, want %s", got, want)
	}
	if got, want := signature(items, "a"), "R:a1 R:a2 MARK"; got != want {
		t.Errorf("source a was disturbed: %s, want %s", got, want)
	}
}

// A source of a finite session that is disabled before its stream ended has no
// boundary to report: its stream closing is not its end. The source that stayed is
// marked when its own stream ends. Both streams are held, so the session is still
// running when the update arrives.
func TestAFiniteSourceDisabledBeforeItsEndSendsNoMarker(t *testing.T) {
	logsBySource := map[string]*tailLog{"a": newTailLog("a1"), "b": newTailLog("b1", "b2")}
	releaseA, releaseB := logsBySource["a"].Hold(), logsBySource["b"].Hold()
	defer releaseA()
	defer releaseB()
	handler := logs.Handler{
		Plugin: "test", Resource: "pod",
		Handler: func(pctx *types.PluginContext, req logs.LogStreamRequest) (io.ReadCloser, error) {
			return logsBySource[req.SourceID].open(pctx, req)
		},
		ReplayCount: func(pctx *types.PluginContext, req logs.LogStreamRequest) (int64, error) {
			return logsBySource[req.SourceID].count(pctx, req)
		},
	}
	r := newRig(t, map[string]logs.Handler{"test/pod": handler}, func(c *logs.ManagerConfig) {
		c.Resolvers = map[string]logs.SourceResolver{
			"deployment": func(*types.PluginContext, *resourcev2.Value, logs.SourceResolverOptions) (*logs.SourceResolverResult, error) {
				return &logs.SourceResolverResult{Sources: []logs.LogSource{{ID: "a"}, {ID: "b"}}}, nil
			},
		}
	})
	session := r.openKey("deployment", asking(logs.LogSessionOptions{Follow: false}))
	waitOpen(t, logsBySource["a"], 1)
	waitOpen(t, logsBySource["b"], 1)
	if _, err := r.mgr.UpdateSessionOptions(r.ctx, session.ID, logs.LogSessionOptions{
		Follow: false, Params: map[string]string{"enabled_sources": "a"},
	}); err != nil {
		t.Fatal(err)
	}
	r.tl.until(t, "source b to be removed", func(items []timelineItem) bool {
		return slices.ContainsFunc(items, func(it timelineItem) bool {
			return it.event != nil && it.event.Type == logs.StreamEventSourceRemoved && it.event.SourceID == "b"
		})
	})
	releaseB()
	time.Sleep(30 * time.Millisecond)
	if n := countMarkers(r.tl.snapshot(), "b"); n != 0 {
		t.Fatalf("a source disabled before its stream ended was marked %d times", n)
	}
	releaseA()
	r.marker("a", 1)
	if n := countMarkers(r.tl.snapshot(), "b"); n != 0 {
		t.Fatalf("the source that stayed ended, and the disabled one was marked %d times", n)
	}
}

// The host reads these JSON keys from the Go core, and a renamed key would silently turn
// replay markers off for every plugin.
func TestTheJSONKeysTheHostReads(t *testing.T) {
	marshal := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, c := range []struct{ what, got, want string }{
		{"a replay line", marshal(logs.LogLine{Replay: true}), `"replay":true`},
		{"a session that accepted", marshal(logs.LogSession{ReplayMarkers: true}), `"replay_markers":true`},
		{"the caught-up event", marshal(logs.LogStreamEvent{Type: logs.StreamEventSourceCaughtUp}), `"type":7`},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("%s: %s does not contain %s", c.what, c.got, c.want)
		}
	}
	// Absent means false, as the host reads it.
	if got := marshal(logs.LogLine{}); strings.Contains(got, `"replay"`) {
		t.Errorf("a line that is not replay carries the key: %s", got)
	}
	if got := marshal(logs.LogSession{}); strings.Contains(got, `"replay_markers"`) {
		t.Errorf("a session that did not accept carries the key: %s", got)
	}
}

// Only the value "1" opts in: "0", "false" and the like must not turn replay markers on.
func TestOnlyTheValueOneOptsIn(t *testing.T) {
	for value, want := range map[string]bool{"1": true, "0": false, "false": false, "true": false, "yes": false, "": false} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			r := newRig(t, newTailLog("a").handlers(true))
			session := r.open(logs.LogSessionOptions{Follow: true, Params: map[string]string{logs.ParamReplayMarkers: value}})
			if session.ReplayMarkers != want {
				t.Errorf("ReplayMarkers = %v for %q, want %v", session.ReplayMarkers, value, want)
			}
			if _, has := session.Options.Params[logs.ParamReplayMarkersAccepted]; has != want {
				t.Errorf("the accepted key is present = %v for %q, want %v", has, value, want)
			}
		})
	}
}

// The context a ReplayCount call is given is the source's: removing the source cancels a
// call that is blocked, so it cannot hold a slot or a goroutine for a source nobody wants.
func TestReplayCountContextFollowsTheSource(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	h := logs.Handler{
		Plugin: "test", Resource: "pod",
		SourceBuilder: logtest.SingleSourceBuilder("pod", nil),
		Handler: func(*types.PluginContext, logs.LogStreamRequest) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		},
		ReplayCount: func(pctx *types.PluginContext, _ logs.LogStreamRequest) (int64, error) {
			close(entered)
			<-pctx.Context.Done()
			close(cancelled)
			return 0, pctx.Context.Err()
		},
	}
	r := newRig(t, map[string]logs.Handler{"test/pod": h})
	session := r.open(asking(logs.LogSessionOptions{Follow: true}))
	select {
	case <-entered:
	case <-time.After(replayWait):
		t.Fatal("ReplayCount was never called")
	}
	if _, err := r.mgr.UpdateSessionOptions(r.ctx, session.ID, asking(logs.LogSessionOptions{
		Follow: true, Params: map[string]string{"enabled_sources": "other"},
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(replayWait):
		t.Fatal("a ReplayCount call kept running after its source was removed")
	}
}

// failingReader fails every Read with its error.
type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

// A finite source whose stream fails midway and is retried is caught up once, at its
// real end: the lines of the failed attempt are replay too, and the event is not sent
// for it.
func TestAFiniteSourceThatFailsAndIsRetriedIsCaughtUpOnceAtItsEnd(t *testing.T) {
	opens := 0
	h := logs.Handler{
		Plugin: "test", Resource: "pod",
		SourceBuilder: logtest.SingleSourceBuilder("pod", nil),
		Handler: func(*types.PluginContext, logs.LogStreamRequest) (io.ReadCloser, error) {
			opens++
			if opens == 1 {
				return io.NopCloser(io.MultiReader(strings.NewReader("a\nb\n"), failingReader{errBoom})), nil
			}
			return io.NopCloser(strings.NewReader("a\nb\nc\n")), nil
		},
		ReplayCount: func(*types.PluginContext, logs.LogStreamRequest) (int64, error) { return 0, nil },
	}
	r := newRig(t, map[string]logs.Handler{"test/pod": h})
	r.open(asking(logs.LogSessionOptions{Follow: false}))
	items := r.tl.until(t, "the session to end", func(items []timelineItem) bool {
		return slices.ContainsFunc(items, func(it timelineItem) bool {
			return it.event != nil && it.event.Type == logs.StreamEventStreamEnded
		})
	})
	if got, want := signature(items, "pod"), "R:a R:b R:a R:b R:c MARK"; got != want {
		t.Fatalf("timeline = %s, want %s", got, want)
	}
}

// A LogLine is allocated once per line, so its size class is a cost every session pays,
// asking or not. The replay flag sits in padding the struct already had: it must stay in
// the 96-byte class it had before the flag existed.
func TestALogLineStaysInItsSizeClass(t *testing.T) {
	if got := unsafe.Sizeof(logs.LogLine{}); got > 96 {
		t.Fatalf("a LogLine is %d bytes; it was 96 before the replay flag and must not leave that size class", got)
	}
}
