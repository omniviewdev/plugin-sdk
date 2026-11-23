package exec_test

import (
	"runtime"
	"testing"
	"time"
)

// A keystroke and its echo: one byte sent as an input frame, one byte waited for as an Output frame, on an idle session,
// through a real client and server. The plan's bar is p95 <= 150 us on macOS and 250 us on Linux (the legacy lane measured 53 to
// 65 and 116 to 164 us, and the model 44 and 31). The numbers are logged for the PR; what is asserted is a floor that a shared
// machine and the race detector cannot break, and that a lane that batched by a timer, or paid a hop for each byte, would.
func TestOpenLatencyEchoOneByteRoundTripOnAnIdleSession(t *testing.T) {
	rig := newProofRig(t, proofTarget("echo", proofEchoFunction))
	conn := rig.mustOpenKind("echo", "echo-1", proofTTY)
	echo := newProofEchoer(conn)
	if _, err := echo.measure(proofScaled(500, 200)); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	samples, err := echo.measure(proofScaled(20000, 2000))
	if err != nil {
		t.Fatalf("after %d round trips: %v", len(samples), err)
	}
	stats := proofSummarize(samples)
	bar := 250 * time.Microsecond
	if runtime.GOOS == "darwin" {
		bar = 150 * time.Microsecond
	}
	floor := 2 * time.Millisecond
	if proofRace {
		floor = 10 * time.Millisecond
	}
	t.Logf("idle echo, one byte, %s/%s, GOMAXPROCS %d: %v (the plan's bar: p95 <= %v; this run's floor: %v)",
		runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), stats, bar, floor)
	if stats.P95 > floor {
		t.Errorf("the p95 of an idle echo is %v, the floor is %v (the plan's bar %v)", stats.P95.Round(time.Microsecond), floor, bar)
	}
}
