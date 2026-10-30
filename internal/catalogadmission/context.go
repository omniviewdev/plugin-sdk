// Package catalogadmission owns SDK-only catalog observation storage and framing.
// It does not grant access or issue catalog generations.
package catalogadmission

import (
	"context"
	"unicode/utf8"
)

// Limits are supplied by the domain boundary, not inferred from transport input.
type Limits struct {
	RevisionBytes int
	MetadataBytes int
	Observations  int
}

// Violation contains only static, public-safe boundary diagnostics.
type Violation struct {
	Code    string
	Message string
}

func (v *Violation) Error() string { return v.Message }

// Observation is an exact provider/connection catalog observation, not a grant.
type Observation struct {
	Provider   string
	Connection string
	Revision   string
}
type routesKey struct{}

// Append owns its slice and preserves duplicates for fail-closed validation.
func Append(ctx context.Context, entry Observation) context.Context {
	previous := Entries(ctx)
	entries := make([]Observation, len(previous)+1)
	copy(entries, previous)
	entries[len(previous)] = entry
	return context.WithValue(ctx, routesKey{}, entries)
}

// Entries borrows immutable SDK-owned storage. Callers must not modify it.
func Entries(ctx context.Context) []Observation {
	entries, _ := ctx.Value(routesKey{}).([]Observation)
	return entries
}

// Clear masks inherited observations when crossing an RPC boundary.
func Clear(ctx context.Context) context.Context {
	return context.WithValue(ctx, routesKey{}, []Observation(nil))
}

// Qualified returns an empty, present revision for ambiguous duplicate entries.
func Qualified(ctx context.Context, provider, connection string) (string, bool) {
	var revision string
	found := false
	for _, entry := range Entries(ctx) {
		if entry.Provider == provider && entry.Connection == connection {
			if found {
				return "", true
			}
			revision, found = entry.Revision, true
		}
	}
	return revision, found
}

// ValidateRevision checks observation syntax without discovering a catalog.
func ValidateRevision(revision string, maxBytes int) *Violation {
	if revision == "" || len(revision) > maxBytes || !utf8.ValidString(revision) {
		return &Violation{"RESET_REQUIRED", "An explicit valid accepted catalog revision is required."}
	}
	return nil
}

// EncodedSize validates every retained entry and the aggregate metadata budget.
// An absent qualified set has no frame; the default pin is validated separately.
func EncodedSize(ctx context.Context, defaultRevision string, limits Limits) (int, *Violation) {
	entries := Entries(ctx)
	if len(entries) == 0 {
		return 0, nil
	}
	if len(entries) > limits.Observations {
		return 0, &Violation{"RESOURCE_LIMIT", "Too many route catalog observations."}
	}
	size := 3
	for i, entry := range entries {
		for _, previous := range entries[:i] {
			if previous.Provider == entry.Provider && previous.Connection == entry.Connection {
				return 0, &Violation{"RESET_REQUIRED", "Duplicate qualified catalog revision."}
			}
		}
		if entry.Provider == "" || entry.Connection == "" || len(entry.Provider) > limits.RevisionBytes ||
			len(entry.Connection) > limits.RevisionBytes ||
			!utf8.ValidString(entry.Provider) ||
			!utf8.ValidString(entry.Connection) {
			return 0, &Violation{"RESET_REQUIRED", "Invalid qualified catalog route."}
		}
		if err := ValidateRevision(entry.Revision, limits.RevisionBytes); err != nil {
			return 0, err
		}
		size += 6 + len(entry.Provider) + len(entry.Connection) + len(entry.Revision)
	}
	if size+len(defaultRevision)+256 > limits.MetadataBytes {
		return 0, &Violation{"RESOURCE_LIMIT", "Catalog revision metadata exceeds bound."}
	}
	return size, nil
}
