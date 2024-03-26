package services

import (
	"fmt"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/factories"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
)

const (
	StartingGroupMapCapacity    = 10
	StartingResourceMapCapacity = 10
)

// ResourceTypeManager is the interface for which resource type managers must implement.
//
// If a resource backend has a dynamic set of resource types that can change with each
// connection (for example, different Kubernetes Clusters running different versions),
// it should instantiate a DynamicResourceTypeManager.
//
// If a resource backend has a static set of resource types that does not change with each
// connection (for example, AWS, GCP, Azure, etc.), it should instantiate the
// StaticResourceTypeManager.
type ResourceTypeManager interface {
	// GetGroups returns the grouped tree of resources available.
	GetGroups() map[string]types.ResourceGroup

	// GetGroup returns the group information by it's string representation
	GetGroup(string) (types.ResourceGroup, error)

	// GetResourceTypes returns the all of the available resource types for the resource manager
	GetResourceTypes() map[string]types.ResourceMeta

	// GetResourceType returns the resource type information by it's string representation
	// For example, "core::v1::Pod" or "ec2::2012-12-01::EC2Instance"
	GetResourceType(string) (*types.ResourceMeta, error)

	// HasResourceType checks to see if the resource type exists
	HasResourceType(string) bool

	// GetAvailableResourceTypes returns the available resource types for the given namespace
	GetConnectionResourceTypes(string) ([]types.ResourceMeta, error)

	// SyncResourceNamespace sets up a given connection with the manager, and syncs the available resource types
	// given a set of options
	SyncConnection(*pkgtypes.PluginContext, *pkgtypes.Connection) error

	// and stops the client for the namespace
	RemoveConnection(*pkgtypes.PluginContext, *pkgtypes.Connection) error
}

// StaticResourceTypeManager is a resource type manager that provides a static set of resource types
// that does not change with each connection. This is useful for resource backends that have
// a static set of resource types that does not change with each connection, for example, AWS,
// GCP, Azure, etc.
type StaticResourceTypeManager struct {
	// resourceGroups is a list of resource groups for the resource manager
	groups map[string]types.ResourceGroup

	// resourceTypes is a map of available resource types for the resource manager
	resourceTypes map[string]types.ResourceMeta

	// namespacedResourceTypes is a map of available resource types for a given connection
	namespacedResourceTypes map[string][]types.ResourceMeta

	sync.RWMutex // embed this last for pointer receiver semantics
}

// NewStaticResourceTypeManager creates a new resource type manager with a static set of resource types
// that does not change with each connection
// For example, AWS, GCP, Azure, etc.
func NewStaticResourceTypeManager(
	resourceTypes []types.ResourceMeta,
	resourceGroups []types.ResourceGroup,
) ResourceTypeManager {
	manager := newStaticResourceTypeManager(resourceTypes, resourceGroups)
	return manager
}

func newStaticResourceTypeManager(
	resourceTypes []types.ResourceMeta,
	resourceGroups []types.ResourceGroup,
) *StaticResourceTypeManager {
	resourceTypesMap := make(map[string]types.ResourceMeta)
	for _, resource := range resourceTypes {
		resourceTypesMap[resource.String()] = resource
	}
	return &StaticResourceTypeManager{
		groups:                  addResourcesToGroups(resourceGroups, resourceTypes),
		resourceTypes:           resourceTypesMap,
		namespacedResourceTypes: make(map[string][]types.ResourceMeta),
	}
}

func addResourcesToGroups(
	groups []types.ResourceGroup,
	resourceTypes []types.ResourceMeta,
) map[string]types.ResourceGroup {
	groupsMap := make(map[string]types.ResourceGroup, StartingGroupMapCapacity)

	for _, group := range groups {
		if _, ok := groupsMap[group.ID]; !ok {
			group.Resources = make(map[string][]types.ResourceMeta)
			groupsMap[group.ID] = group
		}
	}

	for _, resource := range resourceTypes {
		groupID := resource.GetGroup()

		// check if the group exists
		group, ok := groupsMap[groupID]
		if !ok {
			// didn't declare the group - make a minimal one
			group = types.ResourceGroup{
				ID:        groupID,
				Name:      groupID,
				Resources: make(map[string][]types.ResourceMeta),
			}
		}

		// check for version
		if _, ok := group.Resources[resource.Version]; !ok {
			group.Resources[resource.Version] = make(
				[]types.ResourceMeta,
				0,
				StartingResourceMapCapacity,
			)
		}

		group.Resources[resource.Version] = append(group.Resources[resource.Version], resource)
		groupsMap[groupID] = group
	}

	return groupsMap
}

func (r *StaticResourceTypeManager) GetGroups() map[string]types.ResourceGroup {
	r.RLock()
	defer r.RUnlock()
	return r.groups
}

func (r *StaticResourceTypeManager) GetGroup(s string) (types.ResourceGroup, error) {
	r.RLock()
	defer r.RUnlock()
	if group, ok := r.groups[s]; ok {
		return group, nil
	}
	return types.ResourceGroup{}, fmt.Errorf("group %s does not exist", s)
}

func (r *StaticResourceTypeManager) GetResourceTypes() map[string]types.ResourceMeta {
	r.RLock()
	defer r.RUnlock()

	return r.resourceTypes
}

func (r *StaticResourceTypeManager) GetResourceType(
	s string,
) (*types.ResourceMeta, error) {
	r.RLock()
	defer r.RUnlock()

	if resource, ok := r.resourceTypes[s]; ok {
		return &resource, nil
	}
	return nil, fmt.Errorf("resource type %s does not exist", s)
}

func (r *StaticResourceTypeManager) HasResourceType(s string) bool {
	r.RLock()
	defer r.RUnlock()

	_, ok := r.resourceTypes[s]
	return ok
}

func (r *StaticResourceTypeManager) SyncConnection(
	_ *pkgtypes.PluginContext,
	connection *pkgtypes.Connection,
) error {
	r.Lock()
	defer r.Unlock()

	if _, ok := r.namespacedResourceTypes[connection.ID]; !ok {
		r.namespacedResourceTypes[connection.ID] = make(
			[]types.ResourceMeta,
			0,
			len(r.resourceTypes),
		)
		for _, resource := range r.resourceTypes {
			r.namespacedResourceTypes[connection.ID] = append(
				r.namespacedResourceTypes[connection.ID],
				resource,
			)
		}
	}
	return nil
}

func (r *StaticResourceTypeManager) GetConnectionResourceTypes(
	connectionID string,
) ([]types.ResourceMeta, error) {
	r.RLock()
	defer r.RUnlock()
	if availableResourceTypes, ok := r.namespacedResourceTypes[connectionID]; ok {
		return availableResourceTypes, nil
	}
	return nil, fmt.Errorf("no available resource types for connection %s", connectionID)
}

func (r *StaticResourceTypeManager) RemoveConnection(
	pluginCtx *pkgtypes.PluginContext,
	connection *pkgtypes.Connection,
) error {
	r.Lock()
	defer r.Unlock()

	delete(r.namespacedResourceTypes, connection.ID)
	return nil
}

func (r *StaticResourceTypeManager) GetAvailableResourceTypes(
	ctx *pkgtypes.PluginContext,
	connection *pkgtypes.Connection,
) ([]types.ResourceMeta, error) {
	r.RLock()
	defer r.RUnlock()

	if availableResourceTypes, ok := r.namespacedResourceTypes[connection.ID]; ok {
		return availableResourceTypes, nil
	}
	return nil, fmt.Errorf("no available resource types for connection %s", connection.ID)
}

// DynamicResourceTypeManager is an resource type manager that provides a dynamic set of resource types
// that can change with each connection. This is useful for resource backends that have a dynamic
// set of resource types that can change with each connection, for example, different Kubernetes
// Clusters running different versions.
//
// The discovery manager requires defining the the type of the discovery client, as well as
// the options type for the discovery client. The discovery client is responsible for
// discovering the available resource types within a connection, e.g. a Kubernetes
// cluster, AWS account, etc.
//
// This discovery manager is optional, and if none is provided, the resource manager will
// use all resource types provided by the resource type manager.
type DynamicResourceTypeManager[DiscoveryClientT any] struct {
	// clientFactory is the client factory for the resource type discovery manager
	clientFactory factories.ResourceDiscoveryClientFactory[DiscoveryClientT]

	// clients is a map of clients for the resource type discovery manager
	clients map[string]*DiscoveryClientT

	// syncer is the getter function that, taking in the respective client, can retrieve and then
	// return the available resource types for a given namespace
	syncer func(ctx *pkgtypes.PluginContext, client *DiscoveryClientT) ([]types.ResourceMeta, error)

	*StaticResourceTypeManager // embed this last for pointer receiver semantics
}

// NewDynamicResourceTypeManager creates a new resource type discovery manager to be
// used with the the resource backend, given a client factory and a sync function.
func NewDynamicResourceTypeManager[DiscoveryClientT any](
	resourceTypes []types.ResourceMeta,
	resourceGroups []types.ResourceGroup,
	factory factories.ResourceDiscoveryClientFactory[DiscoveryClientT],
	syncer func(ctx *pkgtypes.PluginContext, client *DiscoveryClientT) ([]types.ResourceMeta, error),
) ResourceTypeManager {
	return &DynamicResourceTypeManager[DiscoveryClientT]{
		StaticResourceTypeManager: newStaticResourceTypeManager(
			resourceTypes,
			resourceGroups,
		),
		clientFactory: factory,
		clients:       make(map[string]*DiscoveryClientT),
		syncer:        syncer,
	}
}

func (r *DynamicResourceTypeManager[DiscoveryClientT]) SyncResourceNamespace(
	ctx *pkgtypes.PluginContext,
	connection *pkgtypes.Connection,
) error {
	r.Lock()
	defer r.Unlock()

	// ensure the connection is on the context
	ctx.Connection = connection

	// check if the client already exists for the namespace
	if _, ok := r.clients[connection.ID]; !ok {
		// create the client for the namespace
		client, err := r.clientFactory.CreateClient(ctx)
		if err != nil {
			err = fmt.Errorf(
				"failed to create client for connection %s: %w",
				connection.ID,
				err,
			)
			return err
		}

		// start the client
		if err = r.clientFactory.StartClient(ctx, client); err != nil {
			err = fmt.Errorf(
				"failed to start client for connection %s: %w",
				connection.ID,
				err,
			)
			return err
		}

		r.clients[connection.ID] = client
	}

	// get the client for the namespace and sync the available resource types
	client := r.clients[connection.ID]
	availableResourceTypes, err := r.syncer(ctx, client)
	if err != nil {
		return err
	}
	if availableResourceTypes == nil {
		return fmt.Errorf(
			"syncer returned nil available resource types for connection %s",
			connection.ID,
		)
	}

	return nil
}

func (r *DynamicResourceTypeManager[DiscoveryClientT]) RemoveConnection(
	ctx *pkgtypes.PluginContext,
	connection *pkgtypes.Connection,
) error {
	r.Lock()
	defer r.Unlock()

	// stop the client for the namespace if the namespace exists
	if client, ok := r.clients[connection.ID]; ok {
		if err := r.clientFactory.StopClient(ctx, client); err != nil {
			return err
		}
	}

	// delete the client and the available resource types for the namespace if the namespace exists
	delete(r.clients, connection.ID)
	delete(r.namespacedResourceTypes, connection.ID)

	return nil
}

// GetAvailableResourceTypes returns the available resource types for the given namespace.
func (r *DynamicResourceTypeManager[DiscoveryClientT]) GetAvailableResourceTypes(
	ctx *pkgtypes.PluginContext,
	connection *pkgtypes.Connection,
) ([]types.ResourceMeta, error) {
	r.Lock()
	defer r.Unlock()

	// check if the available resource types for the namespace exist
	if availableResourceTypes, ok := r.namespacedResourceTypes[connection.ID]; ok {
		return availableResourceTypes, nil
	}

	return nil, fmt.Errorf("no available resource types for connection %s", connection.ID)
}
