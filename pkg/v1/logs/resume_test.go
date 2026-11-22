package logs

import (
	"testing"
	"time"
)

func stamp(n int) time.Time { return time.Date(2026, 1, 2, 3, 4, 5, n, time.UTC) }

// A resumed read is given lines the SDK already read: those before the resume
// point, and as many at it as were read. Nothing else is dropped.
func TestResumeFilterDropsOnlyWhatWasRead(t *testing.T) {
	f := &resumeFilter{since: stamp(10), skip: 2, active: true}
	for i, step := range []struct {
		ts   time.Time
		drop bool
	}{
		{stamp(9), true},     // before the point: read
		{stamp(10), true},    // the first line stamped at the point: read
		{time.Time{}, false}, // no timestamp: passes, and does not end the filtering
		{stamp(10), true},    // the second line stamped at the point: read
		{stamp(10), false},   // a third: new
		{stamp(5), false},    // out of order after the filtering ended: delivered
		{stamp(10), false},   // likewise
		{stamp(11), false},   // past the point
	} {
		if got := f.drop(step.ts); got != step.drop {
			t.Fatalf("step %d: drop(%v) = %v, want %v", i, step.ts, got, step.drop)
		}
	}
	var none *resumeFilter
	if none.drop(stamp(1)) {
		t.Fatal("no filter must drop nothing")
	}
}

// The first line past the resume point ends the filtering at once, even when fewer
// lines at the point came back than were read: a source whose own filter was
// strict must not lose its next line.
func TestResumeFilterEndsAtTheFirstLinePastThePoint(t *testing.T) {
	f := &resumeFilter{since: stamp(10), skip: 3, active: true}
	if f.drop(stamp(11)) {
		t.Fatal("a line past the point was dropped")
	}
	if f.drop(stamp(10)) {
		t.Fatal("the filtering did not end at the first line past the point")
	}
}

// The cursor counts how many lines carry the latest timestamp read, which is what
// tells a resumed read how many of them to skip.
func TestCursorCountsLinesSharingTheLatestTimestamp(t *testing.T) {
	var c sourceCursor
	for _, ts := range []time.Time{stamp(10), stamp(10), time.Time{}, stamp(7), stamp(11), stamp(11), stamp(11)} {
		c.observe(ts)
	}
	if !c.last.Equal(stamp(11)) || c.atLast != 3 || c.read != 7 {
		t.Fatalf("cursor = %+v, want last %v read three times of 7 lines", c, stamp(11))
	}
	filter := c.filter()
	if filter == nil || !filter.since.Equal(stamp(11)) || filter.skip != 3 {
		t.Fatalf("filter = %+v", filter)
	}
	if (&sourceCursor{read: 4}).filter() != nil {
		t.Fatal("lines with no timestamp give nothing to filter by")
	}
}
