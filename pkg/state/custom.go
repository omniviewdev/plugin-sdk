package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var renameSlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// These identity-only views mirror the host's frozen v1 machine-local records.
// The SDK consumes them read-only; the host owns creation, claims and rebinding.
type workspaceIdentity struct {
	SchemaVersion int    `json:"schemaVersion"`
	WorkspaceID   string `json:"workspaceID"`
	Profile       struct {
		Kind string `json:"kind"`
	} `json:"profile"`
	Repositories []struct {
		Directory string `json:"directory"`
	} `json:"repositories"`
}

type customProfileIdentity struct {
	SchemaVersion int    `json:"schemaVersion"`
	WorkspaceID   string `json:"workspaceID"`
	StateRoot     string `json:"stateRoot"`
}

type customProfileClaims struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Claims        []customProfileIdentity `json:"claims"`
}

// Missing metadata is distinct from dangling/redirected metadata: only an absent
// binding permits default selection. Never wrap a dangling link as IsNotExist.
func readProfileIdentity(path string, value any) error {
	canonical, err := canonicalPath(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("profile metadata contains a dangling symlink")
	}
	if err != nil {
		return err
	}
	if canonical != path {
		return fmt.Errorf("profile metadata cannot be redirected through symlinks")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("profile metadata must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("invalid machine-local profile metadata: %w", err)
	}
	return nil
}

func validateCustomIdentity(record customProfileIdentity) error {
	id, err := uuid.Parse(record.WorkspaceID)
	if record.SchemaVersion != 1 || err != nil || id == uuid.Nil || id.String() != record.WorkspaceID || !filepath.IsAbs(record.StateRoot) || filepath.Clean(record.StateRoot) != record.StateRoot || filepath.Base(record.StateRoot) != "state" {
		return fmt.Errorf("invalid custom profile identity or state path")
	}
	canonical, err := canonicalPath(record.StateRoot)
	if err != nil {
		return err
	}
	if canonical != record.StateRoot {
		return fmt.Errorf("custom state cannot be redirected through symlinks")
	}
	container := filepath.Dir(record.StateRoot)
	canonical, err = canonicalPath(container)
	if err != nil {
		return err
	}
	if canonical != container {
		return fmt.Errorf("custom profile container cannot be redirected through symlinks")
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	return contained(a, b) || contained(b, a)
}

func validateCustomClaims(claims customProfileClaims, binding customProfileIdentity) error {
	if claims.SchemaVersion != 1 {
		return fmt.Errorf("unsupported custom profile claims schema")
	}
	seen := make(map[string]bool, len(claims.Claims))
	found := false
	for i, claim := range claims.Claims {
		if err := validateCustomIdentity(claim); err != nil {
			return err
		}
		if seen[claim.StateRoot] {
			return fmt.Errorf("duplicate custom profile claim")
		}
		seen[claim.StateRoot] = true
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

func protectedCustomPaths(workspace, home string, manifest workspaceIdentity) ([]string, error) {
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
		err := readProfileIdentity(filepath.Join(collection, ".omniview-dev", "repositories.json"), &registry)
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
	var journal struct {
		SchemaVersion int    `json:"schemaVersion"`
		WorkspaceID   string `json:"workspaceID"`
		Root          string `json:"root"`
		Phase         string `json:"phase"`
		RenameTo      string `json:"renameTo"`
		Manifest      struct {
			SchemaVersion int    `json:"schemaVersion"`
			WorkspaceID   string `json:"workspaceID"`
			Slug          string `json:"slug"`
			Profile       struct {
				Kind string `json:"kind"`
			} `json:"profile"`
		} `json:"manifest"`
		Resources []struct {
			CommonDir string `json:"commonDir"`
		} `json:"resources"`
	}
	err := readProfileIdentity(filepath.Join(workspace, ".local", "ownership.json"), &journal)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if journal.SchemaVersion != 1 || journal.WorkspaceID != manifest.WorkspaceID {
			return nil, fmt.Errorf("workspace ownership boundary disagrees with selected workspace")
		}
		if journal.Root != workspace {
			// The host persists intent, moves the directory, then rewrites its
			// root. Accept only that exact recoverable sibling move. Unrelated
			// repository/link/port validation stays at the host's full ingress.
			intent := journal.Manifest
			validRename := journal.Phase == "rename" && journal.RenameTo == workspace &&
				filepath.IsAbs(journal.Root) && filepath.Clean(journal.Root) == journal.Root &&
				filepath.Dir(journal.Root) == filepath.Dir(workspace) &&
				intent.SchemaVersion == 1 && intent.WorkspaceID == manifest.WorkspaceID &&
				intent.Profile.Kind == "development" && len(intent.Slug) <= 48 &&
				renameSlugPattern.MatchString(intent.Slug) && intent.Slug == filepath.Base(workspace)
			if !validRename {
				return nil, fmt.Errorf("workspace ownership boundary disagrees with selected workspace")
			}
			paths = append(paths, journal.Root)
		}
		for _, resource := range journal.Resources {
			if !filepath.IsAbs(resource.CommonDir) {
				return nil, fmt.Errorf("invalid registered source common directory")
			}
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

func resolveCustomRoot(manifestPath, home string, manifest workspaceIdentity) (string, bool, error) {
	workspace := filepath.Dir(manifestPath)
	var binding customProfileIdentity
	err := readProfileIdentity(filepath.Join(workspace, ".local", "profile.json"), &binding)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if err := validateCustomIdentity(binding); err != nil {
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
	var claims customProfileClaims
	if err := readProfileIdentity(filepath.Join(base, "custom-profiles.json"), &claims); err != nil {
		return "", false, err
	}
	if err := validateCustomClaims(claims, binding); err != nil {
		return "", false, err
	}
	var owner customProfileIdentity
	if err := readProfileIdentity(filepath.Join(filepath.Dir(binding.StateRoot), ".omniview-profile.json"), &owner); err != nil {
		return "", false, err
	}
	if owner != binding {
		return "", false, fmt.Errorf("custom profile owner marker disagrees with binding")
	}
	return binding.StateRoot, true, nil
}
