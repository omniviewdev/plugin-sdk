package exectest

import (
	"testing"
	"time"
)

// The real engine under the stress: every invariant at rest, for as long as the stress runs by default (one second under -short,
// five otherwise, or what OMNIVIEW_LIFECYCLE_HAMMER says). It is the control of the tests below, which give the same stress an
// engine with one fault and see it fail.
//
// The stress must also exercise what it claims to. Each of these is certain in a round whatever the scheduler does: the workers
// start every behaviour, as a session and as a run, before they pick at random; the limits of the blockers, the stuck functions
// and the late writers are smaller than the number of workers; a Close lands among commands that wait to be ended, and the ones
// that ignore their context are given up on.
func TestStressTheEngineLeavesTheEndStateItPromises(t *testing.T) {
	totals := runStress(t, StressConfig{}, stressHooks{})
	for _, b := range stressBehaviours {
		if totals.get("behaviour/"+b.name) == 0 {
			t.Errorf("no command of the behaviour %q was admitted", b.name)
		}
	}
	for _, key := range []string{
		"ended/exited", "ended/signaled", "ended/closed", "ended/failed", "ended/lost",
		"cause/finished", "cause/shutdown",
		"refused/UNAVAILABLE", "refused/LIMIT/TOO_MANY_SESSIONS", "refused/LIMIT/TOO_MANY_RUNS",
		"abandoned", "late writers", "contract/panic",
	} {
		if totals.get(key) == 0 {
			t.Errorf("the stress counted none of %q: it did not exercise what it claims to", key)
		}
	}
}

// stressWithFault runs a short stress against an engine with one fault and returns what the stress said.
func stressWithFault(t *testing.T, fault engineFault) *recordT {
	t.Helper()
	rec := &recordT{}
	runStress(rec, StressConfig{Workers: 4, Duration: 100 * time.Millisecond, Seed: 7}, stressHooks{
		engine:       faultyFactory(t, fault),
		closeBound:   200 * time.Millisecond,
		settle:       300 * time.Millisecond,
		abandonBound: 200 * time.Millisecond,
	})
	rec.finish()
	return rec
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
			rec := stressWithFault(t, tc.fault)
			for _, phrases := range tc.phrases {
				if !rec.said(phrases...) {
					t.Errorf("the stress did not say %q of an engine with the fault %q; it said %q", phrases, faultName(tc.fault), rec.failed())
				}
			}
		})
	}
}
