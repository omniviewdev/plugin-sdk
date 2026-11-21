package resourcev2

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// cancelGrace is how long a start whose generation was canceled may keep running
// before it is abandoned. A callback that honors the cancellation returns within
// moments, and its client is then torn down like that of any failed start, before
// StartConnection, StopConnection or Close returns. A callback that ignores the
// cancellation is abandoned after this long, exactly as at the deadline, so a
// cancel never has to wait out an acquisition deadline of up to five minutes.
const cancelGrace = time.Second

// acquisitionState is where one acquisition stands. It leaves running exactly
// once, for complete or abandoned, under acquisitionContext.mu.
type acquisitionState uint8

const (
	acquisitionRunning acquisitionState = iota
	acquisitionComplete
	acquisitionAbandoned
)

// acquisitionContext is the context handed to CreateClient and OnStart. It is the
// generation context (same values, same cancellation, same lifetime) with one
// addition: while acquisition is running it reports a deadline and is canceled with
// DeadlineExceeded when that deadline passes. complete ends the deadline; after
// that the context reports no deadline, never expires, and is canceled only with
// the generation. A client that retained the context from CreateClient therefore
// keeps a live, deadline-free context for its whole generation.
//
// Unlike a context.WithTimeout child, the deadline is not fixed at construction:
// Deadline changes from set to unset exactly once, when the acquisition succeeds.
// A derived context reports its parent's Deadline, so it follows that change unless
// it sets an earlier deadline of its own. What does not follow is a deadline that
// was copied onto the wire while acquisition ran: a gRPC stream opened in
// CreateClient with this context carries the remaining acquisition time to its
// server, which cancels the stream at the deadline although the start succeeded.
//
// An acquisition ends one way. Either complete wins and the result is delivered,
// or the acquisition is abandoned and the result is discarded: the deadline passed,
// or the generation was canceled and the callbacks did not return within the grace.
// The two are mutually exclusive.
type acquisitionContext struct {
	context.Context
	timeout time.Duration
	grace   time.Duration

	mu       sync.Mutex
	deadline time.Time
	err      error
	done     chan struct{}
	// abandoned closes when the acquisition ends before complete. timedOut says
	// whether the deadline did it; otherwise the generation's cancellation did.
	abandoned  chan struct{}
	state      acquisitionState
	timedOut   bool
	expiry     *time.Timer
	graceTimer *time.Timer
}

func newAcquisitionContext(parent context.Context, timeout, grace time.Duration) *acquisitionContext {
	a := &acquisitionContext{
		Context:   parent,
		timeout:   timeout,
		grace:     grace,
		deadline:  time.Now().Add(timeout),
		done:      make(chan struct{}),
		abandoned: make(chan struct{}),
	}
	// The lock keeps a timer that fires at once from seeing a nil expiry.
	a.mu.Lock()
	a.expiry = time.AfterFunc(timeout, a.expire)
	a.mu.Unlock()
	// The registration is intentionally never stopped: the generation context
	// outlives acquisition and must keep propagating its cancellation.
	context.AfterFunc(parent, a.parentDone)
	return a
}

func (a *acquisitionContext) Deadline() (time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.deadline, !a.deadline.IsZero()
}

func (a *acquisitionContext) Done() <-chan struct{} { return a.done }

func (a *acquisitionContext) Err() error {
	a.mu.Lock()
	err := a.err
	a.mu.Unlock()
	if err != nil {
		return err
	}
	return a.Context.Err()
}

// parentDone propagates the generation's cancellation. An acquisition still
// running keeps its callbacks the grace to return; after that it is abandoned.
func (a *acquisitionContext) parentDone() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return
	}
	a.err = a.Context.Err()
	close(a.done)
	if a.state == acquisitionRunning {
		a.graceTimer = time.AfterFunc(a.grace, a.giveUp)
	}
}

// expire is the deadline. It abandons a running acquisition and cancels the
// context with DeadlineExceeded, unless the generation was canceled first.
func (a *acquisitionContext) expire() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != acquisitionRunning {
		return
	}
	if a.err == nil {
		a.timedOut = true
		a.err = context.DeadlineExceeded
		close(a.done)
	}
	a.abandonLocked()
}

// giveUp ends the grace a canceled acquisition was given.
func (a *acquisitionContext) giveUp() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != acquisitionRunning {
		return
	}
	a.abandonLocked()
}

func (a *acquisitionContext) abandonLocked() {
	a.state = acquisitionAbandoned
	close(a.abandoned)
	a.stopTimersLocked()
}

func (a *acquisitionContext) stopTimersLocked() {
	a.expiry.Stop()
	if a.graceTimer != nil {
		a.graceTimer.Stop()
	}
}

// complete ends the acquisition deadline. It returns false when the acquisition
// was already abandoned, in which case its result must be discarded. The two
// outcomes are mutually exclusive: exactly one of complete and abandonment wins.
func (a *acquisitionContext) complete() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == acquisitionAbandoned {
		return false
	}
	a.state = acquisitionComplete
	a.deadline = time.Time{}
	a.stopTimersLocked()
	return true
}

// abandonment says why the acquisition was abandoned: the trace event that
// records it and the error its callers get. The deadline gives the TIMEOUT
// sentence; a cancellation gives the cancellation. It is valid once abandoned
// has closed.
func (a *acquisitionContext) abandonment() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timedOut {
		return "acquisition_timeout", acquisitionTimeoutError(a.timeout)
	}
	return "acquisition_canceled", a.err
}

type acquisitionResult[T any] struct {
	client *T
	err    error
}

// acquire runs CreateClient and OnStart off the creating goroutine so a callback
// that ignores its context cannot hold the pending slot past the acquisition
// deadline or the grace a cancellation allows. It delivers its result only if the
// acquisition was not abandoned; a result that arrives after that is destroyed
// here, exactly once, because the start has already been reported as failed and
// nothing else owns it.
func (c *Controller[T]) acquire(
	spanCtx context.Context,
	s *clientSlot[T],
	ctx *acquisitionContext,
	connection types.Connection,
	out chan<- acquisitionResult[T],
) {
	var res acquisitionResult[T]
	defer func() {
		if recovered := recover(); recovered != nil {
			res.err = NewError("INTERNAL", "Connection start handler panicked.")
			trace.SpanFromContext(spanCtx).AddEvent("panic_recovered")
			lifecycleLogger().Errorw(
				spanCtx, "connection start handler panicked",
				"connection_id", s.connection.ID,
				"panic_type", fmt.Sprintf("%T", recovered),
				"stack", string(debug.Stack()),
			)
		}
		if ctx.complete() {
			out <- res
			return
		}
		c.discardLateClient(spanCtx, s, res.client)
	}()
	span := trace.SpanFromContext(spanCtx)
	span.AddEvent("create_client")
	res.client, res.err = c.cfg.Lifecycle.CreateClient(ctx, connection)
	if res.err == nil && res.client == nil {
		res.err = NewError("INTERNAL", "CreateClient returned a nil client.")
	}
	if res.err == nil {
		// An acquisition that is already over (its deadline passed, its generation
		// was canceled) has no start left to finish. The client is destroyed with
		// the result; OnStart must not run for a client nothing can use, with an
		// expired context, while the next generation of the connection may be live.
		res.err = ctx.Err()
	}
	if res.err == nil && c.cfg.Lifecycle.OnStart != nil {
		span.AddEvent("on_start")
		res.err = c.cfg.Lifecycle.OnStart(ctx, connection, res.client)
	}
	if res.err == nil {
		res.err = s.ctx.Err()
	}
}

// discardLateClient tears down a client that was produced after its start was
// abandoned. It uses the normal teardown path on a private slot so OnStop and
// DestroyClient run once, with the same cleanup context as any other generation.
// acquire does not call OnStart for such a client once it knows the start is over,
// but a callback already inside OnStart at that moment finishes it. The teardown is
// not ordered against the hooks of the next generation of the same connection, so
// OnStop and DestroyClient can overlap them.
func (c *Controller[T]) discardLateClient(spanCtx context.Context, s *clientSlot[T], client *T) {
	if client == nil {
		return
	}
	ready := make(chan struct{})
	close(ready)
	late := &clientSlot[T]{
		connection: s.connection,
		client:     client,
		ctx:        s.ctx,
		cancel:     func() {},
		ready:      ready,
	}
	if err := c.stopSlot(late); err != nil {
		lifecycleLogger().Warnw(
			spanCtx, "late client teardown failed",
			"connection_id", s.connection.ID, "error_code", ErrorCode(err),
		)
	}
}

// acquisitionTimeoutError is the error a start that outran its deadline returns.
// The sentence is what the user reads: it names the limit and says the plugin's
// connection timeout setting can raise it. It is one short sentence of plain words;
// the host shows at most 512 runes of a provider's sentence. It does not wrap
// context.DeadlineExceeded: classify drops the sentence of an error that does.
func acquisitionTimeoutError(timeout time.Duration) error {
	return NewError("TIMEOUT", fmt.Sprintf(
		"Connecting did not finish within %s; the plugin's connection timeout setting can raise this limit.",
		describeTimeout(timeout),
	))
}

// describeTimeout writes a limit the way a user reads it. The setting counts whole
// seconds, so only a test sees less than that.
func describeTimeout(timeout time.Duration) string {
	switch {
	case timeout < time.Second || timeout%time.Second != 0:
		return timeout.String()
	case timeout == time.Second:
		return "1 second"
	default:
		return fmt.Sprintf("%d seconds", int64(timeout/time.Second))
	}
}
