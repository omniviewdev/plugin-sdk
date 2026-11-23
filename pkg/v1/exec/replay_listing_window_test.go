package exec_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A call that reads two of the engine's stores is not one read: the live sessions are read under the registry's lock and the
// tombstones under the store's, so a session can end between the two. What the call says of it must be what a call at one instant
// says. The engine makes a session's tombstone before it releases the session's id, so a session that is gone from the registry is
// already in the store: a listing reads the live ones first and the tombstones after, and a session that ends between the two is
// then in both, and is listed once, from its tombstone; a read of the output looks for the tombstone, then for the live session, and
// then for the tombstone again, so that a session that ended between the first two lookups is read from it. These tests hold the
// call between its two reads (EngineSeams.StoreWindow), and the end of a session between its own two steps (EngineSeams.EndEmitted),
// and decide nothing by waiting.

// idsOf is the ids of a listing, in the order the listing gives them.
func idsOf(infos []*execv2.SessionInfo) []string {
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.GetFacts().GetSessionId()
	}
	return ids
}

// storeWindow is a seam that holds the first call that reaches it, the one of the test: reached is closed when it is there, and letGo
// lets it go (it may be called again, and a test defers it, so that one that failed does not leave the call waiting).
func storeWindow() (hook func(), reached <-chan struct{}, letGo func()) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var held atomic.Bool
	var once sync.Once
	hook = func() {
		if held.CompareAndSwap(false, true) {
			close(arrived)
			<-release
		}
	}
	return hook, arrived, func() { once.Do(func() { close(release) }) }
}

// A session that ends after the listing read the live sessions and before it read the tombstones is in what it read first and in the
// tombstones, and must be listed once, as ended: not twice, and not at all, which is what a listing that read the tombstones first
// would do (the session is in none of them yet, and it is gone from the registry when that is read). The other session lives on and
// is listed as it is.
func TestASessionThatEndsBetweenTheTwoReadsOfAListingIsListedOnceWithItsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		// The seam is set before a session starts, as the seams of the engine are: the functions that run read the engine's clock.
		hook, reached, letGo := storeWindow()
		defer letGo()
		exec.SetPluginEngineSeams(rig.plugin, exec.EngineSeams{StoreWindow: hook})
		ends := rig.streamOf(rig.startFrame("ends"), nil)
		ends.opened(t)
		stays := rig.streamOf(rig.startFrame("stays"), nil)
		stays.opened(t)
		defer rig.wind(t, ends, stays)

		type listing struct {
			sessions []*execv2.SessionInfo
			err      error
		}
		listed := make(chan listing, 1)
		call := rig.callContext()
		go func() {
			resp, err := rig.server.ListSessions(call, &execv2.ListSessionsRequest{})
			listed <- listing{resp.GetSessions(), err}
		}()
		<-reached // the listing has read the live sessions, and both are among them

		ends.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_KILL)
		synctest.Wait()
		// A host is told Ended once its session's id is released and its tombstone is made: the session has ended for good.
		if lastOf(ends.frames()).GetEnded() == nil {
			t.Fatal("the session did not end while the listing was held between its two reads")
		}

		letGo()
		got := <-listed
		if got.err != nil {
			t.Fatalf("ListSessions: %v", got.err)
		}
		if ids, want := idsOf(got.sessions), []string{"ends", "stays"}; !slices.Equal(ids, want) {
			t.Fatalf("the listing has %v, want each of %v once: a session that ends between the two reads of a listing is listed once", ids, want)
		}
		if got.sessions[0].GetEndedWith() == nil {
			t.Errorf("the session that ended is listed as it was before its end: %v, though its tombstone was there to read", got.sessions[0])
		}
		if got.sessions[1].GetEndedWith() != nil || !got.sessions[1].GetAttached() {
			t.Errorf("the session that lives is listed as %v, want it attached and not ended", got.sessions[1])
		}
	})
}

// A session that has ended and whose id is not released yet is in the registry and in the tombstones at once, which is the state
// that a listing that read the live sessions first meets when a session ends under it. It is listed once, as ended, before the id is
// released and after.
func TestASessionThatHasEndedAndHoldsItsIdIsListedOnceWithItsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		held := make(chan struct{})
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		var once sync.Once
		letGo := func() { once.Do(func() { close(held) }) }
		defer letGo()
		exec.SetPluginEngineSeams(rig.plugin, exec.EngineSeams{EndEmitted: func() { <-held }})
		a := rig.streamOf(rig.startFrame("s1"), nil)
		a.opened(t)
		defer rig.wind(t, a)

		a.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_KILL)
		synctest.Wait()
		if !rig.live("s1") {
			t.Fatal("the registry released the id of the session that ended while its end was held between Done and Gone")
		}
		if lastOf(a.frames()).GetEnded() != nil {
			t.Fatal("the host was told Ended before the id was released")
		}
		infos := rig.listSessions(t)
		if ids := idsOf(infos); !slices.Equal(ids, []string{"s1"}) {
			t.Fatalf("the listing has %v while the id is held, want s1 once", ids)
		}
		if infos[0].GetEndedWith() == nil {
			t.Errorf("the session that ended is listed as live while its id is held: %v", infos[0])
		}

		letGo()
		synctest.Wait()
		infos = rig.listSessions(t)
		if ids := idsOf(infos); !slices.Equal(ids, []string{"s1"}) {
			t.Fatalf("the listing has %v after the id was released, want s1 once", ids)
		}
		if infos[0].GetEndedWith() == nil {
			t.Errorf("the session that ended is listed as live after its id was released: %v", infos[0])
		}
	})
}

// A ReadOutput that looked for the tombstone of a session and found none (the session lives) and then looks for the live session must
// not be told that there is none when the session ended in between: it is in the tombstones by then, and the read finds it there, with
// its output and with its end.
func TestAReadOutputOfASessionThatEndsBetweenItsTwoLookupsIsReadFromTheTombstone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			_, _ = p.Stdout.Write([]byte("hello"))
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}))
		hook, reached, letGo := storeWindow()
		defer letGo()
		exec.SetPluginEngineSeams(rig.plugin, exec.EngineSeams{StoreWindow: hook})
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		defer rig.wind(t, host)

		type read struct {
			resp *execv2.ReadOutputResponse
			err  error
		}
		answered := make(chan read, 1)
		call := rig.callContext()
		go func() {
			resp, err := rig.server.ReadOutput(call, &execv2.ReadOutputRequest{SessionId: "s1"})
			answered <- read{resp, err}
		}()
		<-reached // the read found no tombstone, and has not looked for the live session

		host.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_KILL)
		synctest.Wait()
		if lastOf(host.frames()).GetEnded() == nil {
			t.Fatal("the session did not end while the read was held between its two lookups")
		}

		letGo()
		got := <-answered
		if got.err != nil {
			t.Fatalf("ReadOutput of a session that ended between its two lookups: %v, want the read from its tombstone", exec.StatusError(got.err))
		}
		if !got.resp.GetEnded() || string(chunkBytes(t, got.resp)) != "hello" {
			t.Errorf("the read answered %v, want the output of the session and its end", got.resp)
		}
	})
}
