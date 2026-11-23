package exec_test

import (
	"context"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// Close and cancel leave the process running on Kubernetes (18 of 18 in the proof): Kill is CLOSES_STREAM_ONLY there, and every end
// that the SDK itself makes must say so, as a present and true remote_may_survive, whoever asked for the end. Only a target that
// declared that ending a session terminates the process says it is gone, and a target that declared its Kill
// BEST_EFFORT says it may not be. These tests read the end where a host reads it, on the wire.

// survivalKills are the kinds of Kill a target that runs commands declares, and what an end the SDK made says of the process.
var survivalKills = []struct {
	kill exec.KillBehavior
	want bool // remote_may_survive: present, and this
}{
	{exec.KillTerminates, false},
	{exec.KillBestEffort, true},
	{exec.KillClosesStreamOnly, true},
}

func survivalTarget(kill exec.KillBehavior, fn exec.ExecFunc) exec.Target {
	return engTarget(fn, func(i *exec.TargetInfo) { i.Capabilities.Kill = kill })
}

// checkSurvival fails unless the end says remote_may_survive, present, with the value want.
func checkSurvival(t *testing.T, end *execv2.End, want bool) {
	t.Helper()
	if end.RemoteMaySurvive == nil {
		t.Errorf("remote_may_survive is absent on %v, want present and %v: absent is \"unknown\", and the SDK knows what ending a session does here", end, want)
		return
	}
	if *end.RemoteMaySurvive != want {
		t.Errorf("remote_may_survive is %v on %v, want %v", *end.RemoteMaySurvive, end, want)
	}
}

// Each end of a session that the SDK makes, on the wire.
func TestEveryEndOfASessionThatTheSDKMakesSaysWhetherTheProcessMaySurviveByTheTargetsKill(t *testing.T) {
	for _, tc := range survivalKills {
		for _, end := range []struct {
			name string
			by   execv2.ClosedBy
			make func(t *testing.T, rig *wireRig, conn exec.Conn) (wait func())
		}{
			{"a graceful close", execv2.ClosedBy_CLOSED_BY_REQUEST, func(t *testing.T, _ *wireRig, conn exec.Conn) func() {
				sendClose(t, conn, execv2.CloseMode_CLOSE_MODE_GRACEFUL)
				return func() {}
			}},
			{"a kill", execv2.ClosedBy_CLOSED_BY_REQUEST, func(t *testing.T, _ *wireRig, conn exec.Conn) func() {
				sendClose(t, conn, execv2.CloseMode_CLOSE_MODE_KILL)
				return func() {}
			}},
			{"a retired connection", execv2.ClosedBy_CLOSED_BY_CONNECTION_RETIRED, func(_ *testing.T, rig *wireRig, _ exec.Conn) func() {
				retired := make(chan struct{})
				go func() {
					defer close(retired)
					rig.conns.Retire()
				}()
				return func() { <-retired }
			}},
			{"the shutdown of the plugin", execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN, func(t *testing.T, rig *wireRig, _ exec.Conn) func() {
				shut := make(chan error, 1)
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), engineWait)
					defer cancel()
					shut <- rig.server.Plugin.Shutdown(ctx)
				}()
				return func() {
					if err := awaitValue(t, shut, "the plugin to shut down"); err != nil {
						t.Errorf("Shutdown: %v", err)
					}
				}
			}},
		} {
			t.Run(tc.kill.String()+", "+end.name, func(t *testing.T) {
				rig := newWireRig(t, survivalTarget(tc.kill, blockUntilCancelled))
				conn := rig.mustOpen("s1", withoutTTY)
				wait := end.make(t, rig, conn)
				_, ended := readUntilEnded(t, conn)
				wait()
				if got := ended.GetEnd().GetClosed().GetBy(); got != end.by {
					t.Fatalf("the session ended %v, want Closed by %v", ended.GetEnd(), end.by)
				}
				checkSurvival(t, ended.GetEnd(), tc.want)
			})
		}
	}
}

// The ends of a bounded run that the SDK makes: its timeout, the shutdown of the plugin and the retirement of its connection. A run
// that the host cancels has no answer to read (the call ends with a status of the transport's own), so that end is read at the engine
// below.
func TestEveryEndOfARunThatTheSDKMakesSaysWhetherTheProcessMaySurviveByTheTargetsKill(t *testing.T) {
	for _, tc := range survivalKills {
		// running tells the test that it started and then waits to be ended: each subtest has a channel of its own.
		running := func(started chan<- struct{}) exec.ExecFunc {
			return func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
				started <- struct{}{}
				<-ctx.Done()
				return exec.Exit{}, ctx.Err()
			}
		}
		t.Run(tc.kill.String()+", its timeout", func(t *testing.T) {
			rig := newRunSrvRig(t, survivalTarget(tc.kill, running(make(chan struct{}, 1))))
			res := rig.mustRun(rig.request(runSrvTimeout(30)))
			if res.GetOutcome() != execv2.Outcome_OUTCOME_TIMED_OUT {
				t.Fatalf("the run ended %v, want TIMED_OUT", res.GetOutcome())
			}
			if res.RemoteMaySurvive == nil || *res.RemoteMaySurvive != tc.want {
				t.Errorf("remote_may_survive is %v, want present and %v", res.RemoteMaySurvive, tc.want)
			}
		})
		for _, end := range []struct {
			name    string
			outcome execv2.Outcome
			make    func(t *testing.T, rig *runSrvRig)
		}{
			{"the shutdown of the plugin", execv2.Outcome_OUTCOME_FAILED, func(t *testing.T, rig *runSrvRig) {
				ctx, cancel := context.WithTimeout(context.Background(), engineWait)
				defer cancel()
				if err := rig.server.Plugin.Shutdown(ctx); err != nil {
					t.Errorf("Shutdown: %v", err)
				}
			}},
			{"a retired connection", execv2.Outcome_OUTCOME_FAILED, func(_ *testing.T, rig *runSrvRig) { rig.conns.Retire() }},
		} {
			t.Run(tc.kill.String()+", "+end.name, func(t *testing.T) {
				started := make(chan struct{}, 1)
				rig := newRunSrvRig(t, survivalTarget(tc.kill, running(started)))
				result := make(chan *execv2.RunResult, 1)
				go func() {
					res, err := rig.run(rig.request())
					if err != nil {
						t.Errorf("Run: %v", err)
					}
					result <- res
				}()
				awaitChannel(t, started, "the run to start")
				end.make(t, rig)
				res := awaitValue(t, result, "the run's result")
				if res.GetOutcome() != end.outcome {
					t.Fatalf("the run ended %v, want %v", res.GetOutcome(), end.outcome)
				}
				if res.RemoteMaySurvive == nil || *res.RemoteMaySurvive != tc.want {
					t.Errorf("remote_may_survive is %v, want present and %v", res.RemoteMaySurvive, tc.want)
				}
			})
		}
	}
}

// A host that leaves: the caller of a run goes away, which ends the run with the cancelled cause. Nobody is left to read the end, so it
// is read at the engine, which is where the tombstone of a later step will read it from.
func TestTheEndOfARunWhoseHostLeftSaysWhetherTheProcessMaySurviveByTheTargetsKill(t *testing.T) {
	for _, tc := range survivalKills {
		t.Run(tc.kill.String(), func(t *testing.T) {
			rig := newEngineRig(t, engineRigConfig{}, survivalTarget(tc.kill, blockUntilCancelled))
			ctx, cancel := context.WithCancel(rig.request())
			defer cancel()
			cmd, err := rig.eng.Run(ctx, &execv2.RunRequest{Target: rig.conns.WireTarget(t, "thing", "thing-1"), Argv: []string{"prog"}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			cancel()
			res := awaitDone(t, cmd)
			if by := res.End.GetClosed().GetBy(); by != execv2.ClosedBy_CLOSED_BY_CANCELLED {
				t.Fatalf("the run ended %v, want Closed by CANCELLED", res.End)
			}
			checkSurvival(t, res.End, tc.want)
		})
	}
}
