package resourcev2

// ValidateRelationshipResolutionRequest admits explicit bindings, not permissions.
func ValidateRelationshipResolutionRequest(req RelationshipResolutionRequest) error {
	if len(req.Targets) > MaxRelationshipTargets {
		return limited("relationship target binding limit")
	}
	if err := admission(req); err != nil {
		return err
	}
	if err := ValidateGetRequest(req.Source); err != nil {
		return err
	}
	indexes := make([]relationshipScopeIndex, len(req.Targets))
	for i, target := range req.Targets {
		if err := validateTarget(target); err != nil {
			return err
		}
		if target.Scope == nil {
			return invalid("relationship target scope must be array")
		}
		indexes[i] = indexRelationshipScope(target.Scope)
		for j, prior := range req.Targets[:i] {
			if prior.Provider != target.Provider || prior.Type != target.Type || prior.Route != target.Route {
				continue
			}
			if len(indexes[j]) != len(indexes[i]) {
				return invalid("inconsistent relationship target scope dimensions")
			}
			for dimension := range indexes[j] {
				if _, ok := indexes[i][dimension]; !ok {
					return invalid("inconsistent relationship target scope dimensions")
				}
			}
			if relationshipScopesConflict(indexes[j], indexes[i]) {
				return invalid("overlapping relationship target bindings")
			}
		}
	}
	return nil
}

// Private admission indexes avoid quadratic membership scans across large oneOf
// lists. They do not define resource query or authorization semantics.
type relationshipScopeIndex map[string]relationshipConstraintIndex

type relationshipConstraintIndex struct {
	any    bool
	values map[string]struct{}
}

func indexRelationshipScope(scope QueryScope) relationshipScopeIndex {
	index := make(relationshipScopeIndex, len(scope))
	for _, constraint := range scope {
		entry := relationshipConstraintIndex{any: constraint.Match == ScopeAny}
		if constraint.Values != nil {
			entry.values = make(map[string]struct{}, len(*constraint.Values))
			for _, value := range *constraint.Values {
				entry.values[value] = struct{}{}
			}
		}
		index[constraint.Dimension] = entry
	}
	return index
}

func relationshipScopesConflict(a, b relationshipScopeIndex) bool {
	if len(a) != len(b) {
		return false
	}
	equal, overlap := true, true
	for dimension, left := range a {
		right, found := b[dimension]
		if !found {
			return false
		}
		if left.any && right.any {
			continue
		}
		if left.any || right.any {
			equal = false
			finite := left
			if left.any {
				finite = right
			}
			if len(finite.values) == 0 {
				overlap = false
			}
			continue
		}
		common := 0
		small, large := left.values, right.values
		if len(small) > len(large) {
			small, large = large, small
		}
		for value := range small {
			if _, ok := large[value]; ok {
				common++
			}
		}
		if common == 0 {
			overlap = false
		}
		if common != len(left.values) || common != len(right.values) {
			equal = false
		}
	}
	return equal || overlap
}

// MatchRelationshipTarget selects exactly one host binding for an observed link.
// Neither source identity nor a provider default can supply a missing binding.
func MatchRelationshipTarget(
	req RelationshipResolutionRequest,
	route AccessRoute,
	provider, typ string,
	scope []ScopeEntry,
) (ReadTarget, error) {
	if err := ValidateRelationshipResolutionRequest(req); err != nil {
		return ReadTarget{}, err
	}
	return matchRelationshipTarget(req, route, provider, typ, scope)
}

func matchRelationshipTarget(
	req RelationshipResolutionRequest,
	route AccessRoute,
	provider, typ string,
	scope []ScopeEntry,
) (ReadTarget, error) {
	if err := admission(scope); err != nil {
		return ReadTarget{}, err
	}
	if err := text(provider, true); err != nil {
		return ReadTarget{}, err
	}
	if err := text(typ, true); err != nil {
		return ReadTarget{}, err
	}
	if err := validateRoute(route); err != nil {
		return ReadTarget{}, err
	}
	seen := make(map[string]bool, len(scope))
	for _, entry := range scope {
		if err := text(entry.Dimension, true); err != nil {
			return ReadTarget{}, err
		}
		if err := text(entry.Value, false); err != nil {
			return ReadTarget{}, err
		}
		if seen[entry.Dimension] {
			return ReadTarget{}, invalid("duplicate identity dimension")
		}
		seen[entry.Dimension] = true
	}
	var match ReadTarget
	count := 0
	for _, target := range req.Targets {
		if target.Provider != provider || target.Type != typ || target.Route != route {
			continue
		}
		dimensions := make([]Dimension, len(target.Scope))
		for i, constraint := range target.Scope {
			dimensions[i] = Dimension{Name: constraint.Dimension, AllowEmpty: true}
		}
		exact, err := exactScopeEntries(scope, dimensions)
		if err == nil && scopeContains(target.Scope, exact) {
			match = target
			count++
		}
	}
	if count != 1 {
		return ReadTarget{}, NewError("UNSUPPORTED", "Observed relationship has no unique explicit target binding.")
	}
	return match, nil
}

// ValidateRelationshipResolutionResult rejects the whole result if any observed
// link escapes its explicit target binding. It never filters links.
func ValidateRelationshipResolutionResult(req RelationshipResolutionRequest, results []ResolvedRelationship) error {
	if err := ValidateRelationshipResolutionRequest(req); err != nil {
		return err
	}
	if err := admission(results); err != nil {
		return err
	}
	for _, result := range results {
		if err := validateResolvedRelationship(result); err != nil {
			return err
		}
		for _, target := range result.Targets {
			address := target.Reference.Address
			if address.Type != result.Descriptor.TargetType {
				return NewError("UNSUPPORTED", "Relationship target does not match descriptor target type.")
			}
			binding, err := matchRelationshipTarget(req, target.Route, address.Provider, address.Type, address.Scope)
			if err != nil {
				return err
			}
			if address.Authority != binding.Authority {
				return NewError("UNSUPPORTED", "Relationship target authority does not match explicit binding.")
			}
		}
	}
	return nil
}
