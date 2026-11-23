package exec_test

import (
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// anyGoroutineIn is anyGoroutine for a goroutine that is blocked in the wait state it is given ("select",
// "chan send", ...): the header of the stack says so ("goroutine 7 [select, 2 minutes]:"), and a goroutine that
// is only passing through the functions, running or about to block, does not count.
func anyGoroutineIn(state string, parts ...string) bool {
	var dump string
	for size := 1 << 20; ; size *= 2 {
		buf := make([]byte, size)
		if n := runtime.Stack(buf, true); n < size {
			dump = string(buf[:n])
			break
		}
	}
	for _, g := range strings.Split(dump, "\n\n") {
		header, _, _ := strings.Cut(g, "\n")
		if !strings.Contains(header, "["+state) {
			continue
		}
		all := true
		for _, part := range parts {
			all = all && strings.Contains(g, part)
		}
		if all {
			return true
		}
	}
	return false
}

func blockedInASelectForTheProbe(release, other <-chan struct{}) {
	select {
	case <-release:
	case <-other:
	}
}

func passingThroughForTheProbe(stop *atomic.Bool) {
	for !stop.Load() {
		runtime.Gosched()
	}
}

// The probe must tell a goroutine that is blocked from one that is only in the same function: the second is
// not stuck, and a test that took it for stuck would go on without the state it needs.
func TestTheGoroutineProbeSeesTheWaitStateAndNotJustTheStack(t *testing.T) {
	release, other := make(chan struct{}), make(chan struct{})
	var stop atomic.Bool
	done := make(chan struct{}, 2)
	go func() { blockedInASelectForTheProbe(release, other); done <- struct{}{} }()
	go func() { passingThroughForTheProbe(&stop); done <- struct{}{} }()
	t.Cleanup(func() {
		close(release)
		stop.Store(true)
		<-done
		<-done
	})

	awaitWithin(t, func() bool { return anyGoroutineIn("select", "blockedInASelectForTheProbe") },
		"the goroutine to block in its select")
	for range 200 {
		if anyGoroutineIn("select", "passingThroughForTheProbe") {
			t.Fatal("a goroutine that is running was taken for one that is blocked in a select")
		}
	}
	if anyGoroutineIn("chan send", "blockedInASelectForTheProbe") {
		t.Fatal("a goroutine that is blocked in a select was taken for one that is blocked in a send")
	}
}
