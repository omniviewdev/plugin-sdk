package resourcev2

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// durableAddressKeyVersion tags the canonical key encoding. A future encoding
// must use a new tag so its keys can never collide with these.
const durableAddressKeyVersion = "omniview.durable-address.v1"

// DurableAddress identifies one object for history and correlation. It is a
// ResourceAddress whose host-issued Authority is replaced by the
// EnvironmentIdentity its provider declared for the type. It therefore keeps its
// value across connection stops and starts, profile edits, and host or plugin
// restarts, and differs between distinct targets.
//
// It is not an authorization identity. Policy, consent, call binding and
// reference currency keep using ResourceAddress and its host-issued authority,
// which the host retires on those same events. Equal DurableAddresses are
// correlation evidence only and never permit sharing a body, cache or grant.
type DurableAddress struct {
	Environment EnvironmentIdentity `json:"environment"`
	Provider    string              `json:"provider"`
	Type        string              `json:"type"`
	// Scope holds each declared identity dimension exactly once, ordered by
	// dimension. It is never nil, so equal addresses are equal under DeepEqual.
	Scope []ScopeEntry `json:"scope"`
	ID    string       `json:"id"`
}

// DurableReference adds the object's native incarnation observation to its
// DurableAddress, as ResourceReference does for ResourceAddress. The same
// address with a different incarnation is a delete and recreate; a nil
// incarnation makes no claim about recreation.
type DurableReference struct {
	Address     DurableAddress `json:"address"`
	Incarnation *string        `json:"incarnation,omitempty"`
}

// NewDurableReference derives the durable identity of reference inside
// environment. The reference's authority is dropped, and so is the access route
// it was observed through: neither belongs to the identity. The result owns its
// storage and does not alias the caller's scope or incarnation.
func NewDurableReference(environment EnvironmentIdentity, reference ResourceReference) (DurableReference, error) {
	if err := validateReference(reference); err != nil {
		return DurableReference{}, err
	}
	address := DurableAddress{
		Environment: environment,
		Provider:    reference.Address.Provider,
		Type:        reference.Address.Type,
		Scope:       sortedScope(reference.Address.Scope),
		ID:          reference.Address.ID,
	}
	if err := validateDurableAddress(address); err != nil {
		return DurableReference{}, err
	}
	durable := DurableReference{Address: address}
	if reference.Incarnation != nil {
		incarnation := *reference.Incarnation
		durable.Incarnation = &incarnation
	}
	return durable, nil
}

// Key encodes the address as one canonical string for use as a map or store key.
// Strings stay opaque: nothing is split, Unicode-normalized, case-folded or
// path-cleaned, so equal keys are equal identities. The incarnation is not part
// of the key; it distinguishes versions of the identity the key names.
func (a DurableAddress) Key() (string, error) {
	if err := validateDurableAddress(a); err != nil {
		return "", err
	}
	scope := sortedScope(a.Scope)
	pairs := make([][2]string, len(scope))
	for i, entry := range scope {
		pairs[i] = [2]string{entry.Dimension, entry.Value}
	}
	tuple := [7]any{durableAddressKeyVersion, a.Environment.Scheme, a.Environment.ID, a.Provider, a.Type, pairs, a.ID}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tuple); err != nil {
		return "", err
	}
	return string(bytes.TrimSuffix(data.Bytes(), []byte{'\n'})), nil
}

// sortedScope returns an independent, non-nil copy ordered by dimension.
func sortedScope(scope []ScopeEntry) []ScopeEntry {
	sorted := make([]ScopeEntry, len(scope))
	copy(sorted, scope)
	slices.SortFunc(sorted, func(a, b ScopeEntry) int { return strings.Compare(a.Dimension, b.Dimension) })
	return sorted
}

func validateDurableAddress(a DurableAddress) error {
	if err := admission(a); err != nil {
		return err
	}
	if err := validateEnvironmentIdentity(a.Environment); err != nil {
		return err
	}
	for _, s := range []string{a.Provider, a.Type, a.ID} {
		if err := text(s, true); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(a.Scope))
	for _, entry := range a.Scope {
		if err := validateEntry(entry); err != nil {
			return err
		}
		if seen[entry.Dimension] {
			return invalid("duplicate identity dimension")
		}
		seen[entry.Dimension] = true
	}
	return nil
}

func validateDurableReference(r DurableReference) error {
	if err := validateDurableAddress(r.Address); err != nil {
		return err
	}
	return token(r.Incarnation)
}

// validateEnvironmentIdentity admits bounded, printable text. The value repeats
// on every type of a route and keys durable stores, so it is held to more than
// valid UTF-8: no control characters, and a byte ceiling.
func validateEnvironmentIdentity(v EnvironmentIdentity) error {
	if err := environmentText(v.Scheme, MaxEnvironmentSchemeBytes); err != nil {
		return err
	}
	return environmentText(v.ID, MaxEnvironmentIDBytes)
}

func environmentText(s string, limit int) error {
	if err := text(s, true); err != nil {
		return err
	}
	if len(s) > limit {
		return limited("environment identity exceeds its byte limit")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return invalid("environment identity contains a control character")
		}
	}
	return nil
}
