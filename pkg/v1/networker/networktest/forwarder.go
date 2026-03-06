package networktest

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/networker"
)

// StubResourceForwarder implements networker.ResourceForwarder with configurable behavior.
type StubResourceForwarder struct {
	// FailWith causes ForwardResource to return this error immediately.
	FailWith error

	// FailAfter causes the ErrCh to emit this error after Ready closes.
	// Simulates a tunnel that starts successfully but dies later.
	FailAfter error

	calls atomic.Int32
	mu    sync.Mutex
	opts  []networker.ResourcePortForwardHandlerOpts
}

var _ networker.ResourceForwarder = (*StubResourceForwarder)(nil)

func (s *StubResourceForwarder) ForwardResource(
	_ context.Context,
	_ *types.PluginContext,
	opts networker.ResourcePortForwardHandlerOpts,
) (*networker.ForwarderResult, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.opts = append(s.opts, opts)
	s.mu.Unlock()

	if s.FailWith != nil {
		return nil, s.FailWith
	}

	sessionID := uuid.NewString()
	ready := make(chan struct{})
	errCh := make(chan error, 1)

	close(ready) // immediately ready

	if s.FailAfter != nil {
		go func() {
			errCh <- s.FailAfter
			close(errCh)
		}()
	} else {
		close(errCh) // clean exit
	}

	return &networker.ForwarderResult{
		SessionID: sessionID,
		Ready:     ready,
		ErrCh:     errCh,
	}, nil
}

// Calls returns the number of times ForwardResource was called.
func (s *StubResourceForwarder) Calls() int {
	return int(s.calls.Load())
}

// RecordedOpts returns all opts passed to ForwardResource.
func (s *StubResourceForwarder) RecordedOpts() []networker.ResourcePortForwardHandlerOpts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.opts)
}

// StubStaticForwarder implements networker.StaticForwarder with configurable behavior.
type StubStaticForwarder struct {
	// FailWith causes ForwardStatic to return this error immediately.
	FailWith error

	// FailAfter causes the ErrCh to emit this error after Ready closes.
	// Simulates a tunnel that starts successfully but dies later.
	FailAfter error

	calls atomic.Int32
}

var _ networker.StaticForwarder = (*StubStaticForwarder)(nil)

func (s *StubStaticForwarder) ForwardStatic(
	_ context.Context,
	_ *types.PluginContext,
	_ networker.StaticPortForwardHandlerOpts,
) (*networker.ForwarderResult, error) {
	s.calls.Add(1)

	if s.FailWith != nil {
		return nil, s.FailWith
	}

	sessionID := uuid.NewString()
	ready := make(chan struct{})
	errCh := make(chan error, 1)

	close(ready) // immediately ready

	if s.FailAfter != nil {
		go func() {
			errCh <- s.FailAfter
			close(errCh)
		}()
	} else {
		close(errCh) // clean exit
	}

	return &networker.ForwarderResult{
		SessionID: sessionID,
		Ready:     ready,
		ErrCh:     errCh,
	}, nil
}

// Calls returns the number of times ForwardStatic was called.
func (s *StubStaticForwarder) Calls() int {
	return int(s.calls.Load())
}
