package metric

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

// PluginClient forwards metric queries and streams to a remote plugin.
type PluginClient struct {
	client metricpb.MetricPluginClient
	log    logging.Logger
}

var _ Provider = (*PluginClient)(nil)

// metricDomainError preserves local C02 errors and remote classifications. Only
// an unclassified missing-method status becomes explicit capability absence.
func metricDomainError(err error) error {
	if err == nil {
		return nil
	}
	var operation *resourcev2.ResourceOperationError
	if errors.As(err, &operation) {
		return err
	}
	translated := resourceplugin.ResourceDomainError(err)
	if errors.As(translated, &operation) {
		return translated
	}
	if status.Code(err) == codes.Unimplemented {
		return metricError("UNSUPPORTED", "Resource metric operation is unsupported.")
	}
	return translated
}

// GetSupportedResources returns the remote provider and its resource handlers.
func (c *PluginClient) GetSupportedResources(ctx *types.PluginContext) (*ProviderInfo, []Handler) {
	response, err := c.client.GetSupportedResources(pluginCallContext(ctx), &emptypb.Empty{})
	if err != nil {
		return nil, nil
	}
	info := ProviderInfoFromProto(response.Provider)
	handlers := make([]Handler, 0, len(response.Handlers))
	for _, h := range response.Handlers {
		handlers = append(handlers, HandlerFromProto(h))
	}
	return &info, handlers
}

func pluginCallContext(pc *types.PluginContext) context.Context {
	ctx := context.Background()
	if pc == nil {
		pc = &types.PluginContext{}
	} else {
		snapshot := *pc
		pc = &snapshot
	}
	if pc.Context != nil {
		ctx = pc.Context
	}
	return types.WithPluginContext(ctx, pc)
}

// Query selects the legacy or resource-aware RPC from the request's intent.
func (c *PluginClient) Query(ctx *types.PluginContext, r QueryRequest) (*QueryResponse, error) {
	if r.HasResourceIntent() {
		return c.QueryResource(ctx, r)
	}
	p, err := queryToProto(r, false)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Query(pluginCallContext(ctx), p)
	if err != nil {
		return nil, metricDomainError(err)
	}
	return QueryResponseFromProto(response), nil
}

// QueryResource validates resource intent and requires resource-aware response admission.
func (c *PluginClient) QueryResource(ctx *types.PluginContext, r QueryRequest) (*QueryResponse, error) {
	if !r.HasResourceIntent() {
		return nil, metricError("INVALID_REQUEST", "Resource query requires resource input.")
	}
	if err := validateQuery(r, ctx); err != nil {
		return nil, err
	}
	p, err := queryToProto(r, true)
	if err != nil {
		return nil, err
	}
	option, admission := resourceplugin.ResponseAdmission()
	callContext, err := resourceplugin.OutgoingSessionContext(pluginCallContext(ctx))
	if err != nil {
		return nil, err
	}
	response, err := c.client.QueryResource(callContext, p, option)
	if e := admission(); e != nil {
		return nil, e
	}
	if err != nil {
		return nil, metricDomainError(err)
	}
	if response.ResourceWireVersion != ResourceWireVersion || response.Response == nil {
		return nil, metricError("UNSUPPORTED", "Unsupported metric query response.")
	}
	return QueryResponseFromProto(response.Response), nil
}

func outputFromProto(p *metricpb.MetricStreamOutput) StreamOutput {
	r := StreamOutput{SubscriptionID: p.SubscriptionId, Error: p.Error}
	if p.Timestamp != nil {
		r.Timestamp = p.Timestamp.AsTime()
	}
	for _, v := range p.Results {
		r.Results = append(r.Results, MetricResultFromProto(v))
	}
	if e := p.OperationError; e != nil {
		r.OperationError = &resourcev2.ResourceOperationError{
			Code:        e.Code,
			Title:       e.Title,
			Message:     e.Message,
			Suggestions: e.Suggestions,
		}
	}
	return r
}

// StreamMetrics opens a legacy metric stream bound to ctx.
func (c *PluginClient) StreamMetrics(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	child, cancel := context.WithCancel(ctx)
	stream, err := c.client.StreamMetrics(child)
	if err != nil {
		cancel()
		return nil, metricDomainError(err)
	}
	return clientStream(child, cancel, in, false, stream.Send, func() (StreamOutput, error) {
		p, e := stream.Recv()
		if e != nil {
			return StreamOutput{}, e
		}
		return outputFromProto(p), nil
	}, stream.CloseSend), nil
}

// StreamResourceMetrics waits for remote readiness before forwarding resource frames.
func (c *PluginClient) StreamResourceMetrics(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	ctx, err := resourceplugin.OutgoingSessionContext(ctx)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	option, admission := resourceplugin.ResponseAdmission()
	stream, err := c.client.StreamResourceMetrics(child, option)
	if err != nil {
		cancel()
		return nil, metricDomainError(err)
	}
	timer := time.AfterFunc(metricStreamReadyTimeout, cancel)
	if err = stream.Send(&metricpb.MetricStreamInput{
		ResourceWireVersion: ResourceWireVersion,
		ResourceOpen:        true,
	}); err != nil {
		timer.Stop()
		cancel()
		return nil, metricDomainError(err)
	}
	ack, err := stream.Recv()
	timer.Stop()
	if e := admission(); e != nil {
		cancel()
		return nil, e
	}
	if err != nil {
		cancel()
		return nil, metricDomainError(err)
	}
	if ack.ResourceWireVersion != ResourceWireVersion || !ack.GetReady() {
		cancel()
		return nil, metricError("INVALID_REQUEST", "Missing metric stream readiness acknowledgement.")
	}
	return clientStream(child, cancel, in, true, stream.Send, func() (StreamOutput, error) {
		p, e := stream.Recv()
		if a := admission(); a != nil {
			return StreamOutput{}, a
		}
		if e != nil {
			return StreamOutput{}, e
		}
		if p.ResourceWireVersion != ResourceWireVersion || p.GetOutput() == nil {
			return StreamOutput{}, metricError("INVALID_REQUEST", "Invalid metric stream output frame.")
		}
		return outputFromProto(p.GetOutput()), nil
	}, stream.CloseSend), nil
}

const metricStreamReadyTimeout = 5 * time.Second

type metricSendFailure struct {
	err            error
	subscriptionID string
}

type metricReceiveResult struct {
	output StreamOutput
	err    error
}

func sendMetricInputs(
	ctx context.Context,
	in <-chan StreamInput,
	resource bool,
	send func(*metricpb.MetricStreamInput) error,
	closeSend func() error,
	failure chan<- metricSendFailure,
) {
	// CloseSend only ends the send direction; receive errors remain authoritative.
	defer func() { _ = closeSend() }()
	for {
		select {
		case <-ctx.Done():
			return
		case r, ok := <-in:
			if !ok {
				return
			}
			p, err := streamToProto(r, resource)
			if err == nil {
				err = send(p)
			}
			if err == nil {
				continue
			}
			id := r.SubscriptionID
			if resourcev2.ValidateMetadata(struct{ ID string }{id}) != nil {
				id = ""
			}
			select {
			case failure <- metricSendFailure{err: err, subscriptionID: id}:
			case <-ctx.Done():
			}
			return
		}
	}
}

func receiveMetricOutputs(
	ctx context.Context,
	recv func() (StreamOutput, error),
	received chan<- metricReceiveResult,
) {
	for {
		r, err := recv()
		select {
		case received <- metricReceiveResult{r, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func publishMetricStreamError(ctx context.Context, out chan<- StreamOutput, id string, err error) {
	err = metricDomainError(err)
	var operation *resourcev2.ResourceOperationError
	if !errors.As(err, &operation) {
		operation = &resourcev2.ResourceOperationError{
			Code:    "INTERNAL",
			Message: "Metric stream transport failed.",
		}
	}
	if operation.Message == "" {
		snapshot := *operation
		snapshot.Message = "Metric stream operation failed."
		operation = &snapshot
	}
	select {
	case out <- StreamOutput{SubscriptionID: id, OperationError: operation}:
	case <-ctx.Done():
	}
}

func clientStream(
	ctx context.Context,
	cancel context.CancelFunc,
	in chan StreamInput,
	resource bool,
	send func(*metricpb.MetricStreamInput) error,
	recv func() (StreamOutput, error),
	closeSend func() error,
) chan StreamOutput {
	out := make(chan StreamOutput)
	failure := make(chan metricSendFailure, 1)
	received := make(chan metricReceiveResult)
	go sendMetricInputs(ctx, in, resource, send, closeSend, failure)
	go receiveMetricOutputs(ctx, recv, received)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-failure:
				publishMetricStreamError(ctx, out, e.subscriptionID, e.err)
				return
			case r := <-received:
				if r.err != nil {
					if !errors.Is(r.err, io.EOF) {
						publishMetricStreamError(ctx, out, "", r.err)
					}
					return
				}
				select {
				case out <- r.output:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
