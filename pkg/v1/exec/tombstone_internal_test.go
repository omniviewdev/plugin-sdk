package exec

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"testing/synctest"
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The store of the tombstones, held as a data structure: no engine, no stream. Its clock is the test's, so what expires and what is
// dropped is decided by a time the test sets, and nothing by a wait.

// testTomb is the tombstone of a session that wrote retained bytes. Its ring is a stand-in: the store only asks whether there is one.
func testTomb(id string, retained int) *tombstone {
	return &tombstone{
		id: id, facts: &execv2.SessionFacts{SessionId: id}, ended: &execv2.Ended{LastOffset: uint64(retained)},
		ring: &ring{}, retained: retained,
	}
}

// storeHolds is what the store holds, read without sweeping what has expired: the ids, oldest first, the bytes it counts and whether
// its timer is armed.
func storeHolds(s *tombstones) (ids []string, bytes int, armed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.order {
		ids = append(ids, t.id)
	}
	return ids, s.bytes, s.timer != nil
}

// checkStore holds the store to its own invariants: every tombstone is in the map and in the order exactly once, the order is the
// order of the expiry, the bytes counted are the bytes the tombstones keep, and the bounds hold.
func checkStore(t *testing.T, s *tombstones, when string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byID) != len(s.order) {
		t.Fatalf("%s: %d ids and %d tombstones in the order", when, len(s.byID), len(s.order))
	}
	sum := 0
	for i, tomb := range s.order {
		if s.byID[tomb.id] != tomb {
			t.Fatalf("%s: tombstone %q is in the order and not in the map", when, tomb.id)
		}
		if i > 0 && tomb.expires.Before(s.order[i-1].expires) {
			t.Fatalf("%s: the order is not the order of the expiry at %d", when, i)
		}
		if tomb.ring == nil && tomb.retained != 0 {
			t.Fatalf("%s: tombstone %q has lost its ring and still keeps %d bytes", when, tomb.id, tomb.retained)
		}
		sum += tomb.retained
	}
	if sum != s.bytes {
		t.Fatalf("%s: the store counts %d bytes, the tombstones keep %d", when, s.bytes, sum)
	}
	if len(s.order) > s.maxCount || s.bytes > s.maxBytes {
		t.Fatalf("%s: %d tombstones and %d bytes, the bounds are %d and %d", when, len(s.order), s.bytes, s.maxCount, s.maxBytes)
	}
}

// The count is bounded, oldest first: with the default of 256, the 44 oldest of 300 tombstones are gone with their ids, and the 256
// newest are held in the order they were made.
func TestTheStoreKeeps256TombstonesAndDropsTheOldestFirstWithTheirIds(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newTombstones(defaultTombstoneTTL, defaultMaxTombstones, defaultTombstoneBytes, func() time.Time { return now }, nil)
	for i := range 300 {
		now = now.Add(time.Millisecond)
		s.put(testTomb(fmt.Sprintf("s%03d", i), 10))
		checkStore(t, s, fmt.Sprintf("after %d", i))
	}
	ids, bytes, _ := storeHolds(s)
	if len(ids) != 256 || ids[0] != "s044" || ids[255] != "s299" || bytes != 2560 {
		t.Fatalf("the store holds %d tombstones from %v to %v and %d bytes, want 256, s044 to s299, 2560 bytes", len(ids), ids[0], ids[len(ids)-1], bytes)
	}
	for i := range 300 {
		id := fmt.Sprintf("s%03d", i)
		if has := s.has(id); has != (i >= 44) {
			t.Errorf("%s is held: %v, want %v: the oldest go first, and with them their ids", id, has, i >= 44)
		}
	}
	if len(s.list()) != 256 || s.len() != 256 {
		t.Errorf("list has %d tombstones and len %d, want 256", len(s.list()), s.len())
	}
}

// The output is bounded in all, oldest first: 40 tombstones that each keep 1 MiB hold 32 MiB, which is the 32 newest; the 8 oldest keep
// their end and their id and lose their bytes, and what is released is counted once.
func TestTheStoreKeeps32MiBOfOutputAndTheOldestLoseTheirBytesAndKeepTheirEndAndId(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newTombstones(defaultTombstoneTTL, defaultMaxTombstones, defaultTombstoneBytes, func() time.Time { return now }, nil)
	const mib = 1 << 20
	for i := range 40 {
		now = now.Add(time.Millisecond)
		s.put(testTomb(fmt.Sprintf("s%02d", i), mib))
		checkStore(t, s, fmt.Sprintf("after %d", i))
	}
	if ids, bytes, _ := storeHolds(s); len(ids) != 40 || bytes != 32*mib {
		t.Fatalf("the store holds %d tombstones and %d bytes, want all 40 and 32 MiB", len(ids), bytes)
	}
	for i := range 40 {
		got, ok := s.get(fmt.Sprintf("s%02d", i))
		if !ok {
			t.Fatalf("s%02d is gone: the budget takes the bytes of a tombstone and leaves it", i)
		}
		kept := got.ring != nil
		if kept != (i >= 8) || (kept && got.retained != mib) || (!kept && (got.retained != 0 || got.replayStart != got.ended.GetLastOffset())) {
			t.Errorf("s%02d keeps %d bytes (ring %v, front %d), want the 32 newest to keep 1 MiB and the 8 oldest nothing, their front at the end of their output", i, got.retained, kept, got.replayStart)
		}
		if got.ended == nil {
			t.Errorf("s%02d lost its end with its bytes", i)
		}
	}
	// A tombstone that lost its output and then expires does not take the bytes of the others with it.
	now = now.Add(defaultTombstoneTTL - 30*time.Millisecond)
	if ids, bytes, _ := storeHolds(s); len(ids) != 40 || bytes != 32*mib {
		t.Fatalf("before the oldest expire: %d tombstones and %d bytes", len(ids), bytes)
	}
	s.sweepAt(now)
	checkStore(t, s, "after the oldest expired")
	if ids, bytes, _ := storeHolds(s); len(ids) != 30 || bytes != 30*mib {
		t.Errorf("the store holds %d tombstones and %d bytes after the 10 oldest expired, want 30 and 30 MiB: the 8 that had lost their bytes took none with them, and the 2 that had not took theirs", len(ids), bytes)
	}
}

// sweepAt is a use of the store at a time: what a get, a list or a put does first.
func (s *tombstones) sweepAt(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
}

// A tombstone that alone is larger than the budget keeps no bytes, and does not take the budget of the others with it; and the two
// bounds together, count and bytes, leave the store inside both whatever the order the tombstones come in.
func TestTheStoreStaysInsideBothBoundsWhateverComesInAndInWhatOrder(t *testing.T) {
	for seed := uint64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 99))
			now := time.Unix(1_000_000, 0)
			s := newTombstones(time.Minute, 8, 1000, func() time.Time { return now }, nil)
			live := map[string]bool{}
			for i := range 600 {
				switch rng.IntN(4) {
				case 0, 1:
					id := fmt.Sprintf("id%d", rng.IntN(40))
					if s.has(id) { // an id is made once while its tombstone lives
						break
					}
					s.put(testTomb(id, []int{0, 1, 100, 400, 999, 1000, 1001, 5000}[rng.IntN(8)]))
				case 2:
					now = now.Add(time.Duration(rng.IntN(25)) * time.Second)
				default:
					_, _ = s.get(fmt.Sprintf("id%d", rng.IntN(40)))
				}
				checkStore(t, s, fmt.Sprintf("step %d", i))
				for _, tomb := range s.list() {
					if !tomb.expires.After(now) {
						t.Fatalf("step %d: %q is listed at %v, it expired at %v", i, tomb.id, now, tomb.expires)
					}
					live[tomb.id] = true
				}
			}
			if len(live) == 0 {
				t.Fatal("the run held no tombstone")
			}
		})
	}
}

// A tombstone expires exactly at its time to live: one nanosecond before, the id is held and the output readable; at it, gone.
func TestATombstoneExpiresExactlyAtItsTimeToLive(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s := newTombstones(time.Minute, 8, 1<<20, func() time.Time { return now }, nil)
	s.put(testTomb("a", 10))
	now = now.Add(time.Minute - time.Nanosecond)
	if !s.has("a") {
		t.Fatal("the tombstone was gone one nanosecond before its time to live")
	}
	now = now.Add(time.Nanosecond)
	if s.has("a") {
		t.Fatal("the tombstone was still held at its time to live")
	}
	if ids, bytes, _ := storeHolds(s); len(ids) != 0 || bytes != 0 {
		t.Fatalf("the store holds %v and %d bytes after the last tombstone expired", ids, bytes)
	}
}

// The store's timer releases the memory of what expired when nobody uses the store, takes the next expiry when there is one, and rests
// when there is none. The clock and the timers are the bubble's: a minute passes when every goroutine waits.
func TestTheStoresTimerReleasesWhatExpiredWithoutAnyUseAndRestsWhenTheStoreIsEmpty(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTombstones(time.Minute, 256, 32<<20, time.Now, func() bool { return true })
		s.put(testTomb("a", 100))
		time.Sleep(20 * time.Second)
		s.put(testTomb("b", 200))
		time.Sleep(20 * time.Second)
		s.put(testTomb("c", 300))
		if ids, bytes, armed := storeHolds(s); len(ids) != 3 || bytes != 600 || !armed {
			t.Fatalf("the store holds %v, %d bytes, timer armed %v, want three tombstones, 600 bytes and a timer", ids, bytes, armed)
		}
		for _, step := range []struct {
			sleep time.Duration
			want  []string
			bytes int
			armed bool
		}{
			{20*time.Second - time.Millisecond, []string{"a", "b", "c"}, 600, true}, // 59.999 s after a: nothing yet
			{time.Millisecond, []string{"b", "c"}, 500, true},                       // a expired: the timer took it and armed the next
			{20 * time.Second, []string{"c"}, 300, true},
			{20 * time.Second, nil, 0, false}, // the last one: the timer rests
		} {
			time.Sleep(step.sleep)
			synctest.Wait()
			ids, bytes, armed := storeHolds(s) // not through a use: a use would sweep, and the timer would not be what is shown
			if fmt.Sprint(ids) != fmt.Sprint(step.want) || bytes != step.bytes || armed != step.armed {
				t.Fatalf("after %v more: the store holds %v, %d bytes, timer armed %v; want %v, %d bytes, armed %v", step.sleep, ids, bytes, armed, step.want, step.bytes, step.armed)
			}
		}
	})
}

// Closing the store stops its timer and arms none: what it holds stays readable until it expires, and the use that finds it expired
// takes it. No goroutine of the store is left: the bubble fails the test that leaves one.
func TestClosingTheStoreStopsItsTimerAndArmsNoneAndWhatItHoldsStaysReadableUntilItExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTombstones(time.Minute, 256, 32<<20, time.Now, func() bool { return true })
		s.put(testTomb("a", 100))
		if _, _, armed := storeHolds(s); !armed {
			t.Fatal("the store did not arm its timer for a tombstone")
		}
		s.close()
		s.put(testTomb("b", 100))
		if ids, _, armed := storeHolds(s); len(ids) != 2 || armed {
			t.Fatalf("a closed store holds %v with its timer armed %v, want both tombstones and no timer", ids, armed)
		}
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if ids, _, armed := storeHolds(s); len(ids) != 2 || armed {
			t.Fatalf("a closed store's tombstones were taken by a timer: %v (armed %v)", ids, armed)
		}
		if s.has("a") || s.has("b") {
			t.Fatal("a closed store held a tombstone past its time to live")
		}
		if ids, bytes, _ := storeHolds(s); len(ids) != 0 || bytes != 0 {
			t.Fatalf("the use that found the tombstones expired left %v and %d bytes", ids, bytes)
		}
		s.close() // it may be called again
	})
}
