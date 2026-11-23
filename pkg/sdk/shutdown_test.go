package sdk

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	logging "github.com/omniviewdev/plugin-sdk/log"
)

// shutdownProbe is a capability that has something to end when the plugin stops.
type shutdownProbe struct {
	plugin.Plugin

	mu       sync.Mutex
	calls    int
	deadline bool
	err      error
	wait     <-chan struct{}
}

func (s *shutdownProbe) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.calls++
	_, s.deadline = ctx.Deadline()
	wait, err := s.wait, s.err
	s.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (s *shutdownProbe) state() (calls int, deadline bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.deadline
}

// When the plugin's server has stopped, a capability that holds sessions is told, with a bound, so that it ends them: the
// exec lane's sessions are cancelled and their functions given their grace instead of being left to the process's exit.
func TestStoppingThePluginShutsDownTheCapabilitiesThatHaveSomethingToEnd(t *testing.T) {
	probe := &shutdownProbe{}
	quiet := &shutdownProbe{err: errors.New("it failed")}
	p := &Plugin{Log: logging.NewNop(), pluginMap: map[string]plugin.Plugin{"exec": probe, "other": quiet, "plain": nil}}
	p.shutdownCapabilities(time.Second)
	for name, c := range map[string]*shutdownProbe{"exec": probe, "other": quiet} {
		calls, deadline := c.state()
		require.Equal(t, 1, calls, "%s: Shutdown is called once", name)
		require.True(t, deadline, "%s: Shutdown is given a deadline: a plugin that is stopping must not wait for ever", name)
	}
}

// A capability that does not return is given up on at the bound: the plugin goes on stopping.
func TestAShutdownThatNeverReturnsDoesNotKeepThePluginFromStopping(t *testing.T) {
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	probe := &shutdownProbe{wait: never}
	p := &Plugin{Log: logging.NewNop(), pluginMap: map[string]plugin.Plugin{"exec": probe}}
	done := make(chan struct{})
	go func() {
		p.shutdownCapabilities(50 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdownCapabilities did not return: it must give up at its bound")
	}
}

// The bounds a stopping plugin gives its capabilities, and the method it holds, are written in docs/exec.md and in the doc of the
// exec capability's Shutdown: a change of one is a change of what every author and every host is told, so each is pinned (the
// review of #90 found the 10 s unpinned: a plugin that waited 11 s was seen by no test).
func TestTheBoundsOfAStoppingPluginAndTheMethodItHoldsAreTheOnesTheDocsPrint(t *testing.T) {
	require.Equal(t, 10*time.Second, capabilityShutdownBound, "the bound once the server has stopped")
	require.Equal(t, 1500*time.Millisecond, capabilityPreStopBound, "the bound that holds the controller's Shutdown")
	require.Equal(t, "/plugin.GRPCController/Shutdown", controllerShutdownMethod, "go-plugin's controller method that stops the server")
}

// The interceptor tells the capabilities, with a bound, before it lets the controller's Shutdown run, and for that method only.
func TestTheInterceptorTellsTheCapabilitiesBeforeTheControllersHandlerRunsAndOnlyForIt(t *testing.T) {
	probe := &shutdownProbe{}
	p := &Plugin{Log: logging.NewNop(), pluginMap: map[string]plugin.Plugin{"exec": probe}}
	intercept := p.shutdownBeforeStop(time.Second)
	var toldBeforeHandler int
	var gaveBound bool
	handler := func(context.Context, any) (any, error) {
		toldBeforeHandler, gaveBound = probe.state()
		return "answer", nil
	}

	resp, err := intercept(context.Background(), "request", &grpc.UnaryServerInfo{FullMethod: controllerShutdownMethod}, handler)
	require.NoError(t, err)
	require.Equal(t, "answer", resp, "the handler's answer is the call's answer")
	require.Equal(t, 1, toldBeforeHandler, "the capabilities are told once before the controller's handler runs")
	require.True(t, gaveBound, "and with a bound")

	toldBeforeHandler = -1
	resp, err = intercept(context.Background(), "request", &grpc.UnaryServerInfo{FullMethod: "/omniview.sdk.exec.v2.Exec/Describe"}, handler)
	require.NoError(t, err)
	require.Equal(t, "answer", resp)
	calls, _ := probe.state()
	require.Equal(t, 1, calls, "another method does not tell the capabilities again")
	require.Equal(t, 1, toldBeforeHandler, "and its handler runs at once")
}

// Whatever became of the capabilities, the controller's handler runs: one that fails, and one that does not return within the bound,
// keep the server from stopping for no longer than the bound.
func TestTheControllersHandlerRunsWhateverBecameOfTheCapabilities(t *testing.T) {
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	for name, probe := range map[string]*shutdownProbe{
		"a capability that fails":                {err: errors.New("it failed")},
		"a capability that does not return":      {wait: never},
		"a capability that returns in good time": {},
	} {
		t.Run(name, func(t *testing.T) {
			p := &Plugin{Log: logging.NewNop(), pluginMap: map[string]plugin.Plugin{"exec": probe}}
			ran := false
			handler := func(context.Context, any) (any, error) { ran = true; return nil, nil }
			_, err := p.shutdownBeforeStop(20*time.Millisecond)(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: controllerShutdownMethod}, handler)
			require.NoError(t, err)
			require.True(t, ran, "the controller's handler, which stops the server, did not run")
		})
	}
}
