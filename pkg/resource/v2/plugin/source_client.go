package plugin

import (
	"context"
	"errors"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

type sourceClientStream struct {
	stream   grpc.ClientStream
	cancel   context.CancelFunc
	tracker  *resourcev2.SourcePhaseTracker
	admitted bool
	once     sync.Once
}

// OpenSourceStream opens a bounded, validated source session.
func (c *Client) OpenSourceStream(ctx context.Context, r resourcev2.SourceOpenRequest) (resourcev2.SourceStream, error) {
	p, err := resourcev2.ToProtoSourceOpenRequest(r)
	if err != nil {
		return nil, err
	}
	descriptor, err := sourceAdmission(ctx, c, r)
	if err != nil {
		return nil, err
	}
	ctx, err = outgoingSession(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stream, err := c.conn.NewStream(ctx, &resourcepb.ResourceSources_ServiceDesc.Streams[0], resourcepb.ResourceSources_Open_FullMethodName, grpc.MaxCallRecvMsgSize(int(r.Limits.MaxFrameBytes)), grpc.MaxCallSendMsgSize(MaxWireBytes))
	if err != nil {
		cancel()
		return nil, domainError(err)
	}
	if err = stream.SendMsg(p); err == nil {
		err = stream.CloseSend()
	}
	if err != nil {
		cancel()
		return nil, domainError(err)
	}
	return &sourceClientStream{stream: stream, cancel: cancel, tracker: resourcev2.NewSourcePhaseTracker(r, resourcev2.SourceGuaranteeForTarget(descriptor, r.Target))}, nil
}
func (s *sourceClientStream) Recv() (resourcev2.SourceFrame, error) {
	var zero resourcev2.SourceFrame
	p := &resourcepb.SourceFrame{}
	e := &envelope{message: p}
	if err := s.stream.RecvMsg(e); err != nil {
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
		return zero, s.tracker.TransportError(err)
	}
	if e.err != nil {
		if resourcev2.ErrorCode(e.err) == "RESOURCE_LIMIT" {
			s.Close()
			return zero, e.err
		}
		s.Close()
		return zero, resourcev2.NewError("PROVIDER_ERROR", "invalid source frame")
	}
	if p.GetVersion() != resourcev2.WireVersion {
		s.Close()
		return zero, resourcev2.NewError("PROVIDER_ERROR", "invalid source wire version")
	}
	f, err := resourcev2.FromProtoSourceFrame(p)
	if err != nil {
		s.Close()
		if resourcev2.ErrorCode(err) == "RESOURCE_LIMIT" {
			return zero, err
		}
		return zero, resourcev2.NewError("PROVIDER_ERROR", "invalid source frame")
	}
	if err = s.tracker.AdmitSized(f, proto.Size(p)); err != nil {
		s.Close()
		return zero, err
	}
	s.admitted = true
	return f, nil
}
func (s *sourceClientStream) Close() { s.once.Do(func() { s.tracker.Close(); s.cancel() }) }
