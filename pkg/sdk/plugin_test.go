package sdk

import (
	"testing"

	"github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/lifecycle"
)

func TestCurrentProtocolVersion_IsOne(t *testing.T) {
	assert.Equal(t, 1, CurrentProtocolVersion)
}

func TestRegisterLifecycle_UseCurrentProtocolVersion(t *testing.T) {
	p := &Plugin{
		pluginMap: map[string]plugin.Plugin{
			"resource": nil, // dummy capability
		},
		meta: config.PluginMeta{ID: "test-plugin", Version: "1.0.0"},
	}
	p.registerLifecycle()

	lcPlugin, ok := p.pluginMap["lifecycle"]
	require.True(t, ok, "lifecycle should be registered")
	require.NotNil(t, lcPlugin, "lifecycle plugin should not be nil")

	// Verify the server inside uses CurrentProtocolVersion
	lc, ok := lcPlugin.(*lifecycle.Plugin)
	require.True(t, ok, "lifecycle plugin should be *lifecycle.Plugin")
	assert.Equal(t, int32(CurrentProtocolVersion), lc.Impl.SDKProtocolVersion)
}
