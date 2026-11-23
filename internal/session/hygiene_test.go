package session

import (
	"context"
	"fmt"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
)

// A registry owns every goroutine of its entries. A thousand entries admitted and ended, in each
// of the ways an entry can have goroutines, leave the goroutine count where it was.

func TestAThousandAdmitAndEndCyclesLeaveNoGoroutine(t *testing.T) {
	const cycles = 1000
	for _, tc := range []struct {
		name string
		work func(l *lane, e *Entry[*state], cycle int)
	}{
		{"plain", func(*lane, *Entry[*state], int) {}},
		{"with producers", func(l *lane, e *Entry[*state], cycle int) {
			st := e.Value()
			for range 1 + cycle%3 {
				e.Go(func() {
					st.active.Add(1)
					defer st.active.Add(-1)
					<-e.Context().Done()
				})
			}
		}},
		{"with a watcher", func(l *lane, e *Entry[*state], cycle int) {
			watched, cancel := context.WithCancel(context.Background())
			e.Watch(watched, CauseExpired)
			if cycle%2 == 0 {
				cancel() // the watcher ends the entry
			} else {
				context.AfterFunc(e.Context(), cancel) // the entry ends first; the watcher must follow it
			}
		}},
		{"with a cancellable parent", nil}, // admitted differently, below
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLane(t)
			baseline := lifecycletest.TakeBaseline()
			for cycle := range cycles {
				id := fmt.Sprintf("a%d", cycle)
				var e *Entry[*state]
				if tc.work == nil {
					parent, cancel := context.WithCancel(context.Background())
					var err error
					if e, err = l.admitWith(parent, Spec{ID: id}); err != nil {
						t.Fatal(err)
					}
					if cycle%2 == 0 {
						cancel() // the parent ends the entry
					} else {
						context.AfterFunc(e.Context(), cancel)
					}
				} else {
					e = l.admit(id)
					tc.work(l, e, cycle)
				}
				e.End(CauseRequested) // a no-op when the context already ended it
				l.awaitEnd(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), mustHappen)
			defer cancel()
			if err := l.reg.Close(ctx); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if n := l.reg.Len(); n != 0 {
				t.Fatalf("%d entries left after Close", n)
			}
			if err := baseline.Check(mustHappen); err != nil {
				t.Fatalf("goroutines left behind by %d cycles: %v", cycles, err)
			}
		})
	}
}
