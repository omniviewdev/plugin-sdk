// Package state resolves SDK-owned application state. It does not sandbox plugin code.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/omniviewdev/plugin-sdk/pkg/profilecontract"
)

const (
	StateDirectoryEnvironment = "OMNIVIEW_STATE_DIR"
	WorkspaceFileEnvironment  = "OMNIVIEW_WORKSPACE_FILE"
)

// ResolveRoot consumes the host's resolved root, never choosing a workspace on
// the host's behalf. Only launches without workspace context may use production.
func ResolveRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	selected := os.Getenv(StateDirectoryEnvironment)
	workspace := os.Getenv(WorkspaceFileEnvironment)
	if selected == "" {
		if workspace != "" {
			return "", fmt.Errorf("%s is required in workspace context", StateDirectoryEnvironment)
		}
		selected = filepath.Join(home, ".omniview")
	}
	if !filepath.IsAbs(selected) {
		return "", fmt.Errorf("%s must be an absolute resolved path", StateDirectoryEnvironment)
	}
	root, err := profilecontract.CanonicalPath(selected)
	if err != nil {
		return "", err
	}
	if filepath.Dir(root) == root {
		return "", fmt.Errorf("state root cannot be a filesystem root")
	}
	if workspace == "" {
		return root, nil
	}
	if !filepath.IsAbs(workspace) {
		return "", fmt.Errorf("%s must be absolute", WorkspaceFileEnvironment)
	}
	workspace, err = profilecontract.CanonicalPath(workspace)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(workspace)
	if err != nil {
		return "", fmt.Errorf("read workspace manifest: %w", err)
	}
	// Read only identity fields; the host owns validation of the full manifest.
	var manifest profilecontract.WorkspaceIdentity
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("parse workspace manifest: %w", err)
	}
	id, err := uuid.Parse(manifest.WorkspaceID)
	if err != nil || id == uuid.Nil || id.String() != manifest.WorkspaceID || manifest.SchemaVersion != 1 ||
		manifest.Profile.Kind != "development" {
		return "", fmt.Errorf("workspace requires v1 development identity and canonical nonzero UUID")
	}
	customRoot, custom, err := resolveCustomRoot(workspace, home, manifest)
	if err != nil {
		return "", err
	}
	if custom {
		if root != customRoot {
			return "", fmt.Errorf("state root conflicts with machine-local custom profile binding")
		}
		return root, nil
	}
	expected, err := profilecontract.CanonicalPath(
		filepath.Join(home, ".omniview-dev", "profiles", manifest.WorkspaceID, "state"),
	)
	if err != nil {
		return "", err
	}
	if root != expected {
		return "", fmt.Errorf("state root conflicts with workspace profile")
	}
	profiles, err := profilecontract.CanonicalPath(filepath.Join(home, ".omniview-dev", "profiles"))
	if err != nil {
		return "", err
	}
	expectedProfile := filepath.Join(profiles, manifest.WorkspaceID)
	profile, err := profilecontract.CanonicalPath(expectedProfile)
	if err != nil {
		return "", err
	}
	if profile != expectedProfile || root != filepath.Join(expectedProfile, "state") {
		return "", fmt.Errorf("workspace state must remain at its exact UUID/state namespace")
	}
	for _, name := range []string{".omniview", ".omniview-nightly"} {
		protected, err := profilecontract.CanonicalPath(filepath.Join(home, name))
		if err != nil {
			return "", err
		}
		if contained(protected, root) || contained(root, protected) {
			return "", fmt.Errorf("workspace state overlaps protected profile")
		}
	}
	return root, nil
}

func contained(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ValidateComponent rejects path syntax for plugin IDs and capability filenames.
func ValidateComponent(name string) error {
	if name == "" || name == "." || name == ".." || strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid state path component %q", name)
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("invalid state path component %q", name)
		}
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("invalid trailing dot in state path component")
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' &&
			base[3] <= '9' {
		return fmt.Errorf("reserved state path component %q", name)
	}
	return nil
}

// RejectSymlink refuses aliases between owned state paths. Root operations also
// enforce containment if an ancestor changes between validation and access.
func RejectSymlink(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("state path %q must not be a symlink", name)
	}
	return nil
}

// OpenDirectory opens a direct owned child without allowing symlink aliases.
func OpenDirectory(root *os.Root, name string, create bool) (*os.Root, error) {
	if err := ValidateComponent(name); err != nil {
		return nil, err
	}
	if err := RejectSymlink(root, name); err != nil {
		return nil, err
	}
	if create {
		if err := root.Mkdir(name, 0o700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	return root.OpenRoot(name)
}

// OpenPluginRoot anchors operations to an installed plugin. Callers must close it.
func OpenPluginRoot(pluginID string) (*os.Root, error) {
	if err := ValidateComponent(pluginID); err != nil {
		return nil, err
	}
	path, err := ResolveRoot()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	plugins, err := OpenDirectory(root, "plugins", false)
	if err != nil {
		return nil, err
	}
	defer plugins.Close()
	return OpenDirectory(plugins, pluginID, false)
}
