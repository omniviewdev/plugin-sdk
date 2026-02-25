package lifecycle

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/proto"
)

// Server implements the PluginLifecycle gRPC service.
// It is auto-populated by sdk.Plugin.Serve() — plugin authors never touch it.
type Server struct {
	proto.UnimplementedPluginLifecycleServer

	PluginID           string
	Version            string
	SDKProtocolVersion int32
	Capabilities       []string
}

func (s *Server) GetInfo(_ context.Context, _ *emptypb.Empty) (*proto.GetInfoResponse, error) {
	return &proto.GetInfoResponse{
		PluginId:           s.PluginID,
		Version:            s.Version,
		SdkProtocolVersion: s.SDKProtocolVersion,
	}, nil
}

func (s *Server) GetCapabilities(_ context.Context, _ *emptypb.Empty) (*proto.GetCapabilitiesResponse, error) {
	return &proto.GetCapabilitiesResponse{
		Capabilities: s.Capabilities,
	}, nil
}

func (s *Server) HealthCheck(_ context.Context, _ *emptypb.Empty) (*proto.HealthCheckResponse, error) {
	return &proto.HealthCheckResponse{
		Status: proto.ServingStatus_SERVING,
	}, nil
}
