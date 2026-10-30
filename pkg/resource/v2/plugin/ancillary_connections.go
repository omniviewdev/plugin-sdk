package plugin

import (
	"context"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// connectionCall keeps connection payload checks beside their optional provider traits.
// The shared ancillaryCallHandler owns admission and response validation.
func (s *ancillaryServer) connectionCall(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
) (*resourcepb.AncillaryResponse, error) {
	switch r.Operation {
	case nativeConnectionsOperation:
		return s.nativeConnectionSnapshot(ctx)
	case connectionOverlayOperation:
		return s.applyOverlay(ctx, r)
	case ancillaryListConnections:
		return s.listConfiguredConnections(ctx)
	case ancillaryUpdateConnection:
		return s.updateConfiguredConnection(ctx, r)
	case ancillaryDeleteConnection:
		return s.deleteConfiguredConnection(ctx, r)
	default:
		return nil, unsupported("unknown ancillary operation")
	}
}

func (s *ancillaryServer) nativeConnectionSnapshot(ctx context.Context) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	p, ok := impl.(resourcev2.ConnectionOverlayProvider)
	if !ok {
		return nil, unsupported("native connection snapshots unsupported")
	}
	connections, err := p.NativeConnections(ctx)
	if err != nil {
		return nil, err
	}
	return presentationJSON(connections)
}

func (s *ancillaryServer) applyOverlay(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	p, ok := impl.(resourcev2.ConnectionOverlayProvider)
	if !ok {
		return nil, unsupported("connection overlays unsupported")
	}
	var overlay resourcev2.ConnectionOverlay
	if err := decodeConnection(r.ConnectionJson, &overlay); err != nil {
		return nil, err
	}
	if err := resourcev2.ValidateConnectionOverlay(overlay); err != nil {
		return nil, err
	}
	if err := p.ApplyConnectionOverlay(ctx, overlay); err != nil {
		return nil, err
	}
	return presentationJSON(struct{}{})
}

func (s *ancillaryServer) listConfiguredConnections(ctx context.Context) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	p, ok := impl.(interface {
		ListConnections(context.Context) ([]types.Connection, error)
	})
	if !ok {
		return nil, unsupported("connection listing unsupported")
	}
	v, err := p.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	return presentationJSON(v)
}

func (s *ancillaryServer) updateConfiguredConnection(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	p, ok := impl.(interface {
		UpdateConnection(context.Context, types.Connection) (types.Connection, error)
	})
	if !ok {
		return nil, unsupported("connection updates unsupported")
	}
	var in types.Connection
	if err := decodeConnection(r.ConnectionJson, &in); err != nil {
		return nil, err
	}
	v, err := p.UpdateConnection(ctx, in)
	if err != nil {
		return nil, err
	}
	return presentationJSON(v)
}

func (s *ancillaryServer) deleteConfiguredConnection(
	ctx context.Context,
	r *resourcepb.AncillaryRequest,
) (*resourcepb.AncillaryResponse, error) {
	impl := s.owner.impl
	p, ok := impl.(interface {
		DeleteConnection(context.Context, string) error
	})
	if !ok {
		return nil, unsupported("connection deletion unsupported")
	}
	if r.Id == "" {
		return nil, invalid("missing connection id")
	}
	if err := p.DeleteConnection(ctx, r.Id); err != nil {
		return nil, err
	}
	return presentationJSON(struct{}{})
}
