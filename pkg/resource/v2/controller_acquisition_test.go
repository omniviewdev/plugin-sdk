package resourcev2

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// quietGrace keeps the grace of a canceled acquisition out of the way of tests
// that are not about it.
const quietGrace = time.Hour

func waitClosed(t *testing.T, name string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testWait):
		t.Fatalf("%s did not close within %s", name, testWait)
	}
}

func assertOpen(t *testing.T, name string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s is closed", name)
	default:
	}
}

// The deadline an acquisition reports is the moment it expires: not earlier, and
// not a multiple of it later.
func TestAcquisitionContextExpiresAtItsReportedDeadline(t *testing.T) {
	const timeout = 200 * time.Millisecond
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	began := time.Now()
	a := newAcquisitionContext(parent, timeout, quietGrace)
	reported, ok := a.Deadline()
	if !ok || reported.Sub(began) < timeout || reported.Sub(began) > timeout+time.Second {
		t.Fatalf("reported deadline %v from construction, ok=%v, want about %v", reported.Sub(began), ok, timeout)
	}
	waitClosed(t, "Done", a.Done())
	elapsed := time.Since(began)
	if elapsed < timeout {
		t.Fatalf("expired after %v, before the %v deadline it reported", elapsed, timeout)
	}
	// Nine times the deadline: a timer that runs a multiple of it late is a bug,
	// scheduling latency is not.
	if elapsed > 9*timeout {
		t.Fatalf("expired after %v, long after the %v deadline it reported", elapsed, timeout)
	}
	if !errors.Is(a.Err(), context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want DeadlineExceeded", a.Err())
	}
	waitClosed(t, "abandoned", a.abandoned)
	if a.complete() {
		t.Fatal("complete won against an expired deadline")
	}
	event, err := a.abandonment()
	if event != "acquisition_timeout" || ErrorCode(err) != "TIMEOUT" {
		t.Fatalf("abandonment = %q, %v", event, err)
	}
}

// A timer callback that was already running when complete stopped its timer
// finds a finished acquisition and changes nothing: the context a client
// retained from CreateClient must not be canceled by it.
func TestAcquisitionContextIgnoresLateTimersAfterComplete(t *testing.T) {
	for name, late := range map[string]func(*acquisitionContext){
		"deadline": (*acquisitionContext).expire,
		"grace":    (*acquisitionContext).giveUp,
	} {
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := newAcquisitionContext(parent, time.Hour, quietGrace)
			if !a.complete() {
				t.Fatal("complete lost without a deadline passing")
			}
			late(a)
			if err := a.Err(); err != nil {
				t.Fatalf("a late timer canceled the retained context: %v", err)
			}
			assertOpen(t, "Done", a.Done())
			assertOpen(t, "abandoned", a.abandoned)
			if _, ok := a.Deadline(); ok {
				t.Fatal("deadline reported after complete")
			}
		})
	}
}

// A canceled generation gives the callbacks the grace to return. One that does
// still delivers its result, and the retained context ends with the generation.
func TestAcquisitionContextDeliversAResultThatReturnsWithinTheGrace(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	a := newAcquisitionContext(parent, time.Hour, quietGrace)
	cancel()
	waitClosed(t, "Done", a.Done())
	assertOpen(t, "abandoned", a.abandoned)
	if !a.complete() {
		t.Fatal("a result that returned within the grace was abandoned")
	}
	if !errors.Is(a.Err(), context.Canceled) {
		t.Fatalf("Err = %v, want Canceled", a.Err())
	}
}

// One that does not is abandoned when the grace ends, with the cancellation as
// its error: the creator canceled, so a timeout is not what it asked about.
func TestAcquisitionContextAbandonsACanceledStartAfterTheGrace(t *testing.T) {
	const grace = 50 * time.Millisecond
	parent, cancel := context.WithCancel(context.Background())
	a := newAcquisitionContext(parent, time.Hour, grace)
	began := time.Now()
	cancel()
	waitClosed(t, "abandoned", a.abandoned)
	if elapsed := time.Since(began); elapsed < grace {
		t.Fatalf("abandoned after %v, before the %v grace", elapsed, grace)
	}
	if a.complete() {
		t.Fatal("complete won against an abandoned acquisition")
	}
	event, err := a.abandonment()
	if event != "acquisition_canceled" || !errors.Is(err, context.Canceled) {
		t.Fatalf("abandonment = %q, %v", event, err)
	}
}

// Whichever of cancellation and deadline comes first names the cause.
func TestAcquisitionContextAbandonmentCauseIsTheFirstEvent(t *testing.T) {
	t.Run("cancel then deadline", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		a := newAcquisitionContext(parent, time.Hour, quietGrace)
		cancel()
		waitClosed(t, "Done", a.Done())
		a.expire()
		event, err := a.abandonment()
		if event != "acquisition_canceled" || !errors.Is(err, context.Canceled) {
			t.Fatalf("abandonment = %q, %v", event, err)
		}
	})
	t.Run("deadline then cancel", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		a := newAcquisitionContext(parent, time.Hour, quietGrace)
		a.expire()
		cancel()
		time.Sleep(10 * time.Millisecond)
		event, err := a.abandonment()
		if event != "acquisition_timeout" || ErrorCode(err) != "TIMEOUT" {
			t.Fatalf("abandonment = %q, %v", event, err)
		}
		if !errors.Is(a.Err(), context.DeadlineExceeded) {
			t.Fatalf("Err = %v, want DeadlineExceeded", a.Err())
		}
	})
}

// The sentence a user reads names the limit in words and the setting that raises
// it, in one short sentence the host will not cut.
func TestAcquisitionTimeoutErrorSentence(t *testing.T) {
	const tail = "; the plugin's connection timeout setting can raise this limit."
	for _, tc := range []struct {
		timeout time.Duration
		want    string
	}{
		{time.Second, "Connecting did not finish within 1 second" + tail},
		{60 * time.Second, "Connecting did not finish within 60 seconds" + tail},
		{300 * time.Second, "Connecting did not finish within 300 seconds" + tail},
		{1500 * time.Millisecond, "Connecting did not finish within 1.5s" + tail},
		{50 * time.Millisecond, "Connecting did not finish within 50ms" + tail},
	} {
		err := acquisitionTimeoutError(tc.timeout)
		var typed *ResourceOperationError
		if !errors.As(err, &typed) || typed.Code != "TIMEOUT" || typed.Message != tc.want {
			t.Errorf("acquisitionTimeoutError(%v) = %v, want TIMEOUT %q", tc.timeout, err, tc.want)
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("acquisitionTimeoutError(%v) wraps context.DeadlineExceeded", tc.timeout)
		}
		if n := utf8.RuneCountInString(typed.Message); n > 160 {
			t.Errorf("sentence is %d runes; the host shows at most 512 and a user reads it whole", n)
		}
	}
}

// Neither timer outlives the acquisition: a finished acquisition holds no pending
// timer, and abandonment ends the other timer. A pending timer would keep the
// context, and what it references, alive for up to the full deadline.
func TestAcquisitionContextTimersEndWithTheAcquisition(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		a := newAcquisitionContext(parent, time.Hour, quietGrace)
		cancel()
		waitClosed(t, "Done", a.Done())
		if !a.complete() {
			t.Fatal("complete lost")
		}
		if a.expiry.Stop() {
			t.Fatal("deadline timer still pending after complete")
		}
		if a.graceTimer.Stop() {
			t.Fatal("grace timer still pending after complete")
		}
	})
	t.Run("cancel after complete", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		a := newAcquisitionContext(parent, time.Hour, quietGrace)
		if !a.complete() {
			t.Fatal("complete lost")
		}
		cancel()
		waitClosed(t, "Done", a.Done())
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.graceTimer != nil {
			t.Fatal("a grace timer was started for a finished acquisition")
		}
	})
	t.Run("deadline", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		a := newAcquisitionContext(parent, time.Hour, quietGrace)
		cancel()
		waitClosed(t, "Done", a.Done())
		a.expire()
		if a.graceTimer.Stop() {
			t.Fatal("grace timer still pending after the deadline abandoned the acquisition")
		}
	})
	t.Run("grace", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		a := newAcquisitionContext(parent, time.Hour, quietGrace)
		cancel()
		waitClosed(t, "Done", a.Done())
		a.giveUp()
		if a.expiry.Stop() {
			t.Fatal("deadline timer still pending after the grace abandoned the acquisition")
		}
	})
}

// A deadline that passes while the generation is still live abandons the start
// by itself: acquire skips OnStart for a client CreateClient returns afterwards
// and destroys it, without relying on the generation being canceled (the
// creator retires the slot, and cancels it, only after it has seen the timeout).
func TestAcquireSkipsOnStartWhenTheDeadlinePassedWhileTheGenerationIsLive(t *testing.T) {
	cfg := controllerConfig()
	var destroyed, started atomic.Int32
	lifecycle := controllerLifecycle(&destroyed)
	lifecycle.CreateClient = func(_ context.Context, connection types.Connection) (*string, error) {
		client := connection.ID
		return &client, nil
	}
	lifecycle.OnStart = func(context.Context, types.Connection, *string) error {
		started.Add(1)
		return nil
	}
	cfg.Lifecycle = lifecycle
	c := newTestController(t, cfg)
	generation, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &clientSlot[string]{
		connection: types.Connection{ID: "explicit"}, ctx: generation, cancel: cancel, ready: make(chan struct{}),
	}
	a := newAcquisitionContext(generation, time.Hour, quietGrace)
	a.expire()
	if generation.Err() != nil {
		t.Fatal("the generation was canceled by the deadline")
	}
	out := make(chan acquisitionResult[string], 1)
	c.acquire(context.Background(), s, a, s.connection, out)
	if got := started.Load(); got != 0 {
		t.Fatalf("OnStart ran %d times for an abandoned client", got)
	}
	if got := destroyed.Load(); got != 1 {
		t.Fatalf("destroy calls = %d, want 1", got)
	}
	select {
	case res := <-out:
		t.Fatalf("an abandoned result was delivered: %+v", res)
	default:
	}
}
