package types

import "github.com/omniviewdev/plugin-sdk/pkg/types"

type ResourceTypeProvider interface {
	// GetResourceTypes returns the all of the available resource types for the resource manager
	GetResourceTypes() map[string]ResourceMeta
	// GetResourceType returns the resource type information by it's string representation
	// For example, "core::v1::Pod" or "ec2::2012-12-01::EC2Instance"
	GetResourceType(string) (*ResourceMeta, error)
	// HasResourceType checks to see if the resource type exists
	HasResourceType(string) bool
}

type ResourceConnectionProvider interface {
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
}

type ResourceInformerProvider interface {
	// StartContextInformer signals the resource provider to start an informer for the given resource backend context
	StartConnectionInformer(ctx *types.PluginContext, connectionID string) error
	// StopContextInformer signals the resource provider to stop an informer for the given resource backend context
	StopConnectionInformer(ctx *types.PluginContext, connectionID string) error
	// ListenForEvents registers a listener for resource events
	ListenForEvents(
		ctx *types.PluginContext,
		addStream chan InformerAddPayload,
		updateStream chan InformerUpdatePayload,
		deleteStream chan InformerDeletePayload,
	) error
}

type ResourceOperationProvider interface {
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
}

type ResourceLayoutProvider interface {
	GetLayout(id string) ([]LayoutItem, error)
	SetLayout(id string, layout []LayoutItem) error
	GetDefaultLayout() ([]LayoutItem, error)
}

// ResourceProvider provides an interface for performing operations against a resource backend
// given a resource namespace and a resource identifier.
type ResourceProvider interface {
	ResourceTypeProvider
	ResourceConnectionProvider
	ResourceInformerProvider
	ResourceLayoutProvider
	ResourceOperationProvider
}
