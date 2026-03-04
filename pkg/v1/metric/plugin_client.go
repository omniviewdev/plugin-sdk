package metric

import (
	"context"
	"errors"
	"io"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

type PluginClient struct {
	client metricpb.MetricPluginClient
	log    logging.Logger
}

var _ Provider = (*PluginClient)(nil)

func (c *PluginClient) GetSupportedResources(ctx *types.PluginContext) (*ProviderInfo, []Handler) {
	resp, err := c.client.GetSupportedResources(
		types.WithPluginContext(context.Background(), ctx),
		&emptypb.Empty{},
	)
	if err != nil {
		return nil, nil
	}

	info := ProviderInfoFromProto(resp.GetProvider())
	protoHandlers := resp.GetHandlers()
	handlers := make([]Handler, 0, len(protoHandlers))
	for _, h := range protoHandlers {
		handlers = append(handlers, HandlerFromProto(h))
	}
	return &info, handlers
}

func (c *PluginClient) Query(
	ctx *types.PluginContext,
	req QueryRequest,
) (*QueryResponse, error) {
	var resourceData *structpb.Struct
	if req.ResourceData != nil {
		var err error
		resourceData, err = structpb.NewStruct(req.ResourceData)
		if err != nil {
			return nil, err
		}
	}

	protoReq := &metricpb.QueryMetricsRequest{
		ResourceKey:       req.ResourceKey,
		ResourceId:        req.ResourceID,
		ResourceNamespace: req.ResourceNamespace,
		ResourceData:      resourceData,
		MetricIds:         req.MetricIDs,
		Shape:             req.Shape.ToProto(),
		Params:            req.Params,
	}
	if !req.StartTime.IsZero() {
		protoReq.StartTime = timestamppb.New(req.StartTime)
	}
	if !req.EndTime.IsZero() {
		protoReq.EndTime = timestamppb.New(req.EndTime)
	}
	if req.Step > 0 {
		protoReq.Step = durationpb.New(req.Step)
	}

	resp, err := c.client.Query(
		types.WithPluginContext(context.Background(), ctx),
		protoReq,
	)
	if err != nil {
		return nil, err
	}

	return QueryResponseFromProto(resp), nil
}

func (c *PluginClient) StreamMetrics(
	ctx context.Context,
	in chan StreamInput,
) (chan StreamOutput, error) {
	stream, err := c.client.StreamMetrics(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan StreamOutput)

	// sender — owns stream.CloseSend(); does NOT close out.
	go func() {
		defer func() {
			if err := stream.CloseSend(); err != nil {
				c.log.Error(ctx, "failed to close send metric stream", logging.Error(err))
			}
		}()
		for i := range in {
			msg, err := streamInputToProto(i)
			if err != nil {
				c.log.Error(ctx, "failed to convert metric stream input to proto", logging.Error(err))
				return
			}
			if err := stream.Send(msg); err != nil {
				c.log.Error(ctx, "failed to send metric stream input", logging.Error(err))
				return
			}
		}
	}()

	// receiver — owns close(out).
	go func() {
		defer close(out)
		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				c.log.Error(ctx, "failed to receive metric stream output", logging.Error(err))
				return
			}

			results := make([]MetricResult, 0, len(resp.GetResults()))
			for _, r := range resp.GetResults() {
				results = append(results, MetricResultFromProto(r))
			}

			var ts = resp.GetTimestamp().AsTime()

			out <- StreamOutput{
				SubscriptionID: resp.GetSubscriptionId(),
				Results:        results,
				Timestamp:      ts,
				Error:          resp.GetError(),
			}
		}
	}()

	return out, nil
}
