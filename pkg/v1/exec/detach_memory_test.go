package exec_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A session lives apart from its stream. These tests run in a synctest bubble with the streams in memory: the lease of a detached
// session is read from the bubble's clock, which moves only when every goroutine waits, so a lease that has to end at its timeout
// is seen to end then and not a moment before, and a session or a goroutine that is left behind fails the bubble.

// A Detach ends the stream, with Detached as its last frame and the lease it was given, and the session runs on.
func TestDetachEndsTheStreamWithDetachedAsItsLastFrameAndTheSessionRunsOnForItsLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		host.sendDetachFrame(10_000)
		synctest.Wait()

		frames := host.frames()
		last := lastOf(frames)
		if d := last.GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_REQUESTED || d.GetLeaseExpiresUnixMs() != bubbleNow()+10_000 {
			t.Fatalf("the last frame is %v, want Detached{REQUESTED} whose lease ends in 10 s (%d)", last, bubbleNow()+10_000)
		}
		for _, f := range frames[:len(frames)-1] {
			if f.GetEnded() != nil || f.GetDetached() != nil {
				t.Fatalf("a %s frame before the last one", frameKind(f))
			}
		}
		if err, returned := host.handlerReturned(); !returned || err != nil {
			t.Fatalf("the handler returned %v (returned %v), want the stream to end with status OK after Detached", err, returned)
		}
		if !rig.live("s1") {
			t.Fatal("the session ended with its stream's Detach: Detach leaves the session running")
		}
		info := rig.sessionInfo(t, "s1")
		if info.GetAttached() || info.GetEndedWith() != nil || info.GetLeaseExpiresUnixMs() != bubbleNow()+10_000 {
			t.Fatalf("the session is %v, want detached, not ended, with its lease ending in 10 s", info)
		}
		host.hostLeaves()
		if err := rig.plugin.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// The lease is the host's, or the session's own when the host asks for none, and never more than the target's maximum: what is applied
// is what Detached says, and the session ends exactly then.
func TestTheLeaseOfADetachIsWhatTheHostAskedForTheSessionsOwnOrTheMaximumAndTheSessionEndsExactlyThen(t *testing.T) {
	const minute = time.Minute
	for _, tc := range []struct {
		name   string
		limits func(*exec.TargetInfo)
		start  uint32 // Start.detach_timeout_ms
		ask    uint32 // Detach.lease_ms
		want   time.Duration
	}{
		{"the host's", nil, 0, 20_000, 20 * time.Second},
		{"the session's own, from its start", nil, 7000, 0, 7 * time.Second},
		{"the host's, over the session's own", nil, 7000, 3000, 3 * time.Second},
		{"the target's default for a session with none", nil, 0, 0, 5 * minute},
		{"above the maximum is clamped to it", nil, 0, uint32(31 * minute / time.Millisecond), 30 * minute},
		{"the maximum itself", nil, 0, uint32(30 * minute / time.Millisecond), 30 * minute},
		{"one millisecond above the maximum", nil, 0, uint32(30*minute/time.Millisecond) + 1, 30 * minute},
		{"the smallest", nil, 0, 1, time.Millisecond},
		{"a target's own default and maximum", func(i *exec.TargetInfo) {
			i.Session.DetachTimeout, i.Session.MaxDetachTimeout = 2*minute, 3*minute
		}, 0, uint32(10 * minute / time.Millisecond), 3 * minute},
		{"a target's own default", func(i *exec.TargetInfo) {
			i.Session.DetachTimeout, i.Session.MaxDetachTimeout = 2*minute, 3*minute
		}, 0, 0, 2 * minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var mods []func(*exec.TargetInfo)
				if tc.limits != nil {
					mods = append(mods, tc.limits)
				}
				rig := newMemoryRig(t, engTarget(blockUntilCancelled, mods...))
				host := rig.streamOf(rig.startFrame("s1", func(s *execv2.Start) { s.DetachTimeoutMs = tc.start }), nil)
				host.opened(t)
				begin := bubbleNow()
				host.sendDetachFrame(tc.ask)
				synctest.Wait()
				d := lastOf(host.frames()).GetDetached()
				if d == nil || d.GetLeaseExpiresUnixMs() != begin+tc.want.Milliseconds() {
					t.Fatalf("Detached %v, want a lease that ends %v from now (%d)", d, tc.want, begin+tc.want.Milliseconds())
				}
				time.Sleep(tc.want - time.Millisecond)
				synctest.Wait()
				if !rig.live("s1") {
					t.Fatalf("the session ended before its lease of %v ran out", tc.want)
				}
				time.Sleep(time.Millisecond)
				synctest.Wait()
				if rig.live("s1") {
					t.Fatalf("the session was still live when its lease of %v ran out", tc.want)
				}
				host.hostLeaves()
				_ = rig.plugin.Shutdown(context.Background())
			})
		})
	}
}

// When the lease runs out the session is closed with DETACH_TIMEOUT, its function is told, and no stream is sent an Ended: the end is
// the tombstone's.
func TestTheLeaseEndsADetachedSessionClosedByDetachTimeoutAndNobodyIsSentAnEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cancelled := make(chan struct{})
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
			<-ctx.Done()
			close(cancelled)
			return exec.Exit{}, ctx.Err()
		}))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		host.sendDetachFrame(5000)
		synctest.Wait()
		sent := host.frames()
		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-cancelled:
		default:
			t.Fatal("the function was not told to stop when the lease ran out")
		}
		info := rig.sessionInfo(t, "s1")
		if closed := info.GetEndedWith().GetEnd().GetClosed(); closed.GetBy() != execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
			t.Fatalf("the tombstone says %v, want Closed by DETACH_TIMEOUT", info.GetEndedWith())
		}
		if later := host.frames(); len(later) != 0 {
			t.Fatalf("a stream that had left was sent %d frames after it, the end of the session among them: %v", len(later), later)
		}
		for _, f := range sent {
			if f.GetEnded() != nil {
				t.Fatal("an Ended frame was sent to a stream that left before the session ended")
			}
		}
		host.hostLeaves()
	})
}

// An attach during the lease cancels it: the session is not ended at the time the lease would have run out, and the lease of a later
// detach starts afresh.
func TestAnAttachDuringTheLeaseCancelsItAndTheLeaseOfALaterDetachStartsAfresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		first := rig.streamOf(rig.startFrame("s1"), nil)
		first.opened(t)
		first.sendDetachFrame(10_000)
		synctest.Wait()
		time.Sleep(5 * time.Second)
		second := rig.streamOf(rig.attachFrame("s1", 0), nil)
		second.opened(t)
		synctest.Wait()
		if info := rig.sessionInfo(t, "s1"); !info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 {
			t.Fatalf("after the attach the session is %v, want attached with no lease running", info)
		}

		time.Sleep(10 * time.Second) // 15 s in: the first lease would have ended the session at 10 s
		synctest.Wait()
		if !rig.live("s1") {
			t.Fatal("the lease that the attach cancelled ended the session")
		}
		second.sendDetachFrame(10_000)
		synctest.Wait()
		time.Sleep(9_999 * time.Millisecond)
		synctest.Wait()
		if !rig.live("s1") {
			t.Fatal("the session ended before the lease of the second detach ran out: it counted from the first")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if rig.live("s1") {
			t.Fatal("the session lived on past the lease of the second detach")
		}
		first.hostLeaves()
		second.hostLeaves()
	})
}

// A stream that just breaks is an implicit Detach with the session's own lease: its function is not told to stop, and the session ends
// when the lease does.
func TestAStreamThatBreaksIsAnImplicitDetachWithTheSessionsOwnLease(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start uint32
		want  time.Duration
	}{
		{"the lease of its start", 9000, 9 * time.Second},
		{"the target's default", 0, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cancelled := make(chan struct{})
				rig := newMemoryRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
					<-ctx.Done()
					close(cancelled)
					return exec.Exit{}, ctx.Err()
				}))
				host := rig.streamOf(rig.startFrame("s1", func(s *execv2.Start) { s.DetachTimeoutMs = tc.start }), nil)
				host.opened(t)
				begin := bubbleNow()
				host.cancel() // the host's connection broke
				synctest.Wait()
				select {
				case <-cancelled:
					t.Fatal("the function was told to stop when its host's stream broke: a broken stream is a detach")
				default:
				}
				info := rig.sessionInfo(t, "s1")
				if info.GetAttached() || info.GetLeaseExpiresUnixMs() != begin+tc.want.Milliseconds() {
					t.Fatalf("the session is %v, want detached with a lease of %v (ending at %d)", info, tc.want, begin+tc.want.Milliseconds())
				}
				time.Sleep(tc.want)
				synctest.Wait()
				if rig.live("s1") {
					t.Fatal("the session was still live when the lease ran out")
				}
			})
		})
	}
}

// A host that closes its side of the stream, which no Detach said, is an implicit detach too, and the stream ends with a last frame:
// the host can still receive it.
func TestAHostThatClosesItsSideOfTheStreamIsAnImplicitDetachAndTheStreamEndsWithDetached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		begin := bubbleNow()
		host.hostLeaves()
		synctest.Wait()
		last := lastOf(host.frames())
		if d := last.GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_UNSPECIFIED || d.GetLeaseExpiresUnixMs() != begin+5*time.Minute.Milliseconds() {
			t.Fatalf("the last frame is %v, want Detached with the session's default lease (5 minutes) and no reason: the host sent no Detach", last)
		}
		if err, returned := host.handlerReturned(); !returned || err != nil {
			t.Fatalf("the handler returned %v (returned %v), want status OK", err, returned)
		}
		if !rig.live("s1") {
			t.Fatal("the session ended with its host's half-close")
		}
		_ = rig.plugin.Shutdown(context.Background())
	})
}

// A protocol violation still ends the stream with its status and destroys the session with it: it is not a detach, and no lease runs.
func TestAProtocolViolationStillEndsTheStreamTypedAndDestroysTheSession(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame *execv2.ClientFrame
		class exec.ErrorClass
	}{
		{"a second open frame", &execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: &execv2.OpenRequest{SessionId: "s2"}}}, exec.ErrorClassInvalidRequest},
		{"an input frame over the maximum", &execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: make([]byte, 5000)}}, exec.ErrorClassLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cancelled := make(chan struct{})
				rig := newMemoryRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
					<-ctx.Done()
					close(cancelled)
					return exec.Exit{}, ctx.Err()
				}, func(i *exec.TargetInfo) { i.Session.MaxInputFrame = 4096 }))
				host := rig.streamOf(rig.startFrame("s1"), nil)
				host.opened(t)
				host.send(tc.frame)
				synctest.Wait()
				err, returned := host.handlerReturned()
				if !returned || status.Code(err) == codes.OK {
					t.Fatalf("the handler returned %v (returned %v), want the status the violation earned", err, returned)
				}
				var typed *exec.Error
				if !errors.As(exec.StatusError(err), &typed) || typed.Class != tc.class {
					t.Fatalf("the stream ended with %v, want class %v", err, tc.class)
				}
				for _, f := range host.frames() {
					if f.GetEnded() != nil || f.GetDetached() != nil {
						t.Fatalf("a stream that broke the protocol was sent %s: it ends with its status", frameKind(f))
					}
				}
				select {
				case <-cancelled:
				default:
					t.Fatal("the session was not destroyed with the stream that broke the protocol")
				}
				if rig.live("s1") {
					t.Fatal("the session is still live")
				}
				// A destroyed session has no lease: the clock can run on and nothing is left to end.
				time.Sleep(10 * time.Minute)
			})
		})
	}
}

// Close destroys the session and its stream ends with Ended: and across the streams a session had, exactly one Ended is sent, to the
// one attached when it ended; the stream that was superseded ends with Detached and never with Ended.
func TestEveryStreamOfASessionEndsWithExactlyOneOfEndedOrDetachedAndTheSessionHasOneEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		first := rig.streamOf(rig.startFrame("s1"), nil)
		first.opened(t)
		second := rig.streamOf(rig.attachFrame("s1", 0), nil)
		second.opened(t)
		second.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
		synctest.Wait()

		firstFrames, secondFrames := first.frames(), second.frames()
		if last := endsWith(t, secondFrames); last.GetEnded().GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Fatalf("the stream that closed the session ended with %v, want Ended{closed REQUEST}", last)
		}
		if last := endsWith(t, firstFrames); last.GetDetached().GetReason() != execv2.DetachReason_DETACH_REASON_SUPERSEDED || last.GetDetached().GetLeaseExpiresUnixMs() != 0 {
			t.Fatalf("the stream that was superseded ended with %v, want Detached{SUPERSEDED} with no lease", last)
		}
		ended := 0
		for _, f := range append(firstFrames, secondFrames...) {
			if f.GetEnded() != nil {
				ended++
			}
		}
		if ended != 1 {
			t.Fatalf("%d Ended frames across the streams of one session, want exactly one", ended)
		}
		first.hostLeaves()
		second.hostLeaves()
	})
}

// A Detach and a way of destroying the session that comes from elsewhere, in either order, leave the session in the same place: ended
// by a request, its end the tombstone's, its place free.
func TestDetachThenEndSessionAndCloseThenDetachReachTheSameFinalState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))

		// Detach, then EndSession.
		a := rig.streamOf(rig.startFrame("a"), nil)
		a.opened(t)
		a.sendDetachFrame(0)
		synctest.Wait()
		if _, err := rig.server.EndSession(rig.callContext(), &execv2.EndSessionRequest{SessionId: "a"}); err != nil {
			t.Fatalf("EndSession: %v", err)
		}
		endA := rig.sessionInfo(t, "a")

		// Close, then Detach, on one stream: the Close decided the end, so the Detach after it is moot and the stream is told Ended.
		b := rig.streamOf(rig.startFrame("b"), nil)
		b.opened(t)
		b.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
		b.sendDetachFrame(0)
		synctest.Wait()
		if last := endsWith(t, b.frames()); last.GetEnded() == nil {
			t.Fatalf("the stream that closed its session ended with %v, want Ended: a Detach after a Close is moot", last)
		}
		endB := rig.sessionInfo(t, "b")

		for _, info := range []*execv2.SessionInfo{endA, endB} {
			if info.GetEndedWith() == nil || info.GetAttached() || info.GetLeaseExpiresUnixMs() != 0 {
				t.Fatalf("the session %q is %v, want ended, with no stream and no lease", info.GetFacts().GetSessionId(), info)
			}
		}
		if !proto.Equal(endA.GetEndedWith().GetEnd(), endB.GetEndedWith().GetEnd()) {
			t.Fatalf("the two ended differently: %v and %v", endA.GetEndedWith().GetEnd(), endB.GetEndedWith().GetEnd())
		}
		if closed := endA.GetEndedWith().GetEnd().GetClosed(); closed.GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Fatalf("the end is %v, want Closed by REQUEST", endA.GetEndedWith().GetEnd())
		}
		if len(exec.PluginLive(rig.plugin)) != 0 {
			t.Fatalf("the engine still holds %v", exec.PluginLive(rig.plugin))
		}
		a.hostLeaves()
		b.hostLeaves()
	})
}

// A Detach that arrives after the end of the session was decided is moot: the stream is the one the end goes to, and it is told Ended.
// The engine is held between the decision and the release of the id, which is the window.
func TestADetachThatArrivesAfterTheEndWasDecidedIsMootAndTheStreamIsToldEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := exectest.NewGate(), exectest.NewGate()
		rig := newMemoryRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }))
		exec.SetPluginEngineSeams(rig.plugin, exec.EngineSeams{EndEmitted: func() {
			entered.Open()
			<-release.Opened()
		}})
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()
		awaitChannel(t, entered.Opened(), "the end to be decided")
		host.sendDetachFrame(1000)
		synctest.Wait()
		release.Open()
		synctest.Wait()
		last := endsWith(t, host.frames())
		if last.GetEnded().GetEnd().GetExited().GetExitCode() != 0 || last.GetEnded().GetEnd().GetExited() == nil {
			t.Fatalf("the stream ended with %v, want Ended{exited 0}: the Detach came after the end was decided", last)
		}
		host.hostLeaves()
	})
}

// A Detach that came before the end wins: the stream is told Detached and nobody is told Ended, and the end that follows is the
// tombstone's.
func TestADetachThatCameBeforeTheSessionEndedWinsAndTheEndIsTheTombstones(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := exectest.NewGate()
		rig := newMemoryRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			<-gate.Opened()
			return exec.ExitCode(7), nil
		}))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		host.sendDetachFrame(60_000)
		synctest.Wait()
		sent := host.frames()
		gate.Open()
		synctest.Wait()
		for _, f := range append(sent, host.frames()...) {
			if f.GetEnded() != nil {
				t.Fatal("an Ended was sent to a stream that had detached before the session ended")
			}
		}
		info := rig.sessionInfo(t, "s1")
		if exited := info.GetEndedWith().GetEnd().GetExited(); exited.GetExitCode() != 7 {
			t.Fatalf("the tombstone says %v, want Exited{7}", info.GetEndedWith().GetEnd())
		}
		if rig.live("s1") {
			t.Fatal("the session is still live")
		}
		host.hostLeaves()
	})
}

// A detached session holds its place, and a tombstone only its id: the place is free the moment the session ended.
func TestADetachedSessionHoldsItsPlaceAndATombstoneHoldsOnlyItsId(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) { i.Session.MaxSessions = 1 }))
		first := rig.streamOf(rig.startFrame("s1"), nil)
		first.opened(t)
		first.sendDetachFrame(0)
		synctest.Wait()

		refused := rig.streamOf(rig.startFrame("s2"), nil)
		synctest.Wait()
		if err, returned := refused.handlerReturned(); !returned || !isRefusalOf(exec.StatusError(err), exec.ErrorClassLimit, exec.ErrorReasonTooManySessions) {
			t.Fatalf("a start with the only place held by a detached session: %v (returned %v), want LIMIT / TOO_MANY_SESSIONS", err, returned)
		}
		if _, err := rig.server.EndSession(rig.callContext(), &execv2.EndSessionRequest{SessionId: "s1", Mode: execv2.CloseMode_CLOSE_MODE_KILL}); err != nil {
			t.Fatalf("EndSession: %v", err)
		}

		// The session ended and its tombstone lives: another id is admitted at once, and the same id is not.
		same := rig.streamOf(rig.startFrame("s1"), nil)
		synctest.Wait()
		err, returned := same.handlerReturned()
		var typed *exec.Error
		if !returned || !errors.As(exec.StatusError(err), &typed) || typed.Class != exec.ErrorClassConflict || typed.Reason != exec.ErrorReasonSessionExists {
			t.Fatalf("a start of the id of a session that ended: %v (returned %v), want CONFLICT / SESSION_EXISTS while its tombstone lives", err, returned)
		}
		other := rig.streamOf(rig.startFrame("s3"), nil)
		other.opened(t)
		other.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_KILL)
		synctest.Wait()
		first.hostLeaves()
		other.hostLeaves()
	})
}

// A function that ignores its context holds its place after the lease ended its session, as after any end: the abandoned function is
// counted and its place is held until it returns, and the target says why it refuses (the places are held by functions that were
// given up on) and not that it is full.
func TestTheAbandonedFunctionOfADetachedSessionKeepsItsPlaceUntilItReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := exectest.NewGate()
		t.Cleanup(gate.Open)
		rig := newMemoryRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			<-gate.Opened() // ignores its context
			return exec.Exit{}, nil
		}, func(i *exec.TargetInfo) { i.Session.MaxSessions = 1 }))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		host.sendDetachFrame(1000)
		synctest.Wait()
		time.Sleep(time.Second)      // the lease runs out and the session is ended
		time.Sleep(10 * time.Second) // the function ignored its context for longer than the grace (5 s): it is given up on
		synctest.Wait()
		if rig.live("s1") {
			t.Fatal("the session is still live after the grace: the engine did not give up on a function that ignores its context")
		}
		info := rig.sessionInfo(t, "s1")
		if info.GetEndedWith().GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
			t.Fatalf("the tombstone says %v, want Closed by DETACH_TIMEOUT", info.GetEndedWith().GetEnd())
		}

		refused := rig.streamOf(rig.startFrame("s2"), nil)
		synctest.Wait()
		err, returned := refused.handlerReturned()
		var typed *exec.Error
		if !returned || !errors.As(exec.StatusError(err), &typed) || typed.Class != exec.ErrorClassUnavailable || typed.Reason != exec.ErrorReasonTargetWedged {
			t.Fatalf("a start while an abandoned function holds the only place: %v (returned %v), want UNAVAILABLE / TARGET_WEDGED", err, returned)
		}
		gate.Open()
		synctest.Wait()
		again := rig.streamOf(rig.startFrame("s3"), nil)
		again.opened(t)
		again.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_KILL)
		synctest.Wait()
		host.hostLeaves()
		again.hostLeaves()
	})
}

// Plugin.Shutdown ends the detached sessions too, with PLUGIN_SHUTDOWN, and each leaves its tombstone; the stream that is attached is
// sent its Ended as it was.
func TestShutdownEndsDetachedSessionsClosedByPluginShutdownAndTombstonesThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		attached := rig.streamOf(rig.startFrame("attached"), nil)
		attached.opened(t)
		detached := rig.streamOf(rig.startFrame("detached"), nil)
		detached.opened(t)
		detached.sendDetachFrame(0)
		synctest.Wait()
		detached.frames()

		if err := rig.plugin.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if last := endsWith(t, attached.frames()); last.GetEnded().GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN {
			t.Fatalf("the attached stream ended with %v, want Ended{closed PLUGIN_SHUTDOWN}", last)
		}
		if got := detached.frames(); len(got) != 0 {
			t.Fatalf("the stream that had left was sent %v at shutdown", got)
		}
		info := rig.sessionInfo(t, "detached")
		if info.GetEndedWith().GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN {
			t.Fatalf("the detached session's tombstone says %v, want Closed by PLUGIN_SHUTDOWN", info.GetEndedWith().GetEnd())
		}
		attached.hostLeaves()
		detached.hostLeaves()
	})
}

// Every end of a session says whether its process may survive, from the capability of the session and never from the way it ended or
// whether a stream was attached: a target that only closes its stream (Kubernetes, Docker) leaves the process running after a lease, a
// request, a retirement and a shutdown alike, and the end says so (present and true); a target whose close terminates the process
// says present and false; a best-effort one says true. The tombstone carries the end as the frame would have.
func TestEveryEndOfADetachedSessionSaysWhetherItsProcessMaySurviveFromTheCapability(t *testing.T) {
	type endWay struct {
		name string
		by   execv2.ClosedBy
		end  func(t *testing.T, rig *memoryRig)
	}
	ways := []endWay{
		{"the lease", execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT, func(t *testing.T, _ *memoryRig) { time.Sleep(5 * time.Minute) }},
		{"EndSession", execv2.ClosedBy_CLOSED_BY_REQUEST, func(t *testing.T, rig *memoryRig) {
			if _, err := rig.server.EndSession(rig.callContext(), &execv2.EndSessionRequest{SessionId: "s1"}); err != nil {
				t.Fatalf("EndSession: %v", err)
			}
		}},
		{"the retirement of the connection", execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, func(t *testing.T, rig *memoryRig) { rig.conns.Retire() }},
		{"the plugin's shutdown", execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN, func(t *testing.T, rig *memoryRig) {
			if err := rig.plugin.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		}},
	}
	kills := []struct {
		kill exec.KillBehavior
		want bool
	}{
		{exec.KillClosesStreamOnly, true},
		{exec.KillBestEffort, true},
		{exec.KillTerminates, false},
	}
	for _, k := range kills {
		for _, way := range ways {
			t.Run(fmt.Sprintf("%s/%s", k.kill, way.name), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					rig := newMemoryRig(t, engTarget(blockUntilCancelled, func(i *exec.TargetInfo) { i.Capabilities.Kill = k.kill }))
					host := rig.streamOf(rig.startFrame("s1"), nil)
					host.opened(t)
					host.sendDetachFrame(0)
					synctest.Wait()
					way.end(t, rig)
					synctest.Wait()
					end := rig.sessionInfo(t, "s1").GetEndedWith().GetEnd()
					if end.GetClosed().GetBy() != way.by {
						t.Fatalf("the end is %v, want Closed by %v", end, way.by)
					}
					if end.RemoteMaySurvive == nil || end.GetRemoteMaySurvive() != k.want {
						t.Fatalf("remote_may_survive is %v (present %v), want present and %v for %v", end.GetRemoteMaySurvive(), end.RemoteMaySurvive != nil, k.want, k.kill)
					}
					host.hostLeaves()
					_ = rig.plugin.Shutdown(context.Background())
				})
			})
		}
	}
}

// What ListSessions says of a live session: a stream attached or not, where its output ends and where the ring begins, when it last
// moved, and the lease of a detached one.
func TestListSessionsSaysWhatAStreamAndALeaseAreAndWhenTheSessionLastMoved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(echoFunction))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()
		info := rig.sessionInfo(t, "s1")
		if !info.GetAttached() || info.GetNextOffset() != 0 || info.GetReplayStart() != 0 || info.GetLastActivityUnixMs() != 0 || info.GetEndedWith() != nil || info.GetLeaseExpiresUnixMs() != 0 {
			t.Fatalf("a new session: %v, want attached, nothing written, no activity, no end and no lease", info)
		}
		if info.GetFacts().GetSessionId() != "s1" || info.GetFacts().GetTarget().GetProvider() != "fake" {
			t.Fatalf("a new session's facts %v", info.GetFacts())
		}

		time.Sleep(3 * time.Second)
		host.send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: []byte("hello")}})
		synctest.Wait()
		moved := bubbleNow()
		info = rig.sessionInfo(t, "s1")
		if info.GetNextOffset() != 5 || info.GetLastActivityUnixMs() != moved {
			t.Fatalf("after 5 bytes of echo: next_offset %d, last_activity %d, want 5 and %d", info.GetNextOffset(), info.GetLastActivityUnixMs(), moved)
		}

		time.Sleep(time.Second)
		host.sendDetachFrame(30_000)
		synctest.Wait()
		info = rig.sessionInfo(t, "s1")
		if info.GetAttached() || info.GetLeaseExpiresUnixMs() != bubbleNow()+30_000 || info.GetLastActivityUnixMs() != moved {
			t.Fatalf("a detached session: %v, want no stream, a lease of 30 s and the activity of 1 s ago (%d)", info, moved)
		}
		host.hostLeaves()
		_ = rig.plugin.Shutdown(context.Background())
	})
}
