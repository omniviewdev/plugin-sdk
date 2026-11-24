package exectest

import (
	"testing"
	"time"
)

// The real engine under the stress: every invariant at rest, for as long as the stress runs by default (one second under -short,
// two otherwise, or what OMNIVIEW_LIFECYCLE_HAMMER says). It is the control of the tests below, which give the same stress an
// engine with one fault and see it fail.
//
// The stress must also exercise what it claims to. Each of these happens in a round that the machine lets its workers get through: the workers
// start every behaviour, as a session and as a run, before they pick at random; the limits of the blockers, the stuck functions
// and the late writers are smaller than the number of workers; a Close lands among commands that wait to be ended, and the ones
// that ignore their context are given up on. The Close of a round lands at a moment of its length, though, and where the machine is so busy
// that the workers have not got as far by then a round counts none of them (849 starts refused as UNAVAILABLE and no run refused for its
// limit, seen under a load that made a round take five times as long). So the stress goes on, round after round, until it has counted them
// all, and for a minute at most, which only a stress that never exercises them reaches.
func TestStressTheEngineLeavesTheEndStateItPromises(t *testing.T) {
	totals := newTally()
	unexercised := func() (behaviours, kinds []string) {
		for _, b := range stressBehaviours {
			if totals.get("behaviour/"+b.name) == 0 {
				behaviours = append(behaviours, b.name)
			}
		}
		for _, key := range []string{
			"ended/exited", "ended/signaled", "ended/closed", "ended/failed", "ended/lost",
			"cause/finished", "cause/shutdown",
			"refused/UNAVAILABLE", "refused/LIMIT/TOO_MANY_SESSIONS", "refused/LIMIT/TOO_MANY_RUNS",
			"abandoned", "late writers", "contract/panic",
		} {
			if totals.get(key) == 0 {
				kinds = append(kinds, key)
			}
		}
		return behaviours, kinds
	}
	runStress(t, StressConfig{}, stressHooks{totals: totals, until: func() bool {
		behaviours, kinds := unexercised()
		return len(behaviours) == 0 && len(kinds) == 0
	}})
	behaviours, kinds := unexercised()
	for _, name := range behaviours {
		t.Errorf("no command of the behaviour %q was admitted", name)
	}
	for _, key := range kinds {
		t.Errorf("the stress counted none of %q: it did not exercise what it claims to", key)
	}
}

// stressWithFault runs the stress against an engine with one fault, round after round, until it has said everything the phrases name (each list of
// them is held by one failure), and returns what it said and how many rounds that took. A run that is told for how long to run lets the machine
// decide what it finds: a window of a fixed length is filled by a slow one with too little to meet the fault, and the stress then passes an engine
// that it fails everywhere else. The bounds are the harness's own, which only a real hang exceeds, so that a busy machine cannot make a correct Close
// or a correct canary look broken and keep the stress from meeting the fault; the exceptions are the faults that break a bound for good (a Close that
// does not return, a command that does not end, functions that are never forgotten, a goroutine that never returns), where a shorter bound only
// makes the stress say it sooner.
func stressWithFault(t *testing.T, fault engineFault, phrases [][]string) (*recordT, int) {
	t.Helper()
	rec := &recordT{}
	hooks := stressHooks{engine: faultyFactory(t, fault), until: func() bool { return rec.saidEach(phrases) }}
	switch fault {
	case faultSlowClose, faultNeverDone:
		hooks.closeBound = 200 * time.Millisecond
	case faultAbandoned:
		hooks.abandonBound = 200 * time.Millisecond
	case faultLeak:
		hooks.settle = 300 * time.Millisecond
	}
	totals := runStress(rec, StressConfig{Workers: 4, Duration: 100 * time.Millisecond, Seed: 7}, hooks)
	rec.finish()
	return rec, totals.get("rounds")
}

// Each check at rest is seen to fail: the stress is run on the real engine with one fault added at its edge, and what it says
// names the fault. The same stress finds nothing in the engine without it (the test above).
func TestTheStressFindsWhatAWrongEngineGetsWrong(t *testing.T) {
	for _, tc := range []struct {
		fault   engineFault
		phrases [][]string // each list of phrases must be found in one failure
	}{
		{faultLive, [][]string{{"Live() is", "ghost-1", "must be empty"}}},
		{faultSlowClose, [][]string{{"Close #", "did not finish within the bound"}}},
		{faultAbandoned, [][]string{{"Abandoned() is 1"}}},
		{faultLeak, [][]string{{"goroutines", "after the lane was closed"}}},
		{faultSecondEnd, [][]string{{"the end changed", "the command ended twice"}, {"ended twice"}}},
		{faultHole, [][]string{{"a hole that was not reported as dropped"}}},
		{faultWrongByte, [][]string{{"not what was written there"}}},
		{faultOutputAfterEnd, [][]string{{"nothing may come after the end"}}},
		{faultCompleteWithDrops, [][]string{{"the output is COMPLETE and 5 bytes were dropped"}}},
		{faultBytesOut, [][]string{{"read plus dropped must be what was written"}}},
		{faultNoArm, [][]string{{"the end has no arm"}}},
		{faultCauseRequested, [][]string{{`the command was ended with cause "requested" and is Closed by`, "want CLOSED_BY_REQUEST"}}},
		{faultEndIsLost, [][]string{{"the end is lost, want"}}},
		{faultRefuseWrongClass, [][]string{{"an Open: a start after Close returned was refused as INTERNAL, want UNAVAILABLE"}}},
		{faultNeverDone, [][]string{{"has not ended: Done() is not closed"}}},
	} {
		t.Run(faultName(tc.fault), func(t *testing.T) {
			rec, rounds := stressWithFault(t, tc.fault, tc.phrases)
			for _, phrases := range tc.phrases {
				if !rec.said(phrases...) {
					t.Errorf("the stress did not say %q of an engine with the fault %q in %d rounds; it said %q", phrases, faultName(tc.fault), rounds, rec.failed())
				}
			}
		})
	}
}
