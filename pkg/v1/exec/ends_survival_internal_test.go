package exec

import (
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/session"
)

// What an end that the SDK made says of the process follows the session's Kill and no other fact: not the cause (a close, a kill, a host
// that left, a lease that ran out, a retirement, a shutdown, a timeout), and not what the function returned afterwards. The detach
// timeout is the cause of a step that is not served yet (nothing produces it), so it is held here, at the decision of the end, until
// the wire can say it.
func TestEveryCauseThatEndsASessionSaysTheSurvivalOfTheProcessByTheKillAndNotByTheCause(t *testing.T) {
	causes := []session.Cause{
		session.CauseRequested, session.CauseShutdown, session.CauseRetired, session.CauseExpired, session.CauseTimeout, session.CauseCancelled,
	}
	for _, tc := range []struct {
		kill KillBehavior
		want *bool
	}{
		{KillTerminates, new(false)},
		{KillBestEffort, new(true)},
		{KillClosesStreamOnly, new(true)},
		{KillUnspecified, nil},
	} {
		for _, cause := range causes {
			for _, outcome := range []functionOutcome{{}, {exit: ExitCode(0)}, {exit: ExitLost(LostNoStatus)}, {err: &Error{Class: ErrorClassInternal}}} {
				end := decideEnd(endInputs{outcome: outcome, cause: cause, caps: Capabilities{Kill: tc.kill}}).end
				got := end.RemoteMaySurvive
				if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
					t.Errorf("Kill %v, cause %v, function returned %+v: remote_may_survive %v, want %v", tc.kill, cause, outcome, got, tc.want)
				}
				if end.GetClosed() == nil {
					t.Errorf("Kill %v, cause %v: the end is %v, want Closed", tc.kill, cause, end)
				}
			}
		}
	}
}
