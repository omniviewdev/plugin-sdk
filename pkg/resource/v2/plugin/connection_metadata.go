package plugin

import (
	"context"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

const (
	getConnectionMetadataOperation     = "getConnectionMetadata"
	refreshConnectionMetadataOperation = "refreshConnectionMetadata"
	watchConnectionMetadataOperation   = "watchConnectionMetadata"
	watchConnectionMetadataMethod      = "/omniview.resource.v2.ResourceAncillary/WatchConnectionMetadata"
)

// Metadata uses the existing bounded JSON envelopes, including explicit null values.
var connectionMetadataStream = grpc.StreamDesc{
	StreamName: "WatchConnectionMetadata", Handler: watchConnectionMetadataHandler, ServerStreams: true,
}

func (s *ancillaryServer) connectionMetadataCall(ctx context.Context, r *resourcepb.AncillaryRequest) (*resourcepb.AncillaryResponse, error) {
	p, ok := s.owner.impl.(resourcev2.ConnectionMetadataProvider)
	if !ok {
		return nil, unsupported("connection metadata unsupported")
	}
	if r.Id == "" {
		return nil, invalid("missing connection id")
	}
	var snapshot resourcev2.ConnectionMetadataSnapshot
	var err error
	if r.Operation == refreshConnectionMetadataOperation {
		snapshot, err = p.RefreshConnectionMetadata(ctx, r.Id)
	} else {
		snapshot, err = p.GetConnectionMetadata(ctx, r.Id)
	}
	if err != nil {
		return nil, err
	}
	if err := resourcev2.ValidateConnectionMetadataSnapshot(snapshot); err != nil {
		return nil, err
	}
	return presentationJSON(snapshot)
}

// GetConnectionMetadata returns the provider's cached metadata without refreshing it.
func (c *Client) GetConnectionMetadata(ctx context.Context, id string) (resourcev2.ConnectionMetadataSnapshot, error) {
	var out resourcev2.ConnectionMetadataSnapshot
	err := c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: getConnectionMetadataOperation, Id: id}, &out)
	return out, err
}

// RefreshConnectionMetadata requests a refresh and returns the provider's current snapshot.
func (c *Client) RefreshConnectionMetadata(ctx context.Context, id string) (resourcev2.ConnectionMetadataSnapshot, error) {
	var out resourcev2.ConnectionMetadataSnapshot
	err := c.ancillaryJSON(ctx, &resourcepb.AncillaryRequest{Operation: refreshConnectionMetadataOperation, Id: id}, &out)
	return out, err
}

func watchConnectionMetadataHandler(srv any, stream grpc.ServerStream) error {
	s, ok := srv.(*ancillaryServer)
	if !ok {
		return rpcError(invalid("invalid ancillary server"))
	}
	r := &resourcepb.AncillaryRequest{}
	e := &envelope{message: r}
	if err := stream.RecvMsg(e); err != nil {
		return rpcError(invalid("malformed metadata stream request"))
	}
	if e.err != nil {
		return rpcError(e.err)
	}
	if r.Version != resourcev2.WireVersion {
		return rpcError(unsupported("unsupported stream wire version"))
	}
	if r.Operation != watchConnectionMetadataOperation {
		return rpcError(invalid("unexpected metadata stream operation"))
	}
	ctx, err := streamContext(stream, s)
	if err != nil {
		return rpcError(err)
	}
	p, ok := s.owner.impl.(resourcev2.ConnectionMetadataProvider)
	if !ok {
		return rpcError(unsupported("connection metadata unsupported"))
	}
	var mu sync.Mutex
	err = p.WatchConnectionMetadata(ctx, func(event resourcev2.ConnectionMetadataEvent) error {
		if err := resourcev2.ValidateConnectionMetadataSnapshot(event.Snapshot); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		out, err := presentationJSON(event)
		if err != nil {
			return err
		}
		return sendResource(stream, &resourcepb.ConnectionStreamEvent{Version: resourcev2.WireVersion, ConnectionsJson: out.GetJson()})
	})
	return rpcError(s.owner.classify(err))
}

// WatchConnectionMetadata forwards snapshots and removals until completion or cancellation.
func (c *Client) WatchConnectionMetadata(ctx context.Context, sink resourcev2.ConnectionMetadataSink) error {
	if sink == nil {
		return invalid("missing connection metadata sink")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx, err := outgoingSession(ctx)
	if err != nil {
		return err
	}
	s, err := c.conn.NewStream(ctx, &connectionMetadataStream, watchConnectionMetadataMethod,
		grpc.MaxCallRecvMsgSize(MaxWireBytes), grpc.MaxCallSendMsgSize(MaxWireBytes))
	if err != nil {
		return domainError(err)
	}
	if err = s.SendMsg(&resourcepb.AncillaryRequest{Version: resourcev2.WireVersion, Operation: watchConnectionMetadataOperation}); err != nil {
		return domainError(err)
	}
	if err = s.CloseSend(); err != nil {
		return domainError(err)
	}
	for {
		p := &resourcepb.ConnectionStreamEvent{}
		e := &envelope{message: p}
		if err = s.RecvMsg(e); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return domainError(err)
		}
		if e.err != nil {
			return e.err
		}
		if p.Version != resourcev2.WireVersion {
			return unsupported("unsupported stream response version")
		}
		var event resourcev2.ConnectionMetadataEvent
		if err = decodeConnection(p.ConnectionsJson, &event); err != nil {
			return err
		}
		if err = sink(event); err != nil {
			return err
		}
	}
}
