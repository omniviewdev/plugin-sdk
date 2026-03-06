package sdk

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/stretchr/testify/require"
)

func writeTestPluginYAML(t *testing.T, homeDir string, pluginID string) {
	t.Helper()
	dir := filepath.Join(homeDir, ".omniview", "plugins", pluginID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	yaml := []byte("id: " + pluginID + "\nversion: 1.0.0\nname: test-plugin\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.yaml"), yaml, 0o644))
}

func TestNewPlugin_SetsDefaultLogger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestPluginYAML(t, os.Getenv("HOME"), "plugin-logger-test")

	oldDefault := logging.Default()
	t.Cleanup(func() { logging.SetDefault(oldDefault) })

	p := NewPlugin(PluginOpts{
		ID:    "plugin-logger-test",
		Debug: true,
	})
	require.NotNil(t, p.Log)
	require.NotNil(t, p.LogLevel)

	// NewPlugin should wire SDK default logger so constructors using
	// logging.Default().Named(...) inherit plugin logging behavior.
	require.True(t, logging.Default().Enabled(context.Background(), logging.LevelDebug))
}

func TestPlugin_SetLogLevel_AffectsLoggerGating(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestPluginYAML(t, os.Getenv("HOME"), "plugin-level-test")

	oldDefault := logging.Default()
	t.Cleanup(func() { logging.SetDefault(oldDefault) })

	p := NewPlugin(PluginOpts{
		ID: "plugin-level-test",
	})

	require.True(t, p.Log.Enabled(context.Background(), logging.LevelInfo))
	require.False(t, p.Log.Enabled(context.Background(), logging.LevelDebug))

	p.SetLogLevel(logging.LevelDebug)
	require.True(t, p.Log.Enabled(context.Background(), logging.LevelDebug))

	p.SetLogLevel(logging.LevelError)
	require.False(t, p.Log.Enabled(context.Background(), logging.LevelWarn))
	require.True(t, p.Log.Enabled(context.Background(), logging.LevelError))
}
