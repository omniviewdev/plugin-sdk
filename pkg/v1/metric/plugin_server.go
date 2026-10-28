package metric

import (
	"context"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"io"
)

type PluginServer struct{ Impl Provider }

func (s *PluginServer) GetSupportedResources(ctx context.Context, _ *emptypb.Empty) (*metricpb.GetSupportedMetricResourcesResponse, error) {
	info, handlers := s.Impl.GetSupportedResources(types.PluginContextFromContext(ctx))
	out := &metricpb.GetSupportedMetricResourcesResponse{Provider: info.ToProto()}
	for i := range handlers {
		out.Handlers = append(out.Handlers, handlers[i].ToProto())
	}
	return out, nil
}
func metricPluginContext(ctx context.Context) *types.PluginContext {
	pc := types.PluginContextFromContext(ctx)
	if pc == nil {
		return &types.PluginContext{Context: ctx}
	}
	copy := *pc
	copy.Context = ctx
	return &copy
}
func (s *PluginServer) Query(ctx context.Context, p *metricpb.QueryMetricsRequest) (*metricpb.QueryMetricsResponse, error) {
	r, err := queryFromProto(p, false)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	response, err := s.Impl.Query(metricPluginContext(ctx), r)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return response.ToProto(), nil
}
func (s *PluginServer) QueryResource(ctx context.Context, p *metricpb.QueryMetricsRequest) (*metricpb.ResourceQueryMetricsResponse, error) {
	ctx, err := resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	r, err := queryFromProto(p, true)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	pc := metricPluginContext(ctx)
	if err = validateQuery(r, pc); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	if !r.HasResourceIntent() {
		return nil, resourceplugin.ResourceRPCError(metricError("INVALID_REQUEST", "Resource query requires resource input."))
	}
	impl, ok := s.Impl.(ResourceQueryProvider)
	if !ok {
		return nil, resourceplugin.ResourceRPCError(metricError("UNSUPPORTED", "Resource metric query is unsupported."))
	}
	response, err := impl.QueryResource(pc, r)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return &metricpb.ResourceQueryMetricsResponse{ResourceWireVersion: 2, Response: response.ToProto()}, nil
}
func streamOutputToProto(r StreamOutput) *metricpb.MetricStreamOutput {
	p := &metricpb.MetricStreamOutput{SubscriptionId: r.SubscriptionID, Timestamp: timestamppb.New(r.Timestamp), Error: r.Error}
	for i := range r.Results {
		p.Results = append(p.Results, r.Results[i].ToProto())
	}
	if e := r.OperationError; e != nil {
		p.OperationError = &metricpb.MetricOperationError{Code: e.Code, Title: e.Title, Message: e.Message, Suggestions: e.Suggestions}
	}
	return p
}
func (s *PluginServer) StreamMetrics(stream metricpb.MetricPlugin_StreamMetricsServer) error {
	return s.serveStream(stream.Context(), false, stream.Recv, func(output StreamOutput) error { return stream.Send(streamOutputToProto(output)) }, nil)
}
func (s *PluginServer) StreamResourceMetrics(stream metricpb.MetricPlugin_StreamResourceMetricsServer) error {
	p, err := stream.Recv()
	if err != nil {
		return err
	}
	if p.ResourceWireVersion != 2 || !p.ResourceOpen || proto.Size(p) != 4 {
		return resourceplugin.ResourceRPCError(metricError("INVALID_REQUEST", "Expected resource stream open frame."))
	}
	return s.serveStream(stream.Context(), true, stream.Recv, func(output StreamOutput) error {
		return stream.Send(&metricpb.ResourceMetricStreamOutput{ResourceWireVersion: 2, Payload: &metricpb.ResourceMetricStreamOutput_Output{Output: streamOutputToProto(output)}})
	}, func() error {
		return stream.Send(&metricpb.ResourceMetricStreamOutput{ResourceWireVersion: 2, Payload: &metricpb.ResourceMetricStreamOutput_Ready{Ready: true}})
	})
}
func (s *PluginServer) serveStream(parent context.Context, resource bool, recv func() (*metricpb.MetricStreamInput, error), send func(StreamOutput) error, ready func() error) error {
	parent, parseError := resourceplugin.IncomingSessionContext(parent)
	if parseError != nil {
		return resourceplugin.ResourceRPCError(parseError)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	in := make(chan StreamInput)
	var out chan StreamOutput
	var err error
	if resource {
		impl, ok := s.Impl.(ResourceStreamProvider)
		if !ok {
			return resourceplugin.ResourceRPCError(metricError("UNSUPPORTED", "Resource metric stream is unsupported."))
		}
		out, err = impl.StreamResourceMetrics(ctx, in)
	} else {
		out, err = s.Impl.StreamMetrics(ctx, in)
	}
	if err != nil {
		return resourceplugin.ResourceRPCError(err)
	}
	if ready != nil {
		if err = ready(); err != nil {
			return err
		}
	}
	failures := make(chan error, 1)
	go func() {
		defer close(in)
		for {
			p, e := recv()
			if e != nil {
				failures <- e
				return
			}
			r, e := streamFromProto(p, resource)
			if e != nil {
				failures <- resourceplugin.ResourceRPCError(e)
				return
			}
			pc := types.PluginContextFromContext(ctx)
			if e = validateQuery(QueryRequest{ResourceKey: r.ResourceKey, ResourceData: r.ResourceData, Reference: r.Reference, Route: r.Route}, pc); e != nil {
				failures <- resourceplugin.ResourceRPCError(e)
				return
			}
			select {
			case in <- r:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			if err == io.EOF {
				return nil
			}
			return err
		case r, ok := <-out:
			if !ok {
				return nil
			}
			if err = send(r); err != nil {
				return err
			}
		}
	}
}
