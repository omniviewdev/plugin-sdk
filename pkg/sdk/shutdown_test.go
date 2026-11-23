package sdk

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"

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
