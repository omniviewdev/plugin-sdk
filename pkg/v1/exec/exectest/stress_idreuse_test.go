package exectest

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// An id is used again once its tombstone is gone, and the engine admits a start of it the moment it can, before the worker that made the start has
// told the round: a call that is addressed to the id and made meanwhile, by any worker, meets the new session, and what the stress knows of the
// old one (its terminal, its end, its output) says nothing of the answer. These tests hold the engine at the moments at which that happens, which
// no run reaches on purpose, and see what the stress says of an answer that is another session's.

// scriptedEngine is the real engine with the calls a test scripts: the others are the real ones.
type scriptedEngine struct {
	exechook.Engine
	open         func(ctx context.Context, req *execv2.OpenRequest) (exechook.Command, error)
	readOutput   func(ctx context.Context, req *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error)
	listSessions func(ctx context.Context) ([]*execv2.SessionInfo, error)
	endSession   func(ctx context.Context, req *execv2.EndSessionRequest) error
}

func (e *scriptedEngine) Open(ctx context.Context, req *execv2.OpenRequest) (exechook.Command, error) {
	if e.open != nil {
		return e.open(ctx, req)
	}
	return e.Engine.Open(ctx, req)
}

func (e *scriptedEngine) ReadOutput(ctx context.Context, req *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error) {
	if e.readOutput != nil {
		return e.readOutput(ctx, req)
	}
	return e.Engine.ReadOutput(ctx, req)
}

func (e *scriptedEngine) ListSessions(ctx context.Context) ([]*execv2.SessionInfo, error) {
	if e.listSessions != nil {
		return e.listSessions(ctx)
	}
	return e.Engine.ListSessions(ctx)
}

func (e *scriptedEngine) EndSession(ctx context.Context, req *execv2.EndSessionRequest) error {
	if e.endSession != nil {
		return e.endSession(ctx, req)
	}
	return e.Engine.EndSession(ctx, req)
}

// scripted is the factory of an engine that is the real one with the calls e scripts.
func scripted(e *scriptedEngine) engineFactory {
	return func(targets []exec.Target, cfg exechook.EngineConfig) (exechook.Engine, error) {
		real, err := realEngine(targets, cfg)
		e.Engine = real
		return e, err
	}
}

// heldID is the id that the cases below call about: held by the session of the ledger that the case makes.
const heldID = "x"

func exitedWith(code int64) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Exited{Exited: &execv2.Exited{ExitCode: code}}}
}

// anEndedSession is a session of the round that ended with exit code 0 after ten bytes of output and has no terminal, which the ledger holds under heldID.
func anEndedSession(r *stressRound) *sessionRec {
	ended := make(chan struct{})
	close(ended)
	rec := &sessionRec{id: heldID, key: heldID + "#1", done: ended}
	res := exechook.Result{End: exitedWith(0), BytesOut: 10}
	rec.res.Store(&res)
	r.ledger.beginStart(heldID)
	r.ledger.admit(rec)
	r.ledger.endStart(heldID)
	return rec
}

// anotherSessionsOutput is what a ReadOutput of heldID answers when the id has been taken by a session with a terminal: a chunk of the terminal's
// stream, which a session of the ledger without a terminal never writes.
func anotherSessionsOutput() *execv2.ReadOutputResponse {
	return &execv2.ReadOutputResponse{
		Chunks:     []*execv2.Output{{Stream: execv2.Stream_STREAM_TTY, Offset: 0, Data: patternBytes(0, 10)}},
		NextOffset: 10, Ended: true,
	}
}

// anotherSessionsEntry is what ListSessions lists under heldID when the id has been taken by a session that ended with exit code 7.
func anotherSessionsEntry() []*execv2.SessionInfo {
	return []*execv2.SessionInfo{{
		Facts:      &execv2.SessionFacts{SessionId: heldID},
		NextOffset: 10,
		EndedWith:  &execv2.Ended{End: exitedWith(7), BytesOut: 10, LastOffset: 10},
	}}
}

// overlaps are the ways in which a start of the id may overlap a call that is addressed to it, none of which the ledger's record of the sessions
// shows: the start is in the engine, which may have admitted it, and the worker that made it has not yet told the round.
var overlaps = []struct {
	name string
	// beforeCall is what happens before the call is made, and duringCall what happens inside it, at the engine.
	beforeCall, duringCall func(r *stressRound)
	// reported says whether the stress holds the answer to what it knows of the session of the ledger: only when no start overlapped.
	reported bool
}{
	{"no start of the id", func(*stressRound) {}, func(*stressRound) {}, true},
	{"a start of the id is under way when the call is made", func(r *stressRound) { r.ledger.beginStart(heldID) }, func(*stressRound) {}, false},
	{"a start of the id begins during the call", func(*stressRound) {}, func(r *stressRound) { r.ledger.beginStart(heldID) }, false},
	{"a start of the id is admitted and recorded during the call", func(*stressRound) {}, func(r *stressRound) { admitted(&r.ledger, heldID) }, false},
	{"a start of the id is refused during the call", func(*stressRound) {}, func(r *stressRound) { r.ledger.beginStart(heldID); r.ledger.endStart(heldID) }, true},
}

func TestAReadOfAnIdThatAStartOverlappedIsNotHeldToWhatTheStressKnowsOfTheSessionThatHeldItBefore(t *testing.T) {
	for _, o := range overlaps {
		t.Run(o.name, func(t *testing.T) {
			var r *stressRound
			eng := &scriptedEngine{readOutput: func(context.Context, *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error) {
				o.duringCall(r)
				return anotherSessionsOutput(), nil
			}}
			r = newBareRound(t, stressHooks{engine: scripted(eng)})
			anEndedSession(r)
			o.beforeCall(r)

			r.readOutputOp(rand.New(rand.NewPCG(1, 2)))
			said := r.violations()
			switch {
			case o.reported && (len(said) != 1 || !strings.Contains(said[0], "STREAM_TTY")):
				t.Fatalf("the stress said %q of an answer that has a terminal's stream for a session without one, which nothing overlapped: want that, said once", said)
			case !o.reported && len(said) != 0:
				t.Fatalf("the stress said %q of an answer that may be another session's, because %s", said, o.name)
			}
		})
	}
}

func TestAListOfAnIdThatAStartOverlappedIsNotHeldToWhatTheStressKnowsOfTheSessionThatHeldItBefore(t *testing.T) {
	for _, o := range overlaps {
		t.Run(o.name, func(t *testing.T) {
			var r *stressRound
			eng := &scriptedEngine{listSessions: func(context.Context) ([]*execv2.SessionInfo, error) {
				o.duringCall(r)
				return anotherSessionsEntry(), nil
			}}
			r = newBareRound(t, stressHooks{engine: scripted(eng)})
			anEndedSession(r)
			o.beforeCall(r)

			r.listOp()
			said := r.violations()
			switch {
			case o.reported && (len(said) != 1 || !strings.Contains(said[0], "the tombstone says the session ended")):
				t.Fatalf("the stress said %q of a tombstone that says exit code 7 for a session that exited with 0, which nothing overlapped: want that, said once", said)
			case !o.reported && len(said) != 0:
				t.Fatalf("the stress said %q of a list whose entry may be another session's, because %s", said, o.name)
			}
		})
	}
}

func TestTheReadOfATombstoneByAWindowIsNotHeldToWhatTheStressKnowsOfTheSessionThatHeldTheIdBefore(t *testing.T) {
	for _, o := range overlaps {
		t.Run(o.name, func(t *testing.T) {
			var r *stressRound
			eng := &scriptedEngine{readOutput: func(context.Context, *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error) {
				o.duringCall(r)
				return anotherSessionsOutput(), nil
			}}
			r = newBareRound(t, stressHooks{engine: scripted(eng)})
			rec := anEndedSession(r)
			s := &sessionState{rec: rec}
			c := &stressCommand{id: heldID, key: rec.key, b: behaviourNamed("exit0"), sess: s}
			w := s.newWindow(c, 0, false)
			o.beforeCall(r)

			r.completeFromTombstone(c, w, exechook.Result{BytesOut: 10})
			said := r.violations()
			switch {
			case o.reported && (len(said) == 0 || !strings.Contains(said[0], "STREAM_TTY")):
				t.Fatalf("the stress said %q of a tombstone with a terminal's stream for a session without one, which nothing overlapped: want that", said)
			case !o.reported && len(said) != 0:
				t.Fatalf("the stress said %q of a tombstone that may be another session's, because %s", said, o.name)
			}
		})
	}
}

// openProbe is the engine whose Open sees what the ledger knows of the id at the moment the engine may admit it.
type openProbe struct {
	mu      sync.Mutex
	pending []int
}

func (p *openProbe) saw() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.pending...)
}

// A start of a session is under way, for the ledger, for as long as the engine may have admitted it and the worker has not recorded it: from before
// the engine is asked until after the session is in the ledger, or the start was refused. It is under way for a start that is refused too, which
// the ledger cannot know until the answer.
func TestAStartOfASessionIsUnderWayForTheLedgerWhileTheEngineMayHaveAdmittedItAndNoLonger(t *testing.T) {
	var r *stressRound
	probe := &openProbe{}
	eng := &scriptedEngine{}
	eng.open = func(ctx context.Context, req *execv2.OpenRequest) (exechook.Command, error) {
		probe.mu.Lock()
		probe.pending = append(probe.pending, r.ledger.stamp(req.GetSessionId()).pending)
		probe.mu.Unlock()
		return eng.Engine.Open(ctx, req)
	}
	r = newBareRound(t, stressHooks{engine: scripted(eng)})
	w := &stressWorker{r: r, id: 0, rng: rand.New(rand.NewPCG(1, 1))}

	w.start(behaviourNamed("exit0"), false, "", false)
	if len(w.pool) != 1 {
		t.Fatalf("the start was not admitted: %d commands held", len(w.pool))
	}
	id := w.pool[0].id
	w.start(behaviourNamed("exit0"), false, id, true) // the id is held, by the session or by its tombstone: refused

	if got := probe.saw(); len(got) != 2 || got[0] != 1 || got[1] != 1 {
		t.Errorf("the ledger had %v starts of the id under way when the engine was asked, want one for each of the two starts", got)
	}
	if got := r.ledger.stamp(id); got.pending != 0 || got.admissions != 1 {
		t.Errorf("after both starts returned the ledger has %d starts under way and %d admissions of the id, want none under way and the one that was admitted", got.pending, got.admissions)
	}
}

// refusing is the engine that refuses every Open the way an engine refuses the id of a live session, and counts them.
func refusing(opens *atomic.Int32) *scriptedEngine {
	return &scriptedEngine{open: func(context.Context, *execv2.OpenRequest) (exechook.Command, error) {
		opens.Add(1)
		return nil, &exec.Error{Class: exec.ErrorClassConflict, Reason: exec.ErrorReasonSessionExists, Err: errors.New("held")}
	}}
}

// A worker starts an id again only when no call on a goroutine of its own is addressed to the id, whichever of the id's sessions the call was
// made for: the call, made for the newest of them, would meet the session that the start admits.
func TestAWorkerDoesNotStartAnIdAgainWhileACallOnAGoroutineOfItsOwnIsAddressedToIt(t *testing.T) {
	var opens atomic.Int32
	r := newBareRound(t, stressHooks{engine: scripted(refusing(&opens))})
	w := &stressWorker{r: r, id: 0, rng: rand.New(rand.NewPCG(5, 6))}
	older := &stressCommand{id: heldID, b: behaviourNamed("exit0"), sess: &sessionState{rec: &sessionRec{id: heldID}}}
	w.pool = []*stressCommand{older}
	w.history = []retiredSession{{rec: older.sess.rec, b: older.b}}

	r.ledger.flightOf(heldID).Add(1) // a call is made on a goroutine of its own for the newest session that holds the id
	w.duplicate(older)
	for range 64 {
		w.duplicateAny()
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("the worker asked the engine for the id %d times while a call on a goroutine of its own was addressed to it", got)
	}

	r.ledger.flightOf(heldID).Add(-1)
	w.duplicate(older)
	if got := opens.Load(); got != 1 {
		t.Fatalf("the worker asked the engine for the id %d times once the call had returned, want once", got)
	}
	before := opens.Load()
	for range 64 {
		w.duplicateAny()
	}
	if opens.Load() == before {
		t.Fatalf("the worker never started an id of its history again, with no call in flight")
	}
}

// A call that the worker makes on a goroutine of its own is counted for its id until it returns, so that the start of the id (above) finds it.
func TestACallOnAGoroutineOfItsOwnIsCountedForItsIdForAsLongAsItRuns(t *testing.T) {
	r := newBareRound(t, stressHooks{})
	w := &stressWorker{r: r, id: 0, rng: rand.New(rand.NewPCG(3, 4))}
	c := &stressCommand{id: heldID, sess: &sessionState{rec: &sessionRec{id: heldID}}}
	var most atomic.Int32
	for range 64 {
		w.either(c, func(*rand.Rand) {
			n := r.ledger.flightOf(heldID).Load()
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.ops.WaitContext(ctx); err != nil {
		t.Fatalf("the calls on goroutines of their own did not return: %v", err)
	}
	if most.Load() < 1 {
		t.Error("no call that ran on a goroutine of its own was counted for its id while it ran")
	}
	if got := r.ledger.flightOf(heldID).Load(); got != 0 {
		t.Errorf("%d calls are counted for the id after every call returned, want none", got)
	}
}

// The canary's EndSession of a session that no stream is attached to is a call whose answer has to come: the session is an echo that nobody holds,
// and it ends once it is asked to. The canary's two EndSession calls (one is refused for the stream that is attached, one ends the session) wait
// for their answers up to the bound of the round, and not for the fifty milliseconds that a worker gives the dice's EndSession (a session whose
// function ignores its context answers only when the harness lets it go): how long an echo takes to end is the machine's to say, and on a machine
// that is busy it took longer than fifty, which the canary reported as a refusal of EndSession.
func TestTheCanaryWaitsForTheAnswersOfEndSessionUpToTheBoundOfTheRound(t *testing.T) {
	var mu sync.Mutex
	var waits []time.Duration
	eng := &scriptedEngine{}
	eng.endSession = func(ctx context.Context, req *execv2.EndSessionRequest) error {
		d, _ := ctx.Deadline()
		mu.Lock()
		waits = append(waits, time.Until(d))
		mu.Unlock()
		return eng.Engine.EndSession(ctx, req)
	}
	r := newBareRound(t, stressHooks{engine: scripted(eng)})
	w := &stressWorker{r: r, id: 0, rng: rand.New(rand.NewPCG(1, 1))}
	w.canary()

	if v := r.violations(); len(v) != 0 {
		t.Fatalf("the canary said %q of the real engine", v)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 2 {
		t.Fatalf("the canary called EndSession %d times, want twice", len(waits))
	}
	for i, wait := range waits {
		if wait <= r.cfg.CloseBound/2 {
			t.Errorf("the canary's EndSession #%d was given %v to answer, want the bound of the round, %v, and not the %v of the dice", i+1, wait.Round(time.Millisecond), r.cfg.CloseBound, endSessionPatience)
		}
	}
}
