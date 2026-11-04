package metric

import (
	"bytes"
	"context"
	"encoding/json"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
)

const MetricContractVersion uint32 = 1

type metricConnectionID struct {
	ID string `json:"id"`
}
type metricDefinitionValidation struct {
	Source     MetricSourceReference `json:"source"`
	Definition MetricDefinition      `json:"definition"`
}
type resourceSetServer struct{ impl Provider }

func contractInput(p *metricpb.MetricContractRequest, target any) error {
	if p == nil || p.Version != MetricContractVersion {
		return metricError("UNSUPPORTED", "Unsupported metric contract version.")
	}
	if err := resourcev2.ValidateMetadata(metricConnectionID{ID: p.ProviderConnectionId}); err != nil {
		return err
	}
	return DecodeMetricContract(p.Json, target)
}
func contractOutput(value any, err error) (*metricpb.MetricContractResponse, error) {
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	b, err := json.Marshal(value)
	if err == nil {
		err = resourcev2.ValidateJSON(b)
	}
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return &metricpb.MetricContractResponse{Version: MetricContractVersion, Json: b}, nil
}
func wireQueryContext(ctx context.Context, p *metricpb.MetricContractRequest) *QueryContext {
	pc := metricPluginContext(ctx)
	q := &QueryContext{Context: ctx}
	if pc.Connection != nil {
		q.Connection = pc.Connection
		q.ConnectionID = pc.Connection.ID
	}
	if p.ProviderConnectionId != "" {
		q.ConnectionID = p.ProviderConnectionId
	}
	return q
}
func (s *resourceSetServer) QueryResources(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input ResourceSetQueryRequest
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	if err := ValidateResourceSetQuery(input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(ResourceSetProvider)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Resource-set metrics are unavailable."))
	}
	q := wireQueryContext(ctx, p)
	if err := validateProviderQueryContext(q, input.Source); err != nil {
		return contractOutput(nil, err)
	}
	output, err := provider.QueryResources(q, input)
	if err == nil {
		err = ValidateResourceSetResponse(input, output)
	}
	return contractOutput(output, err)
}
func (s *resourceSetServer) GetMetricDefinitions(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input struct{}
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(MetricCatalogProvider)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Metric catalog is unavailable."))
	}
	out, err := provider.GetMetricDefinitions(wireQueryContext(ctx, p))
	return contractOutput(out, err)
}
func (s *resourceSetServer) GetProviderConnections(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input struct{}
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(ProviderConfiguration)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Metric configuration is unavailable."))
	}
	out, err := provider.GetProviderConnections(wireQueryContext(ctx, p))
	return contractOutput(out, err)
}
func (s *resourceSetServer) SaveProviderConnection(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input ProviderConnectionInput
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(ProviderConfiguration)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Metric configuration is unavailable."))
	}
	out, err := provider.SaveProviderConnection(wireQueryContext(ctx, p), input)
	return contractOutput(out, err)
}
func (s *resourceSetServer) DeleteProviderConnection(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input metricConnectionID
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(ProviderConfiguration)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Metric configuration is unavailable."))
	}
	err := provider.DeleteProviderConnection(wireQueryContext(ctx, p), input.ID)
	return contractOutput(struct{}{}, err)
}
func (s *resourceSetServer) GetDataSources(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input MetricSourceReference
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(ProviderConfiguration)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Metric configuration is unavailable."))
	}
	out, err := provider.GetDataSources(wireQueryContext(ctx, p), input)
	return contractOutput(out, err)
}
func (s *resourceSetServer) TestProviderConnection(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input metricConnectionID
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(ProviderConfiguration)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Metric configuration is unavailable."))
	}
	out, err := provider.TestProviderConnection(wireQueryContext(ctx, p), input.ID)
	return contractOutput(out, err)
}
func (s *resourceSetServer) ValidateMetricDefinition(ctx context.Context, p *metricpb.MetricContractRequest) (*metricpb.MetricContractResponse, error) {
	var input metricDefinitionValidation
	if err := contractInput(p, &input); err != nil {
		return contractOutput(nil, err)
	}
	provider, ok := s.impl.(ProviderConfiguration)
	if !ok {
		return contractOutput(nil, metricError("UNSUPPORTED", "Metric configuration is unavailable."))
	}
	out, err := provider.ValidateMetricDefinition(wireQueryContext(ctx, p), input.Source, input.Definition)
	return contractOutput(out, err)
}
func (c *PluginClient) contractCall(q *QueryContext, name string, input any, output any) error {
	if c.resourceSetClient == nil {
		return metricError("UNSUPPORTED", "Resource-set transport is unavailable.")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if err = resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	ctx := context.Background()
	if q != nil && q.Context != nil {
		ctx = q.Context
	}
	pc := &types.PluginContext{Context: ctx}
	if q != nil {
		pc.Connection = q.Connection
	}
	ctx = pluginCallContext(pc)
	p := &metricpb.MetricContractRequest{Version: MetricContractVersion, Json: data}
	if q != nil {
		p.ProviderConnectionId = q.ConnectionID
	}
	var response *metricpb.MetricContractResponse
	switch name {
	case "DiscoverMetricLabels":
		response, err = c.resourceSetClient.DiscoverMetricLabels(ctx, p)
	case "QueryResources":
		response, err = c.resourceSetClient.QueryResources(ctx, p)
	case "GetMetricDefinitions":
		response, err = c.resourceSetClient.GetMetricDefinitions(ctx, p)
	case "GetProviderConnections":
		response, err = c.resourceSetClient.GetProviderConnections(ctx, p)
	case "SaveProviderConnection":
		response, err = c.resourceSetClient.SaveProviderConnection(ctx, p)
	case "DeleteProviderConnection":
		response, err = c.resourceSetClient.DeleteProviderConnection(ctx, p)
	case "GetDataSources":
		response, err = c.resourceSetClient.GetDataSources(ctx, p)
	case "TestProviderConnection":
		response, err = c.resourceSetClient.TestProviderConnection(ctx, p)
	case "ValidateMetricDefinition":
		response, err = c.resourceSetClient.ValidateMetricDefinition(ctx, p)
	default:
		return metricError("UNSUPPORTED", "Unknown metric operation.")
	}
	if err != nil {
		return metricDomainError(err)
	}
	if response == nil || response.Version != MetricContractVersion {
		return metricError("UNSUPPORTED", "Unsupported metric response version.")
	}
	if err = resourcev2.ValidateJSON(response.Json); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(response.Json))
	d.DisallowUnknownFields()
	return d.Decode(output)
}
func (c *PluginClient) QueryResources(q *QueryContext, input ResourceSetQueryRequest) (*ResourceSetQueryResponse, error) {
	if err := ValidateResourceSetQuery(input); err != nil {
		return nil, err
	}
	var out ResourceSetQueryResponse
	err := c.contractCall(q, "QueryResources", input, &out)
	if err == nil {
		err = ValidateResourceSetResponse(input, &out)
	}
	return &out, err
}
func (c *PluginClient) GetMetricDefinitions(q *QueryContext) ([]MetricDefinition, error) {
	var out []MetricDefinition
	err := c.contractCall(q, "GetMetricDefinitions", struct{}{}, &out)
	return out, err
}
func (c *PluginClient) GetProviderConnections(q *QueryContext) ([]ProviderConnection, error) {
	var out []ProviderConnection
	err := c.contractCall(q, "GetProviderConnections", struct{}{}, &out)
	return out, err
}
func (c *PluginClient) SaveProviderConnection(q *QueryContext, input ProviderConnectionInput) (*ProviderConnection, error) {
	var out ProviderConnection
	err := c.contractCall(q, "SaveProviderConnection", input, &out)
	return &out, err
}
func (c *PluginClient) DeleteProviderConnection(q *QueryContext, id string) error {
	var out struct{}
	return c.contractCall(q, "DeleteProviderConnection", metricConnectionID{ID: id}, &out)
}
func (c *PluginClient) GetDataSources(q *QueryContext, source MetricSourceReference) ([]MetricDataSource, error) {
	var out []MetricDataSource
	err := c.contractCall(q, "GetDataSources", source, &out)
	return out, err
}
func (c *PluginClient) TestProviderConnection(q *QueryContext, id string) (*MetricValidationResult, error) {
	var out MetricValidationResult
	err := c.contractCall(q, "TestProviderConnection", metricConnectionID{ID: id}, &out)
	return &out, err
}
func (c *PluginClient) ValidateMetricDefinition(q *QueryContext, source MetricSourceReference, definition MetricDefinition) (*MetricValidationResult, error) {
	var out MetricValidationResult
	err := c.contractCall(q, "ValidateMetricDefinition", metricDefinitionValidation{Source: source, Definition: definition}, &out)
	return &out, err
}
