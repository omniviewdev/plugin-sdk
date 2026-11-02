package lifecycle

import (
	"context"
	"os"
	"sync"

	"github.com/hashicorp/go-plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/hostconnections"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/pkg/profiling"

	"google.golang.org/protobuf/types/known/emptypb"

	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
)

// Server implements the PluginLifecycle gRPC service.
// It is auto-populated by sdk.Plugin.Serve() — plugin authors never touch it.
type Server struct {
	lifecyclepb.UnimplementedPluginLifecycleServer

	PluginID           string
	Version            string
	SDKProtocolVersion int32
	Capabilities       []string
	ProfilingStatus    func() profiling.Status
	HostConnections    *hostconnections.Binding
	broker             *plugin.GRPCBroker
	mu                 sync.Mutex
	bound              bool
}

// GetInfo returns the process metadata captured at lifecycle registration.
func (s *Server) GetInfo(_ context.Context, _ *emptypb.Empty) (*lifecyclepb.GetInfoResponse, error) {
	return &lifecyclepb.GetInfoResponse{
		PluginId:               s.PluginID,
		Version:                s.Version,
		SdkProtocolVersion:     s.SDKProtocolVersion,
		HostConnectionsVersion: hostconnections.Version,
	}, nil
}

// GetCapabilities returns the registered capability snapshot. Direct callers must not mutate its slice.
func (s *Server) GetCapabilities(_ context.Context, _ *emptypb.Empty) (*lifecyclepb.GetCapabilitiesResponse, error) {
	return &lifecyclepb.GetCapabilitiesResponse{
		Capabilities: s.Capabilities,
	}, nil
}

// HealthCheck reports that this plugin process serves RPCs; it does not probe providers.
func (s *Server) HealthCheck(_ context.Context, _ *emptypb.Empty) (*lifecyclepb.HealthCheckResponse, error) {
	return &lifecyclepb.HealthCheckResponse{
		Status: lifecyclepb.ServingStatus_SERVING_STATUS_SERVING,
	}, nil
}

// GetProfilingStatus reports current listener facts and this process's PID.
func (s *Server) GetProfilingStatus(_ context.Context, _ *emptypb.Empty) (*lifecyclepb.ProfilingStatusResponse, error) {
	var status profiling.Status
	if s.ProfilingStatus != nil {
		status = s.ProfilingStatus()
	}
	return &lifecyclepb.ProfilingStatusResponse{
		Running: status.Running,
		Url:     status.URL,
		Error:   status.Error,
		Pid:     int64(os.Getpid()),
	}, nil
}

// BindHostServices dials and binds the host callback exactly once.
func (s *Server) BindHostServices(_ context.Context, req *lifecyclepb.BindHostServicesRequest) (*lifecyclepb.BindHostServicesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.GetVersion() != hostconnections.Version || req.GetHostConnectionsBrokerId() == 0 {
		return nil, status.Error(codes.FailedPrecondition, "Unsupported host connections binding.")
	}
	if s.bound {
		return nil, status.Error(codes.FailedPrecondition, "Host connections service is already bound.")
	}
	if s.broker == nil || s.HostConnections == nil {
		return nil, status.Error(codes.FailedPrecondition, "Host connections service is unavailable.")
	}
	conn, err := s.broker.Dial(req.GetHostConnectionsBrokerId())
	if err != nil {
		return nil, status.Error(codes.Unavailable, "Host connections service is unavailable.")
	}
	if err := s.HostConnections.Bind(conn); err != nil {
		conn.Close()
		return nil, status.Error(codes.FailedPrecondition, "Host connections service is already bound.")
	}
	s.bound = true
	return &lifecyclepb.BindHostServicesResponse{Version: hostconnections.Version}, nil
}
