package exec

import (
	"context"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The control frames of an Open stream (resize, signal, close_input, query) and what they earn the host (Resized, Fact). The host's
// frames are applied by the receiver, one at a time and in the order they were sent, like every frame; what comes back is sent by the
// goroutine that sends the stream's events (serve), under the same lock as the pump's output, so that a Resized or a Fact is a frame
// of the stream in its order, and never follows the Ended or the Detached.
//
// What a frame earns belongs to the stream that sent it. A session outlives its stream and another stream may attach to it, so none
// of this is the session's: the ack of a resize goes to the stream that sent the resize (it is the size's owner, see resizeOwner),
// the answer to a query to the stream that asked it, and the facts that the function pushes to the stream that is attached (the
// newest of each kind, once, to a stream that attaches). What a stream that left was owed is dropped with it, and is never sent to the
// next one: the next one learns the size in force from what Opened says of the replay. A frame of a stream that the session has
// moved away from takes no effect (attachment.whileCurrent).
//
// The receiver never sends and never waits for the host to read: a host that does not read must not keep the receiver from seeing
// its next frame. What a frame earns goes into a small place that serve takes from: the one ack of the resizes (a newer ack replaces
// one that was not sent), the answers to the queries, and the newest fact pushed of each kind. The answers are the one place that can
// fill, with queries of a host that asks and does not read: the receiver then waits for room, as it does for the function's input,
// which is the backpressure the host sees, and the way out is the same (cancel the stream; there is no frame that jumps the queue).

// maxPendingAnswers is how many answers a stream holds for a host that does not read before its receiver stops reading the host's
// frames.
const maxPendingAnswers = 64

// pushSlot is the place of one kind of pushed fact: what the host has, what waits for its turn, and when the turn is.
type pushSlot struct {
	sent    *execv2.Fact
	pending *execv2.Fact
	nextAt  time.Time
}

// streamControl is the part of an open stream that holds what the host's control frames earn it. Its methods are safe for the
// goroutines that call them: the receiver (resizeIgnored, answerWait), the resizer's (resizeAccepted, resizeTaken), the function's
// and the deadline's (answer, pushFact), and serve (the rest).
type streamControl struct {
	o *openStream

	// wake has a value when there is something to send or a timer to set; space when serve took answers from a full place. Both have
	// room for one: a wake that finds one waiting adds nothing.
	wake  chan struct{}
	space chan struct{}

	// inputClosed is true once this stream sent close_input, which makes an input frame of this stream a violation. Only the receiver
	// reads or writes it. The input of the session stays closed when the stream leaves.
	inputClosed bool

	mu      sync.Mutex
	acks    resizeAcks
	answers []*execv2.Fact
	pushes  [len(factKinds)]pushSlot

	// timerC, timerAt and stopTimer are the one timer of the stream (the deadline of a size that waits, the turn of a pushed fact),
	// which only serve touches.
	timerC    <-chan time.Time
	timerAt   time.Time
	timerStop func()
}

func newStreamControl(o *openStream) *streamControl {
	return &streamControl{o: o, wake: make(chan struct{}, 1), space: make(chan struct{}, 1)}
}

// signal wakes serve.
func (s *streamControl) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------------------------------------------------------
// what the receiver applies
// ---------------------------------------------------------------------------------------------------------------

// applyResize is a resize of the host: the engine decides what becomes of it, and the host is told in an ack, now or when the function
// takes the size or the deadline comes. A size the engine refuses is a malformed frame. The stream is the size's owner (it is told
// when the size is accepted, before the function can take it, and when it is taken), so the ack of a size goes to the stream that
// sent it and to no other.
func (o *openStream) applyResize(r *execv2.Resize) bool {
	wire := r.GetSize()
	size := Size{Cols: dimension(wire.GetCols()), Rows: dimension(wire.GetRows())}
	seq := r.GetSeq()
	var disposition resizeDisposition
	if !o.att.whileCurrent(func() { disposition = o.cmd.Resize(seq, size, o.ctl) }) {
		return false
	}
	switch disposition {
	case resizeInvalid:
		o.fail(invalidRequest("a resize with a zero dimension, or one above the target's limit"))
		return false
	case resizeIgnored:
		o.ctl.resizeIgnored(seq, size)
	case resizeAccepted:
		// The resizer told the stream so before it could deliver the size.
	case resizeClosed:
		// The command ended: the stream ends with it, and there is nothing to tell.
	}
	return true
}

// applySignal is a signal of the host. A signal that the session does not declare is the host breaking what it was told.
func (o *openStream) applySignal(sig execv2.Signal) bool {
	var disposition signalDisposition
	if !o.att.whileCurrent(func() { disposition = o.cmd.Signal(enumFromProto(signalPairs, sig)) }) {
		return false
	}
	if disposition == signalUndeclared {
		o.fail(unsupportedRequest("the signal %s is not among the signals the session declares", sig))
		return false
	}
	return true
}

// applyCloseInput is stdin EOF: the function's input ends after what was sent before it. A session that does not declare StdinEOF
// cannot honor it, and the host was told so in Opened. The input of a session stays closed when its stream leaves; the input frames
// of a stream that attaches later are dropped, as those of a command that has ended are, and are not its host's violation: only the
// stream that closed the input is held to it (see apply).
func (o *openStream) applyCloseInput() bool {
	if !o.cmd.gate.peek().StdinEOF {
		o.fail(unsupportedRequest("close_input for a session that does not declare StdinEOF"))
		return false
	}
	if !o.att.whileCurrent(o.cmd.CloseInput) {
		return false
	}
	o.ctl.inputClosed = true
	return true
}

// applyQuery is a query for a fact. The SDK answers the ones that are its own (the integration tier, a kind it does not know, a fact
// the target does not provide, a function that holds as many queries as it may), and hands the others to the function, which
// answers, or has the deadline answer for it: a query is never left without an answer while the stream that asked is there to be
// answered. The answer goes to that stream and to none other: the function's answer to a query whose stream has left is dropped.
func (o *openStream) applyQuery(q *execv2.Query) bool {
	id, wire := q.GetId(), q.GetKind()
	if id == 0 || wire == execv2.FactKind_FACT_KIND_UNSPECIFIED {
		o.fail(invalidRequest("a query with the id zero or with no kind"))
		return false
	}
	var immediate *execv2.Fact
	switch kind := factKindFromProto(wire); {
	case wire == execv2.FactKind_FACT_KIND_INTEGRATION_TIER:
		immediate = tierFact(id)
	case kind == FactKindUnspecified:
		immediate = &execv2.Fact{QueryId: id} // a kind this build does not know: a fact with no value, which says so
	default:
		if !o.att.whileCurrent(func() { immediate = o.cmd.AskFact(id, kind, o.ctl.answer) }) {
			return false
		}
	}
	if immediate != nil {
		o.ctl.answerWait(o.ctx, immediate)
	}
	return true
}

// resizeAccepted records a resize the function was handed (resizeOwner).
func (s *streamControl) resizeAccepted(seq uint32, size Size) {
	e := s.o.s.engine
	s.mu.Lock()
	s.acks.accepted(e.clock(), e.cfg.resizeUnreadAfter, seq, size)
	s.mu.Unlock()
	s.signal()
}

// resizeTaken records that the function took a size this stream sent, which is the ack to send now (resizeOwner).
func (s *streamControl) resizeTaken(seq uint32, size Size) {
	s.mu.Lock()
	s.acks.applied(seq, size)
	s.mu.Unlock()
	s.signal()
}

// resizeIgnored records a resize of a session that cannot be resized.
func (s *streamControl) resizeIgnored(seq uint32, size Size) {
	s.mu.Lock()
	s.acks.ignored(seq, size)
	s.mu.Unlock()
	s.signal()
}

// answerWait queues an answer that the receiver has to give, and waits for room when the host has not read the ones before it: the
// receiver reads no more of the host's frames meanwhile. It returns when the answer is queued or ctx ends.
func (s *streamControl) answerWait(ctx context.Context, fact *execv2.Fact) {
	for {
		s.mu.Lock()
		if len(s.answers) < maxPendingAnswers {
			s.answers = append(s.answers, fact)
			s.mu.Unlock()
			s.signal()
			return
		}
		s.mu.Unlock()
		select {
		case <-s.space:
		case <-ctx.Done():
			return
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// what the function and the deadline give
// ---------------------------------------------------------------------------------------------------------------

// answer queues the answer to a query that the function or the deadline settled. It never waits: the answers of the function and the
// deadline are at most as many as the queries that wait for it (maxOutstandingQueries), which is a bound of its own.
func (s *streamControl) answer(fact *execv2.Fact) {
	s.mu.Lock()
	s.answers = append(s.answers, fact)
	s.mu.Unlock()
	s.signal()
}

// pushFact takes a fact the function pushed (it is the stream's pushSink). A value the host already has is not sent again, and the
// newest value of a kind replaces one that waits for its turn, so that a value that changes faster than the turns costs the host the
// newest, and never an older one after it.
func (s *streamControl) pushFact(kind FactKind, fact *execv2.Fact) {
	s.mu.Lock()
	slot := &s.pushes[factSlot(kind)]
	switch {
	case slot.sent != nil && proto.Equal(slot.sent, fact):
		slot.pending = nil // back to what the host has: a newer value that waited is not worth sending
	case slot.pending != nil && proto.Equal(slot.pending, fact):
	default:
		slot.pending = fact
	}
	s.mu.Unlock()
	s.signal()
}

// ---------------------------------------------------------------------------------------------------------------
// what serve sends
// ---------------------------------------------------------------------------------------------------------------

// timer is the channel the stream's timer runs out on, nil while there is none.
func (s *streamControl) timer() <-chan time.Time { return s.timerC }

// timerFired is serve's word that the timer ran out: it is over, and flush sets the next if one is needed.
func (s *streamControl) timerFired() { s.stopTimer() }

func (s *streamControl) stopTimer() {
	if s.timerStop != nil {
		s.timerStop()
		s.timerC, s.timerStop = nil, nil
	}
}

// flush sends what is due, in this order: the ack of the resizes, the answers, the facts whose turn has come. It sets the timer for
// what is not due yet. Only serve calls it, and serve is also what sends the last frame (finish): no control frame follows it, and
// what has not been sent when the session ends is moot and is dropped with it. When the stream leaves a session that lives on,
// finish flushes once more before Detached, so that what the host's frames earned before its Detach is answered before it.
func (s *streamControl) flush() error {
	e := s.o.s.engine
	now := e.clock()
	frames, at, timed := s.collect(now)
	s.rearm(e, now, at, timed)
	if len(frames) == 0 {
		return nil
	}
	return s.o.sendFrames(frames...)
}

// collect takes what is due at now and says when the next thing is, if there is one.
func (s *streamControl) collect(now time.Time) (frames []*execv2.ServerFrame, next time.Time, timed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	soonest := func(t time.Time) {
		if !timed || t.Before(next) {
			next, timed = t, true
		}
	}
	s.acks.expire(now, s.o.cmd.gate.peek().Resize)
	if ack, ok := s.acks.take(); ok {
		frames = append(frames, resizedFrame(ack))
	}
	if len(s.answers) > 0 {
		for _, f := range s.answers {
			frames = append(frames, factFrame(f))
		}
		clear(s.answers)
		s.answers = s.answers[:0]
		select {
		case s.space <- struct{}{}:
		default:
		}
	}
	for i := range s.pushes {
		slot := &s.pushes[i]
		switch {
		case slot.pending == nil:
		case now.Before(slot.nextAt):
			soonest(slot.nextAt)
		default:
			frames = append(frames, factFrame(slot.pending))
			slot.sent, slot.pending, slot.nextAt = slot.pending, nil, now.Add(minPushInterval)
		}
	}
	if deadline, waiting := s.acks.deadline(); waiting {
		soonest(deadline)
	}
	return frames, next, timed
}

// rearm makes the stream's timer run out at next, or stops it when there is nothing to wait for.
func (s *streamControl) rearm(e *engine, now, next time.Time, timed bool) {
	switch {
	case !timed:
		s.stopTimer()
	case s.timerStop != nil && s.timerAt.Equal(next):
	default:
		s.stopTimer()
		s.timerC, s.timerStop = e.timer(next.Sub(now))
		s.timerAt = next
	}
}

// factFrame is the Fact frame of a fact.
func factFrame(fact *execv2.Fact) *execv2.ServerFrame {
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Fact{Fact: fact}}
}
