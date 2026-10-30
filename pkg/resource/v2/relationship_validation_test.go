package resourcev2

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func relationshipAdmissionFixture() (RelationshipResolutionRequest, []ResolvedRelationship) {
	scope := []ScopeEntry{{Dimension: "namespace", Value: "team"}}
	values := []string{"team"}
	route := AccessRoute{Connection: "target-connection"}
	req := RelationshipResolutionRequest{
		Source: GetRequest{
			Route: AccessRoute{Connection: "source-connection"},
			Reference: ResourceReference{
				Address: ResourceAddress{
					Provider:  "source",
					Authority: "source-authority",
					Type:      "source/type",
					Scope:     []ScopeEntry{},
					ID:        "source",
				},
			},
			Representation: "native",
		},
		Targets: []ReadTarget{
			{
				Route:          route,
				Provider:       "target",
				Authority:      "target-authority",
				Type:           "opaque/target",
				Scope:          QueryScope{{Dimension: "namespace", Match: ScopeOneOf, Values: &values}},
				Representation: "native",
			},
		},
	}
	results := []ResolvedRelationship{
		{
			Descriptor: RelationshipDescriptor{
				Type:            RelUses,
				TargetType:      "opaque/target",
				Label:           "uses",
				Direction:       EdgeOutgoing,
				TargetScope:     []ScopeEntry{},
				ScopeExtractors: []ScopeExtractor{},
			},
			Targets: []RelationshipTarget{
				{
					Route: route,
					Reference: ResourceReference{
						Address: ResourceAddress{
							Provider:  "target",
							Authority: "target-authority",
							Type:      "opaque/target",
							Scope:     scope,
							ID:        "target",
						},
					},
				},
			},
		},
	}
	return req, results
}

func TestRelationshipExplicitTargetAdmission(t *testing.T) {
	req, results := relationshipAdmissionFixture()
	if err := ValidateRelationshipResolutionResult(req, results); err != nil {
		t.Fatalf("different target authority must be valid: %v", err)
	}
	address := results[0].Targets[0].Reference.Address
	binding, err := MatchRelationshipTarget(req, req.Targets[0].Route, address.Provider, address.Type, address.Scope)
	if err != nil || binding.Authority != "target-authority" {
		t.Fatalf("explicit target binding: %+v, %v", binding, err)
	}
	for _, name := range []string{"missing binding", "scope escape", "authority swap", "type mismatch", "route swap", "missing dimension", "provider swap"} {
		t.Run(name, func(t *testing.T) {
			req, results := relationshipAdmissionFixture()
			target := &results[0].Targets[0]
			switch name {
			case "missing binding":
				req.Targets = nil
			case "scope escape":
				target.Reference.Address.Scope[0].Value = "other"
			case "authority swap":
				target.Reference.Address.Authority = req.Source.Reference.Address.Authority
			case "type mismatch":
				results[0].Descriptor.TargetType = "different"
			case "route swap":
				target.Route = req.Source.Route
			case "missing dimension":
				target.Reference.Address.Scope = []ScopeEntry{}
			case "provider swap":
				target.Reference.Address.Provider = "other"
			}
			if err := ValidateRelationshipResolutionResult(req, results); ErrorCode(err) != "UNSUPPORTED" {
				t.Fatalf("expected UNSUPPORTED: %v", err)
			}
		})
	}
}

func TestRelationshipBindingAmbiguityAndLimits(t *testing.T) {
	for _, name := range []string{"duplicate", "authority ambiguity", "partial overlap"} {
		t.Run(name, func(t *testing.T) {
			req, _ := relationshipAdmissionFixture()
			other := req.Targets[0]
			if name != "duplicate" {
				other.Authority = "another-authority"
			}
			if name == "partial overlap" {
				values := []string{"team", "other"}
				other.Scope = QueryScope{{Dimension: "namespace", Match: ScopeOneOf, Values: &values}}
			}
			req.Targets = append(req.Targets, other)
			if err := ValidateRelationshipResolutionRequest(req); ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("ambiguous binding admitted: %v", err)
			}
		})
	}
	req, _ := relationshipAdmissionFixture()
	req.Targets = make([]ReadTarget, MaxRelationshipTargets+1)
	if err := ValidateRelationshipResolutionRequest(req); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("target cap: %v", err)
	}
	req, _ = relationshipAdmissionFixture()
	other := req.Targets[0]
	values := []string{"other"}
	other.Scope = QueryScope{{Dimension: "namespace", Match: ScopeOneOf, Values: &values}}
	other.Authority = "another-authority"
	req.Targets = append(req.Targets, other)
	if err := ValidateRelationshipResolutionRequest(req); err != nil {
		t.Fatalf("disjoint target scopes rejected: %v", err)
	}
}

func TestRelationshipRequestStrictJSON(t *testing.T) {
	req, _ := relationshipAdmissionFixture()
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	base := string(encoded)
	cases := []string{
		strings.Replace(base, `"targets":`, `"unknown":0,"targets":`, 1),
		strings.Replace(base, `"targets":`, `"targets":[],"targets":`, 1),
		strings.Replace(base, `"targets":[`, `"targets":null,"discard":[`, 1),
		strings.Replace(base, `"target-authority"`, `"\ud800"`, 1),
		strings.Replace(base, `"target-authority"`, "\"\xff\"", 1),
		`{"source":null,"targets":[]}`,
		`{"targets":[]}`,
		strings.Replace(
			base,
			`"scope":[{"dimension":"namespace","match":"oneOf","values":["team"]}]`,
			`"scope":null`,
			1,
		),
	}
	for _, data := range cases {
		var got RelationshipResolutionRequest
		if decodeErr := json.Unmarshal([]byte(data), &got); ErrorCode(decodeErr) != "INVALID_REQUEST" {
			t.Fatalf("strict request admitted %s: %v", data, decodeErr)
		}
	}
	var got RelationshipResolutionRequest
	if decodeErr := json.Unmarshal(encoded, &got); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	again, err := json.Marshal(got)
	if err != nil || string(again) != base {
		t.Fatalf("canonical output changed: %s, %v", again, err)
	}
	req.Targets = nil
	encoded, err = json.Marshal(req)
	if err != nil || !strings.Contains(string(encoded), `"targets":[]`) {
		t.Fatalf("nil targets must emit array: %s, %v", encoded, err)
	}
	if decodeErr := json.Unmarshal(encoded, &got); decodeErr != nil {
		t.Fatalf("canonical empty request rejected: %v", decodeErr)
	}
	if decodeErr := json.Unmarshal(
		[]byte(strings.Replace(string(encoded), `"targets":[]`, `"targets":null`, 1)),
		&got,
	); ErrorCode(
		decodeErr,
	) != "INVALID_REQUEST" {
		t.Fatalf("null target array admitted: %v", decodeErr)
	}
}

func TestRelationshipLargeScopeAdmission(t *testing.T) {
	req, _ := relationshipAdmissionFixture()
	values := make([]string, 20000)
	for i := range values {
		values[i] = strconv.Itoa(i)
	}
	req.Targets[0].Scope[0].Values = &values
	other := req.Targets[0]
	other.Authority = "another-authority"
	overlap := append([]string(nil), values...)
	other.Scope = QueryScope{{Dimension: "namespace", Match: ScopeOneOf, Values: &overlap}}
	req.Targets = append(req.Targets, other)
	if err := ValidateRelationshipResolutionRequest(req); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("large duplicate binding admitted: %v", err)
	}
	overlap[0] = "nonoverlapping-first-value"
	if err := ValidateRelationshipResolutionRequest(req); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("large overlapping binding admitted: %v", err)
	}
}

func TestRelationshipNativeScopeAdmission(t *testing.T) {
	for _, scope := range [][]ScopeEntry{
		{{Dimension: "namespace", Value: "team"}, {Dimension: "namespace", Value: "other"}},
		{{Dimension: "", Value: "team"}},
		{{Dimension: "namespace", Value: "\xff"}},
	} {
		req, _ := relationshipAdmissionFixture()
		target := req.Targets[0]
		if _, err := MatchRelationshipTarget(
			req,
			target.Route,
			target.Provider,
			target.Type,
			scope,
		); ErrorCode(
			err,
		) != "INVALID_REQUEST" {
			t.Fatalf("invalid native scope admitted: %v", err)
		}
	}
}

func TestRelationshipIdentityScopeProjection(t *testing.T) {
	req, results := relationshipAdmissionFixture()
	target := &results[0].Targets[0]
	target.Reference.Address.Scope = append(
		target.Reference.Address.Scope,
		ScopeEntry{Dimension: "account", Value: "account-1"},
	)
	if err := ValidateRelationshipResolutionResult(req, results); err != nil {
		t.Fatalf("extra identity dimension must not change query coverage: %v", err)
	}
	binding := req.Targets[0]
	binding.Scope = QueryScope{}
	req.Targets = append(req.Targets, binding)
	if err := ValidateRelationshipResolutionRequest(req); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("inconsistent query dimension sets admitted: %v", err)
	}
}
