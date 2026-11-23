package session

import (
	"context"
	"errors"
	"testing"
)

// The end function is the one place an end is emitted. These tests are about when it runs and
// what it may do; that it runs once and after the last producer is checked, for every entry of
// every test, by the lane's own end function (see lane.end and lane.cleanup).

// The end function may use the registry: it runs outside the registry's lock, with the entry
// still registered and its id still reserved.
func TestTheEndFunctionMayUseTheRegistryAndFindsItsEntryStillThere(t *testing.T) {
	l := newLane(t)
	type observed struct {
		err      error
		infos    []Info
		length   int
		hasCause bool
	}
	seen := make(chan observed, 1)
	l.endHook = func(e *Entry[*state]) {
		_, err := l.admitWith(context.Background(), Spec{ID: e.ID()})
		seen <- observed{err: err, infos: l.reg.Snapshot(), length: l.reg.Len(), hasCause: e.Cause() != CauseNone}
	}
	e := l.admit("a")
	e.End(CauseFinished)
	got := awaitValue(t, seen, "the end function")
	if !errors.Is(got.err, ErrExists) {
		t.Fatalf("Admit of the entry's id from its end function: %v, want %v", got.err, ErrExists)
	}
	if len(got.infos) != 1 || got.infos[0].ID != "a" || got.infos[0].Cause != CauseFinished || got.length != 1 {
		t.Fatalf("the end function saw Snapshot %v and Len %d, want the entry with cause %v", got.infos, got.length, CauseFinished)
	}
	if !got.hasCause {
		t.Fatal("the end function ran with no cause recorded")
	}
}

// The end function that panics: the entry is still removed and the registry's accounting still
// released, and the panic goes on, unchanged, to whoever runs the finisher. The seam makes that
// a recover wrapper, because the panic of another goroutine cannot be recovered from outside it.
func TestAPanicInTheEndFunctionStillRemovesTheEntryAndReleasesTheRegistry(t *testing.T) {
	const boom = "the end function panicked"
	r := New(Config[struct{}]{OnEnd: func(*Entry[struct{}]) { panic(boom) }})
	panicked := make(chan any, 2)
	r.seams.spawn = func(run func()) {
		go func() {
			defer func() { panicked <- recover() }()
			run()
		}()
	}
	spec := Spec{ID: "a", Group: "g", Limit: 1}
	e, err := r.Admit(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.End(CauseRequested)
	if got := awaitValue(t, panicked, "the finisher"); got != boom {
		t.Fatalf("the finisher ended with %v, want the end function's panic %q unchanged", got, boom)
	}

	// By the time the panic reached the wrapper the entry was removed: the id, the slot and the
	// registry's count of live entries are free.
	if n := r.Len(); n != 0 {
		t.Fatalf("the registry holds %d entries after the end function panicked", n)
	}
	e2, err := r.Admit(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("Admit of the same id and group after the end function panicked: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), mustHappen)
	defer cancel()
	if err := r.Close(ctx); err != nil { // would wait for ever if the first entry's count were never released
		t.Fatalf("Close after the end function panicked: %v", err)
	}
	if got := awaitValue(t, panicked, "the second finisher"); got != boom {
		t.Fatalf("the second finisher ended with %v, want %q", got, boom)
	}
	if e2.Cause() != CauseShutdown {
		t.Fatalf("cause %v, want %v", e2.Cause(), CauseShutdown)
	}
}

// The finisher of an entry is the first End's, and there is none before: an entry that no one
// ended has no goroutine waiting for it, and costs the registry nothing while it runs.
func TestNoFinisherStartsUntilTheFirstEnd(t *testing.T) {
	var spawned int
	r := New(Config[struct{}]{})
	r.seams.spawn = func(run func()) {
		spawned++ // only ever called under the first End of an entry
		go run()
	}
	var entries []*Entry[struct{}]
	for i := range 50 {
		e, err := r.Admit(context.Background(), Spec{ID: string(rune('A' + i))}, nil)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	if spawned != 0 {
		t.Fatalf("%d finishers started for entries that no one ended", spawned)
	}
	for _, e := range entries {
		e.End(CauseFinished)
		e.End(CauseFinished) // a second End starts no second finisher
	}
	if spawned != len(entries) {
		t.Fatalf("%d finishers started for %d entries ended twice, want one each", spawned, len(entries))
	}
	ctx, cancel := context.WithTimeout(context.Background(), mustHappen)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if spawned != len(entries) {
		t.Fatalf("Close started %d more finishers for entries that were already ending", spawned-len(entries))
	}
}
