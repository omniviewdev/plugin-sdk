package plugin

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

var sourceSendStallTimeout = 5 * time.Second

type sourceServer struct {
	resourcepb.UnimplementedResourceSourcesServer
	owner *server
}

type sourceDelivery struct {
	frame    *resourcepb.SourceFrame
	ack      chan error
	terminal bool
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
	features, err := resourcev2.DetectFeatures(ctx, p)
	if err != nil {
		return resourcev2.ResourceType{}, err
	}
	if !features.Has(resourcev2.FeatureSourceSessions) {
		return resourcev2.ResourceType{}, unsupported("source sessions unsupported")
	}
	catalog, err := p.Catalog(ctx, resourcev2.CatalogRequest{Route: r.Target.Route})
	if err != nil {
		return resourcev2.ResourceType{}, err
	}
	var acquisition any = resourcev2.EnumerateRequest{Target: r.Target}
	var native *resourcev2.NativeQueryRequest
	if r.Acquisition.Kind == resourcev2.SourceAcquisitionNativeQuery {
		native = &resourcev2.NativeQueryRequest{Target: r.Target, Language: r.Acquisition.Language, Query: *r.Acquisition.Query}
		acquisition = *native
	}
	typ, err := resourcev2.AdmitCatalogOperation(catalog, acquisition)
	if err != nil {
		return resourcev2.ResourceType{}, err
	}
	if native != nil {
		if err := resourcev2.ValidateNativeQueryInput(typ, *native); err != nil {
			return resourcev2.ResourceType{}, err
		}
	}
	if r.Mode == resourcev2.SourceModeSnapshotAndUpdates {
		typ, err = resourcev2.AdmitCatalogOperation(catalog, r.Target)
		if err != nil {
			return resourcev2.ResourceType{}, err
		}
	}
	for _, candidate := range typ.Representations {
		if candidate.ID == r.Target.Representation && reflect.DeepEqual(candidate, r.Binding.Representation) {
			return typ, nil
		}
	}
	return resourcev2.ResourceType{}, resourcev2.NewError("RESET_REQUIRED", "source binding changed")
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
	done := make(chan error, 1)
	go func() {
		defer close(done)
		descriptor, err := sourceAdmission(ctx, s.owner.impl, r)
		if err == nil {
			err = s.owner.checkCatalogRoute(ctx, p.GetTarget().GetRoute())
		}
		e.tracker = resourcev2.NewCatalogBoundSourcePhaseTracker(r, descriptor)
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
		if err == nil {
			e.mu.Lock()
			ended, terminal := e.ended, e.terminal
			e.mu.Unlock()
			if terminal {
				return
			}
			if ended {
				_ = e.Reset(ctx, resourcev2.SourceResetSourceInvalidated, "source ended")
				return
			}
			err = resourcev2.NewError("INTERNAL", "source ended before snapshot boundary")
		} else {
			err = s.owner.classify(err)
		}
		e.mu.Lock()
		begun := e.sequence != 0
		e.mu.Unlock()
		if !begun {
			done <- rpcError(err)
			return
		}
		_ = e.failure(ctx, err)
	}()
	frames := make(chan *resourcepb.SourceFrame)
	sent := make(chan error, 1)
	go func() {
		for {
			select {
			case frame := <-frames:
				sent <- stream.SendMsg(frame)
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case d := <-queue:
			select {
			case frames <- d.frame:
			case <-ctx.Done():
				return ctx.Err()
			}
			timer := time.NewTimer(sourceSendStallTimeout)
			select {
			case err := <-sent:
				if !timer.Stop() {
					<-timer.C
				}
				d.ack <- err
				if err == nil && d.terminal {
					cancel()
					return nil
				}
				if err != nil {
					cancel()
					return err
				}
			case <-timer.C:
				close(stalled)
				cancel()
				d.ack <- limited("source host stalled")
				// Returning lets gRPC terminate the RPC and unblock SendMsg.
				return rpcError(limited("source host stalled"))
			case <-stream.Context().Done():
				if !timer.Stop() {
					<-timer.C
				}
				cancel()
				d.ack <- stream.Context().Err()
				return stream.Context().Err()
			}
		case err := <-done:
			return err
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
	if f.Changes != nil {
		size, err := resourcev2.SourceChangesWireSize(*f.Changes)
		if err != nil {
			return resourcev2.NewError("PROVIDER_ERROR", "invalid source frame")
		}
		header := &resourcepb.SourceFrame{Version: resourcev2.WireVersion, SessionId: f.SessionID, Generation: f.Generation, Sequence: f.Sequence}
		if proto.Size(header)+protowire.SizeTag(8)+protowire.SizeBytes(size) > int(e.request.Limits.MaxFrameBytes) {
			return limited("source changes exceed frame limit")
		}
	}
	p, err := resourcev2.ToProtoSourceFrame(f)
	if err != nil {
		return resourcev2.NewError("PROVIDER_ERROR", "invalid source frame")
	}
	if f.Changes != nil {
		nodes, fields := protoWireCost(p.ProtoReflect())
		if nodes > MaxNodes || fields > MaxFields {
			return limited("source changes exceed frame limit")
		}
	}
	return e.sendEncoded(f, p)
}

func (e *sourceEmitter) sendEncoded(f resourcev2.SourceFrame, p *resourcepb.SourceFrame) error {
	if err := e.tracker.AdmitSized(f, proto.Size(p)); err != nil {
		return err
	}
	e.sequence++
	d := sourceDelivery{frame: p, ack: make(chan error, 1), terminal: f.Reset != nil || f.Failed != nil || f.SnapshotEnd != nil && e.request.Mode == resourcev2.SourceModeSnapshot}
	select {
	case e.queue <- d:
	case <-e.ctx.Done():
		return e.contextError()
	}
	select {
	case err := <-d.ack:
		return err
	case <-e.ctx.Done():
		select {
		case err := <-d.ack:
			return err
		default:
			return e.contextError()
		}
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.ctx.Err(); err != nil {
			return e.contextError()
		}
		f := resourcev2.SourceFrame{SessionID: e.request.SessionID, Generation: e.request.Generation, Sequence: e.sequence + 1}
		p := &resourcepb.SourceFrame{Version: resourcev2.WireVersion, SessionId: f.SessionID, Generation: f.Generation, Sequence: f.Sequence}
		headerSize := proto.Size(p)
		nodes, fields := protoWireCost(p.ProtoReflect())
		nodes++  // snapshot payload
		fields++ // snapshot payload field
		snapshot := &resourcepb.SourceSnapshot{}
		payloadSize := 0
		for len(snapshot.Observations) < len(observations) && len(snapshot.Observations) < int(e.request.Limits.MaxBatchItems) {
			itemSize, err := resourcev2.SourceObservationWireSize(observations[len(snapshot.Observations)])
			if err != nil {
				return resourcev2.NewError("PROVIDER_ERROR", "invalid source observation")
			}
			nextSize := payloadSize + protowire.SizeTag(1) + protowire.SizeBytes(itemSize)
			if headerSize+protowire.SizeTag(6)+protowire.SizeBytes(nextSize) > int(e.request.Limits.MaxFrameBytes) {
				break
			}
			observation, err := resourcev2.ToProtoSourceObservation(observations[len(snapshot.Observations)])
			if err != nil {
				return resourcev2.NewError("PROVIDER_ERROR", "invalid source observation")
			}
			itemNodes, itemFields := protoWireCost(observation.ProtoReflect())
			if nodes+itemNodes > MaxNodes || fields+1+itemFields > MaxFields {
				break
			}
			nodes += itemNodes
			fields += 1 + itemFields
			snapshot.Observations = append(snapshot.Observations, observation)
			payloadSize = nextSize
		}
		n := len(snapshot.Observations)
		if n == 0 {
			return limited("source observation exceeds frame limit")
		}
		f.Snapshot = &resourcev2.SourceSnapshot{Observations: observations[:n]}
		p.Payload = &resourcepb.SourceFrame_Snapshot{Snapshot: snapshot}
		if err := e.sendEncoded(f, p); err != nil {
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
