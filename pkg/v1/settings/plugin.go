package settings

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
)

// This is the implementation of plugin.Plugin so we can serve/consume this.
type SettingsPlugin struct {
	plugin.Plugin
	// Concrete implementation, written in Go. This is only used for plugins
	// that are written in Go.
	Impl Provider
}

func (p *SettingsPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	settingspb.RegisterSettingsPluginServer(s, &SettingsPluginServer{Impl: p.Impl})
	return nil
}

func (p *SettingsPlugin) GRPCClient(
	_ context.Context,
	_ *plugin.GRPCBroker,
	c *grpc.ClientConn,
) (interface{}, error) {
	return &Client{client: settingspb.NewSettingsPluginClient(c)}, nil
}
