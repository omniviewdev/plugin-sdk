// Package lifecycle exposes plugin identity, declared capabilities, and process readiness over gRPC.
package lifecycle

import (
	"context"
	"errors"
	"sync"

	"github.com/hashicorp/go-plugin"
	rp "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/hostconnections"
	"google.golang.org/grpc"

	"google.golang.org/protobuf/types/known/emptypb"

	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
)

// Client wraps the generated gRPC client for the PluginLifecycle service.
type Client struct {
	client lifecyclepb.PluginLifecycleClient
	broker *plugin.GRPCBroker
}

// NewClient creates a properly initialized lifecycle Client.
func NewClient(c lifecyclepb.PluginLifecycleClient) *Client {
	return &Client{client: c}
}

// GetInfo returns plugin identity and protocol metadata, or an error for an uninitialized client.
func (c *Client) GetInfo(ctx context.Context) (*lifecyclepb.GetInfoResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("lifecycle client not initialized")
	}
	return c.client.GetInfo(ctx, &emptypb.Empty{})
}

// GetCapabilities returns declared transports, not authorization or per-resource operation support.
func (c *Client) GetCapabilities(ctx context.Context) (*lifecyclepb.GetCapabilitiesResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("lifecycle client not initialized")
	}
	return c.client.GetCapabilities(ctx, &emptypb.Empty{})
}

// HealthCheck queries process serving status, not native connection health.
func (c *Client) HealthCheck(ctx context.Context) (*lifecyclepb.HealthCheckResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("lifecycle client not initialized")
	}
	return c.client.HealthCheck(ctx, &emptypb.Empty{})
}

// GetProfilingStatus queries the profiler owned by the plugin process.
func (c *Client) GetProfilingStatus(ctx context.Context) (*lifecyclepb.ProfilingStatusResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("lifecycle client not initialized")
	}
	return c.client.GetProfilingStatus(ctx, &emptypb.Empty{})
}

// BindHostServices negotiates the callback service for this plugin process.
func (c *Client) BindHostServices(ctx context.Context, brokerID uint32, version uint32) (uint32, error) {
	if c == nil || c.client == nil {
		return 0, errors.New("lifecycle client not initialized")
	}
	response, err := c.client.BindHostServices(ctx, &lifecyclepb.BindHostServicesRequest{HostConnectionsBrokerId: brokerID, Version: version})
	if err != nil {
		return 0, err
	}
	return response.GetVersion(), nil
}

// ServeHostConnections binds a callback server to this plugin process.
// The caller must invoke stop when the process or its grants retire.
// The context bounds binding only; stop owns the accepted server's lifetime.
// A binding error can follow remote acceptance. Retire the process on error
// instead of retrying a binding whose outcome is uncertain.
func (c *Client) ServeHostConnections(ctx context.Context, service hostconnections.Service) (stop func(), err error) {
	if c == nil || c.client == nil || c.broker == nil || service == nil {
		return nil, errors.New("host connections broker is unavailable")
	}
	var mutex sync.Mutex
	var once sync.Once
	var server *grpc.Server
	stopped := false
	stop = func() {
		once.Do(func() {
			mutex.Lock()
			stopped = true
			current := server
			mutex.Unlock()
			if current != nil {
				current.Stop()
			}
		})
	}
	brokerID := c.broker.NextId()
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.broker.AcceptAndServe(brokerID, func(options []grpc.ServerOption) *grpc.Server {
			current := grpc.NewServer(append(options, rp.ServerOptions()...)...)
			hostconnections.RegisterServer(current, service)
			mutex.Lock()
			server = current
			alreadyStopped := stopped
			mutex.Unlock()
			if alreadyStopped {
				current.Stop()
			}
			close(ready)
			return current
		})
	}()
	select {
	case <-ready:
	case <-done:
		stop()
		return nil, errors.New("host connections server could not start")
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	}
	version, err := c.BindHostServices(ctx, brokerID, hostconnections.Version)
	if err != nil {
		stop()
		return nil, err
	}
	if version != hostconnections.Version {
		stop()
		return nil, errors.New("unsupported host connections version")
	}
	return stop, nil
}
