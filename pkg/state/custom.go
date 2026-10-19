package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/omniviewdev/plugin-sdk/pkg/profilecontract"
)

func pathsOverlap(a, b string) bool {
	return contained(a, b) || contained(b, a)
}

func validateCustomClaims(claims profilecontract.Claims, binding profilecontract.Record) error {
	if err := profilecontract.ValidateClaims(claims); err != nil {
		return err
	}
	found := false
	for i, claim := range claims.Claims {
		for _, previous := range claims.Claims[:i] {
			if pathsOverlap(filepath.Dir(claim.StateRoot), filepath.Dir(previous.StateRoot)) {
				return fmt.Errorf("custom profile claim containers overlap")
			}
		}
		if claim == binding {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("custom profile binding has no matching durable claim")
	}
	return nil
}

func protectedCustomPaths(workspace, home string, manifest profilecontract.WorkspaceIdentity) ([]string, error) {
	paths := []string{workspace, filepath.Join(home, ".omniview"), filepath.Join(home, ".omniview-nightly"), filepath.Join(home, ".omniview-dev", "profiles")}
	for _, repo := range manifest.Repositories {
		if repo.Directory == "" || repo.Directory == "." || strings.ContainsAny(repo.Directory, "\\:\x00") || filepath.IsAbs(repo.Directory) || filepath.Clean(repo.Directory) != repo.Directory || repo.Directory == ".." || strings.HasPrefix(repo.Directory, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("invalid workspace repository path")
		}
		paths = append(paths, filepath.Join(workspace, repo.Directory))
	}
	// Only an actual registered workspaces layout makes the collection and its
	// external source directories protected; an ordinary temp grandparent is not.
	if filepath.Base(filepath.Dir(workspace)) == "workspaces" {
		collection := filepath.Dir(filepath.Dir(workspace))
		var registry struct {
			SchemaVersion int `json:"schemaVersion"`
			Repositories  []struct {
				ID        string `json:"id"`
				CommonDir string `json:"commonDir"`
				Worktree  string `json:"worktree"`
			} `json:"repositories"`
		}
		err := profilecontract.ReadJSON(filepath.Join(collection, ".omniview-dev", "repositories.json"), &registry)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			if registry.SchemaVersion != 1 || len(registry.Repositories) == 0 {
				return nil, fmt.Errorf("invalid registered source boundary metadata")
			}
			paths = append(paths, collection)
			for _, source := range registry.Repositories {
				if source.ID == "" || source.ID == "." || source.ID == ".." || strings.ContainsAny(source.ID, "/\\\x00\r\n") || !filepath.IsAbs(source.CommonDir) || source.Worktree != "" && !filepath.IsAbs(source.Worktree) {
					return nil, fmt.Errorf("invalid registered source paths")
				}
				paths = append(paths, source.CommonDir)
				if source.Worktree != "" {
					paths = append(paths, source.Worktree)
				}
			}
		}
	}
	var journal profilecontract.OwnershipJournal[profilecontract.ManifestIdentity]
	err := profilecontract.ReadJSON(filepath.Join(workspace, ".local", "ownership.json"), &journal)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		oldRoot, err := journal.ValidateBoundary(workspace, manifest.WorkspaceID, journal.Manifest)
		if err != nil {
			return nil, err
		}
		if oldRoot != "" {
			paths = append(paths, oldRoot)
		}
		for _, resource := range journal.Resources {
			paths = append(paths, resource.CommonDir)
		}
	}
	for i, path := range paths {
		canonical, err := canonicalPath(path)
		if err != nil {
			return nil, err
		}
		paths[i] = canonical
	}
	return paths, nil
}

func resolveCustomRoot(manifestPath, home string, manifest profilecontract.WorkspaceIdentity) (string, bool, error) {
	workspace := filepath.Dir(manifestPath)
	var binding profilecontract.Record
	err := profilecontract.ReadJSON(filepath.Join(workspace, ".local", "profile.json"), &binding)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if err := profilecontract.ValidateRecord(binding); err != nil {
		return "", false, err
	}
	if binding.WorkspaceID != manifest.WorkspaceID {
		return "", false, fmt.Errorf("custom profile belongs to another workspace")
	}
	protected, err := protectedCustomPaths(workspace, home, manifest)
	if err != nil {
		return "", false, err
	}
	for _, path := range protected {
		if pathsOverlap(filepath.Dir(binding.StateRoot), path) {
			return "", false, fmt.Errorf("custom profile overlaps protected application or source storage")
		}
	}
	base, err := canonicalPath(filepath.Join(home, ".omniview-dev"))
	if err != nil {
		return "", false, err
	}
	var claims profilecontract.Claims
	if err := profilecontract.ReadJSON(filepath.Join(base, "custom-profiles.json"), &claims); err != nil {
		return "", false, err
	}
	if err := validateCustomClaims(claims, binding); err != nil {
		return "", false, err
	}
	var owner profilecontract.Record
	if err := profilecontract.ReadJSON(filepath.Join(filepath.Dir(binding.StateRoot), ".omniview-profile.json"), &owner); err != nil {
		return "", false, err
	}
	if owner != binding {
		return "", false, fmt.Errorf("custom profile owner marker disagrees with binding")
	}
	return binding.StateRoot, true, nil
}
