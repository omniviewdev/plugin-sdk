package exec_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Close and kill on the wire (plan section 4.5): Close{graceful} cancels the function's context and waits the grace; Close{kill}
// cancels it and waits the kill wait, which is far shorter, and then gives up on a function that has not returned. Both end the
// session Closed{REQUEST}, and what the end says of the remote process follows the Kill behaviour the session declares.

// A kill frame ends a function that ignores its context at the kill wait, and a graceful close ends it at the grace: the clock of the
// bubble is exact, so the time a close takes is the bound that applied to it and no other.
func TestACloseFrameEndsAFunctionThatIgnoresItsContextAtTheBoundThatAppliesToIt(t *testing.T) {
	const grace, kill = 4 * time.Second, 70 * time.Millisecond
	for _, tc := range []struct {
		name string
		mode execv2.CloseMode
		want time.Duration
	}{
		{"a kill", execv2.CloseMode_CLOSE_MODE_KILL, kill},
		{"a graceful close", execv2.CloseMode_CLOSE_MODE_GRACEFUL, grace},
		{"a close with no mode, which reads as graceful", execv2.CloseMode_CLOSE_MODE_UNSPECIFIED, grace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate := ctlGate(t)
				rig := newMemoryRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
					<-gate.Opened() // ignores its context
					return exec.ExitCode(0), nil
				}))
				exec.ConfigurePluginEngine(rig.plugin, exechook.EngineConfig{CloseGrace: grace, KillWait: kill})
				host := rig.ctlPlainSession("s1")
				synctest.Wait()
				start := time.Now()
				host.ctlSend(ctlClose(tc.mode))
				synctest.Wait()
				time.Sleep(tc.want - time.Millisecond)
				synctest.Wait()
				if ended := ctlEnded(host.ctlFrames()); ended != nil {
					t.Fatalf("Ended after %v, before the bound of %v: %v", time.Since(start), tc.want, ended)
				}
				time.Sleep(2 * time.Millisecond)
				synctest.Wait()
				ended := ctlEnded(host.ctlFrames())
				if ended == nil {
					t.Fatalf("no Ended at %v, the bound that applies to %s", time.Since(start), tc.name)
				}
				if ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
					t.Errorf("the end is %v, want closed by request", ended.GetEnd())
				}
				if took := time.Since(start); took != tc.want+time.Millisecond {
					t.Errorf("Ended at %v, want the bound of %v", took, tc.want)
				}
			})
		})
	}
}

// A close or a kill frame that is behind input the function does not read is not seen until the function reads it (the receiver
// applies the frames in order and stops while the command's input queue is full), and the way out is to cancel the stream, which is an
// implicit detach: the session lives for its lease, and EndSession ends it, with a kill if the function ignores its context. The
// frames are never reordered: once the function reads, the close is applied.
func TestACloseFrameBehindInputTheFunctionDoesNotReadWaitsAndCancellingTheStreamDetachesAndEndSessionEndsTheSession(t *testing.T) {
	const frame, frames = 60 << 10, 5 // 300 KiB: more than the 256 KiB the command's input queue holds
	feed := func(host *memoryStream) {
		for range frames {
			host.ctlSend(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: bytes.Repeat([]byte{'x'}, frame)}})
		}
		host.ctlSend(ctlClose(execv2.CloseMode_CLOSE_MODE_KILL))
	}
	t.Run("the function reads at last", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			gate := ctlGate(t)
			rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
				<-gate.Opened()
				_, _ = io.Copy(io.Discard, p.Stdin)
				<-ctx.Done()
				return exec.Exit{}, ctx.Err()
			}))
			host := rig.ctlPlainSession("s1")
			feed(host)
			synctest.Wait()
			time.Sleep(time.Minute)
			synctest.Wait()
			if ended := ctlEnded(host.ctlFrames()); ended != nil {
				t.Fatalf("the session ended (%v) behind input that nobody reads: the close frame waits behind it", ended)
			}
			gate.Open()
			synctest.Wait()
			if ended := ctlEnded(host.ctlFrames()); ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
				t.Errorf("Ended %v once the function read the input, want closed by request: the close was applied after it", ended)
			}
		})
	})
	t.Run("the host cancels the stream and ends the session", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			cancelled := make(chan struct{})
			rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
				<-ctx.Done() // never reads its input
				close(cancelled)
				return exec.Exit{}, ctx.Err()
			}))
			host := rig.ctlPlainSession("s1")
			feed(host)
			synctest.Wait()
			select {
			case <-cancelled:
				t.Fatal("the session ended behind input that nobody reads")
			default:
			}
			host.cancel()
			synctest.Wait()
			if returned, _ := host.ctlStatus(); !returned {
				t.Fatal("cancelling the stream did not end the stream: its receiver waits for room in the input queue, and the cancel must release it")
			}
			select {
			case <-cancelled:
				t.Fatal("cancelling the stream ended the session: a stream that is cancelled is an implicit detach, and the session lives for its lease")
			default:
			}
			if info := rig.sessionInfo(t, "s1"); info.GetAttached() || info.GetEndedWith() != nil || info.GetLeaseExpiresUnixMs() == 0 {
				t.Fatalf("the session is %v, want detached, with a lease, and not ended", info)
			}
			if err := rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL); err != nil {
				t.Fatalf("EndSession: %v", err)
			}
			synctest.Wait()
			select {
			case <-cancelled:
			default:
				t.Error("EndSession did not end the session: its function was not told to stop")
			}
			if by := rig.sessionInfo(t, "s1").GetEndedWith().GetEnd().GetClosed().GetBy(); by != execv2.ClosedBy_CLOSED_BY_REQUEST {
				t.Errorf("the session ended %v, want closed by request", rig.sessionInfo(t, "s1").GetEndedWith().GetEnd())
			}
		})
	})
}

// What an end that was asked for says of the remote process follows the Kill behaviour the session declares (what it was opened with,
// narrowed by what it reported): false only when ending the session terminates the process, true when it only usually does or only
// closes the stream, for a graceful close and a kill alike.
func TestEndedClosedSaysRemoteMaySurviveAsTheSessionsKillBehaviourAgrees(t *testing.T) {
	cases := []struct {
		name    string
		kill    exec.KillBehavior
		narrow  *exec.KillBehavior
		mayLive bool
	}{
		{"terminates", exec.KillTerminates, nil, false},
		{"best effort", exec.KillBestEffort, nil, true},
		{"closes the stream only", exec.KillClosesStreamOnly, nil, true},
		{"terminates, narrowed to closes the stream only", exec.KillTerminates, new(exec.KillClosesStreamOnly), true},
		{"terminates, narrowed to best effort", exec.KillTerminates, new(exec.KillBestEffort), true},
	}
	for _, tc := range cases {
		for _, mode := range []execv2.CloseMode{execv2.CloseMode_CLOSE_MODE_GRACEFUL, execv2.CloseMode_CLOSE_MODE_KILL} {
			t.Run(tc.name+", "+mode.String(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					rig := newMemoryRig(t, ctlTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
						if tc.narrow != nil {
							narrowed := p.Capabilities
							narrowed.Kill = *tc.narrow
							p.Started(exec.SessionFacts{Capabilities: &narrowed})
						}
						<-ctx.Done()
						return exec.Exit{}, ctx.Err()
					}, func(c *exec.Capabilities) { c.Kill = tc.kill }))
					host := rig.ctlPlainSession("s1")
					synctest.Wait()
					host.ctlSend(ctlClose(mode))
					synctest.Wait()
					ended := ctlEnded(host.ctlFrames())
					if ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
						t.Fatalf("Ended %v, want closed by request", ended)
					}
					survive := ended.GetEnd().RemoteMaySurvive
					if survive == nil || *survive != tc.mayLive {
						t.Errorf("remote_may_survive is %v, want %v for a session whose Kill is %s", survive, tc.mayLive, tc.name)
					}
				})
			})
		}
	}
}
