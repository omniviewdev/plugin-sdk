package lifecycletest_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/internal/producers"
)

// mentions fails the test unless one of the violations says kind, and returns it.
func mentions(t *testing.T, violations []string, kind string) string {
	t.Helper()
	for _, v := range violations {
		if strings.Contains(v, kind) {
			return v
		}
	}
	t.Fatalf("no violation says %q: %q", kind, violations)
	return ""
}

func TestRecorderAcceptsTheWholeLifeOfASession(t *testing.T) {
	r := lifecycletest.NewRecorder()
	r.Admitted("a")
	r.Event("a")
	r.Event("a")
	r.Ended("a")
	if v := r.Violations(); v != nil {
		t.Fatalf("Violations() = %q for a clean life", v)
	}
	if v := r.CheckAtRest(); v != nil {
		t.Fatalf("CheckAtRest() = %q for a session that ended", v)
	}
	if n := r.AdmittedCount(); n != 1 {
		t.Fatalf("AdmittedCount() = %d, want 1", n)
	}
}

func TestRecorderReportsAnEventAfterTheEnd(t *testing.T) {
	r := lifecycletest.NewRecorder()
	r.Admitted("a")
	r.Ended("a")
	r.Event("a")
	mentions(t, r.Violations(), "event after the end")
}

func TestRecorderReportsAnEventForASessionThatWasNeverAdmitted(t *testing.T) {
	r := lifecycletest.NewRecorder()
	r.Event("ghost")
	mentions(t, r.Violations(), "never admitted")
}

func TestRecorderReportsASecondEnd(t *testing.T) {
	r := lifecycletest.NewRecorder()
	r.Admitted("a")
	r.Ended("a")
	r.Ended("a")
	mentions(t, r.Violations(), "ended twice")
}

func TestRecorderReportsAnEndForASessionThatWasNeverAdmitted(t *testing.T) {
	r := lifecycletest.NewRecorder()
	r.Ended("ghost")
	mentions(t, r.Violations(), "ended without an admission")
}

func TestRecorderReportsASecondAdmissionOfALiveID(t *testing.T) {
	r := lifecycletest.NewRecorder()
	r.Admitted("a")
	r.Admitted("a")
	mentions(t, r.Violations(), "admitted twice")
}

// The generations: an id that ended may be admitted again, and that is a new session with its own
// life, which the old one's end does not cover.
func TestRecorderTellsAdmissionsOfOneIDApartByGeneration(t *testing.T) {
	r := lifecycletest.NewRecorder()
	r.Admitted("a")
	r.Event("a")
	r.Ended("a")
	r.Admitted("a") // a new admission, not a second admission of a live id
	r.Event("a")
	if v := r.Violations(); v != nil {
		t.Fatalf("an id admitted again after its end is a violation: %q", v)
	}
	if n := r.AdmittedCount(); n != 2 {
		t.Fatalf("AdmittedCount() = %d for two admissions of one id", n)
	}

	// The second admission has not ended: that is what rest reports, naming its generation.
	v := mentions(t, r.CheckAtRest(), `never ended: "a"`)
	if !strings.Contains(v, "admission #2") {
		t.Fatalf("%q does not name the second admission", v)
	}
	r.Ended("a")
	if v := r.CheckAtRest(); v != nil {
		t.Fatalf("CheckAtRest() = %q once both admissions ended", v)
	}
	if v := r.Violations(); v != nil {
		t.Fatalf("Violations() = %q for two clean lives of one id", v)
	}

	// And a third end, or an event, is for the generation that is over.
	r.Ended("a")
	r.Event("a")
	mentions(t, r.Violations(), "ended twice")
	mentions(t, r.Violations(), "event after the end")
}

func TestRecorderCheckAtRestNamesEverySessionThatWasNeverEnded(t *testing.T) {
	r := lifecycletest.NewRecorder()
	for _, id := range []string{"c", "a", "b"} {
		r.Admitted(id)
	}
	r.Ended("b")
	got := r.CheckAtRest()
	if len(got) != 2 || !strings.Contains(got[0], `"a"`) || !strings.Contains(got[1], `"c"`) {
		t.Fatalf("CheckAtRest() = %q, want sessions a and c, sorted", got)
	}
}

func TestRecorderBoundsAndSortsItsViolations(t *testing.T) {
	r := lifecycletest.NewRecorder()
	const found = 200
	for i := range found {
		r.Event(fmt.Sprintf("ghost-%03d", found-1-i)) // found out of order
	}
	got := r.Violations()
	if len(got) != 51 {
		t.Fatalf("Violations() has %d lines for %d violations, want the first 50 and a count", len(got), found)
	}
	if !slices.IsSorted(got[:50]) {
		t.Fatalf("the violations are not sorted: %q", got[:50])
	}
	if last := got[50]; !strings.Contains(last, "150 more") {
		t.Fatalf("the last line is %q, want a count of the 150 that were left out", last)
	}
	// The ones kept are the first found, which were the highest numbered.
	if !strings.Contains(got[49], "ghost-199") {
		t.Fatalf("the violations kept are not the first found: ends with %q", got[49])
	}
}

func TestRecorderBoundsWhatCheckAtRestReports(t *testing.T) {
	r := lifecycletest.NewRecorder()
	for i := range 120 {
		r.Admitted(fmt.Sprintf("s-%03d", i))
	}
	got := r.CheckAtRest()
	if len(got) != 51 || !strings.Contains(got[50], "70 more") {
		t.Fatalf("CheckAtRest() has %d lines, last %q, want 50 and a count of 70", len(got), got[len(got)-1])
	}
	if !strings.Contains(got[0], "s-000") {
		t.Fatalf("CheckAtRest() starts with %q, want the first of the sorted ids", got[0])
	}
}

func TestRecorderIsSafeForConcurrentUse(t *testing.T) {
	r := lifecycletest.NewRecorder()
	var workers producers.Group
	for w := range 8 {
		workers.Start(1)
		go func() {
			defer workers.Done()
			for i := range 500 {
				id := fmt.Sprintf("w%d-%d", w, i)
				r.Admitted(id)
				r.Event(id)
				r.Ended(id)
				_ = r.AdmittedCount()
				_ = r.Violations()
			}
		}()
	}
	workers.Wait()
	if v := r.Violations(); v != nil {
		t.Fatalf("Violations() = %q", v)
	}
	if v := r.CheckAtRest(); v != nil {
		t.Fatalf("CheckAtRest() = %q", v)
	}
	if n := r.AdmittedCount(); n != 8*500 {
		t.Fatalf("AdmittedCount() = %d, want %d", n, 8*500)
	}
}
