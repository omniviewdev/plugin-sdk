package sdk

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/internal/testutil"
	logging "github.com/omniviewdev/plugin-sdk/log"
)

func writeTestPluginYAML(t *testing.T, homeDir string, pluginID string) {
	t.Helper()
	dir := filepath.Join(homeDir, ".omniview", "plugins", pluginID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	yaml := []byte("id: " + pluginID + "\nversion: 1.0.0\nname: test-plugin\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.yaml"), yaml, 0o644))
}

func TestNewPlugin_SetsDefaultLogger(t *testing.T) {
	testutil.SetHome(t, t.TempDir())
	t.Setenv("OMNIVIEW_STATE_DIR", "")
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", "")
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
	testutil.SetHome(t, t.TempDir())
	t.Setenv("OMNIVIEW_STATE_DIR", "")
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", "")
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

func TestNewPluginReadsOnlySelectedStateRoot(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv("OMNIVIEW_STATE_DIR", root)
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", "")
	pluginDir := filepath.Join(root, "plugins", "isolated-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o700))
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(pluginDir, "plugin.yaml"),
			[]byte("id: isolated-plugin\nversion: selected\n"),
			0o600,
		),
	)
	writeTestPluginYAML(t, home, "isolated-plugin")
	oldDefault := logging.Default()
	t.Cleanup(func() { logging.SetDefault(oldDefault) })
	p := NewPlugin(PluginOpts{ID: "isolated-plugin"})
	require.Equal(t, "selected", p.GetMeta().Version)
	require.Panics(t, func() { NewPlugin(PluginOpts{ID: "../escape"}) })
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", filepath.Join(t.TempDir(), "missing-workspace.json"))
	require.Panics(t, func() { NewPlugin(PluginOpts{ID: "isolated-plugin"}) })
}

func newEnvLevelPlugin(t *testing.T, id, envLevel string, debug bool) *Plugin {
	t.Helper()
	testutil.SetHome(t, t.TempDir())
	t.Setenv("OMNIVIEW_STATE_DIR", "")
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", "")
	t.Setenv(LogLevelEnvironment, envLevel)
	writeTestPluginYAML(t, os.Getenv("HOME"), id)
	oldDefault := logging.Default()
	t.Cleanup(func() { logging.SetDefault(oldDefault) })
	return NewPlugin(PluginOpts{ID: id, Debug: debug})
}

func TestNewPlugin_LogLevelEnvironment(t *testing.T) {
	ctx := context.Background()

	p := newEnvLevelPlugin(t, "plugin-env-debug", "debug", false)
	require.True(t, p.Log.Enabled(ctx, logging.LevelDebug))

	p = newEnvLevelPlugin(t, "plugin-env-warn", "warn", false)
	require.False(t, p.Log.Enabled(ctx, logging.LevelInfo))
	require.True(t, p.Log.Enabled(ctx, logging.LevelWarn))

	p = newEnvLevelPlugin(t, "plugin-env-bogus", "bogus", false)
	require.True(t, p.Log.Enabled(ctx, logging.LevelInfo))
	require.False(t, p.Log.Enabled(ctx, logging.LevelDebug))

	p = newEnvLevelPlugin(t, "plugin-env-empty", "", false)
	require.True(t, p.Log.Enabled(ctx, logging.LevelInfo))
	require.False(t, p.Log.Enabled(ctx, logging.LevelDebug))

	// Debug mode wins over a stricter host level.
	p = newEnvLevelPlugin(t, "plugin-env-debug-wins", "error", true)
	require.True(t, p.Log.Enabled(ctx, logging.LevelDebug))
}
