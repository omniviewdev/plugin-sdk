package types

import "github.com/omniviewdev/plugin-sdk/pkg/types"

type ResourceProviderInput[I OperationInput] struct {
	Input       I
	ResourceID  string
	NamespaceID string
}

type RegisterPreHookRequest[I OperationInput] struct {
	Hook  PreHookFunc[I]
	ID    string
	Phase PreHookType
}

// ResourceProvider provides an interface for performing operations against a resource backend
// given a resource namespace and a resource identifier.
type ResourceProvider interface {
	// GetResourceTypes returns the all of the available resource types for the resource manager
	GetResourceTypes() map[string]ResourceMeta
	// GetResourceType returns the resource type information by it's string representation
	// For example, "core::v1::Pod" or "ec2::2012-12-01::EC2Instance"
	GetResourceType(string) (*ResourceMeta, error)
	// HasResourceType checks to see if the resource type exists
	HasResourceType(string) bool

	// LoadConnections loads the connections for the resource provider
	LoadConnections(ctx *types.PluginContext) ([]types.Connection, error)
	// ListConnections lists the connections for the resource provider
	ListConnections(ctx *types.PluginContext) ([]types.Connection, error)
	// GetConnection gets a connection for the resource provider
	GetConnection(ctx *types.PluginContext, id string) (types.Connection, error)
	// UpdateConnection updates the connection for the resource provider
	UpdateConnection(
		ctx *types.PluginContext,
		connection types.Connection,
	) (types.Connection, error)
	// DeleteConnection deletes the connection for the resource provider
	DeleteConnection(ctx *types.PluginContext, id string) error

	// Get returns a single resource in the given resource namespace.
	Get(ctx *types.PluginContext, key string, input GetInput) (*GetResult, error)
	// Get returns a single resource in the given resource namespace.
	List(ctx *types.PluginContext, key string, input ListInput) (*ListResult, error)
	// FindResources returns a list of resources in the given resource namespace that
	// match a set of given options.
	Find(ctx *types.PluginContext, key string, input FindInput) (*FindResult, error)
	// Create creates a new resource in the given resource namespace.
	Create(
		ctx *types.PluginContext,
		key string,
		input CreateInput,
	) (*CreateResult, error)
	// Update updates an existing resource in the given resource namespace.
	Update(
		ctx *types.PluginContext,
		key string,
		input UpdateInput,
	) (*UpdateResult, error)
	// Delete deletes an existing resource in the given resource namespace.
	Delete(
		ctx *types.PluginContext,
		key string,
		input DeleteInput,
	) (*DeleteResult, error)

	// StartContextInformer signals the resource provider to start an informer for the given resource backend context
	StartContextInformer(ctx *types.PluginContext, contextID string) error
	// StopContextInformer signals the resource provider to stop an informer for the given resource backend context
	StopContextInformer(ctx *types.PluginContext, contextID string) error
	// ListenForEvents registers a listener for resource events
	ListenForEvents(
		ctx *types.PluginContext,
		addStream chan InformerAddPayload,
		updateStream chan InformerUpdatePayload,
		deleteStream chan InformerDeletePayload,
	) error
}
