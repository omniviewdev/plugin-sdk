package exec_test

// Every operation of the Manager against every other, and against itself.
//
// A Manager is a set of operations that share one set of sessions. A race between two of
// them is as likely as the one between a start and Close that was found, so this test runs
// every pair of the operations in allOperations (see manager_ops_test.go) together, the pair
// of an operation with itself included: two goroutines of each, released together, on a
// manager that already has live sessions. The operations are
//
//	Create, CreateResource, CreateSameID (starts: a fresh ID each time, and one ID for all)
//	Get, List, Attach, Detach, CloseSession, Resize, Write (input through a stream)
//	Stream, StreamEnd (opening a stream and closing its input; opening one and ending its context. Either way the stream ends, and cancels the sessions it served)
//	Close
//	ProcessExit, TerminalHangup (a session that ends by itself: its process leaves, its terminal hangs up)
//
// which is 15 operations and 120 pairs, every cell of the triangle, the diagonal included.
// There is no Stop or Kill operation to add: CloseSession is the one way to stop a session
// from outside.
//
// Every pair is held to what operations that merely overlap in time must still guarantee,
// whatever the interleaving was:
//
//   - every call returns a result it may return (see env.create and env.settled);
//   - a start is only refused when a Close has begun;
//   - when the pair is over and the manager is closed, nothing is registered, no goroutine of
//     a session is alive, every terminal the manager opened is closed, every process a
//     handler started has exited, and every admitted session said goodbye once, last.
//
// The pairs run under -race in CI (the SDK suite does), where they also assert the absence
// of a data race, which is the failure Manager.Close had.

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
)

const (
	matrixGoroutines = 2  // goroutines that run each of the pair's operations
	matrixCalls      = 12 // calls each of them makes
	matrixSeeds      = 4  // sessions that are live before the pair starts
)

func TestOperationMatrix(t *testing.T) {
	ops := allOperations()
	for i := range ops {
		for j := i; j < len(ops); j++ {
			a, b := ops[i], ops[j]
			t.Run(a.name+"_vs_"+b.name, func(t *testing.T) { racePair(t, a, b) })
		}
	}
}

func racePair(t *testing.T, a, b operation) {
	e, endStream := newEnv(t)
	for i := range matrixSeeds {
		kind, id, labels := startLegacy, fmt.Sprintf("seed-%d", i), map[string]string{}
		if i%2 == 1 {
			kind = startResource
		}
		if i == 0 {
			labels["handler"] = "chatty" // writes output, so that an attached session delivers some
		}
		if _, err := e.r.start(kind, id, labels); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		e.remember(id)
		if _, _, err := e.r.m.AttachSession(nil, id); err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
	}

	begin := make(chan struct{})
	var wg sync.WaitGroup
	for n, op := range []operation{a, b} {
		for g := range matrixGoroutines {
			rng := rand.New(rand.NewPCG(uint64(n), uint64(g)))
			wg.Go(func() {
				<-begin
				for range matrixCalls {
					op.run(e, rng)
				}
			})
		}
	}
	close(begin)
	wg.Wait()
	e.finish(endStream)
	t.Logf("what the calls returned:\n%s", e.summary())
}
