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

// DurableAddress locates one object inside an environment: a ResourceAddress
// without its host-issued Authority. The environment is not part of it. A store
// resolves the environment from the fingerprints of the DurableReference that
// carries the address, because different connections to one target declare
// different fingerprints of it.
type DurableAddress struct {
	Provider string `json:"provider"`
	Type     string `json:"type"`
	// Scope holds each declared identity dimension exactly once, ordered by
	// dimension. It is never nil, so equal addresses are equal under DeepEqual.
	Scope []ScopeEntry `json:"scope"`
	ID    string       `json:"id"`
}

// DurableReference is one observation of an object for history and correlation:
// the environment fingerprints it was observed under, its DurableAddress and its
// native incarnation. The same address with a different incarnation is a delete
// and recreate; a nil incarnation makes no claim about recreation.
//
// It is not an authorization identity. Policy, consent, call binding and
// reference currency keep using ResourceAddress and its host-issued authority,
// which the host retires on connection stops, endpoint changes and restarts.
// Equal durable references are correlation evidence only: they never permit
// sharing a body, cache or grant, and they never grant access to history. A
// reader may see history for an (environment, type, scope) population only if
// the reader's own current route is permitted to read that type and scope now.
type DurableReference struct {
	// Environment holds the fingerprints ordered by scheme and ID; at least one is strong.
	Environment []EnvironmentIdentity `json:"environment"`
	Address     DurableAddress        `json:"address"`
	Incarnation *string               `json:"incarnation,omitempty"`
}

// NewDurableReference derives the durable reference of reference observed under
// environment. The reference's authority is dropped, and so is the access route
// it was observed through: neither belongs to the identity. The environment must
// hold a strong fingerprint, because weak ones never identify. The result owns
// its storage and does not alias the caller's scope, incarnation or environment.
func NewDurableReference(environment []EnvironmentIdentity, reference ResourceReference) (DurableReference, error) {
	if err := validateReference(reference); err != nil {
		return DurableReference{}, err
	}
	durable := DurableReference{
		Environment: sortedEnvironment(environment),
		Address: DurableAddress{
			Provider: reference.Address.Provider,
			Type:     reference.Address.Type,
			Scope:    sortedScope(reference.Address.Scope),
			ID:       reference.Address.ID,
		},
	}
	if reference.Incarnation != nil {
		incarnation := *reference.Incarnation
		durable.Incarnation = &incarnation
	}
	if err := validateDurableReference(durable); err != nil {
		return DurableReference{}, err
	}
	return durable, nil
}

// Key encodes the address as one canonical string for use as a map or store key
// inside its environment. Strings stay opaque: nothing is split,
// Unicode-normalized, case-folded or path-cleaned, so equal keys are equal
// identities. The incarnation is not part of the key; it distinguishes versions
// of the identity the key names.
func (a DurableAddress) Key() (string, error) {
	if err := validateDurableAddress(a); err != nil {
		return "", err
	}
	scope := sortedScope(a.Scope)
	pairs := make([][2]string, len(scope))
	for i, entry := range scope {
		pairs[i] = [2]string{entry.Dimension, entry.Value}
	}
	tuple := [5]any{durableAddressKeyVersion, a.Provider, a.Type, pairs, a.ID}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tuple); err != nil {
		return "", err
	}
	return string(bytes.TrimSuffix(data.Bytes(), []byte{'\n'})), nil
}

// HasStrongEnvironment reports whether environment holds a strong fingerprint,
// the minimum for a durable identity.
func HasStrongEnvironment(environment []EnvironmentIdentity) bool {
	return slices.ContainsFunc(environment, func(fingerprint EnvironmentIdentity) bool {
		return fingerprint.Strength == EnvironmentStrong
	})
}

// StrongEnvironment returns the strong fingerprints of environment, ordered by
// scheme and ID. A store keys environments by these.
func StrongEnvironment(environment []EnvironmentIdentity) []EnvironmentIdentity {
	strong := make([]EnvironmentIdentity, 0, len(environment))
	for _, fingerprint := range environment {
		if fingerprint.Strength == EnvironmentStrong {
			strong = append(strong, fingerprint)
		}
	}
	return sortedEnvironment(strong)
}

// SameEnvironment reports whether two declarations refer to the same
// environment: they share a strong fingerprint (equal Scheme and ID, strong in
// both) and no exclusive scheme carries different values in them. Weak
// fingerprints never decide. The veto matters because strong fingerprints can be
// shared by different environments: minikube profiles on one machine share a
// root CA, so the CA alone would join them unless their exclusive namespace UIDs
// differ.
//
// The relation is not transitive. One cluster seen by an administrator
// ({uid, ca}), by a namespace-scoped user ({ca}) and by a reader of the UID alone
// ({uid}) is one environment, yet the last two declarations share nothing. A
// store resolves environments by merging every record that shares a fingerprint
// with a declaration, and applies the same veto when it merges records; it never
// compares declarations pairwise.
//
// Fingerprints cannot tell apart two clusters restored from one backup, which
// share every fingerprint. A store splits them when their objects diverge.
func SameEnvironment(a, b []EnvironmentIdentity) bool {
	shared := false
	for _, x := range a {
		if x.Strength != EnvironmentStrong {
			continue
		}
		for _, y := range b {
			if y.Strength != EnvironmentStrong || x.Scheme != y.Scheme {
				continue
			}
			switch {
			case x.ID == y.ID:
				shared = true
			case x.Exclusive && y.Exclusive:
				return false
			}
		}
	}
	return shared
}

// EnvironmentContinuous reports whether current may replace previous without a
// change of environment: neither declares a strong fingerprint, or they refer to
// the same environment (SameEnvironment). A declaration that gains, loses or
// changes fingerprints while keeping a strong one in common is continuous.
// Learning a first strong fingerprint, losing every one, or moving to another
// environment is not.
func EnvironmentContinuous(previous, current []EnvironmentIdentity) bool {
	if !HasStrongEnvironment(previous) && !HasStrongEnvironment(current) {
		return true
	}
	return SameEnvironment(previous, current)
}

// sortedScope returns an independent, non-nil copy ordered by dimension.
func sortedScope(scope []ScopeEntry) []ScopeEntry {
	sorted := make([]ScopeEntry, len(scope))
	copy(sorted, scope)
	slices.SortFunc(sorted, func(a, b ScopeEntry) int { return strings.Compare(a.Dimension, b.Dimension) })
	return sorted
}

// sortedEnvironment returns an independent, non-nil copy ordered by scheme and ID.
func sortedEnvironment(environment []EnvironmentIdentity) []EnvironmentIdentity {
	sorted := make([]EnvironmentIdentity, len(environment))
	copy(sorted, environment)
	slices.SortFunc(sorted, func(a, b EnvironmentIdentity) int {
		if order := strings.Compare(a.Scheme, b.Scheme); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return sorted
}

func validateDurableAddress(a DurableAddress) error {
	if err := admission(a); err != nil {
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
	if err := validateEnvironment(r.Environment); err != nil {
		return err
	}
	if !HasStrongEnvironment(r.Environment) {
		return invalid("a durable reference requires a strong environment fingerprint")
	}
	if err := validateDurableAddress(r.Address); err != nil {
		return err
	}
	return token(r.Incarnation)
}

// validateEnvironment admits a bounded declaration: each fingerprint printable
// and bounded, none repeated, and no exclusive scheme with two values. An empty
// declaration is valid and declares nothing.
func validateEnvironment(environment []EnvironmentIdentity) error {
	if len(environment) == 0 {
		return nil
	}
	if len(environment) > MaxEnvironmentIdentities {
		return limited("environment declares too many fingerprints")
	}
	if err := admission(environment); err != nil {
		return err
	}
	seen := make(map[[2]string]bool, len(environment))
	exclusive := make(map[string]string)
	for _, fingerprint := range environment {
		if err := validateEnvironmentIdentity(fingerprint); err != nil {
			return err
		}
		pair := [2]string{fingerprint.Scheme, fingerprint.ID}
		if seen[pair] {
			return invalid("duplicate environment fingerprint")
		}
		seen[pair] = true
		if !fingerprint.Exclusive {
			continue
		}
		if value, declared := exclusive[fingerprint.Scheme]; declared && value != fingerprint.ID {
			return invalid("an exclusive environment scheme declares one value")
		}
		exclusive[fingerprint.Scheme] = fingerprint.ID
	}
	return nil
}

// validateEnvironmentIdentity admits bounded, printable text. The value repeats
// on every type of a route and keys durable stores, so it is held to more than
// valid UTF-8: no control characters, and a byte ceiling.
func validateEnvironmentIdentity(v EnvironmentIdentity) error {
	if err := environmentText(v.Scheme, MaxEnvironmentSchemeBytes); err != nil {
		return err
	}
	if err := environmentText(v.ID, MaxEnvironmentIDBytes); err != nil {
		return err
	}
	switch v.Strength {
	case EnvironmentStrong:
	case EnvironmentWeak:
		if v.Exclusive {
			return invalid("only a strong environment fingerprint can be exclusive")
		}
	default:
		return invalid("unknown environment fingerprint strength")
	}
	return nil
}

func environmentText(s string, limit int) error {
	if err := text(s, true); err != nil {
		return err
	}
	if len(s) > limit {
		return limited("environment fingerprint exceeds its byte limit")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return invalid("environment fingerprint contains a control character")
		}
	}
	return nil
}
