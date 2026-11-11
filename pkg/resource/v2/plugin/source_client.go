package plugin

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// IsFatalSourceTransportError reports a connection-level physical framing failure.
// The host must retire the affected provider transport rather than treat it as
// an ordinary per-RPC source reset.
func IsFatalSourceTransportError(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == grpc.ReceivePolicyErrorDomain && info.Reason == grpc.PartialHTTP2DataFrameTimeoutReason {
			return true
		}
	}
	return false
}

var sourceBeginTimeout = 30 * time.Second

const defaultSourceSnapshotProgressTimeout = 5 * time.Minute

type sourceSnapshotProgressTimeoutKey struct{}

// WithSourceSnapshotProgressTimeout configures the idle progress deadline for
// snapshot acquisition. It does not limit following or connection acquisition.
func WithSourceSnapshotProgressTimeout(ctx context.Context, timeout time.Duration) (context.Context, error) {
	if timeout <= 0 {
		return nil, resourcev2.NewError("INVALID_REQUEST", "source snapshot progress timeout must be positive")
	}
	return context.WithValue(ctx, sourceSnapshotProgressTimeoutKey{}, timeout), nil
}

type sourceDeadline struct {
	mu     sync.Mutex
	timer  *time.Timer
	epoch  uint64
	closed bool
	cancel context.CancelCauseFunc
}

func (d *sourceDeadline) arm(duration time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.epoch++
	epoch := d.epoch
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(duration, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.epoch == epoch {
			d.closed = true
			d.cancel(context.DeadlineExceeded)
		}
	})
}

func (d *sourceDeadline) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	d.epoch++
	if d.timer != nil {
		d.timer.Stop()
	}
}

type sourceClientStream struct {
	stream           grpc.ClientStream
	cancel           context.CancelFunc
	ctx              context.Context
	maxBatchItems    uint32
	deadline         *sourceDeadline
	snapshotTimeout  time.Duration
	tracker          *resourcev2.SourcePhaseTracker
	client           *Client
	request          resourcev2.SourceOpenRequest
	descriptor       resourcev2.ResourceType
	nextCatalogCheck time.Time
	admitted         bool
	once             sync.Once
}

// sourceBeginTimedOut logs the begin-handshake timeout and returns its domain error.
func sourceBeginTimedOut(ctx context.Context, sourceID string) error {
	trace.SpanFromContext(ctx).AddEvent("source.begin_timeout")
	logging.Default().Warnw(ctx, "source begin timed out", "source_id", sourceID, "timeout_ms", sourceBeginTimeout.Milliseconds(), "error_code", "TIMEOUT")
	return resourcev2.NewError("TIMEOUT", "source begin timed out")
}

// sourceSessionTimeout logs the snapshot-progress timeout and returns its domain error.
func sourceSessionTimeout(ctx context.Context, timeout time.Duration) error {
	logging.Default().Warnw(ctx, "source session timed out", "timeout_ms", timeout.Milliseconds(), "error_code", "TIMEOUT")
	return resourcev2.NewError("TIMEOUT", "source session timed out")
}

// sourceFrameInvalid logs a rejected provider frame with a closed reason; frame content is never logged.
func sourceFrameInvalid(ctx context.Context, reason string) {
	logging.Default().Warnw(ctx, "source frame invalid", "reason", reason, "error_code", "PROVIDER_ERROR")
}

// OpenSourceStream opens a bounded, validated source session.
func (c *Client) OpenSourceStream(ctx context.Context, r resourcev2.SourceOpenRequest) (resourcev2.SourceStream, error) {
	snapshotTimeout, ok := ctx.Value(sourceSnapshotProgressTimeoutKey{}).(time.Duration)
	if !ok {
		snapshotTimeout = defaultSourceSnapshotProgressTimeout
	}
	ctx, cancelCause := context.WithCancelCause(ctx)
	deadline := &sourceDeadline{cancel: cancelCause}
	deadline.arm(sourceBeginTimeout)
	opened := false
	defer func() {
		if !opened {
			deadline.stop()
			cancelCause(context.Canceled)
		}
	}()
	p, err := resourcev2.ToProtoSourceOpenRequest(r)
	if err != nil {
		return nil, err
	}
	descriptor, err := sourceAdmission(ctx, c, r)
	if err != nil {
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return nil, sourceBeginTimedOut(ctx, r.SessionID)
		}
		return nil, err
	}
	ctx, err = outgoingSession(ctx)
	if err != nil {
		return nil, err
	}
	cancel := func() { cancelCause(context.Canceled) }
	stream, err := c.conn.NewStream(ctx, &resourcepb.ResourceSources_ServiceDesc.Streams[0], resourcepb.ResourceSources_Open_FullMethodName, grpc.MaxCallRecvMsgSize(int(r.Limits.MaxFrameBytes)), grpc.MaxCallSendMsgSize(256<<10), grpc.PartialMessageReadTimeout(5*time.Second))
	if err != nil {
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return nil, sourceBeginTimedOut(ctx, r.SessionID)
		}
		cancel()
		return nil, domainError(err)
	}
	if err = stream.SendMsg(p); err == nil {
		err = stream.CloseSend()
	}
	if err != nil {
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return nil, sourceBeginTimedOut(ctx, r.SessionID)
		}
		cancel()
		return nil, domainError(err)
	}
	opened = true
	return &sourceClientStream{stream: stream, cancel: cancel, ctx: ctx, maxBatchItems: r.Limits.MaxBatchItems, deadline: deadline, snapshotTimeout: snapshotTimeout, tracker: resourcev2.NewCatalogBoundSourcePhaseTracker(r, descriptor), client: c, request: r, descriptor: descriptor, nextCatalogCheck: time.Now().Add(time.Second)}, nil
}
func (s *sourceClientStream) Recv() (resourcev2.SourceFrame, error) {
	var zero resourcev2.SourceFrame
	p := &resourcepb.SourceFrame{}
	e := &envelope{message: p, sourceBatchLimit: int(s.maxBatchItems)}
	if err := s.stream.RecvMsg(e); err != nil {
		if IsFatalSourceTransportError(err) {
			result := s.tracker.TransportError(err)
			s.Close()
			return zero, result
		}
		if s.ctx != nil && errors.Is(context.Cause(s.ctx), context.DeadlineExceeded) {
			s.Close()
			return zero, sourceSessionTimeout(s.ctx, s.snapshotTimeout)
		}
		if s.ctx != nil && s.ctx.Err() != nil {
			s.Close()
			return zero, resourcev2.NewError("CANCELLED", "source stream closed")
		}
		mapped := domainError(err)
		if status.Code(err) == codes.ResourceExhausted || resourcev2.ErrorCode(mapped) == "RESOURCE_LIMIT" {
			s.Close()
			return zero, resourcev2.NewError("RESOURCE_LIMIT", "source frame exceeds transport limit")
		}
		var typed *resourcev2.ResourceOperationError
		if !s.admitted && errors.As(mapped, &typed) {
			s.Close()
			return zero, mapped
		}
		result := s.tracker.TransportError(err)
		s.Close()
		return zero, result
	}
	if e.err != nil {
		if resourcev2.ErrorCode(e.err) == "RESOURCE_LIMIT" {
			s.Close()
			return zero, e.err
		}
		s.Close()
		sourceFrameInvalid(s.ctx, "envelope_error")
		return zero, resourcev2.NewError("PROVIDER_ERROR", "invalid source frame")
	}
	if p.GetVersion() != resourcev2.WireVersion {
		s.Close()
		sourceFrameInvalid(s.ctx, "wire_version")
		return zero, resourcev2.NewError("PROVIDER_ERROR", "invalid source wire version")
	}
	f, err := resourcev2.FromProtoSourceFrame(p)
	if err != nil {
		s.Close()
		if resourcev2.ErrorCode(err) == "RESOURCE_LIMIT" {
			return zero, err
		}
		sourceFrameInvalid(s.ctx, "decode_failed")
		return zero, resourcev2.NewError("PROVIDER_ERROR", "invalid source frame")
	}
	if s.client != nil && (f.Snapshot != nil || f.SnapshotEnd != nil || f.Changes != nil) && !time.Now().Before(s.nextCatalogCheck) {
		s.nextCatalogCheck = time.Now().Add(time.Second)
		catalog, err := s.client.Catalog(s.ctx, resourcev2.CatalogRequest{Route: s.request.Target.Route})
		if err == nil {
			err = resourcev2.CheckSourceCatalog(s.ctx, catalog, s.request, s.descriptor)
		}
		if err != nil {
			s.Close()
			return zero, err
		}
	}
	if err = s.tracker.AdmitSized(f, proto.Size(p)); err != nil {
		s.Close()
		return zero, err
	}
	s.admitted = true
	if s.deadline != nil {
		if f.Begin != nil || f.Snapshot != nil {
			s.deadline.arm(s.snapshotTimeout)
		} else if f.SnapshotEnd != nil || f.Reset != nil || f.Failed != nil {
			s.deadline.stop()
		}
	}
	if s.ctx != nil && s.ctx.Err() != nil {
		if errors.Is(context.Cause(s.ctx), context.DeadlineExceeded) {
			s.Close()
			return zero, sourceSessionTimeout(s.ctx, s.snapshotTimeout)
		}
		s.Close()
		return zero, resourcev2.NewError("CANCELLED", "source stream closed")
	}
	return f, nil
}
func (s *sourceClientStream) Close() {
	s.once.Do(func() {
		if s.deadline != nil {
			s.deadline.stop()
		}
		s.cancel()
		s.tracker.Close()
	})
}
