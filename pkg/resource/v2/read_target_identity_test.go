package resourcev2

import (
	"reflect"
	"testing"
)

func TestCanonicalReadTargetOwnsSelectionsAndNormalizesEquality(t *testing.T) {
	values := []string{"west", "east", "west"}
	target := controllerTarget()
	target.Scope = QueryScope{{Dimension: "zone", Match: ScopeOneOf, Values: &values}, {Dimension: "namespace", Match: ScopeAny}}
	snapshot, key, err := CanonicalReadTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	canonicalValues := []string{"east", "west"}
	equivalent := target
	equivalent.Scope = QueryScope{{Dimension: "namespace", Match: ScopeAny}, {Dimension: "zone", Match: ScopeOneOf, Values: &canonicalValues}}
	_, equivalentKey, err := CanonicalReadTarget(equivalent)
	if err != nil {
		t.Fatal(err)
	}
	if key != equivalentKey {
		t.Fatal("equivalent scope sets produced distinct acquisition identities")
	}
	if !reflect.DeepEqual(values, []string{"west", "east", "west"}) || target.Scope[0].Dimension != "zone" {
		t.Fatal("canonicalization changed caller-owned admission")
	}
	values[0] = "outside"
	if !reflect.DeepEqual(*snapshot.Scope[1].Values, []string{"east", "west"}) {
		t.Fatal("caller mutation changed retained acquisition")
	}
	(*snapshot.Scope[1].Values)[0] = "snapshot-only"
	if values[1] != "east" {
		t.Fatal("snapshot mutation changed caller storage")
	}
}

func TestCanonicalReadTargetSeparatesEveryAcquisitionBoundary(t *testing.T) {
	base := controllerTarget()
	values := []string{"east"}
	base.Scope = QueryScope{{Dimension: "zone", Match: ScopeOneOf, Values: &values}}
	_, baseKey, err := CanonicalReadTarget(base)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*ReadTarget){
		"route":           func(v *ReadTarget) { v.Route.Connection = "another-route" },
		"provider":        func(v *ReadTarget) { v.Provider = "another-provider" },
		"authority":       func(v *ReadTarget) { v.Authority = "another-authority" },
		"type":            func(v *ReadTarget) { v.Type = "another-type" },
		"representation":  func(v *ReadTarget) { v.Representation = "another-representation" },
		"selection":       func(v *ReadTarget) { (*v.Scope[0].Values)[0] = "west" },
		"empty selection": func(v *ReadTarget) { empty := []string{}; v.Scope[0].Values = &empty },
		"any selection":   func(v *ReadTarget) { v.Scope[0] = ScopeConstraint{Dimension: "zone", Match: ScopeAny} },
	}
	seen := map[string]bool{baseKey: true}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			changed := cloneReadTarget(base)
			change(&changed)
			_, key, err := CanonicalReadTarget(changed)
			if err != nil {
				t.Fatal(err)
			}
			if seen[key] {
				t.Fatal("distinct acquisition contexts shared an identity")
			}
			seen[key] = true
		})
	}
}

func TestCanonicalReadTargetRejectsAmbiguousDimensionsBeforeNormalization(t *testing.T) {
	target := controllerTarget()
	target.Scope = QueryScope{{Dimension: "zone", Match: ScopeAny}, {Dimension: "zone", Match: ScopeAny}}
	if _, _, err := CanonicalReadTarget(target); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("ambiguous dimension admission = %v", err)
	}
}
