package lifecycle

import (
	"context"

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
}

func (s *Server) GetInfo(_ context.Context, _ *emptypb.Empty) (*lifecyclepb.GetInfoResponse, error) {
	return &lifecyclepb.GetInfoResponse{
		PluginId:           s.PluginID,
		Version:            s.Version,
		SdkProtocolVersion: s.SDKProtocolVersion,
	}, nil
}

func (s *Server) GetCapabilities(_ context.Context, _ *emptypb.Empty) (*lifecyclepb.GetCapabilitiesResponse, error) {
	return &lifecyclepb.GetCapabilitiesResponse{
		Capabilities: s.Capabilities,
	}, nil
}

func (s *Server) HealthCheck(_ context.Context, _ *emptypb.Empty) (*lifecyclepb.HealthCheckResponse, error) {
	return &lifecyclepb.HealthCheckResponse{
		Status: lifecyclepb.ServingStatus_SERVING_STATUS_SERVING,
	}, nil
}
