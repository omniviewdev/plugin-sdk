package sdk

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"
	"github.com/omniviewdev/plugin-sdk/internal/testutil"
	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/utils"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/lifecycle"
	"github.com/stretchr/testify/require"
)

// This helper runs only as the managed client's disposable child, never against
// installed user plugins. The parent supplies temporary HOME, USERPROFILE/state.
func TestManagedServeHelperProcess(t *testing.T) {
	if os.Getenv("OMNIVIEW_SDK_MANAGED_TEST_CHILD") != "1" {
		return
	}
	store, err := utils.GetStore("resource", "managed-test-plugin")
	require.NoError(t, err)
	_, err = store.WriteString("workspace sentinel")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	NewPlugin(PluginOpts{ID: "managed-test-plugin"}).Serve()
}

func TestManagedServePreservesLegacyDiscoveryAndProductionState(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv("OMNIVIEW_STATE_DIR", root)
	t.Setenv("OMNIVIEW_WORKSPACE_FILE", "")
	t.Setenv("OMNIVIEW_DEV", "1") // Must not reactivate the removed writer.
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "false")
	t.Setenv("OMNIVIEW_TELEMETRY_PROFILING", "false")
	pluginDir := filepath.Join(root, "plugins", "managed-test-plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("id: managed-test-plugin\nversion: 1.0.0\n"), 0600))
	legacyPath := filepath.Join(pluginDir, ".devinfo")
	legacy := []byte(`{"pid":1,"generation":"foreign-owner"}`)
	require.NoError(t, os.WriteFile(legacyPath, legacy, 0600))
	productionPath := filepath.Join(home, ".omniview", "plugins", "managed-test-plugin", "store", "resource")
	require.NoError(t, os.MkdirAll(filepath.Dir(productionPath), 0700))
	require.NoError(t, os.WriteFile(productionPath, []byte("production sentinel"), 0600))

	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedServeHelperProcess$")
	cmd.Env = append(os.Environ(), "OMNIVIEW_SDK_MANAGED_TEST_CHILD=1", "GOWORK=off")
	meta := config.PluginMeta{ID: "managed-test-plugin"}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: meta.GenerateHandshakeConfig(),
		VersionedPlugins: map[int]plugin.PluginSet{
			CurrentProtocolVersion: {"lifecycle": &lifecycle.Plugin{}},
		},
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		StartTimeout:     10 * time.Second,
	})
	t.Cleanup(client.Kill)
	rpc, err := client.Client()
	require.NoError(t, err)
	capability, err := rpc.Dispense("lifecycle")
	require.NoError(t, err)
	lc, ok := capability.(*lifecycle.Client)
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := lc.GetInfo(ctx)
	require.NoError(t, err)
	require.Equal(t, "managed-test-plugin", info.PluginId)
	_, err = lc.HealthCheck(ctx)
	require.NoError(t, err)

	client.Kill()

	data, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	require.Equal(t, legacy, data, "managed startup/shutdown must not replace or delete a foreign discovery record")
	data, err = os.ReadFile(productionPath)
	require.NoError(t, err)
	require.Equal(t, "production sentinel", string(data))
	data, err = os.ReadFile(filepath.Join(pluginDir, "store", "resource"))
	require.NoError(t, err)
	require.Equal(t, "workspace sentinel", string(data))
	discoveryFiles, err := filepath.Glob(filepath.Join(pluginDir, ".devinfo*"))
	require.NoError(t, err)
	require.Equal(t, []string{legacyPath}, discoveryFiles, "managed serving must not create discovery files, temporary files, or locks")
}
