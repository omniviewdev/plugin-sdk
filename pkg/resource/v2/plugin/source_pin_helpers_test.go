package plugin

import (
	"context"
	"sync/atomic"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

type sourcePinProvider struct {
	qualifiedProvider
	starts atomic.Int32
	seen   chan resourcev2.Session
}

func (p *sourcePinProvider) Catalog(ctx context.Context, in resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	c, err := p.qualifiedProvider.Catalog(ctx, in)
	if err == nil {
		c.Types[0].Capabilities = append(c.Types[0].Capabilities,
			resourcev2.Capability{Operation: resourcev2.OperationEnumerate, Scope: resourcev2.QueryScope{}},
			resourcev2.Capability{Operation: resourcev2.OperationSourceUpdates, Scope: resourcev2.QueryScope{}, Guarantee: resourcev2.ContinuityBestEffort},
		)
	}
	return c, err
}

func (p *sourcePinProvider) OpenSource(ctx context.Context, _ resourcev2.SourceOpenRequest, emitter resourcev2.SourceEmitter) error {
	p.starts.Add(1)
	if p.seen != nil {
		s, _ := resourcev2.SessionFromContext(ctx)
		p.seen <- s
	}
	return emitter.EndSnapshot(ctx, resourcev2.SourceCoverage{Enumeration: resourcev2.EnumerationExhausted, Consistency: resourcev2.ConsistencyBestEffort}, resourcev2.ContinuityNotApplicable)
}

func sourcePinRequest() resourcev2.SourceOpenRequest {
	r := testGet()
	return resourcev2.SourceOpenRequest{
		SessionID: "session", Generation: "generation", Mode: resourcev2.SourceModeSnapshot,
		Target:      resourcev2.ReadTarget{Route: r.Route, Provider: r.Reference.Address.Provider, Authority: r.Reference.Address.Authority, Type: r.Reference.Address.Type, Scope: resourcev2.QueryScope{}, Representation: r.Representation},
		Acquisition: resourcev2.SourceAcquisition{Kind: resourcev2.SourceAcquisitionEnumerate},
		Limits:      resourcev2.SourceLimits{MaxFrameBytes: 64 << 10, MaxBatchItems: 10},
		Binding:     resourcev2.RepresentationBinding{Representation: testRepresentation()},
	}
}
