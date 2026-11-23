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

// A session's end is emitted before its id can be taken, and Done, the moment the end is final, comes a moment before Gone. Once a
// session is gone its tombstone holds its id (for the tombstone's TTL) and its place is free: a start of the same id is refused as
// a conflict every time, however fast it comes after Gone, and a start of any other id is admitted every time. The independent
// review of the engine measured a window in which a start right after Done was refused 9.5% of the time for the id and admitted
// for another: the id is now held on purpose, so the window is closed from both sides.
func TestTheIdOfAGoneSessionIsHeldByItsTombstoneAndItsPlaceIsFree(t *testing.T) {
	const workers, rounds = 8, 50
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.ExitCode(0), nil
	}))
	var heldOK, freshRefused, other atomic.Int64
	done := make(chan struct{}, workers)
	for w := range workers {
		go func() {
			defer func() { done <- struct{}{} }()
			for round := range rounds {
				id := fmt.Sprintf("worker-%d-%d", w, round) // a new id each round: the place is what is reused
				cmd, err := rig.open(id, withoutTTY)
				if err != nil {
					freshRefused.Add(1)
					return
				}
				<-cmd.Done()
				<-cmd.Gone()
				_, again := rig.open(id, withoutTTY)
				var refusal *exec.Error
				switch {
				case again == nil:
					other.Add(1) // the id was admitted while its tombstone holds it
				case errors.As(again, &refusal) && refusal.Class == exec.ErrorClassConflict && refusal.Reason == exec.ErrorReasonSessionExists:
					heldOK.Add(1)
				default:
					other.Add(1)
				}
			}
		}()
	}
	for range workers {
		<-done
	}
	if freshRefused.Load() != 0 || other.Load() != 0 || heldOK.Load() != workers*rounds {
		t.Fatalf("%d starts of a new id were refused, %d starts of a gone session's id were not refused as SESSION_EXISTS, and %d were (of %d), want 0, 0 and all",
			freshRefused.Load(), other.Load(), heldOK.Load(), workers*rounds)
	}
}

// The same operation against itself: two starts of one fresh id race, and exactly one wins; once the winner has ended its tombstone
// holds the id, and two more starts of it both lose.
func TestTwoStartsOfAnIdAdmitExactlyOneAndTheTombstoneRefusesBoth(t *testing.T) {
	type attempt struct {
		cmd exechook.Command
		err error
	}
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	startTwo := func(id string) []attempt {
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
		return []attempt{<-results, <-results}
	}
	for round := range 100 {
		id := fmt.Sprintf("r%d", round)
		// Both attempts are resolved before the winner ends: a winner that ended first would hold the id for the loser as a tombstone,
		// which is the same refusal, but this round is about the live one.
		attempts := startTwo(id)
		var won, lost int
		var winner exechook.Command
		for _, a := range attempts {
			switch {
			case a.err == nil:
				won++
				winner = a.cmd
			case refusalOf(t, a.err).Class == exec.ErrorClassConflict:
				lost++
			}
		}
		if won != 1 || lost != 1 {
			t.Fatalf("round %d: %d won and %d lost the id, want exactly one of each", round, won, lost)
		}
		winner.Close(true)
		<-winner.Done()
		<-winner.Gone()
		for _, a := range startTwo(id) {
			if a.err == nil {
				t.Fatalf("round %d: a start of the id of a session that has ended was admitted while its tombstone holds it", round)
			}
			if got := refusalOf(t, a.err); got.Class != exec.ErrorClassConflict || got.Reason != exec.ErrorReasonSessionExists {
				t.Fatalf("round %d: the refusal of a held id is %v/%v, want CONFLICT/SESSION_EXISTS", round, got.Class, got.Reason)
			}
		}
	}
}
