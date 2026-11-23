package exec

import (
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// What the host is told of its resizes (plan section 4.5): the SDK answers Resize{seq, size} with Resized{seq, size, result}, in order
// with the stream's other frames, and one ack covers every resize up to its seq, so that a storm costs the host one ack as it costs the
// function one delivery.
//
//   - APPLIED: the function took the size from Proc.Resize. The seq is the newest resize it took, and a resize that a newer one
//     replaced before the function took it is covered by that newer ack.
//   - IGNORED: the session cannot be resized (no terminal, or no Resize in its effective capabilities), said at once, for the resize
//     that arrived.
//   - FAILED: the function declared Resize and has not taken a size within the engine's resizeUnreadAfter (5 seconds). The host is
//     told once for the newest size it sent, and a function that takes it later is told APPLIED, so that the host ends with the
//     truth. Nothing waits for the function: the SDK and the stream go on, and the host's later frames are applied as they come.
//     A session that narrowed itself to no Resize while a size waited is told IGNORED instead: the target said it cannot, and that is
//     no failure to read.
//
// A size with a zero dimension or one above the target's limit is not answered at all: it is a malformed frame, and ends the stream.
//
// resizeAcks is the state machine of those acknowledgements for one stream. It is pure: the stream calls it under its own lock and
// gives it the time, and what it holds is the one ack to send next (a newer ack replaces one that was not sent, which is how the acks
// of a storm coalesce) and the deadline of the sizes that wait for the function.
type resizeAcks struct {
	// waiting is true while a resize was accepted for the function and no ack covers it. waitSeq and waitSize are the newest of
	// them, and expiresAt is when the oldest of them is told FAILED: the deadline counts from the first resize that no ack covers
	// and is not moved by the ones that follow.
	waiting   bool
	waitSeq   uint32
	waitSize  Size
	expiresAt time.Time

	// next is the ack to send, if there is one. last is the one put most recently: an ack that equals it is not put again.
	next    resizedAck
	hasNext bool
	last    resizedAck
	hasLast bool
}

// resizedAck is one Resized.
type resizedAck struct {
	seq    uint32
	size   Size
	result execv2.ResizeResult
}

// put makes ack the one to send next, unless the last one put was the same.
func (a *resizeAcks) put(ack resizedAck) {
	if a.hasLast && a.last == ack {
		return
	}
	a.next, a.hasNext = ack, true
	a.last, a.hasLast = ack, true
}

// accepted records a resize the function was handed: nothing is told until it takes it or the deadline comes.
func (a *resizeAcks) accepted(now time.Time, unreadAfter time.Duration, seq uint32, size Size) {
	if !a.waiting {
		a.expiresAt = now.Add(unreadAfter)
	}
	a.waiting, a.waitSeq, a.waitSize = true, seq, size
}

// ignored records a resize of a session that cannot be resized, and tells it at once.
func (a *resizeAcks) ignored(seq uint32, size Size) {
	a.waiting = false // the ack that this resize earns has a seq no older than the ones that waited
	a.put(resizedAck{seq: seq, size: size, result: execv2.ResizeResult_RESIZE_RESULT_IGNORED})
}

// applied records a size the function took, the newest it has: it covers every resize up to its seq.
func (a *resizeAcks) applied(seq uint32, size Size) {
	if a.waiting && a.waitSeq == seq && a.waitSize == size {
		a.waiting = false
	}
	a.put(resizedAck{seq: seq, size: size, result: execv2.ResizeResult_RESIZE_RESULT_APPLIED})
}

// expire is the deadline: when it has passed for a size that waits, the newest size is told FAILED, or IGNORED when the session can no
// longer be resized, and nothing waits. It reports whether it told.
func (a *resizeAcks) expire(now time.Time, canResize bool) bool {
	if !a.waiting || now.Before(a.expiresAt) {
		return false
	}
	result := execv2.ResizeResult_RESIZE_RESULT_FAILED
	if !canResize {
		result = execv2.ResizeResult_RESIZE_RESULT_IGNORED
	}
	a.waiting = false
	a.put(resizedAck{seq: a.waitSeq, size: a.waitSize, result: result})
	return true
}

// deadline returns when a size that waits is told FAILED, and false when none waits.
func (a *resizeAcks) deadline() (time.Time, bool) { return a.expiresAt, a.waiting }

// take returns the ack to send and clears it.
func (a *resizeAcks) take() (resizedAck, bool) {
	ack, ok := a.next, a.hasNext
	a.next, a.hasNext = resizedAck{}, false
	return ack, ok
}

// resizedFrame is the Resized frame of an ack.
func resizedFrame(ack resizedAck) *execv2.ServerFrame {
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Resized{Resized: &execv2.Resized{
		Seq: ack.seq, Size: sizeToProto(ack.size), Result: ack.result,
	}}}
}
