package exec

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// The count of the calls that hold a command (inflight.go), on the clock of a synctest bubble: a wait that is to block is seen
// blocked once every goroutine waits, and a wait that is to give up does so at its context's deadline, in no time.

// waitInBackground starts a wait and returns the channel that receives what it returned.
func waitInBackground(f *inflight, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- f.wait(ctx) }()
	return done
}

// stillWaiting says that a wait has not returned, once every goroutine of the bubble has done what it can.
func stillWaiting(done <-chan error) bool {
	synctest.Wait()
	select {
	case <-done:
		return false
	default:
		return true
	}
}

func TestAWaitWithNoCallInFlightReturnsAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var f inflight
		if err := f.wait(context.Background()); err != nil {
			t.Fatalf("wait with nothing in flight: %v", err)
		}
		end := f.begin()
		end()
		if err := f.wait(context.Background()); err != nil {
			t.Fatalf("wait after the only call ended: %v", err)
		}
	})
}

func TestAWaitReturnsWhenTheLastCallEndsAndNotBefore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var f inflight
		first, second := f.begin(), f.begin()
		done := waitInBackground(&f, context.Background())
		if !stillWaiting(done) {
			t.Fatal("wait returned with two calls in flight")
		}
		first()
		if !stillWaiting(done) {
			t.Fatal("wait returned with one call in flight")
		}
		second()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatalf("wait: %v", err)
		}
	})
}

// A call that begins while a wait is blocked is waited for too: the wait returns when no call is left, not when the calls it found
// have ended.
func TestAWaitAlsoWaitsForACallThatBeginsWhileItBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var f inflight
		first := f.begin()
		done := waitInBackground(&f, context.Background())
		synctest.Wait()
		second := f.begin()
		first()
		if !stillWaiting(done) {
			t.Fatal("wait returned while a call that began after it was still in flight")
		}
		second()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatalf("wait: %v", err)
		}
	})
}

// Every wait is released when the last call ends, not one of them.
func TestEveryWaitIsReleasedWhenTheLastCallEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var f inflight
		end := f.begin()
		waits := []<-chan error{waitInBackground(&f, context.Background()), waitInBackground(&f, context.Background()), waitInBackground(&f, context.Background())}
		synctest.Wait()
		end()
		synctest.Wait()
		for i, done := range waits {
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("wait %d: %v", i, err)
				}
			default:
				t.Errorf("wait %d is still blocked after the last call ended", i)
			}
		}
	})
}

// A wait gives up with its context's error when the context ends first, and leaves the count as it was: the call that holds it
// goes on and a later wait still waits for it.
func TestAWaitGivesUpWhenItsContextEndsAndLeavesTheCallCounted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var f inflight
		end := f.begin()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := f.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait with a call in flight and a context of 100ms: %v, want the context's error", err)
		}
		done := waitInBackground(&f, context.Background())
		if !stillWaiting(done) {
			t.Fatal("a later wait returned while the call was still in flight: giving up forgot the call")
		}
		end()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatalf("wait: %v", err)
		}
	})
}

// A call that ends twice is a bug of the server, said loudly.
func TestACallThatEndsTwiceIsAPanic(t *testing.T) {
	var f inflight
	end := f.begin()
	end()
	defer func() {
		if recover() == nil {
			t.Error("a call that ended twice did not panic")
		}
	}()
	end()
}
