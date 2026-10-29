package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// relationshipHealthCall keeps relationship, health, and event response checks local.
// The shared ancillaryCallHandler owns admission and response validation.
func (s *ancillaryServer) relationshipHealthCall(ctx context.Context, r *resourcepb.AncillaryRequest, route resourcev2.AccessRoute) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	out := &resourcepb.AncillaryResponse{Version: 2}
	switch r.Operation {
	case "relationships":
		if route.Connection == "" || r.Type == "" {
			return nil, invalid("missing relationship target")
		}
		p, ok := impl.(resourcev2.RelationshipProvider)
		if !ok {
			return nil, unsupported("relationships unsupported")
		}
		v, err := p.GetRelationships(ctx, route, r.Type)
		if err != nil {
			return nil, err
		}
		result := &resourcepb.RelationshipDescriptors{}
		for _, item := range v {
			p, err := resourcev2.ToProtoRelationshipDescriptor(item)
			if err != nil {
				return nil, err
			}
			result.Descriptors = append(result.Descriptors, p)
		}
		out.Result = &resourcepb.AncillaryResponse_Relationships{Relationships: result}
	case "resolveRelationships":
		if r.Get != nil {
			return nil, invalid("legacy get field is not valid for relationship resolution")
		}
		in, err := resourcev2.FromProtoRelationshipResolutionRequest(r.RelationshipResolution)
		if err != nil {
			return nil, err
		}
		if err := resourcev2.ValidateRelationshipResolutionRequest(in); err != nil {
			return nil, err
		}
		p, ok := impl.(resourcev2.RelationshipProvider)
		if !ok {
			return nil, unsupported("relationships unsupported")
		}
		v, err := p.ResolveRelationships(ctx, in)
		if err != nil {
			return nil, err
		}
		if err := resourcev2.ValidateRelationshipResolutionResult(in, v); err != nil {
			return nil, err
		}
		result := &resourcepb.ResolvedRelationships{}
		for _, item := range v {
			p, err := resourcev2.ToProtoResolvedRelationship(item)
			if err != nil {
				return nil, err
			}
			result.Relationships = append(result.Relationships, p)
		}
		out.Result = &resourcepb.AncillaryResponse_Resolved{Resolved: result}
	case "health", "events":
		in, err := resourcev2.FromProtoGetRequest(r.Get)
		if err != nil {
			return nil, err
		}
		switch r.Operation {
		case "health":
			p, ok := impl.(resourcev2.HealthProvider)
			if !ok {
				return nil, unsupported("health unsupported")
			}
			body, err := resourcev2.FromProtoValue(r.Value)
			if err != nil {
				return nil, err
			}
			v, err := p.GetHealth(ctx, in, body)
			if err != nil {
				return nil, err
			}
			result := &resourcepb.OptionalHealth{}
			if v != nil {
				result.Health, err = resourcev2.ToProtoResourceHealth(*v)
				if err != nil {
					return nil, err
				}
			}
			out.Result = &resourcepb.AncillaryResponse_Health{Health: result}
		case "events":
			if r.Limit > MaxPageSize {
				return nil, limited("event limit exceeds bound")
			}
			p, ok := impl.(resourcev2.HealthProvider)
			if !ok {
				return nil, unsupported("health unsupported")
			}
			v, err := p.GetResourceEvents(ctx, in, r.Limit)
			if err != nil {
				return nil, err
			}
			if len(v) > MaxPageSize || r.Limit > 0 && len(v) > int(r.Limit) {
				return nil, limited("event count exceeds bound")
			}
			result := &resourcepb.ResourceEvents{}
			for _, item := range v {
				p, err := resourcev2.ToProtoResourceEvent(item)
				if err != nil {
					return nil, err
				}
				result.Events = append(result.Events, p)
			}
			out.Result = &resourcepb.AncillaryResponse_Events{Events: result}
		}
	default:
		return nil, unsupported("unknown ancillary operation")
	}
	return out, nil
}
