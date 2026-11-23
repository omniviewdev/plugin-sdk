package session

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// naiveRegistry is the registry as it is usually first written, and as this package is built
// not to be. It is the stand-in that shows the crossing scenarios can catch the fault: a suite
// that passes a registry with these faults too proves nothing about the real one.
//
// It counts its sessions with a sync.WaitGroup, which is the only place a sync.WaitGroup is used
// in this package, and it decides each of the things the real registry decides under one lock in
// separate steps that nothing orders:
//
//   - Admit reads the closed flag outside the lock that registers, and calls wg.Add after, with
//     no order against the wg.Wait of a Close. A Close that runs in between returns with the
//     session admitted and forgotten.
//   - End decides that "nobody is running, so the end is mine" outside any lock, and the last
//     producer to return decides that "the last producer finishes an ending entry" the same
//     way. Both can be true at once: the entry is finished twice.
//   - Go reads the ending flag and registers the producer in two steps, so a producer can be
//     registered after the end has run.
//
// It uses atomics for its flags, not plain fields, so that the faults are logical and not data
// races: the race detector must stay quiet for the suite to pass, and the faults must show
// anyway. Its panics (a second finish closes a closed channel, and a WaitGroup counter goes
// negative) are recovered and reported by faults, where the real thing would end the process.
type naiveRegistry struct {
	eventLog
	pauses pauses

	closed atomic.Bool    // read outside the lock that registers
	wg     sync.WaitGroup // one count for each entry, added after the closed check

	mu      sync.Mutex
	entries map[string]*naiveEntry
	ends    map[string]int
	panics  []string

	inFlight atomic.Int32 // producer goroutines that have not finished everything they do
}

type naiveEntry struct {
	reg       *naiveRegistry
	id        string
	running   atomic.Int32 // producers running
	ending    atomic.Bool  // End was called
	causeBits atomic.Uint32
	doneCh    chan struct{}
}

func newNaiveRegistry() *naiveRegistry {
	return &naiveRegistry{entries: make(map[string]*naiveEntry), ends: make(map[string]int)}
}

func (r *naiveRegistry) admit(id string) (crossingSession, error) {
	if r.closed.Load() { // decided outside the lock that registers
		return nil, ErrClosed
	}
	r.pauses.hit(atAdmitDecided, id)
	r.mu.Lock()
	if r.entries[id] != nil {
		r.mu.Unlock()
		return nil, ErrExists
	}
	e := &naiveEntry{reg: r, id: id, doneCh: make(chan struct{})}
	r.entries[id] = e
	r.mu.Unlock()
	r.wg.Add(1) // after the closed check, with no order against Close's Wait
	return e, nil
}

func (r *naiveRegistry) closeAll() {
	r.closed.Store(true)
	r.mu.Lock()
	live := make([]*naiveEntry, 0, len(r.entries))
	for _, e := range r.entries {
		live = append(live, e)
	}
	r.mu.Unlock()
	for _, e := range live {
		e.end(CauseShutdown)
	}
	r.wg.Wait()
}

func (r *naiveRegistry) length() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

func (r *naiveRegistry) endCount(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ends[id]
}

func (r *naiveRegistry) log() *eventLog { return &r.eventLog }

func (r *naiveRegistry) pause(at point, id string) (reached <-chan struct{}, release func()) {
	return r.pauses.arm(at, id)
}

func (r *naiveRegistry) closeFrame() string { return "(*naiveRegistry).closeAll(" }

func (r *naiveRegistry) awaitProducersGone() bool {
	return poll(mustHappen, func() bool { return r.inFlight.Load() == 0 })
}

func (r *naiveRegistry) awaitRemoved(id string) bool {
	return poll(mustHappen, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.entries[id] == nil
	})
}

func (r *naiveRegistry) faults() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.panics...)
}

func (r *naiveRegistry) fault(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.panics = append(r.panics, fmt.Sprintf(format, args...))
}

func (e *naiveEntry) end(c Cause) bool {
	if !e.ending.CompareAndSwap(false, true) {
		return false
	}
	e.causeBits.Store(uint32(c))
	e.reg.pauses.hit(atEndRecorded, e.id)
	if e.running.Load() == 0 { // "nobody is running, so the end is mine", racing the last producer's return
		e.finish()
	}
	return true
}

func (e *naiveEntry) goProducer(fn func()) bool {
	if e.ending.Load() { // decided here, acted on below
		return false
	}
	e.reg.pauses.hit(atGoChecked, e.id)
	e.running.Add(1)
	e.reg.inFlight.Add(1)
	go func() {
		defer e.reg.inFlight.Add(-1)
		defer e.exit()
		fn()
	}()
	return true
}

// exit is the end of a producer: the last one to return finishes an ending entry.
func (e *naiveEntry) exit() {
	if e.running.Add(-1) == 0 && e.ending.Load() {
		e.finish()
	}
}

func (e *naiveEntry) done() <-chan struct{} { return e.doneCh }

func (e *naiveEntry) cause() Cause { return Cause(e.causeBits.Load()) }

// finish publishes the end of the entry and removes it. Nothing stops it from running twice.
func (e *naiveEntry) finish() {
	r := e.reg
	defer func() {
		if p := recover(); p != nil {
			r.fault("finishing %s panicked: %v", e.id, p)
		}
	}()
	close(e.doneCh) // a second finish panics here, as it would in production
	if n := e.running.Load(); n != 0 {
		r.fault("the end function of %s ran with %d producers running", e.id, n)
	}
	r.mu.Lock()
	r.ends[e.id]++
	r.mu.Unlock()
	r.event("end:" + e.id)
	r.mu.Lock()
	delete(r.entries, e.id)
	r.mu.Unlock()
	r.wg.Done()
}
