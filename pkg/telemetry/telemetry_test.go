package telemetry

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigFromEnv_Disabled(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "false")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "test-plugin")

	cfg := configFromEnv()
	assert.False(t, cfg.Enabled)
	assert.Equal(t, "test-plugin", cfg.PluginID)
}

func TestConfigFromEnv_Enabled(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "true")
	t.Setenv("OMNIVIEW_TELEMETRY_OTLP_ENDPOINT", "localhost:4318")
	t.Setenv("OMNIVIEW_TELEMETRY_AUTH_HEADER", "Authorization")
	t.Setenv("OMNIVIEW_TELEMETRY_AUTH_VALUE", "Bearer tok")
	t.Setenv("OMNIVIEW_TELEMETRY_PROFILING", "true")
	t.Setenv("OMNIVIEW_TELEMETRY_PYROSCOPE_ENDPOINT", "localhost:4040")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "kubernetes")

	cfg := configFromEnv()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "localhost:4318", cfg.OTLPEndpoint)
	assert.Equal(t, "Authorization", cfg.AuthHeader)
	assert.Equal(t, "Bearer tok", cfg.AuthValue)
	assert.True(t, cfg.Profiling)
	assert.Equal(t, "localhost:4040", cfg.PyroscopeEndpoint)
	assert.Equal(t, "kubernetes", cfg.PluginID)
}

func TestConfigFromEnv_MissingEnv(t *testing.T) {
	os.Unsetenv("OMNIVIEW_TELEMETRY_ENABLED")
	os.Unsetenv("OMNIVIEW_PLUGIN_ID")

	cfg := configFromEnv()
	assert.False(t, cfg.Enabled)
	assert.Empty(t, cfg.PluginID)
}

func TestInitFromEnv_Disabled(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "false")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "test")

	provider, err := InitFromEnv()
	require.NoError(t, err)
	assert.Nil(t, provider)
}

func TestInitFromEnv_Enabled(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "true")
	t.Setenv("OMNIVIEW_TELEMETRY_OTLP_ENDPOINT", "localhost:4318")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "test-plugin")
	t.Setenv("OMNIVIEW_TELEMETRY_PROFILING", "false")

	provider, err := InitFromEnv()
	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.tracerProvider)
	assert.Nil(t, provider.profiler)

	// Shutdown should be idempotent
	require.NoError(t, provider.Shutdown(t.Context()))
	require.NoError(t, provider.Shutdown(t.Context()))
}
