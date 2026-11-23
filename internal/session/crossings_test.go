package session

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// The crossing scenarios. Admit against Close, End against the last producer, and Go against End
// are the three places where a lifecycle that is decided in separate steps loses an entry, ends
// it twice, or runs a producer after its end. Each is written once, as a function of a
// crossingImpl that returns what it found wrong, and run against two implementations: the real
// registry, which must find nothing, and naiveRegistry, a WaitGroup and a pair of flags with
// the classic faults, which the crossing scenarios must catch. A suite that passes the naive one
// proves nothing, and this is how it shows it can fail.

// crossingImpl is the surface of a registry the scenarios drive.
type crossingImpl interface {
	// admit admits a session, and reports the refusal otherwise.
	admit(id string) (crossingSession, error)
	// closeAll shuts the registry down, and returns once every session's end ran.
	closeAll()
	// length is how many sessions the registry still holds.
	length() int
	// endCount is how many times the end function ran for id.
	endCount(id string) int
	// log is the ordered record of what happened.
	log() *eventLog
	// pause makes the next call that reaches the crossing for id park there: the admission after
	// its decision and before it takes effect, End after it recorded that the entry is
	// ending, or Go after it found the entry running and before it registered the producer.
	pause(at point, id string) (reached <-chan struct{}, release func())
	// closeFrame is the frame of a goroutine that is inside closeAll, for the runtime's record of
	// whether it is blocked.
	closeFrame() string
	// awaitProducersGone returns once every producer goroutine has finished everything it does,
	// and reports whether that happened in time.
	awaitProducersGone() bool
	// awaitRemoved returns once the registry no longer holds id, and reports whether that
	// happened in time.
	awaitRemoved(id string) bool
	// faults reports what the implementation did wrong that is not an ordering: a panic it
	// recovered, a rule its end function saw broken.
	faults() []string
}

// crossingSession is one admitted session.
type crossingSession interface {
	end(c Cause) bool
	goProducer(fn func()) bool
	done() <-chan struct{}
	cause() Cause
}

// realImpl adapts the real registry, through the test lane, to crossingImpl.
type realImpl struct{ l *lane }

type realSession struct{ e *Entry[*state] }

func (r realImpl) admit(id string) (crossingSession, error) {
	e, err := r.l.admitWith(context.Background(), Spec{ID: id})
	if err != nil {
		return nil, err
	}
	return realSession{e}, nil
}

func (r realImpl) closeAll() { _ = r.l.reg.Close(context.Background()) }

func (r realImpl) length() int { return r.l.reg.Len() }

func (r realImpl) endCount(id string) int { return r.l.endCount(id) }

func (r realImpl) log() *eventLog { return &r.l.eventLog }

func (r realImpl) pause(at point, id string) (<-chan struct{}, func()) { return r.l.pauses.arm(at, id) }

func (r realImpl) closeFrame() string { return closeFrame }

func (r realImpl) awaitProducersGone() bool {
	return poll(mustHappen, func() bool { return goroutinesIn(false, producerFrame) == 0 })
}

func (r realImpl) awaitRemoved(id string) bool {
	return poll(mustHappen, func() bool {
		for _, info := range r.l.reg.Snapshot() {
			if info.ID == id {
				return false
			}
		}
		return true
	})
}

func (r realImpl) faults() []string { return r.l.problemsSoFar() }

func (s realSession) end(c Cause) bool { return s.e.End(c) }

func (s realSession) goProducer(fn func()) bool {
	st := s.e.Value()
	return s.e.Go(func() {
		st.active.Add(1)
		defer st.active.Add(-1)
		fn()
	})
}

func (s realSession) done() <-chan struct{} { return s.e.Done() }

func (s realSession) cause() Cause { return s.e.Cause() }

// verdict collects what a scenario found wrong.
type verdict struct{ found []string }

func (v *verdict) addf(format string, args ...any) {
	v.found = append(v.found, fmt.Sprintf(format, args...))
}

// await reports whether c closed in time.
func await(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	case <-time.After(mustHappen):
		return false
	}
}

// admitted is the outcome of an Admit that ran on its own goroutine.
type admitted struct {
	s   crossingSession
	err error
}

// admitInBackground runs im.admit on a goroutine.
func admitInBackground(im crossingImpl, id string) <-chan admitted {
	out := make(chan admitted, 1)
	go func() {
		s, err := im.admit(id)
		out <- admitted{s, err}
	}()
	return out
}

// receive returns what arrives on c, and false when nothing does in time.
func receive[T any](c <-chan T) (T, bool) {
	select {
	case v := <-c:
		return v, true
	case <-time.After(mustHappen):
		var zero T
		return zero, false
	}
}

// admitOrFail admits id, and records that it could not.
func admitOrFail(im crossingImpl, v *verdict, id string) crossingSession {
	s, err := im.admit(id)
	if err != nil {
		v.addf("Admit(%q) of a registry that is open and empty failed: %v", id, err)
		return nil
	}
	return s
}

// finish collects the implementation's own faults, and returns everything found.
func (v *verdict) finish(im crossingImpl) []string {
	return append(v.found, im.faults()...)
}

// Admit against Close.

// scenarioAdmitStraddlesClose is the crossing: an Admit has decided to admit and has not yet
// registered, and a Close begins. A registry that decides under one lock holds the Close behind the
// Admit and ends what it admitted; one that decides in steps lets the Close finish first, and the
// Admit then registers an entry that no Close will ever end.
func scenarioAdmitStraddlesClose(im crossingImpl) []string {
	var v verdict
	reached, release := im.pause(atAdmitDecided, "a")
	outcome := admitInBackground(im, "a")
	if !await(reached) {
		v.addf("the Admit never reached its crossing")
		release()
		return v.finish(im)
	}

	closed := make(chan struct{})
	go func() {
		im.closeAll()
		close(closed)
	}()
	// Close goes as far as it can with the Admit held: a registry that decides in steps lets it
	// finish, and one that decides under a lock holds it behind the Admit, blocked.
	ranAsFarAsItCan := poll(mustHappen, func() bool {
		select {
		case <-closed:
			return true
		default:
			return blockedIn(im.closeFrame()) > 0
		}
	})
	if !ranAsFarAsItCan {
		v.addf("Close neither returned nor blocked while an Admit was held at its crossing")
	}
	release()

	res, ok := receive(outcome)
	if !ok {
		v.addf("the Admit did not return after it was released")
		return v.finish(im)
	}
	if !await(closed) {
		v.addf("Close did not return after the Admit was released")
		return v.finish(im)
	}

	switch {
	case res.err == nil:
		if n := im.length(); n != 0 {
			v.addf("an Admit that returned nil left %d entries registered after Close returned: admitted, and forgotten", n)
		}
		if n := im.endCount("a"); n != 1 {
			v.addf("the entry an Admit admitted before Close returned had its end function run %d times, want once", n)
		}
		if got := res.s.cause(); got != CauseShutdown {
			v.addf("the entry an Admit admitted before Close returned has cause %v, want %v", got, CauseShutdown)
		}
	case errors.Is(res.err, ErrClosed):
		if n := im.endCount("a"); n != 0 {
			v.addf("an Admit that was refused had its end function run %d times", n)
		}
	default:
		v.addf("Admit returned %v, want nil or %v", res.err, ErrClosed)
	}
	if s, err := im.admit("late"); err == nil {
		v.addf("an Admit after Close returned succeeded, and left %v registered", s)
	}
	return v.finish(im)
}

// scenarioCloseThenAdmit is the other order, with no crossing: the Admit after Close is refused
// and registers nothing.
func scenarioCloseThenAdmit(im crossingImpl) []string {
	var v verdict
	im.closeAll()
	if s, err := im.admit("a"); !errors.Is(err, ErrClosed) {
		v.addf("Admit after Close returned (%v, %v), want %v", s, err, ErrClosed)
	}
	if n := im.length(); n != 0 {
		v.addf("an Admit after Close registered %d entries", n)
	}
	if n := im.endCount("a"); n != 0 {
		v.addf("an Admit that was refused had its end function run %d times", n)
	}
	return v.finish(im)
}

// scenarioAdmitThenClose is the order with no crossing the other way round: Close ends what was
// admitted, with CauseShutdown, and returns after its end function.
func scenarioAdmitThenClose(im crossingImpl) []string {
	var v verdict
	s := admitOrFail(im, &v, "a")
	if s == nil {
		return v.finish(im)
	}
	im.closeAll()
	if n := im.length(); n != 0 {
		v.addf("Close returned with %d entries left", n)
	}
	if n := im.endCount("a"); n != 1 {
		v.addf("Close returned after the end function ran %d times, want once", n)
	}
	if got := s.cause(); got != CauseShutdown {
		v.addf("Close ended the entry with %v, want %v", got, CauseShutdown)
	}
	return v.finish(im)
}

// End against the last producer.

// producerGate starts a producer of s that runs until release is closed, and returns once it
// runs. It logs "producer returned" as its last act.
func producerGate(im crossingImpl, v *verdict, s crossingSession, release <-chan struct{}) bool {
	started := make(chan struct{})
	if !s.goProducer(func() {
		close(started)
		<-release
		im.log().event("producer returned")
	}) {
		v.addf("Go was refused by an entry that has not ended")
		return false
	}
	if !await(started) {
		v.addf("the producer never ran")
		return false
	}
	return true
}

// checkEndedOnce checks what must hold once an entry has ended: Done is closed, the entry is
// removed, the end function ran once, and after the producer.
func checkEndedOnce(im crossingImpl, v *verdict, s crossingSession, id string) {
	if !await(s.done()) {
		v.addf("Done was never closed")
		return
	}
	if !im.awaitRemoved(id) {
		v.addf("the entry was never removed")
		return
	}
	if n := im.endCount(id); n != 1 {
		v.addf("the end function ran %d times, want once", n)
	}
	log := im.log()
	if p, e := log.indexOf("producer returned"), log.indexOf("end:"+id); p >= 0 && (e < 0 || p > e) {
		v.addf("the end function ran before the last producer returned: %v", log.events())
	}
}

func scenarioLastProducerBeforeEnd(im crossingImpl) []string {
	var v verdict
	s := admitOrFail(im, &v, "a")
	if s == nil {
		return v.finish(im)
	}
	release := make(chan struct{})
	if !producerGate(im, &v, s, release) {
		return v.finish(im)
	}
	close(release)
	if !im.awaitProducersGone() {
		v.addf("the producer never returned")
	}
	s.end(CauseRequested)
	checkEndedOnce(im, &v, s, "a")
	return v.finish(im)
}

func scenarioEndThenLastProducer(im crossingImpl) []string {
	var v verdict
	s := admitOrFail(im, &v, "a")
	if s == nil {
		return v.finish(im)
	}
	release := make(chan struct{})
	if !producerGate(im, &v, s, release) {
		return v.finish(im)
	}
	if !s.end(CauseRequested) {
		v.addf("End did not win")
	}
	select {
	case <-s.done():
		v.addf("Done was closed with a producer running")
	default:
	}
	if n := im.endCount("a"); n != 0 {
		v.addf("the end function ran %d times with a producer running", n)
	}
	close(release)
	checkEndedOnce(im, &v, s, "a")
	return v.finish(im)
}

// scenarioEndStraddlesLastProducer is the crossing: End has recorded that the entry is ending
// and has not yet acted on it when the last producer returns. An entry whose end is decided by
// two flags, one the caller's and one the last producer's, is ended by both; one whose end is
// a state is ended once.
func scenarioEndStraddlesLastProducer(im crossingImpl) []string {
	var v verdict
	s := admitOrFail(im, &v, "a")
	if s == nil {
		return v.finish(im)
	}
	release := make(chan struct{})
	if !producerGate(im, &v, s, release) {
		return v.finish(im)
	}
	reached, releaseEnd := im.pause(atEndRecorded, "a")
	ended := make(chan bool, 1)
	go func() { ended <- s.end(CauseRequested) }()
	if !await(reached) {
		v.addf("End never reached its crossing")
		releaseEnd()
		return v.finish(im)
	}
	close(release) // the last producer returns while End is held
	if !im.awaitProducersGone() {
		v.addf("the producer never returned")
	}
	releaseEnd()
	if first, ok := receive(ended); !ok {
		v.addf("End did not return after it was released")
	} else if !first {
		v.addf("End did not win")
	}
	checkEndedOnce(im, &v, s, "a")
	return v.finish(im)
}

// Go against End.

func scenarioGoAfterEnd(im crossingImpl) []string {
	var v verdict
	s := admitOrFail(im, &v, "a")
	if s == nil {
		return v.finish(im)
	}
	release := make(chan struct{})
	if !producerGate(im, &v, s, release) {
		return v.finish(im)
	}
	s.end(CauseRequested)
	var ran atomic.Bool
	if s.goProducer(func() { ran.Store(true) }) {
		v.addf("Go was accepted after End returned, while a producer ran")
	}
	close(release)
	checkEndedOnce(im, &v, s, "a")
	if s.goProducer(func() { ran.Store(true) }) {
		v.addf("Go was accepted after the entry ended")
	}
	if !im.awaitProducersGone() || ran.Load() {
		v.addf("a producer that Go refused ran")
	}
	return v.finish(im)
}

// scenarioGoStraddlesEnd is the crossing: Go has found the entry running and has not yet
// registered the producer when the entry ends, completely, end function and removal. A producer
// registered after that runs after the end; a registry that decides the end as a state refuses it.
func scenarioGoStraddlesEnd(im crossingImpl) []string {
	var v verdict
	s := admitOrFail(im, &v, "a")
	if s == nil {
		return v.finish(im)
	}
	reached, release := im.pause(atGoChecked, "a")
	accepted := make(chan bool, 1)
	go func() {
		accepted <- s.goProducer(func() { im.log().event("producer ran") })
	}()
	if !await(reached) {
		v.addf("Go never reached its crossing")
		release()
		return v.finish(im)
	}
	s.end(CauseRequested)
	if !await(s.done()) || !im.awaitRemoved("a") {
		v.addf("the entry did not end while Go was held")
		release()
		return v.finish(im)
	}
	release()
	ok, returned := receive(accepted)
	if !returned {
		v.addf("Go did not return after it was released")
		return v.finish(im)
	}
	if ok {
		if !im.awaitProducersGone() {
			v.addf("the producer Go accepted never returned")
		}
		log := im.log()
		if p, e := log.indexOf("producer ran"), log.indexOf("end:a"); p >= 0 && e >= 0 && p > e {
			v.addf("Go accepted a producer after the entry's end function had run, and it ran after it: %v", log.events())
		}
	} else if log := im.log(); log.indexOf("producer ran") >= 0 {
		v.addf("Go refused a producer, and it ran")
	}
	if n := im.endCount("a"); n != 1 {
		v.addf("the end function ran %d times, want once", n)
	}
	return v.finish(im)
}

// scenarioGoThenEnd is the other order, with no crossing: a producer registered before End is
// waited for.
func scenarioGoThenEnd(im crossingImpl) []string {
	var v verdict
	s := admitOrFail(im, &v, "a")
	if s == nil {
		return v.finish(im)
	}
	release := make(chan struct{})
	if !producerGate(im, &v, s, release) {
		return v.finish(im)
	}
	s.end(CauseRequested)
	if n := im.endCount("a"); n != 0 {
		v.addf("the end function ran %d times before the producer registered before End had returned", n)
	}
	close(release)
	checkEndedOnce(im, &v, s, "a")
	return v.finish(im)
}

// crossingScenarios lists the scenarios. A scenario is a crossing if the naive registry fails it:
// the others are the orders no crossing is needed for, which the naive registry gets right, and
// which show that the scenarios do not fail what is correct.
var crossingScenarios = []struct {
	crossing string // a, b or c
	name     string
	run      func(crossingImpl) []string
	naive    bool // the naive registry must be caught
}{
	{"a", "an Admit that straddles Close", scenarioAdmitStraddlesClose, true},
	{"a", "Close then Admit", scenarioCloseThenAdmit, false},
	{"a", "Admit then Close", scenarioAdmitThenClose, false},
	{"b", "the last producer returns before End", scenarioLastProducerBeforeEnd, false},
	{"b", "End then the last producer returns", scenarioEndThenLastProducer, false},
	{"b", "End straddles the last producer", scenarioEndStraddlesLastProducer, true},
	{"c", "Go after End", scenarioGoAfterEnd, false},
	{"c", "Go straddles End", scenarioGoStraddlesEnd, true},
	{"c", "Go then End", scenarioGoThenEnd, false},
}

func TestTheCrossingScenariosFindNothingWrongWithTheRealRegistry(t *testing.T) {
	for _, sc := range crossingScenarios {
		t.Run(sc.crossing+" "+sc.name, func(t *testing.T) {
			if found := sc.run(realImpl{newLane(t)}); len(found) != 0 {
				t.Fatalf("the real registry violates the scenario: %v", found)
			}
		})
	}
}

// The suite is sensitive: each crossing is caught in the naive registry.
func TestTheCrossingScenariosCatchTheNaiveWaitGroupRegistry(t *testing.T) {
	caught := make(map[string]int)
	for _, sc := range crossingScenarios {
		t.Run(sc.crossing+" "+sc.name, func(t *testing.T) {
			found := sc.run(newNaiveRegistry())
			switch {
			case sc.naive && len(found) == 0:
				t.Fatal("the scenario found nothing wrong with the naive registry: it cannot catch this fault")
			case !sc.naive && len(found) != 0:
				t.Fatalf("the scenario failed the naive registry in an order where it is correct: %v", found)
			}
			caught[sc.crossing] += len(found)
			t.Logf("found %d violations: %v", len(found), found)
		})
	}
	for _, crossing := range []string{"a", "b", "c"} {
		if caught[crossing] == 0 {
			t.Errorf("no scenario of crossing %s caught the naive registry", crossing)
		}
	}
}
