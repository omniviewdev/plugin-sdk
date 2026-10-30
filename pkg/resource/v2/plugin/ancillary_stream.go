package plugin

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func streamContext(stream grpc.ServerStream, s *ancillaryServer) (context.Context, error) {
	return incomingSession(stream.Context(), s.owner.settings)
}

func watchConnectionsHandler(srv any, stream grpc.ServerStream) error {
	s, ok := srv.(*ancillaryServer)
	if !ok {
		return rpcError(invalid("invalid ancillary server"))
	}
	r := &resourcepb.AncillaryRequest{}
	e := &envelope{message: r}
	if err := stream.RecvMsg(e); err != nil {
		return rpcError(invalid("malformed connection stream request"))
	}
	if e.err != nil {
		return rpcError(e.err)
	}
	if r.Version != resourcev2.WireVersion {
		return rpcError(unsupported("unsupported stream wire version"))
	}
	if r.Operation != "watchConnections" {
		return rpcError(invalid("unexpected connection stream operation"))
	}
	base, err := streamContext(stream, s)
	if err != nil {
		return rpcError(err)
	}
	p, ok := s.owner.impl.(resourcev2.ConnectionBrowser)
	if !ok {
		return rpcError(unsupported("connection browser unsupported"))
	}
	ctx, cancel := context.WithCancel(base)
	events := make(chan []types.Connection, 1)
	done := make(chan error, 1)
	go func() { done <- p.WatchConnections(ctx, events) }()

	send := func(v []types.Connection) error {
		out, err := presentationJSON(v)
		if err != nil {
			return err
		}
		return sendResource(stream, &resourcepb.ConnectionStreamEvent{
			Version:         resourcev2.WireVersion,
			ConnectionsJson: out.GetJson(),
		})
	}
	finish := func(err error) error { return rpcError(s.owner.classify(err)) }
	return relayAncillaryEvents(ctx, cancel, events, done, send, finish, "connection")
}

func streamActionHandler(srv any, stream grpc.ServerStream) error {
	s, ok := srv.(*ancillaryServer)
	if !ok {
		return rpcError(invalid("invalid ancillary server"))
	}
	r := &resourcepb.ActionStreamRequest{}
	e := &envelope{message: r}
	if err := stream.RecvMsg(e); err != nil {
		return rpcError(invalid("malformed action stream request"))
	}
	if e.err != nil {
		return rpcError(e.err)
	}
	if r.Version != resourcev2.WireVersion {
		return rpcError(unsupported("unsupported stream wire version"))
	}
	in, err := resourcev2.FromProtoActionRequest(r.Action)
	if err != nil {
		return rpcError(err)
	}
	base, err := streamContext(stream, s)
	if err != nil {
		return rpcError(err)
	}
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	if fenceErr := s.owner.checkCatalogRoute(ctx, r.Action.GetRoute()); fenceErr != nil {
		return rpcError(s.owner.classify(fenceErr))
	}
	if _, err = resourcev2.AdmitStreamingAction(ctx, s.owner.impl, in); err != nil {
		return rpcError(err)
	}
	p, ok := s.owner.impl.(resourcev2.StreamingActioner)
	if !ok {
		return rpcError(unsupported("streaming action unsupported"))
	}
	events := make(chan resourcev2.ActionEvent, 1)
	done := make(chan error, 1)
	go func() { done <- p.StreamAction(ctx, in, events) }()

	ready := false
	send := func(v resourcev2.ActionEvent) error {
		return s.sendActionEvent(ctx, stream, r.Action.GetRoute(), &ready, v)
	}
	finish := func(err error) error {
		if err != nil {
			return rpcError(s.owner.classify(err))
		}
		if !ready {
			return rpcError(invalid("action completed without native readiness"))
		}
		return nil
	}
	return relayAncillaryEvents(ctx, cancel, events, done, send, finish, "action")
}

func (c *Client) openStream(ctx context.Context, index int, method string, request any) (grpc.ClientStream, error) {
	ctx, err := outgoingSession(ctx)
	if err != nil {
		return nil, err
	}
	s, err := c.conn.NewStream(
		ctx,
		&resourcepb.ResourceAncillary_ServiceDesc.Streams[index],
		method,
		grpc.MaxCallRecvMsgSize(MaxWireBytes),
		grpc.MaxCallSendMsgSize(MaxWireBytes),
	)
	if err != nil {
		return nil, domainError(err)
	}
	if err = s.SendMsg(request); err != nil {
		return nil, domainError(err)
	}
	if err = s.CloseSend(); err != nil {
		return nil, domainError(err)
	}
	return s, nil
}

// WatchConnections forwards connection snapshots until completion or cancellation without closing out.
func (c *Client) WatchConnections(ctx context.Context, out chan<- []types.Connection) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s, err := c.openStream(
		ctx,
		0,
		resourcepb.ResourceAncillary_WatchConnections_FullMethodName,
		&resourcepb.AncillaryRequest{Version: resourcev2.WireVersion, Operation: "watchConnections"},
	)
	if err != nil {
		return err
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
		var v []types.Connection
		if err = decodeConnection(p.ConnectionsJson, &v); err != nil {
			return err
		}
		select {
		case out <- v:
		case <-ctx.Done():
			return domainError(ctx.Err())
		}
	}
}

// StreamAction forwards action events after exactly one native readiness event and leaves out open.
func (c *Client) StreamAction(
	ctx context.Context,
	in resourcev2.ActionRequest,
	out chan<- resourcev2.ActionEvent,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, err := resourcev2.AdmitStreamingAction(ctx, c, in); err != nil {
		return err
	}
	p, err := resourcev2.ToProtoActionRequest(in)
	if err != nil {
		return err
	}
	s, err := c.openStream(
		ctx,
		1,
		resourcepb.ResourceAncillary_StreamAction_FullMethodName,
		&resourcepb.ActionStreamRequest{Version: resourcev2.WireVersion, Action: p},
	)
	if err != nil {
		return err
	}
	ready := false
	for {
		p := &resourcepb.ActionStreamEvent{}
		e := &envelope{message: p}
		if err = s.RecvMsg(e); err != nil {
			if errors.Is(err, io.EOF) {
				if !ready {
					return invalid("action completed without native readiness")
				}
				return nil
			}
			return domainError(err)
		}
		if e.err != nil {
			return e.err
		}
		v, decodeErr := decodeActionEvent(p, &ready)
		if decodeErr != nil {
			return decodeErr
		}
		select {
		case out <- v:
		case <-ctx.Done():
			return domainError(ctx.Err())
		}
	}
}

func (s *ancillaryServer) sendActionEvent(
	ctx context.Context,
	stream grpc.ServerStream,
	route *resourcepb.AccessRoute,
	ready *bool,
	v resourcev2.ActionEvent,
) error {
	if err := s.owner.checkCatalogRoute(ctx, route); err != nil {
		return err
	}
	if v.Type == "ready" {
		if *ready {
			return invalid("duplicate action readiness")
		}
		if v.Error != nil {
			return invalid("ready event cannot carry error")
		}
		*ready = true
	} else if !*ready {
		return invalid("action event before native readiness")
	}
	out := &resourcepb.ActionStreamEvent{Version: resourcev2.WireVersion, Type: v.Type}
	if v.Type == "" {
		return invalid("missing action event type")
	}
	if v.Error != nil {
		out.ErrorCode = resourcev2.ErrorCode(v.Error)
	}
	if (v.Type == "progress" || v.Type == "output") && v.Data == nil {
		return invalid("action data required")
	}
	if v.Data != nil {
		p, err := resourcev2.ToProtoValue(*v.Data)
		if err != nil {
			return err
		}
		out.Data = p
	}
	return sendResource(stream, out)
}

// relayAncillaryEvents cancels and joins the producer on every exit, draining queued events after completion.
func relayAncillaryEvents[T any](
	ctx context.Context,
	cancel context.CancelFunc,
	events <-chan T,
	done <-chan error,
	send func(T) error,
	finish func(error) error,
	channel string,
) error {
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done //nolint:errcheck // Join after cancellation; the selected stream failure already owns the result.
		}
	}()
	deliver := func(v T, ok bool) error {
		if !ok {
			return rpcError(invalid("provider closed caller-owned " + channel + " channel"))
		}
		return rpcError(send(v))
	}
	for {
		select {
		case <-ctx.Done():
			return rpcError(ctx.Err())
		case v, ok := <-events:
			if err := deliver(v, ok); err != nil {
				return err
			}
		case err := <-done:
			joined = true
			return drainAncillaryEvents(events, deliver, finish, err)
		}
	}
}

func drainAncillaryEvents[T any](
	events <-chan T,
	deliver func(T, bool) error,
	finish func(error) error,
	producerErr error,
) error {
	for {
		select {
		case v, ok := <-events:
			if err := deliver(v, ok); err != nil {
				return err
			}
		default:
			return finish(producerErr)
		}
	}
}

func decodeActionEvent(p *resourcepb.ActionStreamEvent, ready *bool) (resourcev2.ActionEvent, error) {
	if p.Version != resourcev2.WireVersion {
		return resourcev2.ActionEvent{}, unsupported("unsupported stream response version")
	}
	if p.Type == "ready" {
		if *ready || p.ErrorCode != "" {
			return resourcev2.ActionEvent{}, invalid("invalid action readiness")
		}
		*ready = true
	} else if !*ready {
		return resourcev2.ActionEvent{}, invalid("action event before native readiness")
	}
	if p.Type == "" {
		return resourcev2.ActionEvent{}, invalid("missing action event type")
	}
	if (p.Type == "progress" || p.Type == "output") && p.Data == nil {
		return resourcev2.ActionEvent{}, invalid("action data required")
	}
	v := resourcev2.ActionEvent{Type: p.Type}
	if p.Data != nil {
		data, err := resourcev2.FromProtoValue(p.Data)
		if err != nil {
			return resourcev2.ActionEvent{}, err
		}
		v.Data = &data
	}
	if p.ErrorCode != "" && !resourcev2.IsErrorCode(p.ErrorCode) {
		return resourcev2.ActionEvent{}, resourcev2.NewError("INTERNAL", "invalid action error classification")
	}
	if p.ErrorCode != "" {
		v.Error = &resourcev2.ResourceOperationError{Code: p.ErrorCode, Message: "action operation failed"}
	}
	return v, nil
}
