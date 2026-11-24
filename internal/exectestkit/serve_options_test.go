package exectestkit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// options counts the calls that pass the interceptors a test gives Serve: one on the plugin's server, one on the host's client.
type options struct {
	served, dialed atomic.Int64
}

func (o *options) config(extra exectestkit.ServeConfig) exectestkit.ServeConfig {
	extra.Targets = []exec.Target{{
		Info: exec.TargetInfo{Provider: "k8s", Type: "pods", DisplayName: "Pod"},
		Exec: func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil },
	}}
	extra.ServerOptions = []grpc.ServerOption{grpc.ChainUnaryInterceptor(
		func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			o.served.Add(1)
			return handler(ctx, req)
		})}
	extra.DialOptions = []grpc.DialOption{grpc.WithChainUnaryInterceptor(
		func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			o.dialed.Add(1)
			return invoker(ctx, method, req, reply, cc, opts...)
		})}
	return extra
}

func (o *options) check(t *testing.T, server *exectestkit.Server) {
	t.Helper()
	if _, err := server.V2(t).Describe(context.Background()); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got := o.served.Load(); got != 1 {
		t.Errorf("the server interceptor of the test saw %d calls, want 1: the server options were not given to the factory", got)
	}
	if got := o.dialed.Load(); got != 1 {
		t.Errorf("the client interceptor of the test saw %d calls, want 1: the dial options were not given to the client", got)
	}
}

// The options a test gives Serve reach the server's factory and the client's connection, over a socket.
func TestServeGivesTheCallersOptionsToTheServerAndTheClient(t *testing.T) {
	var o options
	o.check(t, exectestkit.Serve(t, o.config(exectestkit.ServeConfig{})))
}

// The same over a Path, which is what a test on a bubble's clock serves a plugin with: the plugin and its client are joined by the
// path, and a call crosses it.
func TestServeOverAPathCarriesTheCallsAndGivesTheCallersOptions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var o options
		path := exectestkit.NewPath(t)
		o.check(t, exectestkit.Serve(t, o.config(exectestkit.ServeConfig{Path: path})))
		if got := path.Dials(); got != 1 {
			t.Errorf("the client connected through the path %d times, want once", got)
		}
	})
}
