package exec_test

// Stress of Manager.Close against everything else. Each round makes a fresh Manager with
// a stream open, lets goroutines call random operations on it, calls Close from one to
// three goroutines at a random moment, keeps the callers going for a little while after
// the first Close has returned, and then checks the state the Manager must be left in.
//
// What is checked is not that nothing panicked, but the end state:
//
//   - when Close returns, no session is registered, and no output is delivered later;
//   - a start that begins after a Close returned is refused with the refusal (CONNECTION_ERROR),
//     never admitted, and a start is never refused while the manager is open;
//   - when the callers have stopped: no goroutine of a session is alive, every terminal the
//     manager opened is closed, every process the handlers started has exited, and every
//     admitted session delivered its close signal once and nothing after it;
//   - when the whole run is over, the process has the goroutines it started with.
//
// The run lasts as long as EXEC_CLOSE_STRESS_SECONDS says (a number, fractions allowed),
// 1.5 seconds by default so that the suite stays fast. The 30-second runs that back the
// claim in the change that introduced it are:
//
//	EXEC_CLOSE_STRESS_SECONDS=30 go test ./pkg/v1/exec -run TestStressEveryOperationAgainstClose -count=1
//	EXEC_CLOSE_STRESS_SECONDS=30 go test -race ./pkg/v1/exec -run TestStressEveryOperationAgainstClose -count=1

import (
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const stressSecondsEnv = "EXEC_CLOSE_STRESS_SECONDS"

func stressDuration(t *testing.T) time.Duration {
	t.Helper()
	value := os.Getenv(stressSecondsEnv)
	if value == "" {
		return 1500 * time.Millisecond
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || seconds <= 0 {
		t.Fatalf("%s=%q: want a positive number of seconds", stressSecondsEnv, value)
	}
	return time.Duration(seconds * float64(time.Second))
}

// round is the shape of one round of the stress.
type round struct {
	ops       []operation   // what the callers do
	callers   int           // goroutines that call them
	closers   int           // goroutines that call Close, at a random moment
	maxDelay  time.Duration // how long after the start of the round the latest Close may begin
	aftermath time.Duration // how long the callers go on after the first Close returned, at most
}

// run makes a round from seed and reports the violations it found to t.
func (s round) run(t *testing.T, seed uint64) (admitted int64) {
	t.Helper()
	e, endStream := newUnownedEnv(t)
	rng := rand.New(rand.NewPCG(seed, 0))

	var stop atomic.Bool
	var callers sync.WaitGroup
	for c := range s.callers {
		callerRNG := rand.New(rand.NewPCG(seed, uint64(c+1)))
		callers.Go(func() {
			for !stop.Load() {
				s.ops[callerRNG.IntN(len(s.ops))].run(e, callerRNG)
			}
		})
	}
	var closers sync.WaitGroup
	for range s.closers {
		delay := time.Duration(rng.Int64N(int64(s.maxDelay) + 1))
		closers.Go(func() {
			time.Sleep(delay)
			e.close()
		})
	}
	closers.Wait()
	time.Sleep(time.Duration(rng.Int64N(int64(s.aftermath) + 1)))
	stop.Store(true)
	callers.Wait()
	e.finish(endStream)
	return e.r.admitted.Load()
}

func without(ops []operation, names ...string) []operation {
	var out []operation
	for _, op := range ops {
		keep := true
		for _, name := range names {
			keep = keep && op.name != name
		}
		if keep {
			out = append(out, op)
		}
	}
	return out
}

func only(ops []operation, names ...string) []operation {
	var out []operation
	for _, op := range ops {
		for _, name := range names {
			if op.name == name {
				out = append(out, op)
			}
		}
	}
	return out
}

// TestCloseRacesStarts is the failure that was found, in a form that does not depend on
// the race detector: callers start sessions and stop them while Close is called, and the
// end state is checked. (The probe it grew from called Close 3000 times from one goroutine
// while four others started and stopped sessions; with the manager open it saw a data race
// in four runs in five under -race, and once a panic.)
func TestCloseRacesStarts(t *testing.T) {
	ops := only(allOperations(), "Create", "CreateResource", "CreateSameID", "CloseSession")
	shape := round{ops: ops, callers: 4, closers: 1, maxDelay: 2 * time.Millisecond, aftermath: 2 * time.Millisecond}
	for seed := range uint64(150) {
		shape.run(t, seed)
		if t.Failed() {
			t.Fatalf("round %d broke an invariant (above)", seed)
		}
	}
}

func TestStressEveryOperationAgainstClose(t *testing.T) {
	duration := stressDuration(t)
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// A stream that ends cancels every session, so half of the rounds do without it: they
	// are the ones in which the sessions are alive when Close arrives.
	shapes := []round{
		{ops: without(allOperations(), "Close", "StreamEnd"), callers: 6, closers: 1, maxDelay: 3 * time.Millisecond, aftermath: 2 * time.Millisecond},
		{ops: without(allOperations(), "Close"), callers: 6, closers: 3, maxDelay: 3 * time.Millisecond, aftermath: 2 * time.Millisecond},
		{ops: only(allOperations(), "Create", "CreateResource", "CloseSession", "Attach"), callers: 8, closers: 2, maxDelay: time.Millisecond, aftermath: time.Millisecond},
	}
	var rounds, admitted int64
	deadline := time.Now().Add(duration)
	for seed := uint64(1); time.Now().Before(deadline); seed++ {
		admitted += shapes[int(seed)%len(shapes)].run(t, seed)
		rounds++
		if t.Failed() {
			t.Fatalf("round %d broke an invariant (above); %d rounds before it, %d sessions admitted", seed, rounds-1, admitted)
		}
	}

	deadline = time.Now().Add(drainWait)
	for runtime.NumGoroutine() > baseline+2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if now := runtime.NumGoroutine(); now > baseline+2 {
		buf := make([]byte, 1<<20)
		t.Errorf("%d goroutines at the end, %d at the start:\n%s", now, baseline, buf[:runtime.Stack(buf, true)])
	}
	t.Logf("%d rounds in %s, %d sessions admitted; goroutines %d -> %d",
		rounds, duration, admitted, baseline, runtime.NumGoroutine())
}
