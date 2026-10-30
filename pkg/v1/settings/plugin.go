package settings

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
)

// SettingsPlugin serves or consumes settings through [plugin.Plugin] gRPC integration.
type SettingsPlugin struct {
	plugin.Plugin

	// Concrete implementation, written in Go. This is only used for plugins
	// that are written in Go.
	Impl Provider
}

// GRPCServer registers the supplied provider on the plugin-owned gRPC server.
func (p *SettingsPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	settingspb.RegisterSettingsPluginServer(s, &SettingsPluginServer{Impl: p.Impl})
	return nil
}

// GRPCClient wraps the broker-owned connection without taking ownership of its lifetime.
func (p *SettingsPlugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (any, error) {
	return &Client{client: settingspb.NewSettingsPluginClient(c)}, nil
}
