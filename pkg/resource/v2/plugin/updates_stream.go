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
	s.push(&resourcepb.UpdateStreamEvent{Version: 2, Event: &resourcepb.UpdateStreamEvent_Add{Add: p}}, err)
}

func (s *updateSink) OnUpdate(v resourcev2.WatchUpdatePayload) {
	p, err := resourcev2.ToProtoWatchUpdatePayload(v)
	s.push(&resourcepb.UpdateStreamEvent{Version: 2, Event: &resourcepb.UpdateStreamEvent_Update{Update: p}}, err)
}

func (s *updateSink) OnDelete(v resourcev2.WatchDeletePayload) {
	p, err := resourcev2.ToProtoWatchDeletePayload(v)
	s.push(&resourcepb.UpdateStreamEvent{Version: 2, Event: &resourcepb.UpdateStreamEvent_Delete{Delete: p}}, err)
}

func (s *updateSink) OnStateChange(v resourcev2.WatchStateEvent) {
	p, err := resourcev2.ToProtoWatchStateEvent(v)
	s.push(&resourcepb.UpdateStreamEvent{Version: 2, Event: &resourcepb.UpdateStreamEvent_State{State: p}}, err)
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
	case s.events <- updateDelivery{message: &resourcepb.UpdateStreamEvent{Version: 2, Event: &resourcepb.UpdateStreamEvent_Ready{Ready: true}}, ack: ack}:
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
	if r.Version != 2 {
		return rpcError(unsupported("unsupported update stream version"))
	}
	if r.Operation != "listen" {
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
			<-done
		}
	}()
	ready := false
	send := func(d updateDelivery) error {
		if d.err != nil {
			return d.err
		}
		if d.message.GetReady() {
			if ready {
				return invalid("duplicate update listener readiness")
			}
			ready = true
		}
		err := sendResource(stream, d.message)
		if d.ack != nil {
			d.ack <- err
		}
		return err
	}
	finish := func(err error) error {
		if err != nil {
			return rpcError(s.owner.classify(err))
		}
		if !ready {
			return rpcError(invalid("listener ended without registration readiness"))
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return rpcError(ctx.Err())
		case d := <-sink.events:
			if err := send(d); err != nil {
				return rpcError(err)
			}
		case err := <-done:
			joined = true
			for {
				select {
				case d := <-sink.events:
					if e := send(d); e != nil {
						return rpcError(e)
					}
				default:
					return finish(err)
				}
			}
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
	if err = stream.SendMsg(&resourcepb.UpdateRequest{Version: 2, Operation: "listen"}); err != nil {
		return domainError(err)
	}
	if err = stream.CloseSend(); err != nil {
		return domainError(err)
	}
	ready := false
	for {
		p := &resourcepb.UpdateStreamEvent{}
		e := &envelope{message: p}
		if err = stream.RecvMsg(e); err != nil {
			if errors.Is(err, io.EOF) {
				if !ready {
					return invalid("listener ended without registration readiness")
				}
				return nil
			}
			return domainError(err)
		}
		if e.err != nil {
			return e.err
		}
		if p.Version != 2 {
			return unsupported("unsupported update stream version")
		}
		if r, ok := p.Event.(*resourcepb.UpdateStreamEvent_Ready); ok {
			if ready || !r.Ready {
				return invalid("invalid listener readiness")
			}
			ready = true
			if err = onReady(); err != nil {
				return err
			}
			continue
		}
		if !ready {
			return invalid("update event before listener readiness")
		}
		switch event := p.Event.(type) {
		case *resourcepb.UpdateStreamEvent_Add:
			v, err := resourcev2.FromProtoWatchAddPayload(event.Add)
			if err != nil {
				return err
			}
			sink.OnAdd(v)
		case *resourcepb.UpdateStreamEvent_Update:
			v, err := resourcev2.FromProtoWatchUpdatePayload(event.Update)
			if err != nil {
				return err
			}
			sink.OnUpdate(v)
		case *resourcepb.UpdateStreamEvent_Delete:
			v, err := resourcev2.FromProtoWatchDeletePayload(event.Delete)
			if err != nil {
				return err
			}
			sink.OnDelete(v)
		case *resourcepb.UpdateStreamEvent_State:
			v, err := resourcev2.FromProtoWatchStateEvent(event.State)
			if err != nil {
				return err
			}
			sink.OnStateChange(v)
		default:
			return invalid("missing update event")
		}
	}
}
