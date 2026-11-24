package exec

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/session"
)

// What a session's streams do to one another's control frames and to the acknowledgements those earn. The frames of the stream that is
// the session's take effect while it holds the session (attachment.whileCurrent), an attach, a detach and a release wait for the frame
// that is taking effect, and a size is acknowledged to the stream that sent it and to no other (resizeOwner). These are the parts of
// that which a test can hold without a transport; the interactions over a stream are in open_control_attach_test.go.

// A frame whose effect does not wait takes effect before the session changes hands, or never: an attach, a detach and a release cannot
// begin while one is taking effect (the lock they take for writing is held for reading by the frame), they complete once it is done, and
// a frame of the stream the session left finds the attachment over and is not applied. The frame of the stream that now holds the
// session is.
func TestAFrameTakesEffectBeforeTheSessionChangesHandsOrNeverAndTheMoveWaitsForItToFinish(t *testing.T) {
	now := time.Unix(1000, 0)
	moves := []struct {
		name string
		move func(tr *testRing, old *attachment)
	}{
		{"an attach", func(tr *testRing, _ *attachment) {
			if _, _, err := tr.attach(context.Background(), 0); err != nil {
				t.Error(err)
			}
		}},
		{"a detach", func(tr *testRing, old *attachment) {
			if _, ok := tr.detach(old, time.Minute, now); !ok {
				t.Error("the stream could not detach")
			}
		}},
		{"a release", func(tr *testRing, old *attachment) {
			if _, ok := tr.release(old, time.Minute, now); !ok {
				t.Error("the stream could not be released")
			}
		}},
	}
	for _, tc := range moves {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestRing(1<<20, false, false)
			old := tr.creator
			inside, proceed, ran, moved := make(chan struct{}), make(chan struct{}), make(chan bool, 1), make(chan struct{})
			go func() { ran <- old.whileCurrent(func() { close(inside); <-proceed }) }()
			<-inside
			// The frame is taking effect: nothing can take the lock that the move takes for writing, whoever asks.
			if tr.att.frames.TryLock() {
				tr.att.frames.Unlock()
				t.Fatal("a frame is taking effect and the session could change hands: the move is not atomic with the frame")
			}
			go func() { defer close(moved); tc.move(tr, old) }()
			// The move is given every chance to run while the frame takes effect, and must not complete.
			for range 2000 {
				runtime.Gosched()
				select {
				case <-moved:
					t.Fatal("the session changed hands while a frame of its stream was taking effect")
				default:
				}
			}
			close(proceed)
			if !<-ran {
				t.Fatal("the frame of the session's own stream did not take effect")
			}
			<-moved
			if !old.over.Load() {
				t.Fatal("the stream the session left is not over")
			}
			applied := false
			if old.whileCurrent(func() { applied = true }) || applied {
				t.Fatal("a frame of a stream the session has left was applied after the move")
			}
		})
	}

	t.Run("the stream that holds the session applies its frames, however many times the session changed hands", func(t *testing.T) {
		tr := newTestRing(1<<20, false, false)
		first := tr.creator
		second, _, err := tr.attach(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		third, _, err := tr.attach(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		for name, stream := range map[string]*attachment{"first": first, "second": second} {
			if stream.whileCurrent(func() { t.Errorf("a frame of the %s stream was applied: the session is the third's", name) }) {
				t.Errorf("the %s stream applied a frame", name)
			}
		}
		applied := false
		if !third.whileCurrent(func() { applied = true }) || !applied {
			t.Fatal("the stream that holds the session did not apply its frame")
		}
	})
}

// sizeOwner is the stream of a host that sent sizes, as the resizer sees it: it logs what it is told, in order. Told that a size was
// accepted, it gives the scheduler every chance to run the resizer before it logs, which lets a resizer that made the size visible
// before it told its owner deliver it first.
type sizeOwner struct {
	mu    sync.Mutex
	log   []string
	taken chan uint32
}

func newSizeOwner() *sizeOwner { return &sizeOwner{taken: make(chan uint32, 1024)} }

func (o *sizeOwner) resizeAccepted(seq uint32, _ Size) {
	for range 20 {
		runtime.Gosched()
	}
	o.mu.Lock()
	o.log = append(o.log, fmt.Sprintf("accepted %d", seq))
	o.mu.Unlock()
}

func (o *sizeOwner) resizeTaken(seq uint32, _ Size) {
	o.mu.Lock()
	o.log = append(o.log, fmt.Sprintf("taken %d", seq))
	o.mu.Unlock()
	o.taken <- seq
}

func (o *sizeOwner) entries() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.log...)
}

// A size is accepted, as far as its owner is told, before the function can take it, and the owner is told when it was taken: for every
// size, in that order, and for no size of another owner. The same seq from two owners is two sizes, each told to its own owner only.
func TestAnOwnerIsToldThatASizeWasAcceptedBeforeTheFunctionCanTakeItAndThatItWasTakenAndNoOtherOwnerIs(t *testing.T) {
	r := newResizer(Size{}, nil, 0, nil)
	go r.run()
	taken := make(chan Size, 1)
	go func() {
		for size := range r.channel() {
			taken <- size
		}
	}()
	defer r.stop()
	a, b := newSizeOwner(), newSizeOwner()
	const rounds = 300
	for seq := uint32(1); seq <= rounds; seq++ {
		owner := a
		if seq%3 == 0 {
			owner = b // the same seq numbers are used by both: only the owner of the size is told
		}
		if !r.submit(seq, Size{Cols: uint16(seq), Rows: 24}, owner) {
			t.Fatalf("seq %d: the resizer refused a size", seq)
		}
		if got := <-taken; got.Cols != uint16(seq) {
			t.Fatalf("seq %d: the function took %v", seq, got)
		}
		select {
		case got := <-owner.taken:
			if got != seq {
				t.Fatalf("seq %d: the owner was told that %d was taken", seq, got)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("seq %d: the owner was never told that its size was taken", seq)
		}
	}
	for name, owner := range map[string]*sizeOwner{"a": a, "b": b} {
		entries := owner.entries()
		for i := 0; i+1 < len(entries); i += 2 {
			var seq uint32
			if _, err := fmt.Sscanf(entries[i], "accepted %d", &seq); err != nil || entries[i+1] != fmt.Sprintf("taken %d", seq) {
				t.Fatalf("owner %s: the log is %v around entry %d, want each size accepted and then taken, and nothing for the other owner's", name, entries, i)
			}
		}
		if len(entries)%2 != 0 {
			t.Fatalf("owner %s: the log %v has an entry without its partner", name, entries)
		}
	}
	if got := len(a.entries()) + len(b.entries()); got != 2*rounds {
		t.Fatalf("the owners were told %d things for %d sizes, want two each", got, rounds)
	}
}

// A stream that broke the protocol ends the session while it holds it, and also when it has already left it for want of anything else to
// do (it returns the status it earned, which can reach the host and end the stream before the session is ended): the session then lives
// for the lease that leaving began, and the violation ends it. It does not end a session that another stream holds, or that the stream
// detached from by its own Detach.
func TestAStreamThatBrokeTheProtocolEndsTheSessionItHoldsOrLeftOnlyBecauseItEndedAndNoOtherStreamHoldsIt(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		name string
		prep func(tr *testRing) *attachment // the stream that broke the protocol, in the state the case puts it in
		ends bool
	}{
		{"the stream holds the session", func(tr *testRing) *attachment { return tr.creator }, true},
		{"the stream was released and nobody holds the session", func(tr *testRing) *attachment {
			tr.release(tr.creator, time.Minute, now)
			return tr.creator
		}, true},
		{"the stream was released and another stream attached", func(tr *testRing) *attachment {
			old := tr.creator
			tr.release(old, time.Minute, now)
			if _, _, err := tr.attach(context.Background(), 0); err != nil {
				t.Fatal(err)
			}
			return old
		}, false},
		{"the stream was superseded", func(tr *testRing) *attachment {
			old := tr.creator
			if _, _, err := tr.attach(context.Background(), 0); err != nil {
				t.Fatal(err)
			}
			return old
		}, false},
		{"the stream detached", func(tr *testRing) *attachment {
			tr.detach(tr.creator, time.Minute, now)
			return tr.creator
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestRing(1<<20, false, false)
			offender := tc.prep(tr)
			tr.destroyFrom(offender)
			causes, kills := hostEnds(tr.host)
			if tc.ends {
				if len(causes) != 1 || causes[0] != session.CauseCancelled || kills[0] {
					t.Fatalf("the session was asked to end with %v (kills %v), want one graceful cancellation", causes, kills)
				}
				return
			}
			if len(causes) != 0 {
				t.Fatalf("the session was asked to end with %v: it is not this stream's to end", causes)
			}
		})
	}
}
