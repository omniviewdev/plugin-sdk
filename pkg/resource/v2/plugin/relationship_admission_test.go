package plugin

import (
	"context"
	"sync/atomic"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
)

type relationshipBoundaryProvider struct {
	ancillaryFixtureProvider
	calls  atomic.Int32
	mutate func([]resourcev2.ResolvedRelationship)
}

func (p *relationshipBoundaryProvider) ResolveRelationships(context.Context, resourcev2.RelationshipResolutionRequest) ([]resourcev2.ResolvedRelationship, error) {
	p.calls.Add(1)
	out := relationshipFixtureResults()
	if p.mutate != nil {
		p.mutate(out)
	}
	return out, nil
}

func TestActualGRPCRelationshipBindingAdmission(t *testing.T) {
	for _, mode := range []string{"valid", "authority", "scope", "missing"} {
		t.Run(mode, func(t *testing.T) {
			p := &relationshipBoundaryProvider{}
			request := relationshipFixtureRequest()
			switch mode {
			case "authority":
				p.mutate = func(out []resourcev2.ResolvedRelationship) {
					out[0].Targets[0].Reference.Address.Authority = request.Source.Reference.Address.Authority
				}
			case "scope":
				selected := []string{"inside"}
				request.Targets[0].Scope = resourcev2.QueryScope{{Dimension: "region", Match: resourcev2.ScopeOneOf, Values: &selected}}
				p.mutate = func(out []resourcev2.ResolvedRelationship) {
					out[0].Targets[0].Reference.Address.Scope = []resourcev2.ScopeEntry{{Dimension: "region", Value: "outside"}}
				}
			case "missing":
				request.Targets = nil
			}
			s := grpc.NewServer()
			RegisterServer(s, p)
			out, err := NewClient(pipe(t, s)).ResolveRelationships(context.Background(), request)
			if mode == "valid" {
				if err != nil || len(out) != 1 || out[0].Targets[0].Reference.Address.Authority != "target-authority" || out[0].Targets[0].Reference.Address.Type != "opaque/linked-type" {
					t.Fatalf("cross-type binding failed: %v %v", out, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s target binding", mode)
			}
		})
	}
}

func TestActualGRPCMalformedRelationshipRequestDoesNotDispatch(t *testing.T) {
	source, err := resourcev2.ToProtoGetRequest(testGet())
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*resourcepb.AncillaryRequest{
		{Version: 2, Operation: "resolveRelationships", Get: source},
		{Version: 2, Operation: "resolveRelationships"},
		{Version: 2, Operation: "resolveRelationships", RelationshipResolution: &resourcepb.RelationshipResolutionInput{}},
		{Version: 2, Operation: "resolveRelationships", RelationshipResolution: &resourcepb.RelationshipResolutionInput{Source: source, Targets: []*resourcepb.ReadTarget{{}}}},
	} {
		p := &relationshipBoundaryProvider{}
		s := grpc.NewServer()
		RegisterServer(s, p)
		conn := pipe(t, s)
		var response resourcepb.AncillaryResponse
		err := conn.Invoke(context.Background(), resourcepb.ResourceAncillary_Call_FullMethodName, request, &response)
		if err == nil || p.calls.Load() != 0 {
			t.Fatalf("malformed request dispatched: calls=%d err=%v", p.calls.Load(), err)
		}
	}
}

type unboundRelationshipServer struct {
	resourcepb.UnimplementedResourceAncillaryServer
	mode string
}

func (s *unboundRelationshipServer) Call(context.Context, *resourcepb.AncillaryRequest) (*resourcepb.AncillaryResponse, error) {
	out := relationshipFixtureResults()
	switch s.mode {
	case "authority":
		out[0].Targets[0].Reference.Address.Authority = "unbound-authority"
	case "scope":
		out[0].Targets[0].Reference.Address.Scope = []resourcev2.ScopeEntry{{Dimension: "region", Value: "outside"}}
	}
	p, err := resourcev2.ToProtoResolvedRelationship(out[0])
	if err != nil {
		return nil, err
	}
	return &resourcepb.AncillaryResponse{Version: 2, Result: &resourcepb.AncillaryResponse_Resolved{Resolved: &resourcepb.ResolvedRelationships{Relationships: []*resourcepb.ResolvedRelationship{p}}}}, nil
}

func TestActualGRPCClientRejectsUnboundRelationshipResponse(t *testing.T) {
	for _, mode := range []string{"authority", "scope", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s := grpc.NewServer()
			resourcepb.RegisterResourceAncillaryServer(s, &unboundRelationshipServer{mode: mode})
			request := relationshipFixtureRequest()
			if mode == "scope" {
				selected := []string{"inside"}
				request.Targets[0].Scope = resourcev2.QueryScope{{Dimension: "region", Match: resourcev2.ScopeOneOf, Values: &selected}}
			}
			if mode == "missing" {
				request.Targets = nil
			}
			_, err := NewClient(pipe(t, s)).ResolveRelationships(context.Background(), request)
			if err == nil {
				t.Fatal("client accepted unbound relationship response")
			}
		})
	}
}
