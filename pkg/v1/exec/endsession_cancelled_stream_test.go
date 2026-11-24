package exec_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A host that cancels its stream has left its session, whether or not the handler that served the stream has noticed. EndSession is for a
// session that no stream is attached to, and the way out of a session whose function never reads its input is to cancel the stream and end the
// session with a kill (docs/exec.md): once the server has seen the stream end, the answer must not depend on how soon its handler releases the
// session. These tests hold the handler where it has noticed that its stream is over and has not yet released the session
// (EngineSeams.StreamLeaving), end the session from there, and let the handler go: in memory, and over a real transport. (A cancel travels on
// a goroutine of the host's client, so an EndSession that follows it at once may reach the server first, and then the stream is attached and
// the answer is CONFLICT: the tests wait for the server to have seen the stream end, which is the part that the engine decides.)

// holdStreamsLeaving holds every stream's handler where it would leave its session, until the returned function is called.
func holdStreamsLeaving(t testing.TB, rig *memoryRig) (open func()) {
	t.Helper()
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	exec.SetPluginEngineSeams(rig.plugin, exec.EngineSeams{StreamLeaving: func() { <-gate.Opened() }})
	return gate.Open
}

// wantEndedByRequest checks that the session ended, with the end that EndSession asks for, and that nothing holds it.
func wantEndedByRequest(t testing.TB, rig *memoryRig, id string) {
	t.Helper()
	if rig.live(id) {
		t.Fatalf("session %q is live after EndSession answered: it answers once the session has ended", id)
	}
	info := rig.sessionInfo(t, id)
	if info.GetEndedWith() == nil || info.GetAttached() {
		t.Fatalf("session %q is %v, want ended, with its tombstone", id, info)
	}
}

// The stream that started the session is cancelled by its host, and the server has not noticed: EndSession ends the session.
func TestEndSessionOfASessionWhoseStartingStreamTheHostCancelledIsNotRefusedBeforeTheServerNoticed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		letGo := holdStreamsLeaving(t, rig)
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		host.cancel()
		synctest.Wait()
		if _, returned := host.handlerReturned(); returned {
			t.Fatal("the handler returned: it was to be held where it would release the session")
		}
		if info := rig.sessionInfo(t, "s1"); !info.GetAttached() {
			t.Fatalf("the session is %v: the server has not released the stream, so the list says what it said before the cancel", info)
		}

		if err := rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL); err != nil {
			t.Fatalf("EndSession of a session whose stream the host cancelled, before the server noticed: %v, want the session ended: the host has left it", err)
		}
		wantEndedByRequest(t, rig, "s1")
		letGo()
		synctest.Wait()
		if _, returned := host.handlerReturned(); !returned {
			t.Fatal("the handler did not return once it was let go")
		}
	})
}

// A stream that attached to the session is cancelled by its host, and the server has not noticed: the same, for the stream the session was
// attached to by an Open that attaches.
func TestEndSessionOfASessionWhoseAttachedStreamTheHostCancelledIsNotRefusedBeforeTheServerNoticed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		first := rig.streamOf(rig.startFrame("s1"), nil)
		first.opened(t)
		first.sendDetachFrame(0)
		synctest.Wait()
		letGo := holdStreamsLeaving(t, rig)

		second := rig.streamOf(rig.attachFrame("s1", 0), nil)
		second.opened(t)
		second.cancel()
		synctest.Wait()
		if _, returned := second.handlerReturned(); returned {
			t.Fatal("the handler of the attach returned: it was to be held where it would release the session")
		}
		if err := rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL); err != nil {
			t.Fatalf("EndSession of a session whose attached stream the host cancelled, before the server noticed: %v, want the session ended", err)
		}
		wantEndedByRequest(t, rig, "s1")
		letGo()
		synctest.Wait()
		first.hostLeaves()
	})
}

// The control: a stream that its host has not cancelled is attached, however the server's handlers are held, and EndSession is refused for it,
// and the session lives. (A host that half-closes its stream waits for the last frame, which is sent once the server has released the session.)
func TestEndSessionOfASessionWhoseStreamIsStillAttachedIsRefusedAsBefore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		letGo := holdStreamsLeaving(t, rig)
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()

		err := rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL)
		if !isRefusalOf(exec.StatusError(err), exec.ErrorClassConflict, exec.ErrorReasonUnspecified) {
			t.Fatalf("EndSession of a session with a stream attached: %v, want CONFLICT", err)
		}
		if !rig.live("s1") {
			t.Fatal("the session ended with a refused EndSession")
		}
		letGo()
		host.cancel()
		synctest.Wait()
	})
}

// Over a real transport: the server has seen the host cancel its stream, and its handler is held before it releases the session. The host's
// EndSession is answered, and the session is ended. (Nothing is waited for by time: the seam says when the server has seen the end.)
func TestEndSessionOverTheWireOnceTheServerSawTheHostCancelItsStreamIsNotRefusedWhileItsHandlerIsHeld(t *testing.T) {
	rig := newWireRig(t, engTarget(blockUntilCancelled))
	gate := exectest.NewGate()
	t.Cleanup(gate.Open)
	noticed := make(chan struct{})
	var once sync.Once
	exec.SetPluginEngineSeams(rig.server.Plugin, exec.EngineSeams{StreamLeaving: func() {
		once.Do(func() { close(noticed) })
		<-gate.Opened()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := rig.open(ctx, "s1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cancel()
	awaitChannel(t, noticed, "the server to see the host cancel its stream")

	if err := rig.client.EndSession(rig.request(context.Background()), &execv2.EndSessionRequest{SessionId: "s1", Mode: execv2.CloseMode_CLOSE_MODE_KILL}); err != nil {
		t.Fatalf("EndSession of a session whose stream the host cancelled, which the server has seen end but not released: %v, want the session ended", err)
	}
	gate.Open()
	resp, err := rig.client.ListSessions(rig.request(context.Background()))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	for _, info := range resp.GetSessions() {
		if info.GetFacts().GetSessionId() == "s1" && (info.GetEndedWith() == nil || info.GetAttached()) {
			t.Fatalf("the session is %v after EndSession answered, want ended", info)
		}
	}
}
