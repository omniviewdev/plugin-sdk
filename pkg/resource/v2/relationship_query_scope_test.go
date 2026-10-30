package resourcev2

import "testing"

func TestRelationshipBindingQueryOnlyDimension(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		req, results := relationshipAdmissionFixture()
		constraint := ScopeConstraint{Dimension: "region", Match: ScopeAny}
		if restricted {
			values := []string{"east"}
			constraint.Match, constraint.Values = ScopeOneOf, &values
		}
		req.Targets[0].Scope = append(req.Targets[0].Scope, constraint)
		err := ValidateRelationshipResolutionResult(req, results)
		if restricted {
			if ErrorCode(err) != "UNSUPPORTED" {
				t.Fatalf("identity cannot prove query-only membership: %v", err)
			}
		} else if err != nil {
			t.Fatalf("explicit query-only any must admit known identity: %v", err)
		}
	}
}
