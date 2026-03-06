package resourcetest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
)

// TestProvider is a configurable in-process Provider for integration tests.
// It implements the full Provider interface without gRPC or process spawning.
type TestProvider struct {
	t *testing.T

	// Operation handlers. Default: return success with empty JSON.
	GetFunc    func(ctx context.Context, key string, input resource.GetInput) (*resource.GetResult, error)
	ListFunc   func(ctx context.Context, key string, input resource.ListInput) (*resource.ListResult, error)
	FindFunc   func(ctx context.Context, key string, input resource.FindInput) (*resource.FindResult, error)
	CreateFunc func(ctx context.Context, key string, input resource.CreateInput) (*resource.CreateResult, error)
	UpdateFunc func(ctx context.Context, key string, input resource.UpdateInput) (*resource.UpdateResult, error)
	DeleteFunc func(ctx context.Context, key string, input resource.DeleteInput) (*resource.DeleteResult, error)

	// Connection handlers.
	StartConnectionFunc      func(ctx context.Context, id string) (types.ConnectionStatus, error)
	StopConnectionFunc       func(ctx context.Context, id string) (types.Connection, error)
	LoadConnectionsFunc      func(ctx context.Context) ([]types.Connection, error)
	ListConnectionsFunc      func(ctx context.Context) ([]types.Connection, error)
	GetConnectionFunc        func(ctx context.Context, id string) (types.Connection, error)
	GetConnectionNSFunc      func(ctx context.Context, id string) ([]string, error)
	UpdateConnectionFunc     func(ctx context.Context, conn types.Connection) (types.Connection, error)
	DeleteConnectionFunc     func(ctx context.Context, id string) error
	WatchConnectionsFunc     func(ctx context.Context, stream chan<- []types.Connection) error

	// Watch handlers.
	StartConnectionWatchFunc  func(ctx context.Context, connID string) error
	StopConnectionWatchFunc   func(ctx context.Context, connID string) error
	HasWatchFunc              func(ctx context.Context, connID string) bool
	GetWatchStateFunc         func(ctx context.Context, connID string) (*resource.WatchConnectionSummary, error)
	ListenForEventsFunc       func(ctx context.Context, sink resource.WatchEventSink) error
	EnsureResourceWatchFunc   func(ctx context.Context, connID, key string) error
	StopResourceWatchFunc     func(ctx context.Context, connID, key string) error
	RestartResourceWatchFunc  func(ctx context.Context, connID, key string) error
	IsResourceWatchRunningFunc func(ctx context.Context, connID, key string) (bool, error)

	// Type handlers.
	GetResourceGroupsFunc       func(ctx context.Context, connID string) map[string]resource.ResourceGroup
	GetResourceGroupFunc        func(ctx context.Context, id string) (resource.ResourceGroup, error)
	GetResourceTypesFunc        func(ctx context.Context, connID string) map[string]resource.ResourceMeta
	GetResourceTypeFunc         func(ctx context.Context, id string) (*resource.ResourceMeta, error)
	HasResourceTypeFunc         func(ctx context.Context, id string) bool
	GetResourceDefinitionFunc   func(ctx context.Context, id string) (resource.ResourceDefinition, error)
	GetResourceCapabilitiesFunc func(ctx context.Context, key string) (*resource.ResourceCapabilities, error)
	GetResourceSchemaFunc       func(ctx context.Context, connID, key string) (json.RawMessage, error)
	GetFilterFieldsFunc         func(ctx context.Context, connID, key string) ([]resource.FilterField, error)

	// Action handlers.
	GetActionsFunc     func(ctx context.Context, key string) ([]resource.ActionDescriptor, error)
	ExecuteActionFunc  func(ctx context.Context, key, actionID string, input resource.ActionInput) (*resource.ActionResult, error)
	StreamActionFunc   func(ctx context.Context, key, actionID string, input resource.ActionInput, stream chan<- resource.ActionEvent) error

	// Editor schema handler.
	GetEditorSchemasFunc func(ctx context.Context, connID string) ([]resource.EditorSchema, error)

	// Relationship handlers.
	GetRelationshipsFunc     func(ctx context.Context, key string) ([]resource.RelationshipDescriptor, error)
	ResolveRelationshipsFunc func(ctx context.Context, connID, key, id, ns string) ([]resource.ResolvedRelationship, error)

	// Health handlers.
	GetHealthFunc         func(ctx context.Context, connID, key string, data json.RawMessage) (*resource.ResourceHealth, error)
	GetResourceEventsFunc func(ctx context.Context, connID, key, id, ns string, limit int32) ([]resource.ResourceEvent, error)
}

// Option configures a TestProvider.
type Option func(*TestProvider)

// NewTestProvider creates a TestProvider with sensible defaults.
// Options can override any handler.
func NewTestProvider(t *testing.T, opts ...Option) *TestProvider {
	t.Helper()
	p := &TestProvider{t: t}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// --- Option constructors ---

// WithGetFunc sets the Get handler.
func WithGetFunc(fn func(ctx context.Context, key string, input resource.GetInput) (*resource.GetResult, error)) Option {
	return func(p *TestProvider) { p.GetFunc = fn }
}

// WithListFunc sets the List handler.
func WithListFunc(fn func(ctx context.Context, key string, input resource.ListInput) (*resource.ListResult, error)) Option {
	return func(p *TestProvider) { p.ListFunc = fn }
}

// WithLoadConnectionsFunc sets the LoadConnections handler.
func WithLoadConnectionsFunc(fn func(ctx context.Context) ([]types.Connection, error)) Option {
	return func(p *TestProvider) { p.LoadConnectionsFunc = fn }
}

// WithListenForEventsFunc sets the ListenForEvents handler.
func WithListenForEventsFunc(fn func(ctx context.Context, sink resource.WatchEventSink) error) Option {
	return func(p *TestProvider) { p.ListenForEventsFunc = fn }
}

// --- OperationProvider ---

func (p *TestProvider) Get(ctx context.Context, key string, input resource.GetInput) (*resource.GetResult, error) {
	if p.GetFunc != nil {
		return p.GetFunc(ctx, key, input)
	}
	return &resource.GetResult{Success: true, Result: json.RawMessage(`{}`)}, nil
}

func (p *TestProvider) List(ctx context.Context, key string, input resource.ListInput) (*resource.ListResult, error) {
	if p.ListFunc != nil {
		return p.ListFunc(ctx, key, input)
	}
	return &resource.ListResult{Success: true}, nil
}

func (p *TestProvider) Find(ctx context.Context, key string, input resource.FindInput) (*resource.FindResult, error) {
	if p.FindFunc != nil {
		return p.FindFunc(ctx, key, input)
	}
	return &resource.FindResult{Success: true}, nil
}

func (p *TestProvider) Create(ctx context.Context, key string, input resource.CreateInput) (*resource.CreateResult, error) {
	if p.CreateFunc != nil {
		return p.CreateFunc(ctx, key, input)
	}
	return &resource.CreateResult{Success: true, Result: input.Input}, nil
}

func (p *TestProvider) Update(ctx context.Context, key string, input resource.UpdateInput) (*resource.UpdateResult, error) {
	if p.UpdateFunc != nil {
		return p.UpdateFunc(ctx, key, input)
	}
	return &resource.UpdateResult{Success: true, Result: input.Input}, nil
}

func (p *TestProvider) Delete(ctx context.Context, key string, input resource.DeleteInput) (*resource.DeleteResult, error) {
	if p.DeleteFunc != nil {
		return p.DeleteFunc(ctx, key, input)
	}
	return &resource.DeleteResult{Success: true}, nil
}

// --- ConnectionLifecycleProvider ---

func (p *TestProvider) StartConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	if p.StartConnectionFunc != nil {
		return p.StartConnectionFunc(ctx, id)
	}
	return types.ConnectionStatus{Status: types.ConnectionStatusConnected}, nil
}

func (p *TestProvider) StopConnection(ctx context.Context, id string) (types.Connection, error) {
	if p.StopConnectionFunc != nil {
		return p.StopConnectionFunc(ctx, id)
	}
	return types.Connection{ID: id}, nil
}

func (p *TestProvider) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	if p.LoadConnectionsFunc != nil {
		return p.LoadConnectionsFunc(ctx)
	}
	return []types.Connection{{ID: "conn-1", Name: "Test"}}, nil
}

func (p *TestProvider) ListConnections(ctx context.Context) ([]types.Connection, error) {
	if p.ListConnectionsFunc != nil {
		return p.ListConnectionsFunc(ctx)
	}
	return []types.Connection{{ID: "conn-1", Name: "Test"}}, nil
}

func (p *TestProvider) GetConnection(ctx context.Context, id string) (types.Connection, error) {
	if p.GetConnectionFunc != nil {
		return p.GetConnectionFunc(ctx, id)
	}
	return types.Connection{ID: id, Name: "Test"}, nil
}

func (p *TestProvider) GetConnectionNamespaces(ctx context.Context, id string) ([]string, error) {
	if p.GetConnectionNSFunc != nil {
		return p.GetConnectionNSFunc(ctx, id)
	}
	return []string{"default"}, nil
}

func (p *TestProvider) UpdateConnection(ctx context.Context, conn types.Connection) (types.Connection, error) {
	if p.UpdateConnectionFunc != nil {
		return p.UpdateConnectionFunc(ctx, conn)
	}
	return conn, nil
}

func (p *TestProvider) DeleteConnection(ctx context.Context, id string) error {
	if p.DeleteConnectionFunc != nil {
		return p.DeleteConnectionFunc(ctx, id)
	}
	return nil
}

func (p *TestProvider) WatchConnections(ctx context.Context, stream chan<- []types.Connection) error {
	if p.WatchConnectionsFunc != nil {
		return p.WatchConnectionsFunc(ctx, stream)
	}
	<-ctx.Done()
	return nil
}

// --- WatchProvider ---

func (p *TestProvider) StartConnectionWatch(ctx context.Context, connID string) error {
	if p.StartConnectionWatchFunc != nil {
		return p.StartConnectionWatchFunc(ctx, connID)
	}
	return nil
}

func (p *TestProvider) StopConnectionWatch(ctx context.Context, connID string) error {
	if p.StopConnectionWatchFunc != nil {
		return p.StopConnectionWatchFunc(ctx, connID)
	}
	return nil
}

func (p *TestProvider) HasWatch(ctx context.Context, connID string) bool {
	if p.HasWatchFunc != nil {
		return p.HasWatchFunc(ctx, connID)
	}
	return false
}

func (p *TestProvider) GetWatchState(ctx context.Context, connID string) (*resource.WatchConnectionSummary, error) {
	if p.GetWatchStateFunc != nil {
		return p.GetWatchStateFunc(ctx, connID)
	}
	return &resource.WatchConnectionSummary{}, nil
}

func (p *TestProvider) ListenForEvents(ctx context.Context, sink resource.WatchEventSink) error {
	if p.ListenForEventsFunc != nil {
		return p.ListenForEventsFunc(ctx, sink)
	}
	<-ctx.Done()
	return nil
}

func (p *TestProvider) EnsureResourceWatch(ctx context.Context, connID string, key string) error {
	if p.EnsureResourceWatchFunc != nil {
		return p.EnsureResourceWatchFunc(ctx, connID, key)
	}
	return nil
}

func (p *TestProvider) StopResourceWatch(ctx context.Context, connID string, key string) error {
	if p.StopResourceWatchFunc != nil {
		return p.StopResourceWatchFunc(ctx, connID, key)
	}
	return nil
}

func (p *TestProvider) RestartResourceWatch(ctx context.Context, connID string, key string) error {
	if p.RestartResourceWatchFunc != nil {
		return p.RestartResourceWatchFunc(ctx, connID, key)
	}
	return nil
}

func (p *TestProvider) IsResourceWatchRunning(ctx context.Context, connID string, key string) (bool, error) {
	if p.IsResourceWatchRunningFunc != nil {
		return p.IsResourceWatchRunningFunc(ctx, connID, key)
	}
	return false, nil
}

// --- TypeProvider ---

func (p *TestProvider) GetResourceGroups(ctx context.Context, connID string) map[string]resource.ResourceGroup {
	if p.GetResourceGroupsFunc != nil {
		return p.GetResourceGroupsFunc(ctx, connID)
	}
	return map[string]resource.ResourceGroup{}
}

func (p *TestProvider) GetResourceGroup(ctx context.Context, id string) (resource.ResourceGroup, error) {
	if p.GetResourceGroupFunc != nil {
		return p.GetResourceGroupFunc(ctx, id)
	}
	return resource.ResourceGroup{}, nil
}

func (p *TestProvider) GetResourceTypes(ctx context.Context, connID string) map[string]resource.ResourceMeta {
	if p.GetResourceTypesFunc != nil {
		return p.GetResourceTypesFunc(ctx, connID)
	}
	return map[string]resource.ResourceMeta{}
}

func (p *TestProvider) GetResourceType(ctx context.Context, id string) (*resource.ResourceMeta, error) {
	if p.GetResourceTypeFunc != nil {
		return p.GetResourceTypeFunc(ctx, id)
	}
	return nil, nil
}

func (p *TestProvider) HasResourceType(ctx context.Context, id string) bool {
	if p.HasResourceTypeFunc != nil {
		return p.HasResourceTypeFunc(ctx, id)
	}
	return false
}

func (p *TestProvider) GetResourceDefinition(ctx context.Context, id string) (resource.ResourceDefinition, error) {
	if p.GetResourceDefinitionFunc != nil {
		return p.GetResourceDefinitionFunc(ctx, id)
	}
	return resource.ResourceDefinition{}, nil
}

func (p *TestProvider) GetResourceCapabilities(ctx context.Context, key string) (*resource.ResourceCapabilities, error) {
	if p.GetResourceCapabilitiesFunc != nil {
		return p.GetResourceCapabilitiesFunc(ctx, key)
	}
	return &resource.ResourceCapabilities{CanGet: true, CanList: true}, nil
}

func (p *TestProvider) GetResourceSchema(ctx context.Context, connID string, key string) (json.RawMessage, error) {
	if p.GetResourceSchemaFunc != nil {
		return p.GetResourceSchemaFunc(ctx, connID, key)
	}
	return nil, nil
}

func (p *TestProvider) GetFilterFields(ctx context.Context, connID string, key string) ([]resource.FilterField, error) {
	if p.GetFilterFieldsFunc != nil {
		return p.GetFilterFieldsFunc(ctx, connID, key)
	}
	return nil, nil
}

// --- ActionProvider ---

func (p *TestProvider) GetActions(ctx context.Context, key string) ([]resource.ActionDescriptor, error) {
	if p.GetActionsFunc != nil {
		return p.GetActionsFunc(ctx, key)
	}
	return nil, nil
}

func (p *TestProvider) ExecuteAction(ctx context.Context, key string, actionID string, input resource.ActionInput) (*resource.ActionResult, error) {
	if p.ExecuteActionFunc != nil {
		return p.ExecuteActionFunc(ctx, key, actionID, input)
	}
	return &resource.ActionResult{Success: true}, nil
}

func (p *TestProvider) StreamAction(ctx context.Context, key string, actionID string, input resource.ActionInput, stream chan<- resource.ActionEvent) error {
	if p.StreamActionFunc != nil {
		return p.StreamActionFunc(ctx, key, actionID, input, stream)
	}
	return nil
}

// --- EditorSchemaProvider ---

func (p *TestProvider) GetEditorSchemas(ctx context.Context, connID string) ([]resource.EditorSchema, error) {
	if p.GetEditorSchemasFunc != nil {
		return p.GetEditorSchemasFunc(ctx, connID)
	}
	return nil, nil
}

// --- RelationshipProvider ---

func (p *TestProvider) GetRelationships(ctx context.Context, key string) ([]resource.RelationshipDescriptor, error) {
	if p.GetRelationshipsFunc != nil {
		return p.GetRelationshipsFunc(ctx, key)
	}
	return nil, nil
}

func (p *TestProvider) ResolveRelationships(ctx context.Context, connID string, key string, id string, ns string) ([]resource.ResolvedRelationship, error) {
	if p.ResolveRelationshipsFunc != nil {
		return p.ResolveRelationshipsFunc(ctx, connID, key, id, ns)
	}
	return nil, nil
}

// --- HealthProvider ---

func (p *TestProvider) GetHealth(ctx context.Context, connID string, key string, data json.RawMessage) (*resource.ResourceHealth, error) {
	if p.GetHealthFunc != nil {
		return p.GetHealthFunc(ctx, connID, key, data)
	}
	return nil, nil
}

func (p *TestProvider) GetResourceEvents(ctx context.Context, connID string, key string, id string, ns string, limit int32) ([]resource.ResourceEvent, error) {
	if p.GetResourceEventsFunc != nil {
		return p.GetResourceEventsFunc(ctx, connID, key, id, ns, limit)
	}
	return nil, nil
}

// Compile-time check that TestProvider satisfies Provider.
var _ resource.Provider = (*TestProvider)(nil)
