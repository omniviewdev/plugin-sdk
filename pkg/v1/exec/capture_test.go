package exec

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"
	"unicode/utf8"
)

// The capture keeps at most a cap of one stream of a run, as a head, a tail or both, and says exactly what it did not keep.
// Its oracle is a reference that keeps every byte and cuts the window at the end: the obvious implementation, which a test
// can believe.

// refEvent is one thing that happened to a stream: bytes arrived, or a range was lost before it could.
type refEvent struct {
	data []byte
	lost uint64
}

// refCaps are the caps of the head and of the tail of a window of limit bytes under a policy.
func refCaps(limit int, policy truncatePolicy) (headCap, tailCap int) {
	switch policy {
	case truncateHead:
		return limit, 0
	case truncateTail:
		return 0, limit
	}
	return (limit + 1) / 2, limit / 2
}

// refWindow is what the events leave in a window of limit bytes: the kept head, the kept tail and the total. The head is a
// prefix of the bytes that arrived and the tail a suffix, and a lost range ends the head where it falls and discards what the
// tail held before it, because the head and the tail are each one unbroken part of the stream.
func refWindow(events []refEvent, limit int, policy truncatePolicy) (head, tail []byte, total uint64) {
	headCap, tailCap := refCaps(limit, policy)
	var arrived []byte
	firstGap, lastGap := -1, 0
	for _, e := range events {
		if e.lost > 0 {
			total += e.lost
			if firstGap < 0 {
				firstGap = len(arrived)
			}
			lastGap = len(arrived)
			continue
		}
		arrived = append(arrived, e.data...)
	}
	total += uint64(len(arrived))
	h := min(headCap, len(arrived))
	if firstGap >= 0 {
		h = min(h, firstGap)
	}
	from := max(h, lastGap, len(arrived)-tailCap)
	return arrived[:h], arrived[from:], total
}

// retainedBytes is the memory a capture holds for its window: the storage of its head and of its tail.
func retainedBytes(c *capture) int { return cap(c.head) + len(c.ring) }

// checkCapture compares a capture with the reference after the events.
func checkCapture(t *testing.T, label string, c *capture, events []refEvent, limit int, policy truncatePolicy) {
	t.Helper()
	wantHead, wantTail, wantTotal := refWindow(events, limit, policy)
	want := slices.Concat(wantHead, wantTail)
	got, account := c.Result()
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: kept %q, want %q (cap %d, policy %d)", label, got, want, limit, policy)
	}
	wantKept := uint64(len(want))
	if account.Total != wantTotal || account.Kept != wantKept || account.Dropped != wantTotal-wantKept {
		t.Fatalf("%s: total %d kept %d dropped %d, want %d %d %d (cap %d, policy %d)",
			label, account.Total, account.Kept, account.Dropped, wantTotal, wantKept, wantTotal-wantKept, limit, policy)
	}
	if account.Kept+account.Dropped != account.Total {
		t.Fatalf("%s: kept %d + dropped %d != total %d", label, account.Kept, account.Dropped, account.Total)
	}
	if account.Truncated() != (account.Dropped > 0) {
		t.Fatalf("%s: truncated = %v with %d dropped", label, account.Truncated(), account.Dropped)
	}
	if account.Head != len(wantHead) || account.Tail != len(wantTail) {
		t.Fatalf("%s: head %d tail %d, want %d %d (cap %d, policy %d)", label, account.Head, account.Tail, len(wantHead), len(wantTail), limit, policy)
	}
	if account.ValidUTF8 != utf8.Valid(want) {
		t.Fatalf("%s: valid_utf8 = %v for %q, utf8.Valid says %v", label, account.ValidUTF8, want, utf8.Valid(want))
	}
}

// randomText returns n bytes that are mostly text: ASCII, and two-, three- and four-byte sequences, and now and then a byte that
// is no UTF-8, so that a window cut anywhere is valid or not by chance, and both happen often. The last sequence may be cut.
func randomText(rng *rand.Rand, n int) []byte {
	pieces := [][]byte{[]byte("a"), []byte("b"), []byte("é"), []byte("€"), []byte("😀"), {0x80}, {0xC0}, {0xFF}, {0xE2, 0x82}}
	out := make([]byte, 0, n+4)
	for len(out) < n {
		switch r := rng.IntN(20); {
		case r < 11:
			out = append(out, pieces[rng.IntN(2)]...)
		case r < 18:
			out = append(out, pieces[2+rng.IntN(3)]...)
		default:
			out = append(out, pieces[5+rng.IntN(4)]...)
		}
	}
	return out[:n]
}

// randomCap picks a cap: the small ones where every boundary is close, odd ones and a few larger.
func randomCap(rng *rand.Rand) int {
	small := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 16, 17, 31, 32, 33}
	if rng.IntN(4) == 0 {
		return 1 + rng.IntN(600)
	}
	return small[rng.IntN(len(small))]
}

// randomEvents is a stream for a cap: a length around the cap or far beyond it, written in chunks of every size (empty ones
// included) with lost ranges between them now and then.
func randomEvents(rng *rand.Rand, limit int, withGaps bool) []refEvent {
	lengths := []int{0, 1, limit - 1, limit, limit + 1, 2 * limit, 3*limit + 1, 7 * limit, rng.IntN(8*limit + 3)}
	length := max(lengths[rng.IntN(len(lengths))], 0)
	var events []refEvent
	for length > 0 || rng.IntN(6) == 0 {
		if withGaps && rng.IntN(12) == 0 {
			events = append(events, refEvent{lost: uint64(1 + rng.IntN(limit+3))})
		}
		var n int
		switch r := rng.IntN(10); {
		case r == 0:
			n = 0
		case r < 3:
			n = 1
		case r < 9:
			n = 2 + rng.IntN(limit+2)
		default:
			n = 1 + rng.IntN(3*limit+7)
		}
		n = min(n, length)
		events = append(events, refEvent{data: randomText(rng, n)})
		length -= n
		if length == 0 && rng.IntN(2) == 0 {
			break
		}
	}
	return events
}

func TestCaptureMatchesAReferenceModelForRandomChunksCapsAndPolicies(t *testing.T) {
	for seed := uint64(1); seed <= 4000; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed*7919))
		limit := randomCap(rng)
		policy := truncatePolicy(rng.IntN(3))
		events := randomEvents(rng, limit, false)
		c := newCapture(limit, policy)
		for i, e := range events {
			c.Write(e.data)
			if got := retainedBytes(c); got > limit {
				t.Fatalf("seed %d: the capture holds %d bytes of storage for a cap of %d", seed, got, limit)
			}
			checkCapture(t, fmt.Sprintf("seed %d after write %d", seed, i), c, events[:i+1], limit, policy)
		}
		checkCapture(t, fmt.Sprintf("seed %d at the end", seed), c, events, limit, policy)
	}
}

// A lost range is a range the engine's queue overwrote before the SDK read it. It counts as dropped, it ends the head where it
// falls and it discards what the tail held, so that what is kept is one unbroken start and one unbroken end of the stream.
func TestCaptureMatchesAReferenceModelWithLostRanges(t *testing.T) {
	for seed := uint64(1); seed <= 4000; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed*104729))
		limit := randomCap(rng)
		policy := truncatePolicy(rng.IntN(3))
		events := randomEvents(rng, limit, true)
		c := newCapture(limit, policy)
		for i, e := range events {
			if e.lost > 0 {
				c.Lose(e.lost)
			} else {
				c.Write(e.data)
			}
			if got := retainedBytes(c); got > limit {
				t.Fatalf("seed %d: the capture holds %d bytes of storage for a cap of %d", seed, got, limit)
			}
			checkCapture(t, fmt.Sprintf("seed %d after event %d", seed, i), c, events[:i+1], limit, policy)
		}
	}
}

func TestCaptureSplitsTheCapIntoACeilingHeadAndAFloorTail(t *testing.T) {
	for _, tc := range []struct {
		limit            int
		head, tail       int
		wantOf           string
		wantHeadTailText string
	}{
		{limit: 1, head: 1, tail: 0, wantOf: "abcdefgh", wantHeadTailText: "a"},
		{limit: 2, head: 1, tail: 1, wantOf: "abcdefgh", wantHeadTailText: "ah"},
		{limit: 3, head: 2, tail: 1, wantOf: "abcdefgh", wantHeadTailText: "abh"},
		{limit: 4, head: 2, tail: 2, wantOf: "abcdefgh", wantHeadTailText: "abgh"},
		{limit: 5, head: 3, tail: 2, wantOf: "abcdefgh", wantHeadTailText: "abcgh"},
		{limit: 7, head: 4, tail: 3, wantOf: "abcdefgh", wantHeadTailText: "abcdfgh"},
	} {
		t.Run(fmt.Sprintf("cap %d", tc.limit), func(t *testing.T) {
			c := newCapture(tc.limit, truncateHeadTail)
			c.Write([]byte(tc.wantOf))
			got, account := c.Result()
			if string(got) != tc.wantHeadTailText {
				t.Errorf("kept %q, want %q", got, tc.wantHeadTailText)
			}
			if account.Head != tc.head || account.Tail != tc.tail {
				t.Errorf("head %d tail %d, want %d %d", account.Head, account.Tail, tc.head, tc.tail)
			}
			if account.Total != 8 || account.Kept != uint64(tc.limit) || account.Dropped != uint64(8-tc.limit) || !account.Truncated() {
				t.Errorf("account %+v for 8 bytes in a cap of %d", account, tc.limit)
			}
		})
	}
}

func TestCaptureHeadKeepsTheBeginningAndTailKeepsTheEnd(t *testing.T) {
	head := newCapture(3, truncateHead)
	head.Write([]byte("abcdef"))
	got, account := head.Result()
	if string(got) != "abc" || account.Head != 3 || account.Tail != 0 || account.Dropped != 3 {
		t.Errorf("HEAD kept %q with head %d tail %d dropped %d, want abc 3 0 3", got, account.Head, account.Tail, account.Dropped)
	}
	tail := newCapture(3, truncateTail)
	tail.Write([]byte("abcdef"))
	got, account = tail.Result()
	if string(got) != "def" || account.Head != 0 || account.Tail != 3 || account.Dropped != 3 {
		t.Errorf("TAIL kept %q with head %d tail %d dropped %d, want def 0 3 3", got, account.Head, account.Tail, account.Dropped)
	}
}

// Output that fits is kept whole and in order, whatever the policy and however it is cut into writes, and nothing is dropped.
func TestCaptureKeepsOutputThatFitsWholeUnderEveryPolicy(t *testing.T) {
	text := []byte("hello, wörld: 42")
	for _, policy := range []truncatePolicy{truncateHeadTail, truncateHead, truncateTail} {
		for _, chunk := range []int{1, 2, 3, 5, len(text)} {
			c := newCapture(len(text), policy)
			for rest := text; len(rest) > 0; rest = rest[min(chunk, len(rest)):] {
				c.Write(rest[:min(chunk, len(rest))])
			}
			got, account := c.Result()
			if !bytes.Equal(got, text) || account.Dropped != 0 || account.Truncated() || account.Total != uint64(len(text)) {
				t.Errorf("policy %d chunk %d: kept %q account %+v, want the whole text and nothing dropped", policy, chunk, got, account)
			}
		}
	}
}

func TestCaptureTakesZeroLengthWritesForNothing(t *testing.T) {
	c := newCapture(8, truncateHeadTail)
	c.Write(nil)
	c.Write([]byte{})
	got, account := c.Result()
	if len(got) != 0 || account != (outputAccount{ValidUTF8: true}) {
		t.Errorf("empty capture: bytes %q account %+v, want nothing, all zero and valid UTF-8 (empty text is valid)", got, account)
	}
	c.Write([]byte("ab"))
	c.Write(nil)
	c.Write([]byte("c"))
	got, account = c.Result()
	if string(got) != "abc" || account.Total != 3 {
		t.Errorf("empty writes between others: kept %q total %d, want abc 3", got, account.Total)
	}
}

// A multi-byte sequence that the cap cuts is not repaired: valid_utf8 is about the bytes as returned, so a window that ends or
// begins inside a sequence, or joins two pieces of a stream, is not valid just because the stream was.
func TestCaptureValidUTF8IsAboutTheBytesAsReturned(t *testing.T) {
	euro := "€" // E2 82 AC
	for _, tc := range []struct {
		name   string
		limit  int
		policy truncatePolicy
		input  string
		want   bool
		kept   string
	}{
		{"head cut inside a sequence", 4, truncateHead, "abc" + euro, false, "abc\xE2"},
		{"head ends on a boundary", 6, truncateHead, "abc" + euro + "def", true, "abc" + euro},
		{"tail begins inside a sequence", 4, truncateTail, euro + "abc", false, "\xAC" + "abc"},
		{"tail begins on a boundary", 3, truncateTail, euro + euro, true, euro},
		{"head and tail join a cut sequence", 4, truncateHeadTail, "a" + euro + "bcd", false, "a\xE2cd"},
		{"head and tail are both whole", 6, truncateHeadTail, euro + "xxxx" + euro, true, euro + euro},
		{"output within the cap is valid", 16, truncateHeadTail, "a" + euro + "b", true, "a" + euro + "b"},
		{"invalid bytes within the cap are not valid", 16, truncateHeadTail, "ab\xFFcd", false, "ab\xFFcd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCapture(tc.limit, tc.policy)
			c.Write([]byte(tc.input))
			got, account := c.Result()
			if string(got) != tc.kept {
				t.Fatalf("kept %q, want %q", got, tc.kept)
			}
			if account.ValidUTF8 != tc.want {
				t.Errorf("valid_utf8 = %v for the kept bytes %q, want %v", account.ValidUTF8, got, tc.want)
			}
		})
	}
}

// A lost range ends the head where it falls and discards what the tail held, so that the head is the start of the stream and
// the tail its end, each in one piece.
func TestCaptureLostRangesCountAsDroppedAndBreakTheHeadAndTheTail(t *testing.T) {
	t.Run("the head is full when the range falls", func(t *testing.T) {
		c := newCapture(4, truncateHeadTail) // head 2, tail 2
		c.Write([]byte("ab"))
		c.Lose(3)
		c.Write([]byte("cd"))
		got, account := c.Result()
		if string(got) != "abcd" || account.Head != 2 || account.Tail != 2 || account.Total != 7 || account.Dropped != 3 {
			t.Errorf("kept %q account %+v, want abcd with head 2 tail 2, 7 total and 3 dropped", got, account)
		}
	})
	t.Run("the head is not full when the range falls: it ends there", func(t *testing.T) {
		c := newCapture(4, truncateHeadTail) // head 2, tail 2
		c.Write([]byte("a"))
		c.Lose(2)
		c.Write([]byte("bcdef"))
		got, account := c.Result()
		if string(got) != "aef" || account.Head != 1 || account.Tail != 2 || account.Total != 8 || account.Dropped != 5 {
			t.Errorf("kept %q account %+v, want aef with head 1 tail 2, 8 total and 5 dropped", got, account)
		}
	})
	t.Run("the tail held bytes that the range separates from the end", func(t *testing.T) {
		c := newCapture(4, truncateHeadTail)
		c.Write([]byte("abcdef")) // head ab, tail ef
		c.Lose(1)
		c.Write([]byte("g"))
		got, account := c.Result()
		if string(got) != "abg" || account.Head != 2 || account.Tail != 1 || account.Total != 8 || account.Dropped != 5 {
			t.Errorf("kept %q account %+v, want abg with head 2 tail 1, 8 total and 5 dropped", got, account)
		}
	})
	t.Run("a range of nothing is nothing", func(t *testing.T) {
		c := newCapture(4, truncateHeadTail)
		c.Write([]byte("abcdef"))
		c.Lose(0)
		got, account := c.Result()
		if string(got) != "abef" || account.Total != 6 {
			t.Errorf("kept %q total %d, want abef 6", got, account.Total)
		}
	})
	t.Run("a range that is all there is", func(t *testing.T) {
		c := newCapture(4, truncateHeadTail)
		c.Lose(10)
		got, account := c.Result()
		if len(got) != 0 || account.Total != 10 || account.Dropped != 10 || !account.Truncated() {
			t.Errorf("kept %q account %+v, want nothing kept of 10 dropped", got, account)
		}
	})
}

// The bytes Result returns are the caller's: changing them changes nothing the capture holds, and asking again gives the same.
func TestCaptureResultIsAFreshSliceAndDoesNotDisturbTheCapture(t *testing.T) {
	c := newCapture(6, truncateHeadTail)
	c.Write([]byte("abcdefghij"))
	first, firstAccount := c.Result()
	for i := range first {
		first[i] = 'X'
	}
	second, secondAccount := c.Result()
	if string(second) != "abchij" || secondAccount != firstAccount {
		t.Errorf("after the caller changed its copy, the capture gave %q %+v, want abchij %+v", second, secondAccount, firstAccount)
	}
	c.Write([]byte("k"))
	third, _ := c.Result()
	if string(third) != "abcijk" {
		t.Errorf("after one more byte: %q, want abcijk", third)
	}
}

// Memory grows with the data up to the cap and never past it: a run that wrote ten bytes holds ten bytes, and one that wrote
// gigabytes holds the cap.
func TestCaptureMemoryGrowsWithTheDataAndNeverPastTheCap(t *testing.T) {
	t.Run("ten bytes hold about ten bytes", func(t *testing.T) {
		c := newCapture(256<<10, truncateHeadTail)
		c.Write([]byte("0123456789"))
		if got := retainedBytes(c); got > 32 {
			t.Errorf("a capture of 10 bytes holds %d bytes of storage, want about 10", got)
		}
		got, account := c.Result()
		if string(got) != "0123456789" || account.Head != 10 {
			t.Errorf("kept %q head %d", got, account.Head)
		}
	})
	t.Run("storage follows the data", func(t *testing.T) {
		for _, policy := range []truncatePolicy{truncateHeadTail, truncateHead, truncateTail} {
			limit := 64 << 10
			c := newCapture(limit, policy)
			var kept int
			for written := 0; written < 3*limit; {
				n := 1 + written%97
				c.Write(make([]byte, n))
				written += n
				_, account := c.Result()
				kept = int(account.Kept)
				if got := retainedBytes(c); got > limit || got > max(2*kept, 64) {
					t.Fatalf("policy %d: after %d bytes written the capture keeps %d and holds %d bytes of storage (cap %d)", policy, written, kept, got, limit)
				}
			}
		}
	})
	t.Run("a stream far beyond the cap allocates a small multiple of it, once", func(t *testing.T) {
		limit := 256 << 10
		c := newCapture(limit, truncateHeadTail)
		chunk := make([]byte, 64<<10)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for written := 0; written < 64<<20; written += len(chunk) {
			c.Write(chunk)
		}
		runtime.ReadMemStats(&after)
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > uint64(3*limit)+(1<<20) {
			t.Errorf("64 MiB through a capture of %d bytes allocated %d bytes, want at most about 1.5 times the cap", limit, allocated)
		}
		if got := retainedBytes(c); got > limit {
			t.Errorf("the capture holds %d bytes of storage for a cap of %d", got, limit)
		}
	})
	t.Run("a capture at its cap allocates nothing more", func(t *testing.T) {
		for _, policy := range []truncatePolicy{truncateHeadTail, truncateHead, truncateTail} {
			c := newCapture(256<<10, policy)
			chunk := make([]byte, 64<<10)
			for range 16 {
				c.Write(chunk)
			}
			if allocs := testing.AllocsPerRun(200, func() { c.Write(chunk) }); allocs != 0 {
				t.Errorf("policy %d: %v allocations for a write to a capture that is full", policy, allocs)
			}
		}
	})
}

// ---------------------------------------------------------------------------------------------------------------
// the incremental UTF-8 check and the account of a streamed output
// ---------------------------------------------------------------------------------------------------------------

func TestUTF8TrackerAgreesWithUTF8ValidForAnyChunking(t *testing.T) {
	for seed := uint64(1); seed <= 4000; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed*31))
		text := randomText(rng, rng.IntN(60))
		var u utf8Tracker
		for rest := text; len(rest) > 0; {
			n := 1 + rng.IntN(5)
			n = min(n, len(rest))
			u.Write(rest[:n])
			rest = rest[n:]
			consumed := text[:len(text)-len(rest)]
			if got, want := u.Valid(), utf8.Valid(consumed); got != want {
				t.Fatalf("seed %d: after %q the tracker says %v, utf8.Valid says %v", seed, consumed, got, want)
			}
		}
		if got, want := u.Valid(), utf8.Valid(text); got != want {
			t.Fatalf("seed %d: for %q the tracker says %v, utf8.Valid says %v", seed, text, got, want)
		}
	}
}

func TestUTF8TrackerNamesTheCutSequencesAndTheInvalidBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   bool
	}{
		{"nothing", nil, true},
		{"text", []string{"hello"}, true},
		{"a sequence split across chunks", []string{"\xE2", "\x82", "\xAC"}, true},
		{"a four byte sequence split in the middle", []string{"\xF0\x9F", "\x98\x80"}, true},
		{"a sequence cut at the end", []string{"ab\xE2\x82"}, false},
		{"a continuation byte alone", []string{"\x80"}, false},
		{"an invalid start", []string{"\xFF"}, false},
		{"an overlong encoding", []string{"\xC0", "\x80"}, false},
		{"a sequence cut by a start byte", []string{"\xE2\x82", "a"}, false},
		{"invalid early and text after", []string{"\xFF", "all the rest is fine"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var u utf8Tracker
			for _, c := range tc.chunks {
				u.Write([]byte(c))
			}
			if got := u.Valid(); got != tc.want {
				t.Errorf("Valid() = %v for %q, want %v", got, tc.chunks, tc.want)
			}
		})
	}
}

func TestStreamTallyDeliversTheFirstBytesUpToTheCapAndCountsTheRest(t *testing.T) {
	for seed := uint64(1); seed <= 4000; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed*977))
		limit := randomCap(rng)
		events := randomEvents(rng, limit, true)
		tally := newStreamTally(limit)
		var delivered, arrived []byte
		var lost uint64
		for _, e := range events {
			if e.lost > 0 {
				tally.Lose(e.lost)
				lost += e.lost
				continue
			}
			out := tally.Take(e.data)
			if len(out) > len(e.data) || !bytes.HasPrefix(e.data, out) {
				t.Fatalf("seed %d: Take(%q) delivered %q, which is not a prefix of what arrived", seed, e.data, out)
			}
			delivered = append(delivered, out...)
			arrived = append(arrived, e.data...)
		}
		want := arrived[:min(limit, len(arrived))]
		if !bytes.Equal(delivered, want) {
			t.Fatalf("seed %d: delivered %q, want the first %d bytes that arrived, %q", seed, delivered, limit, want)
		}
		account := tally.Account()
		total := uint64(len(arrived)) + lost
		wantAccount := outputAccount{
			Total: total, Kept: uint64(len(want)), Dropped: total - uint64(len(want)), Head: len(want), ValidUTF8: utf8.Valid(want),
		}
		if account != wantAccount {
			t.Fatalf("seed %d (cap %d): account %+v, want %+v", seed, limit, account, wantAccount)
		}
	}
}

// What a stream delivers is what is valid UTF-8 as delivered: a sequence that a cap cuts in two is not valid, and one that two
// chunks share is.
func TestStreamTallyValidUTF8IsAboutTheBytesDelivered(t *testing.T) {
	cut := newStreamTally(4)
	cut.Take([]byte("abc\xE2\x82\xAC"))
	if account := cut.Account(); account.ValidUTF8 || account.Kept != 4 || account.Dropped != 2 {
		t.Errorf("a sequence cut by the cap: %+v, want 4 kept, 2 dropped, not valid", account)
	}
	shared := newStreamTally(16)
	shared.Take([]byte("a\xE2"))
	shared.Take([]byte("\x82\xAC"))
	if account := shared.Account(); !account.ValidUTF8 || account.Kept != 4 {
		t.Errorf("a sequence shared by two chunks: %+v, want valid", account)
	}
}
