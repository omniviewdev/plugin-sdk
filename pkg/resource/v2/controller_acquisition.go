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
// A child derived during acquisition keeps the deadline it observed, as for any
// parent.
type acquisitionContext struct {
	context.Context
	mu       sync.Mutex
	deadline time.Time
	err      error
	done     chan struct{}
	// expired closes only when the deadline passed before complete.
	expired   chan struct{}
	timedOut  bool
	completed bool
	timer     *time.Timer
}

func newAcquisitionContext(parent context.Context, timeout time.Duration) *acquisitionContext {
	a := &acquisitionContext{
		Context:  parent,
		deadline: time.Now().Add(timeout),
		done:     make(chan struct{}),
		expired:  make(chan struct{}),
	}
	// The registration is intentionally never stopped: the generation context
	// outlives acquisition and must keep propagating its cancellation.
	context.AfterFunc(parent, a.parentDone)
	a.timer = time.AfterFunc(timeout, a.expire)
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

func (a *acquisitionContext) parentDone() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return
	}
	a.err = a.Context.Err()
	close(a.done)
}

func (a *acquisitionContext) expire() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.completed || a.timedOut {
		return
	}
	a.timedOut = true
	close(a.expired)
	if a.err == nil {
		a.err = context.DeadlineExceeded
		close(a.done)
	}
}

// complete ends the acquisition deadline. It returns false when the deadline
// already passed, in which case the acquisition is abandoned and its result must
// be discarded. The two outcomes are mutually exclusive: exactly one of complete
// and expiry wins.
func (a *acquisitionContext) complete() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timedOut {
		return false
	}
	a.completed = true
	a.deadline = time.Time{}
	a.timer.Stop()
	return true
}

type acquisitionResult[T any] struct {
	client *T
	err    error
}

// acquire runs CreateClient and OnStart off the creating goroutine so a callback
// that ignores its context cannot hold the pending slot past the acquisition
// deadline. It delivers its result only if the deadline has not passed; a result
// that arrives after the deadline is destroyed here, exactly once, because the
// start has already been reported as failed and nothing else owns it.
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

func acquisitionTimeoutError(timeout time.Duration) error {
	return NewError("TIMEOUT", fmt.Sprintf("Connection acquisition did not finish within %s.", timeout))
}
