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

type updateDelivery struct {
	message *resourcepb.UpdateStreamEvent
	err     error
	ack     chan error
}

type updateSink struct {
	ctx    context.Context
	mu     sync.Mutex
	ready  bool
	events chan updateDelivery
}

func (s *updateSink) push(message *resourcepb.UpdateStreamEvent, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		err = invalid("update event before listener readiness")
	}
	select {
	case s.events <- updateDelivery{message: message, err: err}:
	case <-s.ctx.Done():
	}
}

func (s *updateSink) OnAdd(v resourcev2.WatchAddPayload) {
	p, err := resourcev2.ToProtoWatchAddPayload(v)
	s.push(&resourcepb.UpdateStreamEvent{
		Version: resourcev2.WireVersion, Event: &resourcepb.UpdateStreamEvent_Add{Add: p},
	}, err)
}

func (s *updateSink) OnUpdate(v resourcev2.WatchUpdatePayload) {
	p, err := resourcev2.ToProtoWatchUpdatePayload(v)
	s.push(&resourcepb.UpdateStreamEvent{
		Version: resourcev2.WireVersion, Event: &resourcepb.UpdateStreamEvent_Update{Update: p},
	}, err)
}

func (s *updateSink) OnDelete(v resourcev2.WatchDeletePayload) {
	p, err := resourcev2.ToProtoWatchDeletePayload(v)
	s.push(&resourcepb.UpdateStreamEvent{
		Version: resourcev2.WireVersion, Event: &resourcepb.UpdateStreamEvent_Delete{Delete: p},
	}, err)
}

func (s *updateSink) OnStateChange(v resourcev2.WatchStateEvent) {
	p, err := resourcev2.ToProtoWatchStateEvent(v)
	s.push(&resourcepb.UpdateStreamEvent{
		Version: resourcev2.WireVersion, Event: &resourcepb.UpdateStreamEvent_State{State: p},
	}, err)
}

func (s *updateSink) established() error {
	s.mu.Lock()
	if s.ready {
		err := invalid("duplicate update listener readiness")
		select {
		case s.events <- updateDelivery{err: err}:
		case <-s.ctx.Done():
		}
		s.mu.Unlock()
		return err
	}
	s.ready = true
	ack := make(chan error, 1)
	select {
	case s.events <- updateDelivery{
		message: &resourcepb.UpdateStreamEvent{
			Version: resourcev2.WireVersion, Event: &resourcepb.UpdateStreamEvent_Ready{Ready: true},
		},
		ack: ack,
	}:
	case <-s.ctx.Done():
		s.mu.Unlock()
		return s.ctx.Err()
	}
	s.mu.Unlock()
	select {
	case err := <-ack:
		return err
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func listenUpdatesHandler(srv any, stream grpc.ServerStream) error {
	s, ok := srv.(*updatesServer)
	if !ok {
		return rpcError(invalid("invalid update server"))
	}
	r := &resourcepb.UpdateRequest{}
	e := &envelope{message: r}
	if err := stream.RecvMsg(e); err != nil {
		return rpcError(invalid("malformed update listener request"))
	}
	if e.err != nil {
		return rpcError(e.err)
	}
	if r.Version != resourcev2.WireVersion {
		return rpcError(unsupported("unsupported update stream version"))
	}
	if r.Operation != updateListen {
		return rpcError(invalid("unexpected update listener operation"))
	}
	base, err := incomingSession(stream.Context(), s.owner.settings)
	if err != nil {
		return rpcError(err)
	}
	p, ok := s.owner.impl.(resourcev2.BestEffortUpdateProvider)
	if !ok {
		return rpcError(unsupported("best-effort updates unsupported"))
	}
	ctx, cancel := context.WithCancel(base)
	sink := &updateSink{ctx: ctx, events: make(chan updateDelivery, 1)}
	done := make(chan error, 1)
	go func() { done <- p.ListenForEvents(ctx, sink, sink.established) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done //nolint:errcheck // Join after cancellation; the selected stream failure already owns the result.
		}
	}()
	return s.forwardUpdates(ctx, stream, sink.events, done, &joined)
}

// updateSender serializes readiness and acknowledges its actual transport send.
type updateSender struct {
	stream grpc.ServerStream
	ready  bool
}

func (s *updateSender) send(d updateDelivery) error {
	if d.err != nil {
		return d.err
	}
	if d.message.GetReady() {
		if s.ready {
			return invalid("duplicate update listener readiness")
		}
		s.ready = true
	}
	err := sendResource(s.stream, d.message)
	if d.ack != nil {
		d.ack <- err
	}
	return err
}

func (s *updateSender) drain(events <-chan updateDelivery) error {
	for {
		select {
		case delivery := <-events:
			if err := s.send(delivery); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (s *updatesServer) forwardUpdates(
	ctx context.Context,
	stream grpc.ServerStream,
	events <-chan updateDelivery,
	done <-chan error,
	joined *bool,
) error {
	sender := updateSender{stream: stream}
	for {
		select {
		case <-ctx.Done():
			return rpcError(ctx.Err())
		case delivery := <-events:
			if err := sender.send(delivery); err != nil {
				return rpcError(err)
			}
		case err := <-done:
			*joined = true
			if drainErr := sender.drain(events); drainErr != nil {
				return rpcError(drainErr)
			}
			if err != nil {
				return rpcError(s.owner.classify(err))
			}
			if !sender.ready {
				return rpcError(invalid("listener ended without registration readiness"))
			}
			return nil
		}
	}
}

// ListenForEvents delivers advisory updates after remote registration readiness.
// Cancellation ends the listener; onReady must succeed before any event is delivered.
func (c *Client) ListenForEvents(ctx context.Context, sink resourcev2.WatchEventSink, onReady func() error) error {
	var err error
	ctx, err = outgoingSession(ctx)
	if err != nil {
		return err
	}
	if sink == nil || onReady == nil {
		return invalid("listener sink and readiness callback required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.conn.NewStream(
		ctx,
		&resourcepb.ResourceUpdates_ServiceDesc.Streams[0],
		resourcepb.ResourceUpdates_Listen_FullMethodName,
		grpc.MaxCallRecvMsgSize(MaxWireBytes),
		grpc.MaxCallSendMsgSize(MaxWireBytes),
	)
	if err != nil {
		return domainError(err)
	}
	request := &resourcepb.UpdateRequest{Version: resourcev2.WireVersion, Operation: updateListen}
	if err = stream.SendMsg(request); err != nil {
		return domainError(err)
	}
	if err = stream.CloseSend(); err != nil {
		return domainError(err)
	}
	return receiveUpdates(stream, sink, onReady)
}

func receiveUpdates(stream grpc.ClientStream, sink resourcev2.WatchEventSink, onReady func() error) error {
	ready := false
	for {
		message, frame, err := receiveUpdate(stream)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if !ready {
					return invalid("listener ended without registration readiness")
				}
				return nil
			}
			return domainError(err)
		}
		if frame.err != nil {
			return frame.err
		}
		if err = acceptUpdate(message, &ready, sink, onReady); err != nil {
			return err
		}
	}
}

func acceptUpdate(
	message *resourcepb.UpdateStreamEvent,
	ready *bool,
	sink resourcev2.WatchEventSink,
	onReady func() error,
) error {
	if message.Version != resourcev2.WireVersion {
		return unsupported("unsupported update stream version")
	}
	if registration, ok := message.Event.(*resourcepb.UpdateStreamEvent_Ready); ok {
		if *ready || !registration.Ready {
			return invalid("invalid listener readiness")
		}
		*ready = true
		return onReady()
	}
	if !*ready {
		return invalid("update event before listener readiness")
	}
	return deliverUpdate(sink, message)
}

func receiveUpdate(stream grpc.ClientStream) (*resourcepb.UpdateStreamEvent, *envelope, error) {
	message := &resourcepb.UpdateStreamEvent{}
	frame := &envelope{message: message}
	err := stream.RecvMsg(frame)
	return message, frame, err
}

func deliverUpdate(sink resourcev2.WatchEventSink, message *resourcepb.UpdateStreamEvent) error {
	switch event := message.Event.(type) {
	case *resourcepb.UpdateStreamEvent_Add:
		value, err := resourcev2.FromProtoWatchAddPayload(event.Add)
		if err != nil {
			return err
		}
		sink.OnAdd(value)
	case *resourcepb.UpdateStreamEvent_Update:
		value, err := resourcev2.FromProtoWatchUpdatePayload(event.Update)
		if err != nil {
			return err
		}
		sink.OnUpdate(value)
	case *resourcepb.UpdateStreamEvent_Delete:
		value, err := resourcev2.FromProtoWatchDeletePayload(event.Delete)
		if err != nil {
			return err
		}
		sink.OnDelete(value)
	case *resourcepb.UpdateStreamEvent_State:
		value, err := resourcev2.FromProtoWatchStateEvent(event.State)
		if err != nil {
			return err
		}
		sink.OnStateChange(value)
	default:
		return invalid("missing update event")
	}
	return nil
}
