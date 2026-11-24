package exec_test

import (
	"context"
	"slices"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A run is not a session. The engine holds both under one id space, and a session outlives its stream, is listed, read, attached to
// and ended by id, and leaves a tombstone that holds its id; a run is bound to its call, and none of that is true of it: while it
// lives the calls that manage sessions do not know it (and EndSession must not end it), when its caller goes away it ends with the
// call (there is no stream to detach, and no lease), and when it has ended it leaves no tombstone, no listing and nothing to read, and
// its id is free the moment its result is handed over.
func TestARunIsNotASessionItHasNoTombstoneItEndsWithItsCallAndItsIDIsFree(t *testing.T) {
	hold := newRunSrvHold(t)
	rig := newRunSrvRig(t, engTarget(hold.run, runSrvLimits(exec.RunLimits{MaxConcurrent: 4})))
	v2 := rig.server.V2(t)
	plugin := rig.server.Plugin
	host := rig.conns.Context(context.Background())

	// What the calls that manage sessions say of the id of a run: that no such session exists, whether the run lives or has ended.
	unknown := func(when string) {
		t.Helper()
		_, err := v2.ReadOutput(host, &execv2.ReadOutputRequest{SessionId: "r1"})
		if !isRefusalOf(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Errorf("%s: ReadOutput of the id of a run: %v, want NOT_FOUND / SESSION_UNKNOWN", when, err)
		}
		err = v2.EndSession(host, &execv2.EndSessionRequest{SessionId: "r1", Mode: execv2.CloseMode_CLOSE_MODE_KILL})
		if !isRefusalOf(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Errorf("%s: EndSession of the id of a run: %v, want NOT_FOUND / SESSION_UNKNOWN", when, err)
		}
		conn, err := v2.Open(host, &execv2.OpenRequest{SessionId: "r1", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{}}})
		if err == nil {
			_ = conn.Close()
		}
		if !isRefusalOf(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Errorf("%s: an attach to the id of a run: %v, want NOT_FOUND / SESSION_UNKNOWN", when, err)
		}
		list, err := v2.ListSessions(host)
		if err != nil || len(list.GetSessions()) != 0 {
			t.Errorf("%s: ListSessions lists %v (%v), want none: a run is not a session, live or ended", when, list.GetSessions(), err)
		}
		if count, _ := exec.PluginTombstones(plugin); count != 0 {
			t.Errorf("%s: the plugin holds %d tombstones, want none: a run leaves no tombstone", when, count)
		}
	}

	// A run that lives, and the call that owns it.
	call, cancel := context.WithCancel(host)
	defer cancel()
	answered := make(chan error, 1)
	go func() {
		_, err := v2.Run(call, rig.request(runSrvID("r1")))
		answered <- err
	}()
	awaitChannel(t, hold.entered, "the run to start")
	unknown("while the run lives")
	if live := exec.PluginLive(plugin); !slices.Equal(live, []string{"r1"}) {
		t.Fatalf("the plugin holds %v after the calls that manage sessions were made for the id of a run, want r1 alone: EndSession ended a run", live)
	}

	// Its caller goes away: the run ends with the call. No stream is left behind to detach, and the id is not held for a lease.
	cancel()
	if err := <-answered; err == nil {
		t.Error("the run answered with a result although its caller had cancelled")
	}
	if !proofPoll(engineWait, func() bool { return len(exec.PluginLive(plugin)) == 0 }) {
		t.Fatalf("the plugin still holds %v after the caller of the run cancelled: a run has no stream to detach, it ends with its call", exec.PluginLive(plugin))
	}
	unknown("after the caller cancelled")

	// The id is free the moment the result of a run is handed over, for a run and for a session, and the run that has ended before it
	// is not one of the sessions: what ended has no tombstone to hold the id.
	hold.gate.Open()
	res, err := v2.Run(host, rig.request(runSrvID("r1")))
	if err != nil || res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
		t.Fatalf("a run under the id of a run that was cancelled answered %v, %v", res, err)
	}
	if live := exec.PluginLive(plugin); len(live) != 0 {
		t.Errorf("the plugin holds %v when the result of the run was handed over, want nothing: the id is free by then", live)
	}
	unknown("after the run ended")
	conn, err := v2.Open(host, &execv2.OpenRequest{SessionId: "r1", Kind: &execv2.OpenRequest_Start{Start: &execv2.Start{Target: rig.conns.WireTarget(t, "thing", "thing-1")}}})
	if err != nil {
		t.Fatalf("a session under the id of a run that has ended was refused: %v", err)
	}
	_ = conn.Close() // what the session does from here is the plugin's shutdown to end
}
