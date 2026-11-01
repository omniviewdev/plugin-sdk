package lifecycle

import (
	"context"
	"os"

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
}

// GetInfo returns the process metadata captured at lifecycle registration.
func (s *Server) GetInfo(_ context.Context, _ *emptypb.Empty) (*lifecyclepb.GetInfoResponse, error) {
	return &lifecyclepb.GetInfoResponse{
		PluginId:           s.PluginID,
		Version:            s.Version,
		SdkProtocolVersion: s.SDKProtocolVersion,
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
