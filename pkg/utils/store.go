package utils

import (
	"os"
	"path/filepath"

	"github.com/omniviewdev/plugin-sdk/pkg/state"
)

// GetPluginStorePath returns a validated store path for an installed plugin.
// SDK operations use directory handles rather than reopening this advisory path.
func GetPluginStorePath(pluginID string) (string, error) {
	plugin, err := state.OpenPluginRoot(pluginID)
	if err != nil {
		return "", err
	}
	defer plugin.Close()
	if err := state.RejectSymlink(plugin, "store"); err != nil {
		return "", err
	}
	return filepath.Join(plugin.Name(), "store"), nil
}

// InitializePluginStore creates the private store for an installed plugin.
func InitializePluginStore(pluginID string) error {
	store, err := openStore(pluginID, true)
	if err != nil {
		return err
	}
	return store.Close()
}

func openStore(pluginID string, create bool) (*os.Root, error) {
	plugin, err := state.OpenPluginRoot(pluginID)
	if err != nil {
		return nil, err
	}
	defer plugin.Close()
	return state.OpenDirectory(plugin, "store", create)
}

// GetStore opens a capability store, creating it if needed. The caller closes it.
func GetStore(capability, pluginID string) (*os.File, error) {
	if err := state.ValidateComponent(capability); err != nil {
		return nil, err
	}
	store, err := openStore(pluginID, true)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	if err := state.RejectSymlink(store, capability); err != nil {
		return nil, err
	}
	return store.OpenFile(capability, os.O_CREATE|os.O_RDWR, 0600)
}

// RemoveStore removes only the selected plugin's capability store.
func RemoveStore(capability, pluginID string) error {
	if err := state.ValidateComponent(capability); err != nil {
		return err
	}
	store, err := openStore(pluginID, false)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := state.RejectSymlink(store, capability); err != nil {
		return err
	}
	return store.Remove(capability)
}
