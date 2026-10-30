package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/internal/testutil"
)

const testWorkspaceID = "dcb2fe7b-1d71-4ff1-a05f-a28881457038"

func workspaceFixture(t *testing.T) string {
	t.Helper()
	manifest := filepath.Join(t.TempDir(), "workspace.json")
	require.NoError(
		t,
		os.WriteFile(
			manifest,
			[]byte(`{"schemaVersion":1,"workspaceID":"`+testWorkspaceID+`","profile":{"kind":"development"}}`),
			0o600,
		),
	)
	return manifest
}

func TestResolveRoot(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	testutil.SetHome(t, home)
	t.Setenv(StateDirectoryEnvironment, "")
	t.Setenv(WorkspaceFileEnvironment, "")
	root, err := ResolveRoot()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".omniview"), root)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	selected := filepath.Join(parent, "state")
	t.Setenv(StateDirectoryEnvironment, selected)
	root, err = ResolveRoot()
	require.NoError(t, err)
	require.Equal(t, selected, root)
}

func TestWorkspaceRootFailsClosed(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	testutil.SetHome(t, home)
	manifest := workspaceFixture(t)
	t.Setenv(WorkspaceFileEnvironment, manifest)
	want := filepath.Join(home, ".omniview-dev", "profiles", testWorkspaceID, "state")
	for _, selected := range []string{"", "relative", filepath.Join(home, ".omniview"), filepath.Join(home, ".omniview-nightly"), t.TempDir()} {
		t.Setenv(StateDirectoryEnvironment, selected)
		_, resolveErr := ResolveRoot()
		require.Error(t, resolveErr, selected)
	}
	t.Setenv(StateDirectoryEnvironment, want)
	root, err := ResolveRoot()
	require.NoError(t, err)
	require.Equal(t, want, root)
	t.Setenv(WorkspaceFileEnvironment, manifest+"-missing")
	_, err = ResolveRoot()
	require.Error(t, err)
}

func TestInvalidWorkspaceManifestFailsClosed(t *testing.T) {
	testutil.SetHome(t, t.TempDir())
	t.Setenv(StateDirectoryEnvironment, t.TempDir())
	manifest := filepath.Join(t.TempDir(), "workspace.json")
	t.Setenv(WorkspaceFileEnvironment, manifest)
	for _, data := range []string{`{`, `{}`, `{"schemaVersion":2}`, `{"schemaVersion":1,"workspaceID":"../escape","profile":{"kind":"development"}}`, `{"schemaVersion":1,"workspaceID":"` + testWorkspaceID + `","profile":{"kind":"production"}}`} {
		require.NoError(t, os.WriteFile(manifest, []byte(data), 0o600))
		_, err := ResolveRoot()
		require.Error(t, err, data)
	}
}

func TestWorkspaceRejectsSymlinkToProduction(t *testing.T) {
	home := t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv(WorkspaceFileEnvironment, workspaceFixture(t))
	production := filepath.Join(home, ".omniview")
	require.NoError(t, os.MkdirAll(production, 0o700))
	profile := filepath.Join(home, ".omniview-dev", "profiles", testWorkspaceID)
	require.NoError(t, os.MkdirAll(profile, 0o700))
	selected := filepath.Join(profile, "state")
	require.NoError(t, os.Symlink(production, selected))
	t.Setenv(StateDirectoryEnvironment, selected)
	_, err := ResolveRoot()
	require.Error(t, err)
}

func TestValidateComponentRejectsTraversal(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../outside", "/absolute", `a\b`, "a:b", "a\x00b", "a\nb"} {
		require.Error(t, ValidateComponent(name), name)
	}
	for _, name := range []string{"kubernetes", "resource", "connections.json", "my_plugin-1"} {
		require.NoError(t, ValidateComponent(name), name)
	}
}

func TestWorkspaceRejectsAnotherProfileAlias(t *testing.T) {
	home := t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv(WorkspaceFileEnvironment, workspaceFixture(t))
	other := filepath.Join(home, ".omniview-dev", "profiles", "other", "state")
	require.NoError(t, os.MkdirAll(other, 0o700))
	profile := filepath.Join(home, ".omniview-dev", "profiles", testWorkspaceID)
	require.NoError(t, os.MkdirAll(profile, 0o700))
	require.NoError(t, os.Symlink(other, filepath.Join(profile, "state")))
	t.Setenv(StateDirectoryEnvironment, other)
	_, err := ResolveRoot()
	require.Error(t, err)
}

func TestWorkspaceAllowsSharedProfilesContainerAlias(t *testing.T) {
	home := t.TempDir()
	testutil.SetHome(t, home)
	t.Setenv(WorkspaceFileEnvironment, workspaceFixture(t))
	profiles, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".omniview-dev"), 0o700))
	require.NoError(t, os.Symlink(profiles, filepath.Join(home, ".omniview-dev", "profiles")))
	selected := filepath.Join(profiles, testWorkspaceID, "state")
	t.Setenv(StateDirectoryEnvironment, selected)
	root, err := ResolveRoot()
	require.NoError(t, err)
	require.Equal(t, selected, root)
}

func TestWorkspaceRejectsStateRemapWithinOwnProfile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
	}{
		{name: "profile-root", target: "."},
		{name: "profile-sibling", target: "other-state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			testutil.SetHome(t, home)
			t.Setenv(WorkspaceFileEnvironment, workspaceFixture(t))
			profile := filepath.Join(home, ".omniview-dev", "profiles", testWorkspaceID)
			target := filepath.Join(profile, tc.target)
			require.NoError(t, os.MkdirAll(target, 0o700))
			statePath := filepath.Join(profile, "state")
			require.NoError(t, os.Symlink(target, statePath))
			for _, selected := range []string{statePath, target} {
				t.Setenv(StateDirectoryEnvironment, selected)
				_, err := ResolveRoot()
				require.Error(t, err, "state must remain the literal UUID/state namespace, not %s", selected)
			}
		})
	}
}
