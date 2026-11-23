package lifecycletest

import (
	"fmt"
	"strings"
	"testing"
)

// The recorder's memory is bounded by the sessions that are live and by a fixed window of ends,
// however many sessions the hammer runs: a 30 second run admits millions.
func TestRecorderMemoryIsBoundedByTheLiveSessionsAndTheWindowOfEnds(t *testing.T) {
	r := NewRecorder()
	const sessions = 5 * recentEnded
	for i := range sessions {
		id := fmt.Sprintf("s-%d", i)
		r.Admitted(id)
		r.Ended(id)
	}
	if got := len(r.ended); got > recentEnded {
		t.Fatalf("the recorder remembers %d ended ids, bound %d", got, recentEnded)
	}
	if got := len(r.live); got != 0 {
		t.Fatalf("the recorder holds %d live admissions after every session ended", got)
	}
	if got := r.AdmittedCount(); got != sessions {
		t.Fatalf("AdmittedCount() = %d, want %d", got, sessions)
	}

	// An id that ended lately is named for what it is; one that ended long ago is still a
	// violation, only not named: the recorder has forgotten it.
	r.Event(fmt.Sprintf("s-%d", sessions-1))
	r.Event("s-0")
	got := r.Violations()
	if len(got) != 2 || !strings.Contains(got[0], "after the end") || !strings.Contains(got[1], "never admitted") {
		t.Fatalf("Violations() = %q, want one after the end and one for an id that is forgotten", got)
	}
}

// An id admitted again after it ended supersedes its old record, and the old one's eviction
// from the window does not take the new one's with it.
func TestRecorderEvictionOfAnOldEndKeepsTheNewestRecordOfTheSameID(t *testing.T) {
	r := NewRecorder()
	r.Admitted("a")
	r.Ended("a")
	r.Admitted("a")
	r.Ended("a") // the id is in the window twice, as generations 1 and 2
	for i := range recentEnded - 1 {
		id := fmt.Sprintf("filler-%d", i)
		r.Admitted(id)
		r.Ended(id)
	}
	// The window has wrapped once: the record of generation 1 is gone, and generation 2's is
	// the oldest that is left. Evicting the first must not have taken the second with it.
	r.Event("a")
	got := r.Violations()
	if len(got) != 1 || !strings.Contains(got[0], "after the end of its admission #2") {
		t.Fatalf("Violations() = %q, want the event named as after the end of the latest admission", got)
	}

	r.Admitted("push")
	r.Ended("push") // evicts generation 2's record, the last of "a"
	r.Event("a")
	got = r.Violations()
	if len(got) != 2 || !strings.Contains(got[1], "never admitted") {
		t.Fatalf("Violations() = %q, want the second event reported as one for an id that is forgotten", got)
	}
}

func TestGroupStacksIsBoundedAndKeepsTheMostNumerousGroupsFirst(t *testing.T) {
	var stacks []goroutineStack
	for i := range 400 {
		stacks = append(stacks, goroutineStack{id: i, state: "chan receive", top: fmt.Sprintf("pkg.worker%03d", i), creator: "pkg.start"})
	}
	for i := range 7 {
		stacks = append(stacks, goroutineStack{id: 1000 + i, state: "select", top: "pkg.popular", creator: "pkg.start"})
	}
	out := groupStacks(stacks, dumpLimit)
	if len(out) > dumpLimit+len("... (cut)\n") {
		t.Fatalf("the dump is %d bytes, bound %d", len(out), dumpLimit)
	}
	if !strings.HasSuffix(out, "... (cut)\n") {
		t.Fatalf("a dump of 401 groups is not marked as cut: ends %q", out[len(out)-40:])
	}
	first, _, _ := strings.Cut(out, "\n")
	if !strings.Contains(first, "7 x [select] pkg.popular <- pkg.start") {
		t.Fatalf("the first line is %q, want the most numerous group", first)
	}
}

func TestParseStackReadsAGoroutineBlock(t *testing.T) {
	block := "goroutine 42 [sync.Mutex.Lock, 2 minutes]:\n" +
		"internal/sync.runtime_SemacquireMutex(0x0?, 0x0?, 0x0?)\n" +
		"\t/usr/local/go/src/runtime/sema.go:95 +0x28\n" +
		"example.com/m.(*R[...]).Admit(0xc000012345)\n" +
		"\t/m/r.go:12 +0x6c\n" +
		"created by example.com/m.start in goroutine 1\n" +
		"\t/m/main.go:8 +0x56"
	g, ok := parseStack(block)
	if !ok {
		t.Fatal("a goroutine block was not parsed")
	}
	want := goroutineStack{id: 42, state: "sync.Mutex.Lock", top: "internal/sync.runtime_SemacquireMutex", creator: "example.com/m.start"}
	if g != want {
		t.Fatalf("parseStack = %+v, want %+v", g, want)
	}
	if _, ok := parseStack("not a goroutine"); ok {
		t.Fatal("text that is not a goroutine block was parsed")
	}
}
