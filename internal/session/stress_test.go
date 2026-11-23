package session

import (
	"context"
	"errors"
	"math/rand/v2"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
)

// The registry under the shared lifecycle hammer: eight workers create sessions, update them
// (start a producer, watch a context, take a snapshot, end the parent), and end them with a random
// cause, while one to three Closes land at a random moment and the workers go on. At rest every
// session that was admitted was ended once, nothing was emitted for one after its end, the
// registry is empty, Close returned in its bound, and the goroutines are back where they began.
// The hammer runs for 1 s under -short and 5 s otherwise, or for as long as
// OMNIVIEW_LIFECYCLE_HAMMER says.
func TestRegistryUnderTheLifecycleHammer(t *testing.T) {
	var (
		mu    sync.Mutex
		lanes []*hammerLane
	)
	lifecycletest.Hammer(t, func(rec *lifecycletest.Recorder) lifecycletest.Lane {
		l := newHammerLane(rec)
		mu.Lock()
		defer mu.Unlock()
		lanes = append(lanes, l)
		return l
	}, lifecycletest.Config{})

	mu.Lock()
	defer mu.Unlock()
	if len(lanes) == 0 {
		t.Fatal("the hammer ran no round")
	}
	for round, l := range lanes {
		l.found.report(t)
		if n := l.reg.Len(); n != 0 {
			t.Errorf("round %d: the registry holds %d entries at rest", round, n)
		}
		if infos := l.reg.Snapshot(); len(infos) != 0 {
			t.Errorf("round %d: Snapshot at rest is %v", round, infos)
		}
		if _, err := l.reg.Admit(context.Background(), Spec{ID: "after"}, nil); !errors.Is(err, ErrClosed) {
			t.Errorf("round %d: Admit after Close: %v, want %v", round, err, ErrClosed)
		}
	}
}

// hammerLane is a lane on a Registry for the lifecycle hammer: its sessions emit events through
// producers, report their ends to the recorder from the end function, and check the rules an
// end must keep.
type hammerLane struct {
	reg   *Registry[*hammerState]
	rec   *lifecycletest.Recorder
	found findings
	calls atomic.Uint64 // feeds the random source of each call
}

// hammerState is the state of one session.
type hammerState struct {
	active       atomic.Int32 // producers running
	cancelParent context.CancelFunc
}

func newHammerLane(rec *lifecycletest.Recorder) *hammerLane {
	l := &hammerLane{rec: rec}
	l.reg = New(Config[*hammerState]{OnEnd: l.end})
	return l
}

// rng returns a random source of its own for one call.
func (l *hammerLane) rng() *rand.Rand {
	return rand.New(rand.NewPCG(0x5e55, l.calls.Add(1)))
}

func (l *hammerLane) end(e *Entry[*hammerState]) {
	st := e.Value()
	select {
	case <-e.Done():
	default:
		l.found.add("%s: the end function ran before Done was closed", e.ID())
	}
	if n := st.active.Load(); n != 0 {
		l.found.add("%s: the end function ran with %d producers running", e.ID(), n)
	}
	if e.Cause() == CauseNone || e.Context().Err() == nil {
		l.found.add("%s: the end function ran with cause %v and context error %v", e.ID(), e.Cause(), e.Context().Err())
	}
	if st.cancelParent != nil {
		st.cancelParent()
	}
	l.rec.Ended(e.ID())
}

func (l *hammerLane) Create(ctx context.Context, id string) (lifecycletest.Session, error) {
	rng := l.rng()
	spec := Spec{ID: id}
	if rng.IntN(3) == 0 {
		spec.Group, spec.Limit = "g"+strconv.Itoa(rng.IntN(4)), 8
	}
	parent, cancelParent := ctx, context.CancelFunc(nil)
	if rng.IntN(4) == 0 {
		parent, cancelParent = context.WithCancel(ctx)
	}
	e, err := l.reg.Admit(parent, spec, func(context.Context) (*hammerState, error) {
		l.rec.Admitted(id) // under the registry's lock: atomic with the admission
		return &hammerState{cancelParent: cancelParent}, nil
	})
	if err != nil {
		if cancelParent != nil {
			cancelParent()
		}
		return nil, err
	}
	e.Go(func() { l.produce(e) })
	return &hammerSession{l: l, e: e}, nil
}

// produce emits a few events, and then one more once the entry's context is done: that is after
// End and before the end function, and the end function waits for it.
func (l *hammerLane) produce(e *Entry[*hammerState]) {
	st := e.Value()
	st.active.Add(1)
	defer st.active.Add(-1)
	for range l.rng().IntN(4) {
		l.rec.Event(e.ID())
		runtime.Gosched()
	}
	<-e.Context().Done()
	l.rec.Event(e.ID())
}

func (l *hammerLane) Close(ctx context.Context) error { return l.reg.Close(ctx) }

func (l *hammerLane) Live() int { return l.reg.Len() }

type hammerSession struct {
	l *hammerLane
	e *Entry[*hammerState]
}

var errSessionEnded = errors.New("the session ended")

func (s *hammerSession) Update(ctx context.Context) error {
	l, e := s.l, s.e
	st := e.Value()
	rng := l.rng()
	switch rng.IntN(6) {
	case 0, 1: // a producer that emits one event
		if !e.Go(func() {
			st.active.Add(1)
			defer st.active.Add(-1)
			l.rec.Event(e.ID())
		}) {
			return errSessionEnded
		}
	case 2: // a watcher of a context of its own, which may end the session at once
		watched, cancel := context.WithCancel(ctx)
		context.AfterFunc(e.Context(), cancel) // released when the entry ends
		e.Watch(watched, CauseExpired)
		if rng.IntN(4) == 0 {
			cancel()
		}
	case 3: // a snapshot is sorted, and names what it holds
		var last string
		for _, info := range l.reg.Snapshot() {
			if info.ID == "" || info.ID <= last {
				l.found.add("Snapshot is not sorted by id, or holds an empty one: %q after %q", info.ID, last)
			}
			last = info.ID
		}
	case 4: // the parent ends
		if st.cancelParent != nil {
			st.cancelParent()
		}
	default: // reads
		_, _ = e.Cause(), e.Value()
		select {
		case <-e.Ended():
			if e.Cause() == CauseNone {
				l.found.add("%s: Ended is closed and no cause is recorded", e.ID())
			}
		default:
		}
	}
	return nil
}

func (s *hammerSession) End(context.Context) error {
	causes := [...]Cause{CauseFinished, CauseRequested, CauseRetired, CauseExpired, CauseTimeout}
	s.e.End(causes[s.l.rng().IntN(len(causes))])
	return nil
}
