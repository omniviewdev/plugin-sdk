package exectest

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// A test that shows the stress failing on an engine with a fault asks "does the stress say what the fault is", which only the harness can answer, and
// never "does it say it within this long", which the machine answers: on a runner that is slow a window of a fixed length holds too little work to
// meet the fault, and the stress then passes an engine that it fails everywhere else. These tests hold the mode that asks the first question
// (stressHooks.until) with a fault that no window of one round can meet, so that nothing in them depends on how fast the machine is.

// faultyByRound is the factory of engines of which the n-th one built, a round's, has the n-th of the faults (the real engine for a fault that is
// zero), and the real engine after the last.
func faultyByRound(t testing.TB, faults ...engineFault) engineFactory {
	var built atomic.Int32
	return func(targets []exec.Target, cfg exechook.EngineConfig) (exechook.Engine, error) {
		n := int(built.Add(1))
		if n > len(faults) || faults[n-1] == 0 {
			return realEngine(targets, cfg)
		}
		return faultyFactory(t, faults[n-1])(targets, cfg)
	}
}

// aRoundThatMeetsAFault is what a test runs: the first round is a real engine, the second has a fault that the stress finds, but is not the one that
// the test waits for, and the third has the one it waits for.
func aRoundThatMeetsAFault(t testing.TB) engineFactory {
	return faultyByRound(t, 0, faultNoArm, faultLive)
}

// A run that has a window of one round meets only the engine of that round: it says nothing of a fault that the engine of the third round has, however
// it is run. The run that goes on until the test has what it waits for goes on past the round that said something else, to the round that says it, and
// stops there.
func TestTheStressGoesOnRoundAfterRoundUntilItSaysWhatTheTestWaitsFor(t *testing.T) {
	cfg := StressConfig{Workers: 2, Duration: 100 * time.Millisecond, Seed: 7}

	fixed := &recordT{}
	totals := runStress(fixed, cfg, stressHooks{engine: aRoundThatMeetsAFault(t)})
	fixed.finish()
	if got := totals.get("rounds"); got != 1 || len(fixed.failed()) != 0 {
		t.Fatalf("a window of one round ran %d rounds and said %q, want one round of the real engine and nothing said: no window of one round can meet the fault of the third", got, fixed.failed())
	}

	rec := &recordT{}
	var asked atomic.Int32
	totals = runStress(rec, cfg, stressHooks{
		engine: aRoundThatMeetsAFault(t),
		until:  func() bool { asked.Add(1); return rec.said("Live() is", "ghost-1") },
	})
	rec.finish()
	if !rec.said("Live() is", "ghost-1") {
		t.Fatalf("the stress that goes on until it says that Live holds a ghost said %q after %d rounds", rec.failed(), totals.get("rounds"))
	}
	if !rec.said("the end has no arm") {
		t.Errorf("the stress said %q: want what the second round said as well, which the run goes on past: a round that found a violation does not end it", rec.failed())
	}
	if got := totals.get("rounds"); got != 3 {
		t.Errorf("the stress ran %d rounds, want 3: the real engine, the one that says something else, and the one that says it, where it stops", got)
	}
	if got := asked.Load(); got != 3 {
		t.Errorf("the test was asked %d times whether the stress had said it, want once after each round", got)
	}
}

// What the stress says by the time the bound has run out is all it says: it stops asking then, whatever the test waits for, and the test reports what it
// was told.
func TestTheStressStopsGoingOnOnceItsBoundHasRunOut(t *testing.T) {
	rec := &recordT{}
	var asked atomic.Int32
	totals := runStress(rec, StressConfig{Workers: 2, Duration: 100 * time.Millisecond, Seed: 7}, stressHooks{
		until:      func() bool { return asked.Add(1) >= 3 }, // it would be satisfied at the third round
		untilBound: time.Nanosecond,                          // and the bound has run out at the first
	})
	rec.finish()
	if got := totals.get("rounds"); got != 1 {
		t.Errorf("the stress ran %d rounds past a bound that had run out at the first, want 1", got)
	}
	if got := asked.Load(); got != 1 {
		t.Errorf("the test was asked %d times, want once: after the first round, which is where the bound ended the run", got)
	}
}

// The run of a stress that nothing waits for is as shipped: a round that finds a violation ends it, and the rounds its duration holds are all it makes
// otherwise.
func TestTheStressThatNothingWaitsForEndsAtTheFirstRoundThatFails(t *testing.T) {
	rec := &recordT{}
	// A second and a half holds two rounds, which a run of the real engine makes.
	totals := runStress(rec, StressConfig{Workers: 2, Duration: 1500 * time.Millisecond, Seed: 7}, stressHooks{engine: faultyByRound(t, faultNoArm, faultNoArm)})
	rec.finish()
	if len(rec.failed()) == 0 {
		t.Fatalf("the stress said nothing of an engine that ends every command with no arm")
	}
	if got := totals.get("rounds"); got != 1 {
		t.Errorf("the stress ran %d rounds, want 1: the second round is not made once the first found a violation", got)
	}
}

// A duration holds rounds, and a test that waits for the stress is asked only once they have been made: a stress that is held to what it exercises
// (TestStressTheEngineLeavesTheEndStateItPromises) is held to no less than the run that nothing waits for, the two rounds of the tombstone regimes among
// them, and a test whose wait is satisfied at the first round does not cut them short.
func TestTheStressThatIsWaitedForMakesAtLeastTheRoundsItsDurationHolds(t *testing.T) {
	rec := &recordT{}
	var asked atomic.Int32
	// A second and a half holds two rounds.
	totals := runStress(rec, StressConfig{Workers: 2, Duration: 1500 * time.Millisecond, Seed: 7}, stressHooks{until: func() bool { asked.Add(1); return true }})
	rec.finish()
	if got := totals.get("rounds"); got != 2 {
		t.Errorf("the stress ran %d rounds for a wait that was satisfied at once, want the 2 that its duration holds", got)
	}
	if got := asked.Load(); got != 1 {
		t.Errorf("the test was asked %d times, want once: after the last round that the duration holds", got)
	}
}

// The tally that a stress is given is the one it merges its rounds into and returns, so that a test whose wait reads what the stress counted reads it
// between the rounds: after the first round it holds the first round, and not the run's own tally that it would otherwise be.
func TestTheTotalsAStressIsGivenAreWhatItsRoundsAreMergedIntoAndWhatItsWaitReads(t *testing.T) {
	rec := &recordT{}
	mine := newTally()
	var seen []int
	got := runStress(rec, StressConfig{Workers: 2, Duration: 100 * time.Millisecond, Seed: 7}, stressHooks{
		totals: mine,
		until:  func() bool { seen = append(seen, mine.get("rounds")); return len(seen) == 3 },
	})
	rec.finish()
	if got != mine {
		t.Errorf("the stress returned a tally of its own, want the one it was given")
	}
	if want := []int{1, 2, 3}; !slices.Equal(seen, want) {
		t.Errorf("the wait read %v rounds after each round, want %v: the rounds are merged into the given tally before the wait is asked", seen, want)
	}
	if mine.get("admitted/session")+mine.get("admitted/run") == 0 {
		t.Errorf("the given tally counted no command of the three rounds, want what the rounds counted merged into it")
	}
}
