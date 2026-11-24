package exec

import "time"

// A session that has no stream lives for a lease: the host's own (Start.detach_timeout_ms, or Detach.lease_ms), or the target's
// default (SessionLimits.DetachTimeout, 5 minutes), and never more than the target's maximum (SessionLimits.MaxDetachTimeout, 30
// minutes). A Start that asks for more than the maximum is refused at admission; a Detach that asks for more is clamped, and the
// Detached frame says when the lease ends, which is what was applied. A stream that breaks is an implicit Detach with the
// session's own lease. At the end of the lease the SDK ends the session with Closed{DETACH_TIMEOUT}, unless a stream attached
// meanwhile: an attach cancels the lease (see ring.attach), and a lease that runs out ends only a session that nobody has
// attached to, both decided under the ring's lock so that an attach and the end of a lease are never both true.
//
// A lease costs one goroutine and one timer while it runs. The goroutine is a producer of the session's entry, so it ends with
// the session, and an attach releases it at once. The timer is the engine's lease timer (a test supplies its own: EngineSeams),
// and the lease's end is read from the engine's clock, so that a test that drives both drives the lease.

// leaseTimer returns the timer of a lease of d: the channel it runs out on, and its stop. A test replaces it
// (EngineSeams.NewLeaseTimer).
func (e *engine) leaseTimer(d time.Duration) (c <-chan time.Time, stop func()) {
	if e.newLeaseTimer != nil {
		return e.newLeaseTimer(d)
	}
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// leaseFor is the lease a detach gives: what the host asked for, or the session's own when it asked for none (zero), and at
// most the target's maximum.
func (c *command) leaseFor(requested time.Duration) time.Duration {
	limits := &c.target.info.Session
	d := requested
	if d <= 0 {
		d = c.detachTimeout
	}
	if d <= 0 {
		d = limits.DetachTimeout
	}
	if limits.MaxDetachTimeout > 0 {
		d = min(d, limits.MaxDetachTimeout)
	}
	return d
}

// startLease starts the wait for the lease arm: at its end the session is ended, if the lease is still the one that runs.
func (c *command) startLease(arm leaseArm, d time.Duration) {
	timer, stop := c.e.leaseTimer(d)
	started := c.entry.Go(func() {
		defer stop()
		select {
		case <-timer:
			c.ring.expireLease(arm.gen)
		case <-arm.cancel:
		case <-c.entry.Ended():
		}
	})
	if !started {
		stop() // the session is ending: no lease is needed
	}
}
