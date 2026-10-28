package resourcev2

import resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"

func ToProtoRelationshipResolutionRequest(v RelationshipResolutionRequest) (*resourcepb.RelationshipResolutionInput, error) {
	if err := ValidateRelationshipResolutionRequest(v); err != nil {
		return nil, err
	}
	source, err := ToProtoGetRequest(v.Source)
	if err != nil {
		return nil, err
	}
	out := &resourcepb.RelationshipResolutionInput{Source: source, Targets: make([]*resourcepb.ReadTarget, 0, len(v.Targets))}
	for _, target := range v.Targets {
		p, err := ToProtoReadTarget(target)
		if err != nil {
			return nil, err
		}
		out.Targets = append(out.Targets, p)
	}
	return out, nil
}

func FromProtoRelationshipResolutionRequest(p *resourcepb.RelationshipResolutionInput) (RelationshipResolutionRequest, error) {
	var out RelationshipResolutionRequest
	if p != nil && len(p.Targets) > MaxRelationshipTargets {
		return out, limited("relationship target binding limit")
	}
	if err := admitProto(p); err != nil {
		return out, err
	}
	source, err := FromProtoGetRequest(p.Source)
	if err != nil {
		return out, err
	}
	out.Source = source
	out.Targets = make([]ReadTarget, 0, len(p.Targets))
	for _, target := range p.Targets {
		v, err := FromProtoReadTarget(target)
		if err != nil {
			return RelationshipResolutionRequest{}, err
		}
		out.Targets = append(out.Targets, v)
	}
	return out, ValidateRelationshipResolutionRequest(out)
}
