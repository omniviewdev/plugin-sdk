package resourcev2

import "encoding/json"

func (v *RelationshipResolutionRequest) UnmarshalJSON(data []byte) error {
	type plain RelationshipResolutionRequest
	var out plain
	if _, err := decodeDomainObject(data, &out, "source", "targets"); err != nil {
		return err
	}
	value := RelationshipResolutionRequest(out)
	if value.Targets == nil {
		return invalid("relationship targets must be array")
	}
	if err := ValidateRelationshipResolutionRequest(value); err != nil {
		return err
	}
	*v = value
	return nil
}

func (v RelationshipResolutionRequest) MarshalJSON() ([]byte, error) {
	if err := ValidateRelationshipResolutionRequest(v); err != nil {
		return nil, err
	}
	type plain RelationshipResolutionRequest
	out := plain(v)
	if out.Targets == nil {
		out.Targets = []ReadTarget{}
	}
	return json.Marshal(out)
}
