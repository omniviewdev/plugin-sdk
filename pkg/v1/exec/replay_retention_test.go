package exec_test

import (
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// The memory a tombstone keeps is the ring's output, and the store's budget counts exactly that. An attach takes a copy of the ring
// for the stream that replays it, which is the stream's and not the ring's: a session that ends while that stream has not read its
// replay (its host's window is full, or its host is gone) and a stream that is then left behind must not leave the copy in the
// tombstone, where nothing counts it and it lives for the whole time to live of the tombstone.
func TestTheTombstoneOfASessionKeepsNoCopyOfAReplayThatAStreamLeftUnread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		grants := make(chan int, 4)
		var written atomic.Uint64
		rig := newMemoryRig(t, engTarget(gatedPattern(grants, &written)))

		first := rig.streamOf(rig.startFrame("s1"), nil)
		first.opened(t)
		first.sendDetachFrame(0)
		synctest.Wait()
		grants <- 3 << 20 // the ring (1 MiB) is full, and the output is three times what it holds
		synctest.Wait()

		// A host attaches and takes nothing, not even the Opened frame: the replay is a copy of the ring that its stream holds and has
		// not begun to read. The stream is held before it has a goroutine that could wait for the lock of its sends (a pump that holds
		// the first frame of the replay in a send takes the lock with it, and a serve loop that has an event to send then waits for it
		// where synctest.Wait does not see it blocked, and never returns).
		window := make(chan struct{})
		second := rig.streamOpenedHeld(rig.attachFrame("s1", 0), window)
		synctest.Wait()

		// The session ends while the stream has not read its replay, and then the host that never read it is gone.
		close(grants)
		synctest.Wait()
		info := rig.sessionInfo(t, "s1")
		if info.GetEndedWith() == nil {
			t.Fatalf("the session did not end: %v", info)
		}
		second.cancel()
		synctest.Wait()
		if err, returned := second.handlerReturned(); !returned {
			t.Fatalf("the handler of the stream that was left behind did not return (%v)", err)
		}

		copied, held := exec.PluginTombstoneRetainedCopy(rig.plugin, "s1")
		if !held {
			t.Fatal("the tombstone holds no ring: the budget of the store took its output, and the test cannot see what it refers to")
		}
		if copied != 0 {
			t.Fatalf("the tombstone still refers to %d bytes of the replay that a stream was given: a copy that no budget counts, kept for as long as the tombstone lives", copied)
		}
		first.hostLeaves()
	})
}
