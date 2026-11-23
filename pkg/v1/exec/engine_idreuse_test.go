package exec_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// A command's end is emitted before its id can be taken (so that a session that takes the id over is admitted only once the
// first one has ended), and that is why Done, the moment the end is final, comes a moment before the id is free. Gone is the
// moment the id is free. The independent review of the engine measured the window: a start of the same id right after Done
// was refused 9.5% of the time in a tight loop. A consumer that reuses an id waits for Gone.
func TestAnIdIsFreeOnceGoneIsClosed(t *testing.T) {
	const workers, rounds = 16, 300
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), nil
	}))
	var conflicts, other atomic.Int64
	done := make(chan struct{}, workers)
	for w := range workers {
		go func() {
			defer func() { done <- struct{}{} }()
			id := fmt.Sprintf("worker-%d", w) // one id for every round of the worker
			for range rounds {
				cmd, err := rig.open(id, withoutTTY)
				if err != nil {
					var refusal *exec.Error
					if errors.As(err, &refusal) && refusal.Class == exec.ErrorClassConflict {
						conflicts.Add(1)
					} else {
						other.Add(1)
					}
					return
				}
				<-cmd.Done()
				<-cmd.Gone()
			}
		}()
	}
	for range workers {
		<-done
	}
	if conflicts.Load() != 0 || other.Load() != 0 {
		t.Fatalf("%d starts of an id whose command was gone were refused as a conflict and %d for another reason, want none", conflicts.Load(), other.Load())
	}
}

// The same operation against itself: two starts of one id once the first command is gone race, and exactly one wins.
func TestTwoStartsOfAnIdThatIsGoneAdmitExactlyOne(t *testing.T) {
	type attempt struct {
		cmd exechook.Command
		err error
	}
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	for round := range 200 {
		id := fmt.Sprintf("r%d", round)
		first := rig.mustOpen(id, withoutTTY)
		first.Close(true)
		<-first.Done()
		<-first.Gone()
		results := make(chan attempt, 2)
		start := make(chan struct{})
		for range 2 {
			go func() {
				<-start
				cmd, err := rig.open(id, withoutTTY)
				results <- attempt{cmd, err}
			}()
		}
		close(start)
		// Both attempts are resolved before the winner ends: a winner that ended first would free the id for the loser.
		attempts := []attempt{<-results, <-results}
		var won, lost int
		for _, a := range attempts {
			switch {
			case a.err == nil:
				won++
			case refusalOf(t, a.err).Class == exec.ErrorClassConflict:
				lost++
			}
		}
		for _, a := range attempts {
			if a.err == nil {
				a.cmd.Close(true)
				<-a.cmd.Done()
				<-a.cmd.Gone()
			}
		}
		if won != 1 || lost != 1 {
			t.Fatalf("round %d: %d won and %d lost the id, want exactly one of each", round, won, lost)
		}
	}
}
