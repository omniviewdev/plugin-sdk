package metric

import (
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

type PluginServer struct {
	Impl Provider
}

func (s *PluginServer) GetSupportedResources(
	ctx context.Context,
	_ *emptypb.Empty,
) (*metricpb.GetSupportedMetricResourcesResponse, error) {
	info, handlers := s.Impl.GetSupportedResources(types.PluginContextFromContext(ctx))

	protoHandlers := make([]*metricpb.MetricHandler, 0, len(handlers))
	for i := range handlers {
		protoHandlers = append(protoHandlers, handlers[i].ToProto())
	}

	return &metricpb.GetSupportedMetricResourcesResponse{
		Provider: info.ToProto(),
		Handlers: protoHandlers,
	}, nil
}

func (s *PluginServer) Query(
	ctx context.Context,
	in *metricpb.QueryMetricsRequest,
) (*metricpb.QueryMetricsResponse, error) {
	if in == nil {
		return nil, status.Errorf(codes.InvalidArgument, "request is nil")
	}

	var resourceData map[string]interface{}
	if rd := in.GetResourceData(); rd != nil {
		resourceData = rd.AsMap()
	}

	req := QueryRequest{
		ResourceKey:       in.GetResourceKey(),
		ResourceID:        in.GetResourceId(),
		ResourceNamespace: in.GetResourceNamespace(),
		ResourceData:      resourceData,
		MetricIDs:         in.GetMetricIds(),
		Shape:             MetricShapeFromProto(in.GetShape()),
		Params:            in.GetParams(),
	}
	if in.GetStartTime() != nil {
		req.StartTime = in.GetStartTime().AsTime()
	}
	if in.GetEndTime() != nil {
		req.EndTime = in.GetEndTime().AsTime()
	}
	if in.GetStep() != nil {
		req.Step = in.GetStep().AsDuration()
	}

	resp, err := s.Impl.Query(types.PluginContextFromContext(ctx), req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to query metrics: %v", err)
	}

	return resp.ToProto(), nil
}

func (s *PluginServer) StreamMetrics(stream metricpb.MetricPlugin_StreamMetricsServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	multiplexer := make(chan StreamInput)
	out, err := s.Impl.StreamMetrics(ctx, multiplexer)
	if err != nil {
		return err
	}

	// handle the output
	go s.handleOut(ctx, out, stream)

	// handle the input — run Recv in a goroutine so we can also
	// react to ctx cancellation if the stream hangs.
	type recvResult struct {
		msg *metricpb.MetricStreamInput
		err error
	}
	recvCh := make(chan recvResult, 1)

	go func() {
		for {
			msg, recvErr := stream.Recv()
			select {
			case recvCh <- recvResult{msg: msg, err: recvErr}:
			case <-ctx.Done():
				return
			}
			if recvErr != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-recvCh:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					return nil
				}
				return status.Errorf(codes.Internal, "recv error: %v", r.err)
			}
			select {
			case multiplexer <- streamInputFromProto(r.msg):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (s *PluginServer) handleOut(
	ctx context.Context,
	out <-chan StreamOutput,
	stream metricpb.MetricPlugin_StreamMetricsServer,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case output, ok := <-out:
			if !ok {
				return
			}
			results := make([]*metricpb.MetricResult, 0, len(output.Results))
			for i := range output.Results {
				results = append(results, output.Results[i].ToProto())
			}

			msg := &metricpb.MetricStreamOutput{
				SubscriptionId: output.SubscriptionID,
				Results:        results,
				Timestamp:      timestamppb.New(output.Timestamp),
				Error:          output.Error,
			}

			if err := stream.Send(msg); err != nil {
				return
			}
		}
	}
}

func streamInputFromProto(p *metricpb.MetricStreamInput) StreamInput {
	si := StreamInput{
		SubscriptionID:    p.GetSubscriptionId(),
		Command:           MetricStreamCommandFromProto(p.GetCommand()),
		ResourceKey:       p.GetResourceKey(),
		ResourceID:        p.GetResourceId(),
		ResourceNamespace: p.GetResourceNamespace(),
		MetricIDs:         p.GetMetricIds(),
	}
	if p.GetResourceData() != nil {
		si.ResourceData = p.GetResourceData().AsMap()
	}
	if p.GetInterval() != nil {
		si.Interval = p.GetInterval().AsDuration()
	}
	return si
}

func streamInputToProto(si StreamInput) (*metricpb.MetricStreamInput, error) {
	p := &metricpb.MetricStreamInput{
		SubscriptionId:    si.SubscriptionID,
		Command:           si.Command.ToProto(),
		ResourceKey:       si.ResourceKey,
		ResourceId:        si.ResourceID,
		ResourceNamespace: si.ResourceNamespace,
		MetricIds:         si.MetricIDs,
		Interval:          durationpb.New(si.Interval),
	}
	if si.ResourceData != nil {
		data, err := structpb.NewStruct(si.ResourceData)
		if err != nil {
			return nil, fmt.Errorf("failed to convert ResourceData to proto Struct: %w", err)
		}
		p.ResourceData = data
	}
	return p, nil
}
