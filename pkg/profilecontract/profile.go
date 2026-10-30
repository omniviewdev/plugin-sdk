// Package profilecontract owns the v1 read-only machine-local profile protocol.
// It never creates, repairs, claims, or publishes metadata. The host remains the
// authority for full portable manifests and all ownership mutations.
package profilecontract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/google/uuid"
)

// Record is shared by bindings, durable claims, and external owner markers.
type Record struct {
	SchemaVersion int    `json:"schemaVersion"`
	WorkspaceID   string `json:"workspaceID"`
	StateRoot     string `json:"stateRoot"`
}

// Claims is the durable v1 registry of custom profile bindings. An empty claim
// list grants no ownership; readers must apply their own filesystem overlap policy.
type Claims struct {
	SchemaVersion int      `json:"schemaVersion"`
	Claims        []Record `json:"claims"`
}

// ManifestIdentity deliberately excludes repository/link/port policy validation.
type ManifestIdentity struct {
	SchemaVersion int    `json:"schemaVersion"`
	WorkspaceID   string `json:"workspaceID"`
	Slug          string `json:"slug"`
	Profile       struct {
		Kind string `json:"kind"`
	} `json:"profile"`
}

// WorkspaceIdentity adds source-directory boundaries to the narrow manifest
// identity projection; it does not validate the host's complete manifest schema.
type WorkspaceIdentity struct {
	ManifestIdentity

	Repositories []struct {
		Directory string `json:"directory"`
	} `json:"repositories"`
}

// OwnershipJournal projects the same serialized journal into either the SDK's
// narrow identity view or the host's full manifest, without duplicating schema.
type OwnershipJournal[M any] struct {
	SchemaVersion int    `json:"schemaVersion"`
	WorkspaceID   string `json:"workspaceID"`
	Root          string `json:"root"`
	Phase         string `json:"phase"`
	RenameTo      string `json:"renameTo"`
	Manifest      M      `json:"manifest"`
	Resources     []struct {
		CommonDir string `json:"commonDir"`
	} `json:"resources"`
}

// Error carries a private, fixed diagnostic and the host's existing error class.
// Underlying JSON errors must not expose machine-local metadata values.
type Error struct {
	Code    string
	Message string
}

// Error returns the fixed diagnostic without serializing machine-local metadata.
func (e *Error) Error() string { return e.Message }

func invalid(message string) error {
	return &Error{Code: "invalid_profile_binding", Message: message}
}

func conflict(message string) error {
	return &Error{Code: "conflicting_state_root", Message: message}
}

// ReadJSON distinguishes an absent file from dangling/redirected metadata. Only
// a genuinely absent binding permits default selection. It writes nothing.
func ReadJSON(path string, value any) error {
	canonical, err := CanonicalPath(path)
	if os.IsNotExist(err) {
		return conflict("profile ownership metadata contains a dangling symlink")
	}
	if err != nil {
		return err
	}
	if canonical != path {
		return conflict("profile ownership metadata cannot be redirected through symlinks")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return conflict("profile ownership metadata must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, value) != nil {
		return invalid("invalid machine-local profile metadata")
	}
	return nil
}

// ValidateRecord requires a canonical nonzero workspace UUID and an absolute,
// clean state directory. Existing state and container symlinks fail closed;
// validation never creates the missing directory suffix.
func ValidateRecord(record Record) error {
	id, err := uuid.Parse(record.WorkspaceID)
	if record.SchemaVersion != 1 || err != nil || id == uuid.Nil || id.String() != record.WorkspaceID ||
		!filepath.IsAbs(record.StateRoot) ||
		filepath.Clean(record.StateRoot) != record.StateRoot ||
		filepath.Base(record.StateRoot) != "state" {
		return invalid("invalid machine-local profile identity or state path")
	}
	canonical, err := CanonicalPath(record.StateRoot)
	if err != nil {
		return err
	}
	if canonical != record.StateRoot {
		return conflict("custom state container cannot be redirected through symlinks")
	}
	container := filepath.Dir(record.StateRoot)
	canonical, err = CanonicalPath(container)
	if err != nil {
		return err
	}
	if canonical != container {
		return conflict("custom profile container cannot be redirected through symlinks")
	}
	return nil
}

// ValidateClaims validates record identity and uniqueness. Filesystem overlap
// policy remains with each reader; the host additionally detects case aliases.
func ValidateClaims(registry Claims) error {
	if registry.SchemaVersion != 1 {
		return invalid("unsupported custom profile claims schema")
	}
	seen := make(map[string]bool, len(registry.Claims))
	for _, claim := range registry.Claims {
		if err := ValidateRecord(claim); err != nil {
			return err
		}
		if seen[claim.StateRoot] {
			return conflict("duplicate custom profile claim")
		}
		seen[claim.StateRoot] = true
	}
	return nil
}

var renameSlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateBoundary accepts only the selected root or the exact interrupted
// sibling rename. The returned old root must remain protected during recovery.
// The host must also validate the complete rename manifest before using it.
func (j *OwnershipJournal[M]) ValidateBoundary(root, workspaceID string, intent ManifestIdentity) (string, error) {
	if j.SchemaVersion != 1 || j.WorkspaceID != workspaceID {
		return "", conflict("workspace ownership boundary disagrees with selected workspace")
	}
	oldRoot := ""
	if j.Root != root {
		if j.Phase != "rename" || j.RenameTo != root || !filepath.IsAbs(j.Root) || filepath.Clean(j.Root) != j.Root ||
			filepath.Dir(j.Root) != filepath.Dir(root) ||
			intent.SchemaVersion != 1 ||
			intent.WorkspaceID != workspaceID ||
			intent.Profile.Kind != "development" ||
			len(intent.Slug) > 48 ||
			!renameSlugPattern.MatchString(intent.Slug) ||
			intent.Slug != filepath.Base(root) {
			return "", conflict("workspace ownership boundary disagrees with selected workspace")
		}
		oldRoot = j.Root
	}
	for _, resource := range j.Resources {
		if !filepath.IsAbs(resource.CommonDir) {
			return "", &Error{Code: "invalid_journal", Message: "invalid registered source common directory"}
		}
	}
	return oldRoot, nil
}

// CanonicalPath resolves symlinks in an existing prefix without creating the
// missing suffix. Dangling symlinks and non-absence filesystem errors fail closed.
func CanonicalPath(path string) (string, error) {
	probe := filepath.Clean(path)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for _, component := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, component)
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(probe) == probe {
			return "", err
		}
		if _, statErr := os.Lstat(probe); statErr == nil {
			return "", err
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		missing = append(missing, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
}
