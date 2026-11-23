package exec_test

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A lease that runs out and an attach, at the same moment: an attach cancels the lease and a lease that has run out ends only a session
// that nobody attached to, both decided under one lock, so exactly one of them wins. The lease timer is made by hand, so that it runs
// out at the moment the test chooses, with an attach in flight; the clock is not involved.
//
//   - the attach wins: the session lives, attached, and it ends when its host says (Closed by REQUEST), never by the lease, however late
//     the lease's goroutine gets to run;
//   - the lease wins: the session ended by DETACH_TIMEOUT, no stream was sent an Ended, and the attach was refused as unknown.
func TestALeaseThatRunsOutAndAnAttachAtTheSameMomentHaveExactlyOneWinner(t *testing.T) {
	iterations := proofScaled(400, 120)
	rig := newSessionRig(t, exechook.EngineConfig{}, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) {
		i.Session = exec.DefaultSessionLimits()
		i.Session.MaxSessions = 8
	}))
	attachWon, leaseWon := 0, 0
	for i := range iterations {
		id := fmt.Sprintf("lease-%d", i)
		first := rig.mustOpen(id, withoutTTY)
		sendDetach(t, first, 1000)
		endsWith(t, drainToEnd(t, first))
		timer := rig.leases.next(t) // the lease of that Detach

		type attached struct {
			conn exec.Conn
			err  error
		}
		got := make(chan attached, 1)
		gate := make(chan struct{})
		go func() {
			<-gate
			conn, err := rig.attach(context.Background(), id, 0)
			got <- attached{conn, err}
		}()
		// The lease runs out a little after the attach starts, a little more each time: the delay is a sweep across the time an attach
		// takes (a stream, a frame, a lock), so that the two meet at every point of it. Nothing is asserted about the delay.
		delay := time.Duration(i%40) * 10 * time.Microsecond
		go func() {
			<-gate
			for end := time.Now().Add(delay); time.Now().Before(end); {
				runtime.Gosched() // a processor is given to the attach meanwhile, which a spin would not do on one
			}
			timer.fire()
		}()
		close(gate)
		res := <-got

		if res.err == nil {
			attachWon++
			t.Cleanup(func() { _ = res.conn.Close() })
			// The session lives and is attached to the stream: it ends only when the host says so.
			sendClose(t, res.conn, execv2.CloseMode_CLOSE_MODE_GRACEFUL)
			frames := drainToEnd(t, res.conn)
			ended := endsWith(t, frames).GetEnded()
			if ended == nil || ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
				t.Fatalf("iteration %d: the attach was accepted and the session ended %v, want Closed by REQUEST: the lease that ran out ended a session that a stream was attached to", i, lastFrame(t, frames))
			}
			continue
		}
		leaseWon++
		if !isRefusalOf(res.err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Fatalf("iteration %d: the attach was refused with %v, want NOT_FOUND / SESSION_UNKNOWN: the lease had ended the session", i, res.err)
		}
		var info *execv2.SessionInfo
		if !proofPoll(engineWait, func() bool {
			info = proofListed(rig.client, id)
			return info != nil && info.GetEndedWith() != nil
		}) {
			t.Fatalf("iteration %d: the attach was refused as unknown and the session is %v: nothing ended it", i, info)
		}
		if closed := info.GetEndedWith().GetEnd().GetClosed(); closed.GetBy() != execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
			t.Fatalf("iteration %d: the session ended %v, want Closed by DETACH_TIMEOUT", i, info.GetEndedWith().GetEnd())
		}
	}
	t.Logf("%d iterations: the attach won %d times and the lease %d times", iterations, attachWon, leaseWon)
	if !proofPoll(engineWait, func() bool { return len(exec.PluginLive(rig.server.Plugin)) == 0 }) {
		t.Errorf("the plugin still holds %v", exec.PluginLive(rig.server.Plugin))
	}
}
