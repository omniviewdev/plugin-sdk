package exec

import (
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/session"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

func TestEveryCauseThatEndsASessionNamesItsOwnClosedBy(t *testing.T) {
	want := map[session.Cause]execv2.ClosedBy{
		session.CauseRequested: execv2.ClosedBy_CLOSED_BY_REQUEST,
		session.CauseShutdown:  execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN,
		session.CauseRetired:   execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED,
		session.CauseExpired:   execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT,
		session.CauseTimeout:   execv2.ClosedBy_CLOSED_BY_RUN_TIMEOUT,
		session.CauseCancelled: execv2.ClosedBy_CLOSED_BY_CANCELLED,
	}
	seen := map[execv2.ClosedBy]session.Cause{}
	for cause, by := range want {
		if got := closedBy(cause); got != by {
			t.Errorf("closedBy(%v) = %v, want %v", cause, got, by)
		}
		if other, dup := seen[by]; dup {
			t.Errorf("%v and %v name the same ClosedBy %v", cause, other, by)
		}
		seen[by] = cause
	}
	for _, cause := range []session.Cause{session.CauseNone, session.CauseFinished} {
		if got := closedBy(cause); got != execv2.ClosedBy_CLOSED_BY_UNSPECIFIED {
			t.Errorf("closedBy(%v) = %v: a cause that is no end of the SDK's own reads as unspecified", cause, got)
		}
	}
}

// The survival of a closed command follows the declared Kill and nothing else: a target that declared nothing says
// nothing, and absent is never read as false.
func TestTheSurvivalOfAClosedCommandFollowsTheDeclaredKill(t *testing.T) {
	for kill, want := range map[KillBehavior]*bool{
		KillTerminates:       new(false),
		KillBestEffort:       new(true),
		KillClosesStreamOnly: new(true),
		KillUnspecified:      nil,
	} {
		got := closedEnd(session.CauseRequested, kill).RemoteMaySurvive
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("Kill %v: remote_may_survive %v, want %v", kill, got, want)
		}
	}
}

// Whatever the function returned, an end whose cause is not the function's own return is Closed: the cause decides.
func TestAnEndThatTheFunctionDidNotCauseIsClosedWhateverItReturned(t *testing.T) {
	outcomes := []functionOutcome{
		{},
		{exit: ExitCode(0)},
		{exit: ExitLost(LostNoStatus)},
		{err: &Error{Class: ClassInternal}},
		{panicked: true, panicValue: "x"},
	}
	for _, cause := range []session.Cause{session.CauseRequested, session.CauseShutdown, session.CauseRetired, session.CauseTimeout, session.CauseCancelled} {
		for _, out := range outcomes {
			d := decideEnd(endInputs{outcome: out, cause: cause, caps: Capabilities{Kill: KillTerminates}})
			if d.end.GetClosed() == nil || d.processStatus || len(d.violations) != 0 {
				t.Errorf("cause %v, outcome %+v: end %v, status %v, violations %v, want a Closed end with nothing else", cause, out, d.end, d.processStatus, d.violations)
			}
		}
	}
}

func TestALostEndIsNeverAProcessStatusAndNeverCompleteOutput(t *testing.T) {
	d := decideEnd(endInputs{outcome: functionOutcome{exit: ExitLost(LostLinkDead)}, cause: session.CauseFinished})
	if d.end.GetLost() == nil || d.processStatus {
		t.Fatalf("end %v, process status %v, want Lost and no process status", d.end, d.processStatus)
	}
	if !d.end.GetRemoteMaySurvive() {
		t.Error("a lost command may still run: remote_may_survive is true")
	}
	if got := outputCompleteness(d, 0, 0, false); got != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED {
		t.Errorf("output of a lost command is %v, want unspecified", got)
	}
}

func TestOutputIsCompleteOnlyWhenAProcessStatusCameAndNothingWasLostOrLate(t *testing.T) {
	exited := decideEnd(endInputs{outcome: functionOutcome{exit: ExitCode(0)}, cause: session.CauseFinished, caps: Capabilities{ExitReport: ExitReportCodeAndSignal}})
	if !exited.processStatus {
		t.Fatalf("an exit code is a process status: %+v", exited)
	}
	for name, tc := range map[string]struct {
		dropped, late uint64
		abandoned     bool
		want          execv2.OutputCompleteness
	}{
		"clean":            {want: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE},
		"dropped":          {dropped: 1, want: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED},
		"dropped and late": {dropped: 1, late: 1, want: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED},
		"late":             {late: 1, want: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED},
		"abandoned":        {abandoned: true, want: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED},
	} {
		if got := outputCompleteness(exited, tc.dropped, tc.late, tc.abandoned); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
	closed := decideEnd(endInputs{cause: session.CauseRequested})
	if got := outputCompleteness(closed, 0, 0, false); got != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED {
		t.Errorf("the output of a closed command is %v, want unspecified: nobody saw the process end", got)
	}
}

// A status that arrives with the link reported stalled is the transport's, not the process's.
func TestAStatusReturnedWhileTheLinkWasStalledIsReportedAsTheTransports(t *testing.T) {
	for _, link := range []LinkState{LinkGood, LinkUnspecified} {
		d := decideEnd(endInputs{outcome: functionOutcome{exit: ExitCode(0)}, cause: session.CauseFinished, link: link, caps: Capabilities{ExitReport: ExitReportCodeAndSignal}})
		if d.end.GetExited().GetSource() != execv2.ExitSource_EXIT_SOURCE_PROCESS || len(d.violations) != 0 {
			t.Errorf("link %v: %v %v, want a process exit and no violation", link, d.end, d.violations)
		}
	}
	d := decideEnd(endInputs{outcome: functionOutcome{exit: ExitCode(0)}, cause: session.CauseFinished, link: LinkStalled, caps: Capabilities{ExitReport: ExitReportCodeAndSignal}})
	if d.end.GetExited().GetSource() != execv2.ExitSource_EXIT_SOURCE_TRANSPORT || d.processStatus != true || d.end.RemoteMaySurvive != nil {
		t.Errorf("stalled: %v, want a transport exit with unknown survival", d.end)
	}
	if len(d.violations) != 1 || d.violations[0].kind != violationStalledStatus {
		t.Errorf("stalled: violations %v, want stalled_status once", d.violations)
	}
}
