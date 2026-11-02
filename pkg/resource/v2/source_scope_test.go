package resourcev2

import "testing"

func TestSourceQueryOnlyDimension(t *testing.T) {
	r := phaseRequest(SourceModeSnapshotAndUpdates)
	values := []string{"repository-a"}
	r.Target.Scope = append(r.Target.Scope, ScopeConstraint{Dimension: "repository", Match: ScopeOneOf, Values: &values})
	observation := SourceObservation{Record: codecRecord()}
	if err := ValidateSourceObservation(r.Target, r.Target.Representation, observation); err != nil {
		t.Fatalf("query-only dimension rejected observation: %v", err)
	}
	tracker := NewSourcePhaseTracker(r, ContinuityBestEffort)
	phaseAdmit(t, tracker, phaseBegin(1, r.Mode))
	frame := phaseFrame(2)
	frame.Snapshot = &SourceSnapshot{Observations: []SourceObservation{observation}}
	phaseAdmit(t, tracker, frame)
	phaseAdmit(t, tracker, phaseEnd(3, ContinuityBestEffort))
	ref := observation.Record.Reference
	frame = phaseFrame(4)
	frame.Changes = &SourceChanges{Changes: []SourceChange{{Kind: SourceChangeRemove, Reference: &ref}}}
	phaseAdmit(t, tracker, frame)
}

func TestSourceGuaranteeForTargetScope(t *testing.T) {
	a, b, c := []string{"a"}, []string{"b"}, []string{"c"}
	scope := func(values *[]string) QueryScope {
		return QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: values}}
	}
	d := ResourceType{Capabilities: []Capability{
		{Operation: OperationSourceUpdates, Scope: scope(&a), Guarantee: ContinuityGapFree},
		{Operation: OperationSourceUpdates, Scope: scope(&b), Guarantee: ContinuityBestEffort},
	}}
	for _, tc := range []struct {
		values *[]string
		want   string
	}{{&a, ContinuityGapFree}, {&b, ContinuityBestEffort}, {&c, ""}} {
		if got := SourceGuaranteeForTarget(d, ReadTarget{Scope: scope(tc.values)}); got != tc.want {
			t.Fatalf("scope %v: got %q want %q", *tc.values, got, tc.want)
		}
	}
}
