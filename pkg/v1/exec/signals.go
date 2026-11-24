package exec

import "context"

// Signals (plan section 4.5): ClientFrame.signal is an explicit action ("Stop", "Kill"), delivered to the function through
// Proc.Signals, and only for the signals the session's effective Capabilities.Signals lists, the set the host read in Opened (and in
// Started, when the session narrowed it). Ctrl+C in a terminal is the byte 0x03 as input and is none of this.
//
// The SDK decides whether a signal may be delivered and the function decides what it does: a signal is not an end of the session, and
// nothing here kills a process. Delivery never blocks the SDK. The queue is small (signalQueue), a function that does not read it
// loses the oldest signal for each new one, and each loss is counted and logged: the host sent its signals in order and wants the
// newest to land, and a function that reads at all sees them in that order.

// signalDisposition is what became of a signal the host sent.
type signalDisposition int

const (
	// signalQueued: the signal is in the function's queue.
	signalQueued signalDisposition = iota
	// signalUndeclared: the signal is not among the signals the session declares now, and the host broke the contract it was told.
	signalUndeclared
	// signalClosed: the command has ended and takes no signal.
	signalClosed
)

// Signal queues sig for the function's Proc.Signals, in the order of the calls. The frames of the host are applied one at a time and
// in order, so a signal that follows input is queued after that input was.
func (c *command) Signal(sig Signal) signalDisposition {
	if !c.gate.declaresSignal(sig) {
		return signalUndeclared
	}
	dropped, open := c.ctl.queueSignal(sig)
	switch {
	case !open:
		return signalClosed
	case dropped:
		c.signalDropped()
	}
	return signalQueued
}

// signalDropped counts a signal that was lost to a function that does not read Proc.Signals, and says so in the log: the first time,
// and at most once a second for each target after that, because a host that signals in a loop would otherwise write a line each.
func (c *command) signalDropped() {
	c.e.tel.signalDropped()
	if !c.e.logs.allow("signals_dropped\x00"+c.group, c.e.clock()) {
		return
	}
	c.log.Warnw(context.Background(), "exec signal dropped: the function does not read Proc.Signals fast enough, and its oldest signal was lost for the newest")
}
