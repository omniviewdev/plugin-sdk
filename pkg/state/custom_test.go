package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/internal/testutil"
)

type customFixture struct {
	home      string
	manifest  string
	workspace string
	state     string
	binding   string
	owner     string
	claims    string
	record    map[string]any
}

func writeCustomJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func customRootFixture(t *testing.T) customFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	testutil.SetHome(t, home)
	manifest, err := filepath.EvalSymlinks(workspaceFixture(t))
	require.NoError(t, err)
	container, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	state := filepath.Join(container, "state")
	fixture := customFixture{
		home: home, manifest: manifest, workspace: filepath.Dir(manifest), state: state,
		binding: filepath.Join(filepath.Dir(manifest), ".local", "profile.json"),
		owner:   filepath.Join(container, ".omniview-profile.json"),
		claims:  filepath.Join(home, ".omniview-dev", "custom-profiles.json"),
		record:  map[string]any{"schemaVersion": 1, "workspaceID": testWorkspaceID, "stateRoot": state},
	}
	fixture.writeRecords(t)
	t.Setenv(WorkspaceFileEnvironment, manifest)
	t.Setenv(StateDirectoryEnvironment, state)
	return fixture
}

func (f customFixture) writeRecords(t *testing.T) {
	t.Helper()
	writeCustomJSON(t, f.binding, f.record)
	writeCustomJSON(t, f.owner, f.record)
	writeCustomJSON(t, f.claims, map[string]any{"schemaVersion": 1, "claims": []any{f.record}})
}

func TestResolveCustomRootRequiresMatchingDurableOwnershipWithoutWriting(t *testing.T) {
	fixture := customRootFixture(t)
	root, err := ResolveRoot()
	require.NoError(t, err)
	require.Equal(t, fixture.state, root)
	_, err = os.Lstat(fixture.state)
	require.True(t, os.IsNotExist(err), "read-only resolution must not initialize state")
	t.Setenv(
		StateDirectoryEnvironment,
		filepath.Join(fixture.home, ".omniview-dev", "profiles", testWorkspaceID, "state"),
	)
	_, err = ResolveRoot()
	require.Error(t, err, "default environment cannot override an explicit custom binding")
}

func TestCustomRootRejectsMissingForeignAndMalformedMetadata(t *testing.T) {
	for _, kind := range []string{"missing-binding", "missing-owner", "missing-claims", "foreign-binding", "foreign-owner", "foreign-claim", "malformed-binding", "malformed-owner", "malformed-claims", "unsupported-claims", "empty-claims"} {
		t.Run(kind, func(t *testing.T) {
			fixture := customRootFixture(t)
			switch kind {
			case "missing-binding":
				require.NoError(t, os.Remove(fixture.binding))
			case "missing-owner":
				require.NoError(t, os.Remove(fixture.owner))
			case "missing-claims":
				require.NoError(t, os.Remove(fixture.claims))
			case "foreign-binding", "foreign-owner", "foreign-claim":
				foreign := map[string]any{
					"schemaVersion": 1,
					"workspaceID":   "09e2b911-76a7-4418-8d5f-30f31b50fe32",
					"stateRoot":     fixture.state,
				}
				if kind == "foreign-binding" {
					writeCustomJSON(t, fixture.binding, foreign)
				}
				if kind == "foreign-owner" {
					writeCustomJSON(t, fixture.owner, foreign)
				}
				if kind == "foreign-claim" {
					writeCustomJSON(t, fixture.claims, map[string]any{"schemaVersion": 1, "claims": []any{foreign}})
				}
			case "malformed-binding":
				require.NoError(t, os.WriteFile(fixture.binding, []byte("{"), 0o600))
			case "malformed-owner":
				require.NoError(t, os.WriteFile(fixture.owner, []byte("{"), 0o600))
			case "malformed-claims":
				require.NoError(t, os.WriteFile(fixture.claims, []byte("{"), 0o600))
			case "unsupported-claims":
				writeCustomJSON(t, fixture.claims, map[string]any{"schemaVersion": 2, "claims": []any{fixture.record}})
			case "empty-claims":
				writeCustomJSON(t, fixture.claims, map[string]any{"schemaVersion": 1, "claims": []any{}})
			}
			_, err := ResolveRoot()
			require.Error(t, err)
		})
	}
}

func TestCustomRootRejectsRedirectedMetadataAndState(t *testing.T) {
	for _, kind := range []string{"binding", "owner", "claims", "dangling-binding", "state-alias", "container-alias"} {
		t.Run(kind, func(t *testing.T) {
			fixture := customRootFixture(t)
			if kind == "state-alias" {
				require.NoError(t, os.Symlink(filepath.Dir(fixture.state), fixture.state))
			} else if kind == "container-alias" {
				alias := filepath.Join(filepath.Dir(fixture.state), "alias")
				require.NoError(t, os.Symlink(filepath.Dir(fixture.state), alias))
				fixture.record["stateRoot"] = filepath.Join(alias, "state")
				fixture.writeRecords(t)
				t.Setenv(StateDirectoryEnvironment, filepath.Join(alias, "state"))
			} else {
				path := fixture.binding
				if kind == "owner" {
					path = fixture.owner
				}
				if kind == "claims" {
					path = fixture.claims
				}
				copyPath := filepath.Join(t.TempDir(), "metadata.json")
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				if kind != "dangling-binding" {
					require.NoError(t, os.WriteFile(copyPath, data, 0o600))
				}
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(copyPath, path))
			}
			_, err := ResolveRoot()
			require.Error(t, err)
		})
	}
}

func TestCustomRootRejectsProtectedAndOverlappingClaims(t *testing.T) {
	for _, kind := range []string{"production", "nightly", "default-profiles", "workspace", "duplicate", "nested", "foreign-malformed"} {
		t.Run(kind, func(t *testing.T) {
			fixture := customRootFixture(t)
			switch kind {
			case "production":
				fixture.record["stateRoot"] = filepath.Join(fixture.home, ".omniview", "state")
			case "nightly":
				fixture.record["stateRoot"] = filepath.Join(fixture.home, ".omniview-nightly", "state")
			case "default-profiles":
				fixture.record["stateRoot"] = filepath.Join(
					fixture.home,
					".omniview-dev",
					"profiles",
					"custom",
					"state",
				)
			case "workspace":
				fixture.record["stateRoot"] = filepath.Join(fixture.workspace, "custom", "state")
			}
			fixture.writeRecords(t)
			if kind == "duplicate" {
				writeCustomJSON(
					t,
					fixture.claims,
					map[string]any{"schemaVersion": 1, "claims": []any{fixture.record, fixture.record}},
				)
			} else if kind == "nested" {
				nested := map[string]any{
					"schemaVersion": 1,
					"workspaceID":   "09e2b911-76a7-4418-8d5f-30f31b50fe32",
					"stateRoot":     filepath.Join(filepath.Dir(fixture.state), "nested", "state"),
				}
				writeCustomJSON(
					t,
					fixture.claims,
					map[string]any{"schemaVersion": 1, "claims": []any{fixture.record, nested}},
				)
			} else if kind == "foreign-malformed" {
				writeCustomJSON(
					t,
					fixture.claims,
					map[string]any{
						"schemaVersion": 1,
						"claims": []any{
							fixture.record,
							map[string]any{"schemaVersion": 1, "workspaceID": "invalid", "stateRoot": "/invalid/state"},
						},
					},
				)
			}
			t.Setenv(StateDirectoryEnvironment, fixture.record["stateRoot"].(string))
			_, err := ResolveRoot()
			require.Error(t, err)
		})
	}
}

func TestCustomRootRejectsRegisteredSourceOverlap(t *testing.T) {
	fixture := customRootFixture(t)
	writeCustomJSON(t, filepath.Join(fixture.workspace, ".local", "ownership.json"), map[string]any{
		"schemaVersion": 1, "workspaceID": testWorkspaceID, "root": fixture.workspace,
		"resources": []any{map[string]any{"commonDir": filepath.Dir(fixture.state)}},
	})
	_, err := ResolveRoot()
	require.Error(t, err)
}

func TestCustomRootUsesCanonicalManifestAndHomeAliases(t *testing.T) {
	fixture := customRootFixture(t)
	homeAlias := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.Symlink(fixture.home, homeAlias))
	testutil.SetHome(t, homeAlias)
	manifestAlias := filepath.Join(t.TempDir(), "workspace.json")
	require.NoError(t, os.Symlink(fixture.manifest, manifestAlias))
	t.Setenv(WorkspaceFileEnvironment, manifestAlias)
	root, err := ResolveRoot()
	require.NoError(t, err)
	require.Equal(t, fixture.state, root)
}

func TestCustomRootRejectsRegisteredCollectionSourceOverlap(t *testing.T) {
	for _, kind := range []string{"common-directory", "source-worktree", "unsupported-registry"} {
		t.Run(kind, func(t *testing.T) {
			fixture := customRootFixture(t)
			collection, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			fixture.workspace = filepath.Join(collection, "workspaces", "fixture")
			manifestData, err := os.ReadFile(fixture.manifest)
			require.NoError(t, err)
			fixture.manifest = filepath.Join(fixture.workspace, "workspace.json")
			require.NoError(t, os.MkdirAll(fixture.workspace, 0o700))
			require.NoError(t, os.WriteFile(fixture.manifest, manifestData, 0o600))
			fixture.binding = filepath.Join(fixture.workspace, ".local", "profile.json")
			fixture.writeRecords(t)
			t.Setenv(WorkspaceFileEnvironment, fixture.manifest)
			source := map[string]any{"id": "source", "commonDir": filepath.Dir(fixture.state)}
			if kind == "source-worktree" {
				source["commonDir"] = filepath.Join(collection, "source.git")
				source["worktree"] = filepath.Dir(fixture.state)
			}
			version := 1
			if kind == "unsupported-registry" {
				version = 2
			}
			writeCustomJSON(
				t,
				filepath.Join(collection, ".omniview-dev", "repositories.json"),
				map[string]any{"schemaVersion": version, "repositories": []any{source}},
			)
			_, err = ResolveRoot()
			require.Error(t, err)
		})
	}
}

func interruptedRenameFixture(t *testing.T) (customFixture, map[string]any) {
	t.Helper()
	fixture := customRootFixture(t)
	oldRoot := fixture.workspace
	newRoot := filepath.Join(filepath.Dir(oldRoot), "renamed")
	manifest := map[string]any{
		"schemaVersion": 1,
		"workspaceID":   testWorkspaceID,
		"slug":          "renamed",
		"displayName":   "Renamed workspace",
		"repositories": []any{
			map[string]any{
				"id":         "omniview",
				"directory":  "omniview",
				"baseRef":    "main",
				"baseCommit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"branch":     "feature",
			},
		},
		"profile": map[string]any{"kind": "development"},
		"ports":   map[string]any{"policy": "sticky"},
	}
	journal := map[string]any{
		"schemaVersion": 1, "workspaceID": testWorkspaceID, "root": oldRoot,
		"phase": "rename", "renameTo": newRoot, "manifest": manifest,
	}
	writeCustomJSON(t, filepath.Join(oldRoot, ".local", "ownership.json"), journal)
	require.NoError(t, os.Rename(oldRoot, newRoot))
	fixture.workspace = newRoot
	fixture.manifest = filepath.Join(newRoot, "workspace.json")
	fixture.binding = filepath.Join(newRoot, ".local", "profile.json")
	t.Setenv(WorkspaceFileEnvironment, fixture.manifest)
	return fixture, journal
}

func TestCustomRootAcceptsExactInterruptedSiblingRenameIntent(t *testing.T) {
	fixture, journal := interruptedRenameFixture(t)
	root, err := ResolveRoot()
	require.NoError(t, err)
	require.Equal(t, fixture.state, root)
	var unchanged map[string]any
	data, err := os.ReadFile(filepath.Join(fixture.workspace, ".local", "ownership.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &unchanged))
	require.Equal(t, journal["root"], unchanged["root"], "resolution must not rewrite durable rename intent")
}

func TestCustomRootRejectsUnprovenInterruptedRename(t *testing.T) {
	for _, kind := range []string{"outside-collection", "wrong-phase", "wrong-target", "wrong-UUID", "wrong-manifest-UUID", "wrong-target-slug", "unsupported-manifest-schema", "nondevelopment-manifest", "relative-old-root", "unclean-old-root", "old-source-overlap"} {
		t.Run(kind, func(t *testing.T) {
			fixture, journal := interruptedRenameFixture(t)
			manifest := journal["manifest"].(map[string]any)
			switch kind {
			case "outside-collection":
				journal["root"] = filepath.Join(t.TempDir(), "old")
			case "wrong-phase":
				journal["phase"] = "ready"
			case "wrong-target":
				journal["renameTo"] = journal["root"]
			case "wrong-UUID":
				journal["workspaceID"] = "09e2b911-76a7-4418-8d5f-30f31b50fe32"
			case "wrong-manifest-UUID":
				manifest["workspaceID"] = "09e2b911-76a7-4418-8d5f-30f31b50fe32"
			case "wrong-target-slug":
				manifest["slug"] = "another-name"
			case "unsupported-manifest-schema":
				manifest["schemaVersion"] = 2
			case "nondevelopment-manifest":
				manifest["profile"] = map[string]any{"kind": "production"}
			case "relative-old-root":
				journal["root"] = "old"
			case "unclean-old-root":
				journal["root"] = journal["root"].(string) + string(filepath.Separator) + "."
			case "old-source-overlap":
				fixture.state = filepath.Join(journal["root"].(string), "state")
				fixture.owner = filepath.Join(filepath.Dir(fixture.state), ".omniview-profile.json")
				fixture.record["stateRoot"] = fixture.state
				fixture.writeRecords(t)
				t.Setenv(StateDirectoryEnvironment, fixture.state)
			}
			writeCustomJSON(t, filepath.Join(fixture.workspace, ".local", "ownership.json"), journal)
			_, err := ResolveRoot()
			require.Error(t, err)
		})
	}
}
