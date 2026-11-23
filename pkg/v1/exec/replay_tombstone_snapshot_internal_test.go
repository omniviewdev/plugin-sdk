package exec

import (
	"fmt"
	"testing"
	"time"
)

// What the store of the tombstones hands out is read without its lock: ListSessions builds the entry of each tombstone it was given,
// and ReadOutput reads the ring of the one it was given, after the store has let go of them. The store releases the output of its
// oldest tombstones whenever a new one makes the budget run over, which is the one change a tombstone goes through after it is made,
// so a tombstone that was handed out must not be the one that the release changes: a listing that is read while a session ends is a
// data race, and an entry that says the ring is gone when the ring was there when it was listed is not a listing at one instant.

// A tombstone that was listed, and one that was read, are what they were when the store gave them out, whatever the store does to its
// own after that: the output that the next tombstone's budget takes is the store's to take, and the entry of the one that was listed
// still says where its output began.
func TestATombstoneThatWasHandedOutKeepsWhatItWasWhenItsOutputIsReleased(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newTombstones(defaultTombstoneTTL, defaultMaxTombstones, 150, func() time.Time { return now }, nil)
	first := testTomb("a", 100)
	first.replayStart = 7
	s.put(first)
	listed := s.list()
	read, ok := s.get("a")
	if !ok || len(listed) != 1 {
		t.Fatalf("the store lists %d tombstones and holds a: %v", len(listed), ok)
	}

	// The budget is 150 bytes and a keeps 100: the next tombstone's 100 spend it, and the output of the oldest is released.
	now = now.Add(time.Millisecond)
	s.put(testTomb("b", 100))
	if held, _ := s.get("a"); held.ring != nil || held.retained != 0 || held.replayStart != 100 {
		t.Fatalf("the store still keeps the output of a after the budget was spent: ring %v, %d bytes, front %d, want none, none, 100", held.ring != nil, held.retained, held.replayStart)
	}
	checkStore(t, s, "after the output of a was released")

	for what, tomb := range map[string]*tombstone{"listed": listed[0], "read": read} {
		if tomb.ring == nil || tomb.retained != 100 || tomb.replayStart != 7 {
			t.Errorf("the tombstone that was %s keeps ring %v, %d bytes and its front at %d once the store released the output, want the ring, 100 bytes and 7: it was changed under whoever holds it",
				what, tomb.ring != nil, tomb.retained, tomb.replayStart)
		}
		if got := tomb.info().GetReplayStart(); got != 7 {
			t.Errorf("the entry of the tombstone that was %s says its output begins at %d, want 7", what, got)
		}
	}
}

// Tombstones are listed and read while others end, and each end releases the output of the oldest (the budget is two tombstones' worth
// and every tombstone keeps one): what the readers see is read without the store's lock, and the race detector decides whether the
// release changes it under them. At the end the store holds what it has to.
func TestTheStoreIsListedAndReadWhileTheOutputOfItsTombstonesIsReleased(t *testing.T) {
	const writes, readers = 3000, 3
	s := newTombstones(time.Hour, 64, 250, time.Now, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range writes {
			s.put(testTomb(fmt.Sprintf("t%d", i), 100))
		}
	}()
	joined := make(chan struct{}, readers)
	for range readers {
		go func() {
			defer func() { joined <- struct{}{} }()
			for {
				select {
				case <-done:
					return
				default:
				}
				for _, tomb := range s.list() {
					_ = tomb.info()
					if got, ok := s.get(tomb.id); ok {
						_, _, _ = got.ring, got.retained, got.replayStart
					}
				}
			}
		}()
	}
	<-done
	for range readers {
		<-joined
	}
	checkStore(t, s, "after the writes")
}
