package plugin

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

var sourceSendStallTimeout = 30 * time.Second

type sourceServer struct {
	resourcepb.UnimplementedResourceSourcesServer
	owner *server
}

type sourceDelivery struct {
	frame *resourcepb.SourceFrame
	ack   chan error
}

type sourceEmitter struct {
	mu              sync.Mutex
	ctx             context.Context
	request         resourcev2.SourceOpenRequest
	tracker         *resourcev2.SourcePhaseTracker
	queue           chan sourceDelivery
	stalled         chan struct{}
	sequence        uint64
	ended, terminal bool
}

func sourceAdmission(ctx context.Context, p resourcev2.Provider, r resourcev2.SourceOpenRequest) (resourcev2.ResourceType, error) {
	catalog, err := p.Catalog(ctx, resourcev2.CatalogRequest{Route: r.Target.Route})
	if err != nil {
		return resourcev2.ResourceType{}, err
	}
	for _, typ := range catalog.Types {
		if typ.Type == r.Target.Type {
			return typ, nil
		}
	}
	return resourcev2.ResourceType{}, nil
}

func sourceGuarantee(d resourcev2.ResourceType) string {
	for _, c := range d.Capabilities {
		if c.Operation == resourcev2.OperationSourceUpdates {
			return c.Guarantee
		}
	}
	return ""
}

func registerSources(s *grpc.Server, owner *server) {
	desc := resourcepb.ResourceSources_ServiceDesc
	desc.Streams = append([]grpc.StreamDesc(nil), desc.Streams...)
	desc.Streams[0].Handler = sourceOpenHandler
	s.RegisterService(&desc, &sourceServer{owner: owner})
}

func sourceOpenHandler(srv any, stream grpc.ServerStream) error {
	s, ok := srv.(*sourceServer)
	if !ok {
		return status.Error(codes.Internal, "resource source server unavailable")
	}
	p := &resourcepb.SourceOpenRequest{}
	env := &envelope{message: p}
	if err := stream.RecvMsg(env); err != nil {
		return err
	}
	if env.err != nil {
		return rpcError(env.err)
	}
	if p.GetVersion() != resourcev2.WireVersion {
		return rpcError(unsupported("unsupported source wire version"))
	}
	r, err := resourcev2.FromProtoSourceOpenRequest(p)
	if err != nil {
		return rpcError(err)
	}
	ctx, err := incomingSession(stream.Context(), s.owner.settings)
	if err != nil {
		return rpcError(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan sourceDelivery, 1)
	stalled := make(chan struct{})
	e := &sourceEmitter{ctx: ctx, request: r, queue: queue, stalled: stalled}
	done := make(chan struct{})
	go func() {
		defer close(done)
		descriptor, err := sourceAdmission(ctx, s.owner.impl, r)
		if err == nil {
			err = s.owner.checkCatalogRoute(ctx, p.GetTarget().GetRoute())
		}
		e.tracker = resourcev2.NewSourcePhaseTracker(r, sourceGuarantee(descriptor))
		if err == nil {
			provider, ok := s.owner.impl.(resourcev2.SourceProvider)
			if !ok {
				err = unsupported("source sessions unsupported")
			} else {
				err = provider.OpenSource(ctx, r, e)
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			_ = e.failure(ctx, s.owner.classify(err))
			return
		}
		e.mu.Lock()
		ended, terminal := e.ended, e.terminal
		e.mu.Unlock()
		if terminal {
			return
		}
		if !ended {
			_ = e.failure(ctx, resourcev2.NewError("INTERNAL", "source ended before snapshot boundary"))
			return
		}
		_ = e.Reset(ctx, resourcev2.SourceResetSourceInvalidated, "source ended")
	}()
	for {
		select {
		case d := <-queue:
			sent := make(chan error, 1)
			go func() { sent <- stream.SendMsg(d.frame) }()
			timer := time.NewTimer(sourceSendStallTimeout)
			select {
			case err := <-sent:
				if !timer.Stop() {
					<-timer.C
				}
				d.ack <- err
				if err != nil {
					cancel()
					return err
				}
			case <-timer.C:
				close(stalled)
				cancel()
				d.ack <- limited("source host stalled")
				// gRPC forbids returning while SendMsg is in flight.
				return <-sent
			case <-stream.Context().Done():
				if !timer.Stop() {
					<-timer.C
				}
				cancel()
				d.ack <- stream.Context().Err()
				return <-sent
			}
		case <-done:
			return nil
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (e *sourceEmitter) send(ctx context.Context, f resourcev2.SourceFrame) error {
	if err := e.ctx.Err(); err != nil {
		select {
		case <-e.stalled:
			return limited("source host stalled")
		default:
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.SessionID, f.Generation, f.Sequence = e.request.SessionID, e.request.Generation, e.sequence+1
	p, err := resourcev2.ToProtoSourceFrame(f)
	if err != nil {
		return err
	}
	if proto.Size(p) > int(e.request.Limits.MaxFrameBytes) {
		return limited("source frame exceeds limit")
	}
	if err = e.tracker.Admit(f); err != nil {
		return err
	}
	e.sequence++
	d := sourceDelivery{frame: p, ack: make(chan error, 1)}
	select {
	case e.queue <- d:
	case <-ctx.Done():
		if e.ctx.Err() != nil {
			return e.contextError()
		}
		return ctx.Err()
	case <-e.ctx.Done():
		return e.contextError()
	}
	select {
	case err = <-d.ack:
		return err
	case <-ctx.Done():
		if e.ctx.Err() != nil {
			return e.contextError()
		}
		return ctx.Err()
	case <-e.ctx.Done():
		return e.contextError()
	}
}
func (e *sourceEmitter) contextError() error {
	select {
	case <-e.stalled:
		return limited("source host stalled")
	default:
		return e.ctx.Err()
	}
}
func (e *sourceEmitter) begin(ctx context.Context) error {
	if e.sequence != 0 {
		return nil
	}
	return e.send(ctx, resourcev2.SourceFrame{Begin: &resourcev2.SourceBegin{Mode: e.request.Mode}})
}
func (e *sourceEmitter) emit(ctx context.Context, f resourcev2.SourceFrame) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.begin(ctx); err != nil {
		return err
	}
	return e.send(ctx, f)
}
func (e *sourceEmitter) Snapshot(ctx context.Context, observations []resourcev2.SourceObservation) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.ctx.Err(); err != nil {
		return e.contextError()
	}
	if e.ended || e.terminal {
		return resourcev2.NewError("PROVIDER_ERROR", "snapshot is already complete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.begin(ctx); err != nil {
		return err
	}
	for len(observations) > 0 {
		n := 0
		for n < len(observations) && n < int(e.request.Limits.MaxBatchItems) {
			f := resourcev2.SourceFrame{SessionID: e.request.SessionID, Generation: e.request.Generation, Sequence: e.sequence + 1, Snapshot: &resourcev2.SourceSnapshot{Observations: observations[:n+1]}}
			p, err := resourcev2.ToProtoSourceFrame(f)
			if err != nil {
				return err
			}
			if proto.Size(p) > int(e.request.Limits.MaxFrameBytes) {
				break
			}
			n++
		}
		if n == 0 {
			return limited("source observation exceeds frame limit")
		}
		if err := e.send(ctx, resourcev2.SourceFrame{Snapshot: &resourcev2.SourceSnapshot{Observations: observations[:n]}}); err != nil {
			return err
		}
		observations = observations[n:]
	}
	return nil
}
func (e *sourceEmitter) EndSnapshot(ctx context.Context, coverage resourcev2.SourceCoverage, continuity string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.begin(ctx); err != nil {
		return err
	}
	err := e.send(ctx, resourcev2.SourceFrame{SnapshotEnd: &resourcev2.SourceSnapshotEnd{Coverage: coverage, Continuity: continuity}})
	if err == nil {
		e.ended = true
		e.terminal = e.request.Mode == resourcev2.SourceModeSnapshot
	}
	return err
}
func (e *sourceEmitter) Changes(ctx context.Context, changes []resourcev2.SourceChange, coverage *resourcev2.SourceCoverage) error {
	if err := e.ctx.Err(); err != nil {
		return e.contextError()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(changes) > int(e.request.Limits.MaxBatchItems) {
		return limited("source changes exceed batch limit")
	}
	return e.emit(ctx, resourcev2.SourceFrame{Changes: &resourcev2.SourceChanges{Changes: changes, Coverage: coverage}})
}
func (e *sourceEmitter) Reset(ctx context.Context, reason, message string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.begin(ctx); err != nil {
		return err
	}
	err := e.send(ctx, resourcev2.SourceFrame{Reset: &resourcev2.SourceReset{Reason: reason, Message: message}})
	if err == nil {
		e.terminal = true
	}
	return err
}
func (e *sourceEmitter) failure(ctx context.Context, err error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.terminal {
		return nil
	}
	code := resourcev2.ErrorCode(err)
	message := "source operation failed"
	var typed *resourcev2.ResourceOperationError
	if errors.As(err, &typed) && code == "INTERNAL" && typed.Message == "source ended before snapshot boundary" {
		message = typed.Message
	}
	sendErr := e.send(ctx, resourcev2.SourceFrame{Failed: &resourcev2.SourceFailure{Code: code, Message: message}})
	if sendErr == nil {
		e.terminal = true
	}
	return sendErr
}
