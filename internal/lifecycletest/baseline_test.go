package lifecycletest_test

import (
	"strings"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
)

// leakyWorker is a goroutine that is left behind until stop is closed.
func leakyWorker(stop <-chan struct{}) { <-stop }

func TestBaselineIsMetOnceTheGoroutinesAreGone(t *testing.T) {
	baseline := lifecycletest.TakeBaseline()
	stop := make(chan struct{})
	for range 5 {
		go leakyWorker(stop)
	}
	close(stop)
	// They are on their way out, which takes a moment: Check gives them the time it is given.
	if err := baseline.Check(5 * time.Second); err != nil {
		t.Fatalf("Check failed after the goroutines were told to return: %v", err)
	}
}

func TestBaselineNamesTheGoroutinesThatWereLeftBehind(t *testing.T) {
	baseline := lifecycletest.TakeBaseline()
	stop := make(chan struct{})
	for range 3 {
		go leakyWorker(stop)
	}
	err := baseline.Check(50 * time.Millisecond)
	if err == nil {
		close(stop)
		t.Fatal("Check passed with three goroutines left behind")
	}
	msg := err.Error()
	close(stop)
	for _, want := range []string{
		"goroutines",
		"3 x [chan receive]",
		"lifecycletest_test.leakyWorker",
		"TestBaselineNamesTheGoroutinesThatWereLeftBehind", // who started them
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure does not say %q:\n%s", want, msg)
		}
	}
	if len(msg) > 4<<10+512 {
		t.Errorf("the failure is %d bytes: the goroutine dump is not bounded to 4 KiB", len(msg))
	}
	if err := baseline.Check(5 * time.Second); err != nil {
		t.Fatalf("the goroutines did not return once released: %v", err)
	}
}

// The failure lists the goroutines that were not there at the baseline, and not those that were.
func TestBaselineFailureLeavesOutTheGoroutinesThatWereThereBefore(t *testing.T) {
	older := make(chan struct{})
	go leakyWorker(older)
	defer close(older)
	baseline := lifecycletest.TakeBaseline()

	newer := make(chan struct{})
	go leakyWorker(newer)
	err := baseline.Check(50 * time.Millisecond)
	close(newer)
	if err == nil {
		t.Fatal("Check passed with a goroutine left behind")
	}
	if got := strings.Count(err.Error(), "lifecycletest_test.leakyWorker"); got != 1 {
		t.Fatalf("the failure names leakyWorker %d times, want once, for the one that was started after the baseline:\n%v", got, err)
	}
}
