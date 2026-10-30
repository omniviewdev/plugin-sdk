package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/internal/testutil"
)

func isolatedStore(t *testing.T) (string, string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv("OMNIVIEW_STATE_DIR", root)
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", "")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "plugins", "test-plugin"), 0o700))
	production := filepath.Join(home, ".omniview", "plugins", "test-plugin", "store")
	require.NoError(t, os.MkdirAll(production, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(production, "resource"), []byte("production sentinel"), 0o600))
	return root, production
}

func TestStoreOperationsStayInSelectedRoot(t *testing.T) {
	root, production := isolatedStore(t)
	file, err := GetStore("resource", "test-plugin")
	require.NoError(t, err)
	_, err = file.WriteString("workspace A")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	data, err := os.ReadFile(filepath.Join(root, "plugins", "test-plugin", "store", "resource"))
	require.NoError(t, err)
	require.Equal(t, "workspace A", string(data))
	other := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(other, "plugins", "test-plugin"), 0o700))
	t.Setenv("OMNIVIEW_STATE_DIR", other)
	file, err = GetStore("resource", "test-plugin")
	require.NoError(t, err)
	stat, err := file.Stat()
	require.NoError(t, err)
	require.Zero(t, stat.Size())
	require.NoError(t, file.Close())
	require.NoError(t, RemoveStore("resource", "test-plugin"))
	data, err = os.ReadFile(filepath.Join(production, "resource"))
	require.NoError(t, err)
	require.Equal(t, "production sentinel", string(data))
	data, err = os.ReadFile(filepath.Join(root, "plugins", "test-plugin", "store", "resource"))
	require.NoError(t, err)
	require.Equal(t, "workspace A", string(data))
}

func TestStoreRejectsTraversalAndSymlinkEscapes(t *testing.T) {
	root, _ := isolatedStore(t)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("untouched"), 0o600))
	for _, id := range []string{"../outside", "/tmp/outside", `a\b`} {
		_, err := GetStore("resource", id)
		require.Error(t, err)
	}
	for _, capability := range []string{"../sentinel", "/tmp/sentinel", `a\b`} {
		_, err := GetStore(capability, "test-plugin")
		require.Error(t, err)
		require.Error(t, RemoveStore(capability, "test-plugin"))
	}
	pluginDir := filepath.Join(root, "plugins", "test-plugin")
	require.NoError(t, os.Symlink(outside, filepath.Join(pluginDir, "store")))
	_, err := GetStore("sentinel", "test-plugin")
	require.Error(t, err)
	require.Error(t, RemoveStore("sentinel", "test-plugin"))
	require.NoError(t, os.Remove(filepath.Join(pluginDir, "store")))
	require.NoError(t, os.Mkdir(filepath.Join(pluginDir, "store"), 0o700))
	require.NoError(t, os.Symlink(sentinel, filepath.Join(pluginDir, "store", "resource")))
	_, err = GetStore("resource", "test-plugin")
	require.Error(t, err)
	require.Error(t, RemoveStore("resource", "test-plugin"))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "plugins", "escape")))
	_, err = GetPluginStorePath("escape")
	require.Error(t, err)
	data, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(data))
}
