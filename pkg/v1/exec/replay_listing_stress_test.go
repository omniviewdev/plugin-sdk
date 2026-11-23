package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// ListSessions, ReadOutput and EndSession against a plugin whose sessions are started, attached, detached, taken over and ended at every
// moment (op against itself, op against every other; the race detector decides what the locks do not). Workers cycle through sessions
// of their own, and each says what its session is before and after every step (a seqlock: a state is published only when the step is
// over, with an odd count while it is going on), so a listing taken while a state held the whole time has to agree with it. Victims are
// sessions with a stream attached all along, which adversaries try to end with EndSession from every goroutine at once.
//
// Held for every listing: each session once, in order of id; an ended session has no stream and no lease and its output ends where its
// end says; one that lives has no end, a stream and no lease or a lease and no stream (or neither, for the moment its Close takes); an
// ended session stays ended with the same end until its tombstone is gone, and it goes only when the store is full (the oldest goes
// first) or its time is up (not within this test); the tombstones never exceed their bound. And at the end: the plugin holds no
// session, every Ended a worker read is the tombstone's, and no goroutine is left.

const (
	listingTombstones = 40 // the store of the run holds this many: the workers end more than that
	listingWorkers    = 6
	listingVictims    = 4
	listingAdversary  = 3
	listingListers    = 3
	listingOutput     = 1000
)

func listingFunction(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	buf := make([]byte, listingOutput)
	ledgerFill(buf, 0)
	if _, err := p.Stdout.Write(buf); err != nil {
		return exec.Exit{}, err
	}
	<-ctx.Done()
	return exec.Exit{}, ctx.Err()
}

// A step's published state.
const (
	listingNone     = 0
	listingAttached = 1
	listingDetached = 2
)

// listingTrack is what a worker says of the one session it has now.
type listingTrack struct {
	mu    sync.Mutex
	id    string
	seq   uint64 // odd while a step goes on
	state int
	ended *execv2.Ended // the last Ended a stream of this worker was sent, and the id of its session
	endID string
}

func (t *listingTrack) begin(id string) {
	t.mu.Lock()
	t.seq++
	if id != "" {
		t.id = id
	}
	t.mu.Unlock()
}

func (t *listingTrack) done(state int) {
	t.mu.Lock()
	t.state = state
	t.seq++
	t.mu.Unlock()
}

func (t *listingTrack) read() (id string, seq uint64, state int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id, t.seq, t.state
}

func (t *listingTrack) setEnded(id string, e *execv2.Ended) {
	t.mu.Lock()
	t.ended, t.endID = e, id
	t.mu.Unlock()
}

func (t *listingTrack) lastEnded() (string, *execv2.Ended) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.endID, t.ended
}

type listingWorld struct {
	t       *testing.T
	rig     *proofRig
	tracks  []*listingTrack // the workers', and then the victims'
	victims []string
	stop    atomic.Bool
	errs    chan string
	started atomic.Int64
	ended   atomic.Int64
	listed  atomic.Int64
}

func (w *listingWorld) problem(format string, args ...any) {
	select {
	case w.errs <- fmt.Sprintf(format, args...):
	default:
	}
}

// worker cycles through sessions of its own until the world stops.
func (w *listingWorld) worker(n int, track *listingTrack) {
	rng := rand.New(rand.NewPCG(uint64(n)+1, 77))
	for cycle := 0; !w.stop.Load(); cycle++ {
		id := fmt.Sprintf("w%d-%d", n, cycle)
		track.begin(id)
		conn, err := w.rig.openKind(context.Background(), "list", id, proofTTY)
		if err != nil {
			w.problem("worker %d: open %s: %v", n, id, err)
			return
		}
		w.started.Add(1)
		cur := newLedgerStream(conn, 1, 0)
		track.done(listingAttached)
		state := listingAttached

		for range rng.IntN(4) {
			track.begin("")
			switch {
			case state == listingAttached && rng.IntN(3) == 0:
				// Detach: the stream is read to Detached, and the session is detached from then on.
				_ = cur.conn.Send(detachFrame(0))
				if !w.drainTo(cur, "Detached", func(f *execv2.ServerFrame) bool { return f.GetDetached() != nil }) {
					return
				}
				cur.retire()
				state = listingDetached
			case state == listingAttached && rng.IntN(2) == 0:
				// A second stream takes the session over: the first is told so.
				next, err := w.rig.attach(context.Background(), id, 0)
				if err != nil {
					w.problem("worker %d: supersede %s: %v", n, id, err)
					return
				}
				if !w.drainTo(cur, "Detached{SUPERSEDED}", func(f *execv2.ServerFrame) bool {
					return f.GetDetached().GetReason() == execv2.DetachReason_DETACH_REASON_SUPERSEDED
				}) {
					return
				}
				cur.retire()
				cur = newLedgerStream(next, 2, 0)
			case state == listingAttached:
				// The stream breaks, and the worker waits until the plugin has noticed: only then is the session detached for sure.
				cur.retire()
				if !proofPoll(engineWait, func() bool {
					info := proofListed(w.rig.client, id)
					return info != nil && !info.GetAttached()
				}) {
					w.problem("worker %d: %s still has a stream attached after it broke", n, id)
					return
				}
				state = listingDetached
			default:
				next, err := w.rig.attach(context.Background(), id, 0)
				if err != nil {
					w.problem("worker %d: attach %s: %v", n, id, err)
					return
				}
				cur = newLedgerStream(next, 3, 0)
				state = listingAttached
			}
			track.done(state)
		}

		// The end: Close on the stream, or EndSession of a detached session.
		track.begin("")
		var ended *execv2.Ended
		if state == listingAttached {
			_ = cur.conn.Send(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
			var last *execv2.ServerFrame
			if !w.drainTo(cur, "Ended", func(f *execv2.ServerFrame) bool { last = f; return f.GetEnded() != nil }) {
				return
			}
			ended = last.GetEnded()
		} else if err := w.rig.client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: id, Mode: execv2.CloseMode_CLOSE_MODE_KILL}); err != nil {
			w.problem("worker %d: EndSession(%s) of a detached session: %v", n, id, err)
			return
		}
		cur.retire()
		w.ended.Add(1)
		if ended != nil {
			track.setEnded(id, ended)
		}
		track.done(listingNone)
		// Its tombstone holds its id: a start of it is refused, at once, whatever else is going on. (The store of the run holds 40, and
		// the workers end more than that: once it is full the oldest tombstone goes and takes its id, so the probe is for the time before.)
		if count, _ := exec.PluginTombstones(w.rig.server.Plugin); count < listingTombstones {
			conn, err := w.rig.openKind(context.Background(), "list", id, proofTTY)
			switch {
			case err == nil:
				_ = conn.Send(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
				_ = conn.Close()
				w.problem("worker %d: the id %s of a session that ended was admitted again while its tombstone lives and the store was not full", n, id)
			case !isRefusalOf(err, exec.ErrorClassConflict, exec.ErrorReasonSessionExists):
				w.problem("worker %d: a start of the id %s of a session that ended: %v, want CONFLICT / SESSION_EXISTS", n, id, err)
			}
		}
	}
}

// drainTo reads a stream until a frame says what it was waiting for; it reports false (and the world has the problem) when the stream
// ends first.
func (w *listingWorld) drainTo(s *ledgerStream, what string, done func(*execv2.ServerFrame) bool) bool {
	for {
		select {
		case r := <-s.out:
			if r.err != nil {
				if !errors.Is(r.err, io.EOF) {
					w.problem("a stream broke while waiting for %s: %v", what, r.err)
				} else {
					w.problem("a stream ended before %s", what)
				}
				return false
			}
			if done(r.frame) {
				return true
			}
		case <-time.After(engineWait):
			w.problem("no frame within %v while waiting for %s", engineWait, what)
			return false
		}
	}
}

// lister takes listings until the world stops and holds each to the invariants, and to what the workers said.
func (w *listingWorld) lister(n int) {
	seen := map[string]*execv2.Ended{} // ids this lister saw ended, and how
	lived := map[string]bool{}         // ids this lister saw living and has not seen ended since
	for !w.stop.Load() {
		type snap struct {
			id    string
			seq   uint64
			state int
		}
		var before []snap
		for _, tr := range w.tracks {
			id, seq, state := tr.read()
			before = append(before, snap{id, seq, state})
		}
		list, err := w.rig.client.ListSessions(context.Background())
		if err != nil {
			w.problem("lister %d: ListSessions: %v", n, err)
			return
		}
		w.listed.Add(1)
		byID := map[string]*execv2.SessionInfo{}
		ended := 0
		prev := ""
		for _, info := range list.GetSessions() {
			id := info.GetFacts().GetSessionId()
			if _, dup := byID[id]; dup {
				w.problem("lister %d: the session %s is listed twice", n, id)
			}
			if id <= prev {
				w.problem("lister %d: the sessions are not in order of id: %q after %q", n, id, prev)
			}
			prev = id
			byID[id] = info
			if info.GetEndedWith() != nil {
				ended++
				if info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 || info.GetNextOffset() != info.GetEndedWith().GetLastOffset() {
					w.problem("lister %d: %s ended and is listed %v: no stream, no lease and the output to where it ended", n, id, info)
				}
				if old, ok := seen[id]; ok && !proto.Equal(old, info.GetEndedWith()) {
					w.problem("lister %d: %s ended with %v and is listed ended with %v later", n, id, old, info.GetEndedWith())
				}
				seen[id] = info.GetEndedWith()
				continue
			}
			lived[id] = true
			if info.GetAttached() && info.GetLeaseExpiresUnixMs() != 0 {
				w.problem("lister %d: %s is attached and has a lease: %v", n, id, info)
			}
			if info.GetNextOffset() > listingOutput {
				w.problem("lister %d: %s lists the output to %d, it writes %d", n, id, info.GetNextOffset(), listingOutput)
			}
		}
		for id := range lived {
			if info, ok := byID[id]; ok && info.GetEndedWith() != nil {
				delete(lived, id)
			} else if !ok && ended < listingTombstones {
				w.problem("lister %d: the session %s was listed as living and is listed no more, with no tombstone, though the store holds %d of %d: a session that ends is a tombstone", n, id, ended, listingTombstones)
			}
		}
		for id, e := range seen {
			if info, ok := byID[id]; ok && info.GetEndedWith() == nil {
				w.problem("lister %d: %s ended with %v and is listed as living later", n, id, e)
			}
			if _, ok := byID[id]; !ok && ended < listingTombstones {
				w.problem("lister %d: the tombstone of %s is gone and the store holds %d of %d: the oldest goes only when it is full", n, id, ended, listingTombstones)
			}
		}
		if ended > listingTombstones {
			w.problem("lister %d: the listing has %d tombstones, the bound is %d", n, ended, listingTombstones)
		}
		if count, retained := exec.PluginTombstones(w.rig.server.Plugin); count > listingTombstones || retained > 32<<20 {
			w.problem("lister %d: the store holds %d tombstones keeping %d bytes", n, count, retained)
		}
		// What a worker said, if it said the same before and after (no step went on while the listing was taken).
		for i, tr := range w.tracks {
			id, seq, state := tr.read()
			b := before[i]
			if b.id != id || b.seq != seq || seq%2 == 1 || b.id == "" {
				continue
			}
			info := byID[id]
			switch state {
			case listingAttached:
				if info == nil || info.GetEndedWith() != nil || !info.GetAttached() {
					w.problem("lister %d: %s had a stream attached all along and is listed %v", n, id, info)
				}
			case listingDetached:
				if info == nil || info.GetEndedWith() != nil || info.GetAttached() {
					w.problem("lister %d: %s was detached all along and is listed %v", n, id, info)
				}
			}
		}
	}
}

// adversary tries to end the victims, which have a stream attached all along, and reads the rings of every session.
func (w *listingWorld) adversary(n int) {
	rng := rand.New(rand.NewPCG(uint64(n)+500, 1))
	for !w.stop.Load() {
		switch rng.IntN(3) {
		case 0, 1:
			victim := w.victims[rng.IntN(len(w.victims))]
			err := w.rig.client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: victim, Mode: execv2.CloseMode_CLOSE_MODE_KILL})
			if !isRefusalOf(err, exec.ErrorClassConflict, exec.ErrorReasonUnspecified) {
				w.problem("adversary %d: EndSession of %s, which has a stream attached: %v, want CONFLICT", n, victim, err)
			}
		default:
			tr := w.tracks[rng.IntN(listingWorkers)]
			id, _, _ := tr.read()
			if id == "" {
				continue
			}
			resp, err := w.rig.client.ReadOutput(context.Background(), &execv2.ReadOutputRequest{SessionId: id, FromOffset: 0, MaxBytes: 1 << 20})
			if err != nil {
				if !isRefusalOf(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
					w.problem("adversary %d: ReadOutput(%s): %v", n, id, err)
				}
				continue
			}
			l := newLedger(true, false, true, ledgerRing)
			l.read(resp, 0, 1<<20)
			if len(l.problems) > 0 {
				w.problem("adversary %d: ReadOutput(%s): %s", n, id, strings.Join(l.problems, "; "))
			}
			if resp.GetEnded() && resp.GetNextOffset() != listingOutput && resp.GetNextOffset() != 0 {
				w.problem("adversary %d: ReadOutput(%s) says ended at %d, the session writes %d", n, id, resp.GetNextOffset(), listingOutput)
			}
		}
	}
}

func TestListSessionsAndTheSessionCallsAreConsistentWhileSessionsAreStartedAttachedDetachedAndEnded(t *testing.T) {
	rig := newProofRig(t, proofTarget("list", listingFunction, func(i *exec.TargetInfo) {
		i.Session = exec.DefaultSessionLimits()
		i.Session.MaxSessions = 64
	}), proofTarget("warm", proofEchoFunction))
	exec.ConfigurePluginEngine(rig.server.Plugin, exechook.EngineConfig{MaxTombstones: listingTombstones})
	warm := rig.mustOpenKind("warm", "warm", proofTTY)
	if _, err := newProofEchoer(warm).measure(3); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	_ = warm.Close()
	proofEndDetached(t, rig.client, "warm")
	baseline := lifecycletest.TakeBaseline()

	w := &listingWorld{t: t, rig: rig, errs: make(chan string, 64)}
	for range listingWorkers {
		w.tracks = append(w.tracks, &listingTrack{})
	}
	var victimConns []*ledgerStream
	for i := range listingVictims {
		id := fmt.Sprintf("victim-%d", i)
		conn, err := rig.openKind(context.Background(), "list", id, proofTTY)
		if err != nil {
			t.Fatalf("open the victim %s: %v", id, err)
		}
		victimConns = append(victimConns, newLedgerStream(conn, 1, 0))
		w.victims = append(w.victims, id)
		w.tracks = append(w.tracks, &listingTrack{id: id, state: listingAttached})
	}

	done := make(chan struct{}, listingWorkers+listingAdversary+listingListers)
	run := func(f func()) {
		go func() {
			defer func() { done <- struct{}{} }()
			f()
		}()
	}
	for i := range listingWorkers {
		run(func() { w.worker(i, w.tracks[i]) })
	}
	for i := range listingAdversary {
		run(func() { w.adversary(i) })
	}
	for i := range listingListers {
		run(func() { w.lister(i) })
	}
	begin := time.Now()
	time.AfterFunc(max(lifecycletest.DefaultDuration()/2, 2*time.Second), func() { w.stop.Store(true) })
	for range listingWorkers + listingAdversary + listingListers {
		select {
		case <-done:
		case <-time.After(lifecycletest.DefaultDuration() + 3*engineWait):
			t.Fatalf("the workers did not stop")
		}
	}
	took := time.Since(begin)

	// The victims were attached all along: they are ended by their streams, which are sent their Ended.
	for i, v := range victimConns {
		_ = v.conn.Send(closeFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL))
		got := false
		for !got {
			select {
			case r := <-v.out:
				if r.err != nil {
					t.Fatalf("victim %d: its stream broke: %v", i, r.err)
				}
				got = r.frame.GetEnded() != nil
			case <-time.After(engineWait):
				t.Fatalf("victim %d: no Ended within %v", i, engineWait)
			}
		}
		v.retire()
	}
	close(w.errs)
	for problem := range w.errs {
		t.Error(problem)
	}
	t.Logf("%d sessions started and %d ended in %v by %d workers, %d listings taken by %d listers, %d adversaries", w.started.Load(), w.ended.Load(), took.Round(time.Millisecond),
		listingWorkers, w.listed.Load(), listingListers, listingAdversary)
	if w.started.Load() < 50 || w.listed.Load() < 50 {
		t.Errorf("the run was too short to say anything: %d sessions, %d listings", w.started.Load(), w.listed.Load())
	}

	// At rest: no session lives; every Ended a worker read is the tombstone's, when the tombstone is still there; no goroutine is left.
	plugin := rig.server.Plugin
	if !proofPoll(engineWait, func() bool { return len(exec.PluginLive(plugin)) == 0 }) {
		t.Errorf("the plugin still holds %v", exec.PluginLive(plugin))
	}
	list, err := rig.client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	byID := map[string]*execv2.SessionInfo{}
	var ids []string
	for _, info := range list.GetSessions() {
		id := info.GetFacts().GetSessionId()
		byID[id] = info
		ids = append(ids, id)
		if info.GetEndedWith() == nil {
			t.Errorf("%s lives at rest: %v", id, info)
		}
	}
	if !sort.StringsAreSorted(ids) || len(ids) != listingTombstones {
		t.Errorf("%d sessions are listed at rest (sorted: %v), want %d: the store is full and holds nothing else", len(ids), sort.StringsAreSorted(ids), listingTombstones)
	}
	for _, tr := range w.tracks[:listingWorkers] {
		id, ended := tr.lastEnded()
		if ended == nil {
			continue
		}
		if info, ok := byID[id]; ok && !proto.Equal(info.GetEndedWith(), ended) {
			t.Errorf("%s: the tombstone says %v, the stream was sent %v", id, info.GetEndedWith(), ended)
		}
	}
	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines at rest: %v", err)
	}
}
