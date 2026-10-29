package resourcev2

import (
	"encoding/json"
	"sort"
)

// CanonicalReadTarget validates a target and returns an independently owned,
// normalized snapshot plus its deterministic domain fingerprint. Scope order and
// repeated oneOf values do not affect the fingerprint. Route, provider, authority,
// type, representation, and every scope selection do affect it.
//
// The fingerprint is an in-process equality key, not a durable wire format, source
// revision, authorization grant, or query-continuity token. The caller may mutate
// the returned snapshot without changing the input; recompute its fingerprint
// before using a changed snapshot as an acquisition key.
func CanonicalReadTarget(target ReadTarget) (ReadTarget, string, error) {
	if err := validateTarget(target); err != nil {
		return ReadTarget{}, "", err
	}
	target = cloneReadTarget(target)
	target.Scope = normalizeScopeOwned(target.Scope)
	fingerprint, err := json.Marshal(target)
	if err != nil {
		return ReadTarget{}, "", err
	}
	return target, string(fingerprint), nil
}

func cloneReadTarget(target ReadTarget) ReadTarget {
	target.Scope = append(QueryScope(nil), target.Scope...)
	for i := range target.Scope {
		if target.Scope[i].Values != nil {
			values := copyStrings(*target.Scope[i].Values)
			target.Scope[i].Values = &values
		}
	}
	return target
}

// canonicalScopeValues owns its slice; duplicate dimensions have already failed.
func canonicalScopeValues(values []string) []string {
	sort.Strings(values)
	n := 0
	for _, value := range values {
		if n == 0 || values[n-1] != value {
			values[n] = value
			n++
		}
	}
	return values[:n]
}

func normalizeScopeOwned(scope QueryScope) QueryScope {
	for i := range scope {
		if scope[i].Values != nil {
			*scope[i].Values = canonicalScopeValues(*scope[i].Values)
		}
	}
	sort.Slice(scope, func(i, j int) bool { return scope[i].Dimension < scope[j].Dimension })
	return scope
}
