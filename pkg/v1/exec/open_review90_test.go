package exec_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The gaps the independent review of the exec lane's wire found in what the suite proved (the review of #90, SHOULD 4): each test
// below is green on the code and red against the fault it names.

// A frame of exactly the target's MaxInputFrame is taken and one byte more is refused. The test that the frame over the maximum
// ends the stream cannot see a limit that is one too low, since a frame that is wrongly refused ends the stream in the same way:
// this one reads the frame of exactly the maximum back before it sends the next (M09: a limit that refused the maximum itself).
func TestAnInputFrameOfExactlyTheMaximumIsTakenAndOneByteMoreIsRefused(t *testing.T) {
	const limit = 1024
	rig := newWireRig(t, engTarget(echoFunction, func(i *exec.TargetInfo) { i.Session.MaxInputFrame = limit }))
	conn := rig.mustOpen("m09", withoutTTY)

	sendInput(t, conn, string(make([]byte, limit)))
	for echoed := 0; echoed < limit; {
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("an input frame of exactly the maximum (%d bytes) ended the stream after %d bytes were echoed: %v", limit, echoed, err)
		}
		if frame.GetEnded() != nil {
			t.Fatalf("the session ended after %d of the %d bytes of a frame of exactly the maximum were echoed", echoed, limit)
		}
		echoed += len(frame.GetOutput().GetData())
	}

	// The next frame is one byte over the limit: its sending may find the stream already ended, which says the same.
	_ = conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: make([]byte, limit+1)}})
	for {
		_, err := recvFrame(t, conn)
		if err == nil {
			continue
		}
		var typed *exec.Error
		if !errors.As(err, &typed) || typed.Class != exec.ErrorClassLimit || typed.Reason != exec.ErrorReasonInputTooLarge {
			t.Fatalf("a frame one byte over the maximum ended the stream with %v, want LIMIT / INPUT_TOO_LARGE", err)
		}
		return
	}
}

// The end of a session closes it, it does not kill it: the function's context is cancelled and the function has the grace to
// return, and the session's id is not free for another session until it has (M18: a session that ended killed its function, and
// the id was handed to a new one while the old function was still winding down). A host that leaves does not end its session, it
// detaches, and what ends the session is its lease: so the test ends this one with the lease. Once the function has returned the
// id is still held, by the session's tombstone.
func TestASessionThatEndsWithItsLeaseClosesItsFunctionAndItsIdIsNotFreeWhileTheFunctionWindsDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, told, release := make(chan struct{}), make(chan struct{}), exectest.NewGate()
		var returned atomic.Bool
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
			close(entered)
			<-ctx.Done()
			close(told)
			<-release.Opened() // the function's cleanup, which takes as long as the test says
			returned.Store(true)
			return exec.Exit{}, ctx.Err()
		}))
		t.Cleanup(release.Open)
		host := rig.stream("m18", nil)
		host.opened(t)
		<-entered

		host.hostLeaves()
		// The host left and the session lives: its function is not told while the lease runs (the clock is the bubble's, so a
		// minute passes at once and the lease, five minutes, has not).
		time.Sleep(time.Minute)
		synctest.Wait()
		select {
		case <-told:
			t.Fatal("the function was told to stop when its host left: a host that leaves detaches, and the lease had not run out")
		default:
		}
		time.Sleep(5 * time.Minute) // the lease runs out
		<-told                      // the session is ending: its function was told

		// Everything the server does about a session that ended, while the function has not returned, it does within the clock's next
		// second (a kill gives up on the function within 50 ms; a close waits for the grace, which is 5 s): the id is still held.
		time.Sleep(time.Second)
		synctest.Wait()
		again := rig.stream("m18", nil)
		synctest.Wait()
		select {
		case err := <-again.returned:
			if st, ok := status.FromError(err); !ok || st.Code() != codes.Aborted {
				t.Errorf("a second open of the id while the old function winds down: %v, want a conflict (the id is taken)", err)
			}
		default:
			t.Error("the id of a session that ended was handed to a new session while the old function was still winding down")
		}
		if returned.Load() {
			t.Fatal("the function returned before the test let it")
		}
		again.cancel()

		// The function returns: the session is over and its place is free, and its tombstone holds its id.
		release.Open()
		synctest.Wait()
		next := rig.stream("m18", nil)
		synctest.Wait()
		select {
		case err := <-next.returned:
			if st, ok := status.FromError(err); !ok || st.Code() != codes.Aborted {
				t.Errorf("a second open of the id after the old function returned: %v, want a conflict (the tombstone holds the id)", err)
			}
		case frame := <-next.out:
			t.Errorf("the id of a session that ended was admitted while its tombstone holds it: the first frame is %v", frame)
		}
		next.hostLeaves()
	})
}
