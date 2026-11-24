package exectest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The in-process cases run the real engine against what an author declared. These tests hold them to two things: each body really
// runs (a marker counts it) and passes on every shape of declaration, and each body fails when the engine is wrong. The wrongness
// is a faulty engine, the real engine with one fault added at its edge (faulty_engine_test.go), given to the case through the
// engineFactory seam: a fake target whose declaration contradicts its script would not do, because a case builds its own fake from
// the declaration and so cannot be given a contradiction.

// the bodies, by what the tests call them
type bodyCase struct {
	name string
	body func(t checkT, target exec.Target, env *inProcessEnv)
}

// filledCases returns the cases of the skeleton that carry a body.
func filledCases(t *testing.T) []bodyCase {
	t.Helper()
	var filled []bodyCase
	for _, c := range engineCases() {
		if c.inProcess == nil {
			continue
		}
		name := c.name
		switch {
		case c.number > 0:
			name = fmt.Sprintf("case %02d", c.number)
		case c.name == pairEndingName:
			name = "pair P5"
		case c.name == stressName:
			name = "stress"
		}
		filled = append(filled, bodyCase{name: name, body: c.inProcess})
	}
	return filled
}

func TestTheCasesThatRunInProcessAreTheOnesTheEngineSatisfiesAndNoOther(t *testing.T) {
	var got []string
	for _, c := range filledCases(t) {
		got = append(got, c.name)
	}
	want := []string{"case 09", "case 14", "case 17", "case 28", "pair P5", "stress"}
	if !slices.Equal(got, want) {
		t.Errorf("the cases with a body are %q, want %q", got, want)
	}
	// What is left of each has a step that writes it, unless the case has a body over the wire and that was all that was left (the
	// tombstone of case 9 was, and is the wire's), or the in-process body is all of it: the stress is complete in process, with no step.
	for _, c := range engineCases() {
		if c.inProcess != nil && c.step == "" && c.wire == nil && c.name != stressName {
			t.Errorf("case %d (%q) runs in process and has no step for what is left of it, and no body over the wire", c.number, c.name)
		}
		if c.name == stressName && (c.step != "" || c.inProcess == nil) {
			t.Errorf("the stress row has step %q and a body %v, want no step and a body that runs in process: it is written", c.step, c.inProcess != nil)
		}
	}
}

func TestAnInProcessBodyRunsFirstInItsOwnSubtestAndTheWirePartStillConcludes(t *testing.T) {
	session := execv2.Feature_FEATURE_SESSION
	var order []string
	var bodyName string
	cases := []engineCase{
		{
			caseRow: caseRow{number: 3, name: "a case with a body", step: "X9", requires: session},
			inProcess: func(c checkT, _ exec.Target, _ *inProcessEnv) {
				order = append(order, "body")
				bodyName = c.(*testing.T).Name()
			},
		},
		{caseRow: caseRow{number: 4, name: "a case that is only listed", step: "X9", requires: session}},
	}
	t.Run("engine", func(t *testing.T) {
		listCases(t, cases, exec.Target{}, &inProcessEnv{}, &wireEnv{}, func(t testing.TB) *execv2.DescribeResponse {
			order = append(order, "describe")
			return &execv2.DescribeResponse{}
		})
	})
	if want := []string{"body", "describe", "describe"}; !slices.Equal(order, want) {
		t.Errorf("order %q, want %q: the body runs first, and each case then asks the build what it serves", order, want)
	}
	if !strings.HasSuffix(bodyName, "/case_03:_a_case_with_a_body/in_process") {
		t.Errorf("the body ran in %q, want the subtest \"in process\" of its case", bodyName)
	}
}

// shape is a target as an author declares it.
type shape struct {
	name string
	info func() exec.TargetInfo
}

func shapes() []shape {
	base := func() exec.TargetInfo { return NewFakeTarget().Info }
	return []shape{
		{"a shell: sessions and runs, a terminal, CODE_AND_SIGNAL, TERMINATES", base},
		{"an echo host: no terminal, ExitReport CODE, sessions only", func() exec.TargetInfo {
			return exec.TargetInfo{
				Provider: "echo", Type: "host", DisplayName: "Echo host", DefaultArgv: []string{"cat"},
				Capabilities: exec.Capabilities{Kill: exec.KillTerminates, ExitReport: exec.ExitReportCode, Stall: exec.StallPropagates},
				Session:      exec.DefaultSessionLimits(),
			}
		}},
		{"a pod: a terminal, ExitReport CODE, CLOSES_STREAM_ONLY, LOSSY", func() exec.TargetInfo {
			info := base()
			info.Provider, info.Type = "k8s", "pods"
			info.Capabilities.Kill, info.Capabilities.ExitReport, info.Capabilities.Stall = exec.KillClosesStreamOnly, exec.ExitReportCode, exec.StallLossy
			info.Capabilities.Signals = nil
			info.Run = exec.RunLimits{}
			return info
		}},
		{"a target that reports no exit status and ends best effort", func() exec.TargetInfo {
			info := base()
			info.Capabilities.Kill, info.Capabilities.ExitReport = exec.KillBestEffort, exec.ExitReportNone
			return info
		}},
		{"a target that serves only runs", func() exec.TargetInfo {
			info := base()
			info.Provider, info.Type = "batch", "jobs"
			info.Session = exec.SessionLimits{}
			return info
		}},
		{"a target that serves one session at a time", func() exec.TargetInfo {
			info := base()
			info.Session = exec.SessionLimits{MaxSessions: 1}
			info.Run = exec.RunLimits{}
			return info
		}},
	}
}

// declared returns the target an author would register for the shape, whose Exec fails the test if the suite ever calls it.
func declared(t *testing.T, s shape, calls *atomic.Int64) exec.Target {
	t.Helper()
	return exec.Target{
		Info: s.info(),
		Exec: func(context.Context, *exec.Proc) (exec.Exit, error) {
			calls.Add(1)
			return exec.ExitCode(0), nil
		},
	}
}

func TestEveryInProcessCasePassesOnEveryShapeAnAuthorDeclaresAndNeverCallsTheirExec(t *testing.T) {
	var calls atomic.Int64
	for _, s := range shapes() {
		for _, c := range filledCases(t) {
			if c.name == "stress" && s.name != shapes()[0].name {
				continue // the stress has targets of its own: one run is enough
			}
			t.Run(s.name+"/"+c.name, func(t *testing.T) {
				var ran atomic.Int64
				rec := &recordT{}
				env := &inProcessEnv{resource: "thing-1", stress: StressConfig{Duration: 200 * time.Millisecond}}
				rec.run(func(r checkT) {
					ran.Add(1)
					c.body(r, declared(t, s, &calls), env)
				})
				if ran.Load() != 1 {
					t.Fatalf("the body ran %d times", ran.Load())
				}
				if len(rec.skips) != 0 {
					t.Errorf("the body skipped: %q: a target that serves commands has something to run", rec.skips)
				}
				if failures := rec.failed(); len(failures) != 0 {
					t.Errorf("the real engine fails the case for a target declared as %s: %q", s.name, failures)
				}
			})
		}
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("the author's Exec was called %d times: the cases run a fake built from what the author declared", got)
	}
}

func TestACaseSkipsForATargetThatServesNoCommands(t *testing.T) {
	info := NewFakeTarget().Info
	info.Session, info.Run = exec.SessionLimits{}, exec.RunLimits{}
	target := exec.Target{Info: info}
	for _, c := range filledCases(t) {
		if c.name == "stress" {
			continue
		}
		rec := &recordT{}
		rec.run(func(r checkT) { c.body(r, target, &inProcessEnv{resource: "thing-1"}) })
		if len(rec.skips) != 1 || len(rec.failed()) != 0 {
			t.Errorf("%s on a target that serves neither sessions nor runs: %d skips and failures %q, want one skip and no failure", c.name, len(rec.skips), rec.failed())
		}
	}
}

func TestEveryInProcessCaseFailsWhenTheEngineIsWrong(t *testing.T) {
	short := StressConfig{Duration: 150 * time.Millisecond, Workers: 4}
	for _, tc := range []struct {
		name   string
		fault  engineFault
		phrase string // what the failure says
	}{
		{"case 09", faultEndIsLost, "the end is"},
		{"case 09", faultNoSurvival, "the end is"},
		{"case 09", faultSecondEnd, "asked for again, the result differs"},
		{"case 09", faultOutputAfterEnd, "a read after the end of the output"},
		{"case 09", faultCauseRequested, "the cause is"},
		{"case 14", faultEndIsLost, "the end is"},
		{"case 14", faultCauseRequested, `want "retired"`},
		{"case 28", faultNoSurvival, "remote_may_survive is absent"},
		{"case 28", faultEndIsLost, "the end is lost"},
		{"case 17", faultNoViolations, "exit violations"},
		{"case 17", faultInventedViolation, "want none"},
		{"pair P5", faultSecondEnd, "ended after its exit, the result changed"},
		{"pair P5", faultEndIsLost, "ended once"},
		{"stress", faultCompleteWithDrops, "COMPLETE"},
	} {
		t.Run(tc.name+" with "+faultName(tc.fault), func(t *testing.T) {
			var body func(checkT, exec.Target, *inProcessEnv)
			for _, c := range filledCases(t) {
				if c.name == tc.name {
					body = c.body
				}
			}
			if body == nil {
				t.Fatalf("no case %q has a body", tc.name)
			}
			var calls atomic.Int64
			rec := &recordT{}
			// The stress row goes on, round after round, until the stress says what the fault is, with the bounds the harness states (only a real hang exceeds
			// them): a window of a fixed length is filled by a slow machine with too little to meet the fault. The other rows run no stress.
			env := &inProcessEnv{
				resource: "thing-1", stress: short,
				hooks: stressHooks{engine: faultyFactory(t, tc.fault), until: func() bool { return rec.said(tc.phrase) }},
			}
			rec.run(func(r checkT) { body(r, declared(t, shapes()[0], &calls), env) })
			if !rec.said(tc.phrase) {
				t.Errorf("the case did not fail with %q on an engine with the fault %s: it failed with %q", tc.phrase, faultName(tc.fault), rec.failed())
			}
		})
	}
}

// faultName names a fault for the name of a test.
func faultName(f engineFault) string {
	names := map[engineFault]string{
		faultEndIsLost: "every end lost", faultNoSurvival: "no remote_may_survive", faultCauseRequested: "every cause requested",
		faultSecondEnd: "a second end", faultOutputAfterEnd: "output after the end", faultCompleteWithDrops: "complete output with drops",
		faultHole: "a hole in the output", faultWrongByte: "a wrong byte", faultNoArm: "an end with no arm", faultBytesOut: "a byte counted that was not written",
		faultLive: "a command live after Close", faultAbandoned: "functions abandoned for ever", faultSlowClose: "a Close that does not return",
		faultLeak: "a goroutine left behind", faultRefuseWrongClass: "a start refused as INTERNAL", faultNeverDone: "Done never closed",
		faultNoViolations: "violations hidden", faultInventedViolation: "a violation that was not made",
	}
	return names[f]
}
