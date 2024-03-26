package resource

import (
	"errors"
	"fmt"
	"log"

	"github.com/omniviewdev/settings"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/services"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
)

// ResourceController is responsible for managing the execution of resource operations.
// The resource controller will take in requests from requesters, both inside the IDE
// and outside, and will execute the necessary operations on the resource manager.
//
// This controller is the primary entrypoint for executing operations on resources, and
// operates as the plugin host for the installed resource plugin.
func NewResourceController[ClientT, InformerT any](
	resourcerManager services.ResourcerManager[ClientT],
	connectionManager services.ConnectionManager[ClientT],
	resourceTypeManager services.ResourceTypeManager,
	layoutManager services.LayoutManager,
	informerOpts *types.InformerOptions[ClientT, InformerT],
	settingsProvider settings.Provider,
) types.ResourceProvider {
	controller := &resourceController[ClientT, InformerT]{
		stopChan:            make(chan struct{}),
		resourcerManager:    resourcerManager,
		connectionManager:   connectionManager,
		resourceTypeManager: resourceTypeManager,
		layoutManager:       layoutManager,
		settingsProvider:    settingsProvider,
	}
	if informerOpts != nil {
		controller.withInformer = true
		controller.addChan = make(chan types.InformerAddPayload)
		controller.updateChan = make(chan types.InformerUpdatePayload)
		controller.deleteChan = make(chan types.InformerDeletePayload)
		controller.informerManager = services.NewInformerManager(
			informerOpts,
			controller.addChan,
			controller.updateChan,
			controller.deleteChan,
		)
	}
	return controller
}

type resourceController[ClientT, InformerT any] struct {
	resourcerManager    services.ResourcerManager[ClientT]
	connectionManager   services.ConnectionManager[ClientT]
	resourceTypeManager services.ResourceTypeManager
	layoutManager       services.LayoutManager
	settingsProvider    settings.Provider
	stopChan            chan struct{}
	informerManager     *services.InformerManager[ClientT, InformerT]
	addChan             chan types.InformerAddPayload
	updateChan          chan types.InformerUpdatePayload
	deleteChan          chan types.InformerDeletePayload
	withInformer        bool
}

// get our client and resourcer outside to slim down the methods.
func (c *resourceController[ClientT, InformerT]) retrieveClientResourcer(
	ctx *pkgtypes.PluginContext,
	resource string,
) (*ClientT, types.Resourcer[ClientT], error) {
	var nilResourcer types.Resourcer[ClientT]
	if ctx.Connection == nil {
		return nil, nilResourcer, errors.New("connection is nil")
	}

	// get the resourcer for the given resource type, and check type
	if ok := c.resourceTypeManager.HasResourceType(resource); !ok {
		return nil, nilResourcer, fmt.Errorf(
			"resource type %s not found in resource type manager",
			resource,
		)
	}

	resourcer, err := c.resourcerManager.GetResourcer(resource)
	if err != nil {
		return nil, nilResourcer, fmt.Errorf(
			"resourcer not found for resource type %s: %w",
			resource,
			err,
		)
	}

	// 2. Get the client for the given resource namespace, ensuring it is of the correct type
	client, err := c.connectionManager.GetCurrentConnectionClient(ctx)
	if err != nil {
		return nil, nilResourcer, fmt.Errorf(
			"client unable to be retrieved for auth context %s: %w",
			ctx.Connection.ID,
			err,
		)
	}

	// // ensure the client is of the correct type for the resource controller
	// expectedType := reflect.TypeOf((*ClientT)(nil)).Elem()
	// clientType := reflect.TypeOf(client)
	//
	// // check the type enforcement first before continuing
	// if clientType != expectedType {
	// 	return nil, nilResourcer, fmt.Errorf(
	// 		"client type %s does not match expected type %s for auth context %s",
	// 		clientType,
	// 		expectedType,
	// 		ctx.Connection.ID,
	// 	)
	// }

	// make sure we attach the current settings provider before calling the resourcer
	ctx.SetSettingsProvider(c.settingsProvider)

	return client, resourcer, nil
}

// ================================= Operation Methods ================================= //

// TODO - combine the common logic for the operations here, lots of repetativeness
// Get gets a resource within a resource namespace given an identifier and input options.
func (c *resourceController[ClientT, InformerT]) Get(
	ctx *pkgtypes.PluginContext,
	resource string,
	input types.GetInput,
) (*types.GetResult, error) {
	client, resourcer, err := c.retrieveClientResourcer(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve client and resourcer: %w", err)
	}

	// execute the resourcer
	result, err := resourcer.Get(ctx, client, input)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// List lists resources within a resource namespace given an identifier and input options.
func (c *resourceController[ClientT, InformerT]) List(
	ctx *pkgtypes.PluginContext,
	resource string,
	input types.ListInput,
) (*types.ListResult, error) {
	client, resourcer, err := c.retrieveClientResourcer(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve client and resourcer: %w", err)
	}
	result, err := resourcer.List(ctx, client, input)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// Find finds resources within a resource namespace given an identifier and input options.
func (c *resourceController[ClientT, InformerT]) Find(
	ctx *pkgtypes.PluginContext,
	resource string,
	input types.FindInput,
) (*types.FindResult, error) {
	client, resourcer, err := c.retrieveClientResourcer(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve client and resourcer: %w", err)
	}

	result, err := resourcer.Find(ctx, client, input)
	if err != nil {
		return nil, err
	}

	return result, err
}

// Create creates a resource within a resource namespace given an identifier and input options.
func (c *resourceController[ClientT, InformerT]) Create(
	ctx *pkgtypes.PluginContext,
	resource string,
	input types.CreateInput,
) (*types.CreateResult, error) {
	client, resourcer, err := c.retrieveClientResourcer(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve client and resourcer: %w", err)
	}

	result, err := resourcer.Create(ctx, client, input)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// Update updates a resource within a resource namespace given an identifier and input options.
func (c *resourceController[ClientT, InformerT]) Update(
	ctx *pkgtypes.PluginContext,
	resource string,
	input types.UpdateInput,
) (*types.UpdateResult, error) {
	client, resourcer, err := c.retrieveClientResourcer(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve client and resourcer: %w", err)
	}

	result, err := resourcer.Update(ctx, client, input)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// Delete deletes a resource within a resource namespace given an identifier and input options.
func (c *resourceController[ClientT, InformerT]) Delete(
	ctx *pkgtypes.PluginContext,
	resource string,
	input types.DeleteInput,
) (*types.DeleteResult, error) {
	client, resourcer, err := c.retrieveClientResourcer(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve client and resourcer: %w", err)
	}

	result, err := resourcer.Delete(ctx, client, input)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ================================= Informer Methods ================================= //

func (c *resourceController[ClientT, InformerT]) HasInformer(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) bool {
	return c.informerManager.HasInformer(ctx, connectionID)
}

// StartContextInformer signals to the listen runner to start the informer for the given context.
// If the informer is not enabled, this method will return a nil error.
//
// This typically should not be called by the client, but there may be situations where we need
// to start the informer manually. This gets handled on the StartConnection method.
func (c *resourceController[ClientT, InformerT]) StartConnectionInformer(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) error {
	if !c.withInformer {
		// safety guard just in case
		return nil
	}

	if err := c.connectionManager.InjectConnection(ctx, connectionID); err != nil {
		return fmt.Errorf("unable to inject connection: %w", err)
	}
	client, err := c.connectionManager.GetConnectionClient(ctx, connectionID)
	if err != nil {
		return fmt.Errorf("unable to get connection client: %w", err)
	}
	ctx.SetSettingsProvider(c.settingsProvider)

	// first create the connection informer
	if err = c.informerManager.CreateConnectionInformer(ctx, ctx.Connection, client); err != nil {
		return err
	}

	// get all the resource types
	resourceTypes := c.GetResourceTypes()
	for _, resource := range resourceTypes {
		if err = c.informerManager.RegisterResource(ctx, ctx.Connection, resource); err != nil {
			return fmt.Errorf("unable to register resource: %w", err)
		}
	}

	// finally, start it
	return c.informerManager.StartConnection(ctx, connectionID)
}

// StopContextInformer signals to the listen runner to stop the informer for the given context.
func (c *resourceController[ClientT, InformerT]) StopConnectionInformer(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) error {
	if !c.withInformer {
		// safety guard just in case
		return nil
	}
	if err := c.connectionManager.InjectConnection(ctx, connectionID); err != nil {
		return fmt.Errorf("unable to inject connection: %w", err)
	}
	ctx.SetSettingsProvider(c.settingsProvider)

	if err := c.informerManager.StopConnection(ctx, connectionID); err != nil {
		return fmt.Errorf("unable to stop connection: %w", err)
	}

	// remake the client
	return c.connectionManager.RefreshConnectionClient(ctx, connectionID)
}

// ListenForEvents listens for events from the informer and sends them to the given event channels.
// This method will block until the context is cancelled, and given this will block, the parent
// gRPC plugin host will spin this up in a goroutine.
func (c *resourceController[ClientT, InformerT]) ListenForEvents(
	ctx *pkgtypes.PluginContext,
	addChan chan types.InformerAddPayload,
	updateChan chan types.InformerUpdatePayload,
	deleteChan chan types.InformerDeletePayload,
) error {
	if !c.withInformer {
		log.Println("informer not enabled")
		return nil
	}
	ctx.SetSettingsProvider(c.settingsProvider)
	if err := c.informerManager.Run(c.stopChan, addChan, updateChan, deleteChan); err != nil {
		log.Println("error running informer manager:", err)
		return fmt.Errorf("error running informer manager: %w", err)
	}
	return nil
}

// ================================= Connection Methods ================================= //

// StartConnection starts a connection, initializing any informers as necessary.
func (c *resourceController[ClientT, InformerT]) StartConnection(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) (pkgtypes.Connection, error) {
	conn, err := c.connectionManager.StartConnection(ctx, connectionID)
	if err != nil {
		return conn, fmt.Errorf("unable to start connection: %w", err)
	}

	// check if has informer. if so, start it
	return conn, c.StartConnectionInformer(ctx, connectionID)
}

// StopConnection stops a connection, stopping any informers as necessary.
func (c *resourceController[ClientT, InformerT]) StopConnection(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) (pkgtypes.Connection, error) {
	if err := c.StopConnectionInformer(ctx, connectionID); err != nil {
		return pkgtypes.Connection{}, fmt.Errorf("unable to stop connection informer: %w", err)
	}
	return c.connectionManager.StopConnection(ctx, connectionID)
}

// LoadConnections calls the custom connection loader func to provide the the IDE the possible connections
// available.
func (c *resourceController[ClientT, InformerT]) LoadConnections(
	ctx *pkgtypes.PluginContext,
) ([]pkgtypes.Connection, error) {
	ctx.SetSettingsProvider(c.settingsProvider)
	return c.connectionManager.LoadConnections(ctx)
}

// ListConnections calls the custom connection loader func to provide the the IDE the possible connections.
func (c *resourceController[ClientT, InformerT]) ListConnections(
	ctx *pkgtypes.PluginContext,
) ([]pkgtypes.Connection, error) {
	ctx.SetSettingsProvider(c.settingsProvider)
	return c.connectionManager.ListConnections(ctx)
}

// GetConnection gets a connection by its ID.
func (c *resourceController[ClientT, InformerT]) GetConnection(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) (pkgtypes.Connection, error) {
	ctx.SetSettingsProvider(c.settingsProvider)
	return c.connectionManager.GetConnection(ctx, connectionID)
}

// UpdateConnection updates a connection by its ID.
func (c *resourceController[ClientT, InformerT]) UpdateConnection(
	ctx *pkgtypes.PluginContext,
	connection pkgtypes.Connection,
) (pkgtypes.Connection, error) {
	ctx.SetSettingsProvider(c.settingsProvider)
	return c.connectionManager.UpdateConnection(ctx, connection)
}

// DeleteConnection deletes a connection by its ID.
func (c *resourceController[ClientT, InformerT]) DeleteConnection(
	ctx *pkgtypes.PluginContext,
	connectionID string,
) error {
	ctx.SetSettingsProvider(c.settingsProvider)
	return c.connectionManager.DeleteConnection(ctx, connectionID)
}

// ================================= Resource Type Methods ================================= //

// GetResourceGroups gets the resource groups available to the resource controller.
func (c *resourceController[ClientT, InformerT]) GetResourceGroups() map[string]types.ResourceGroup {
	return c.resourceTypeManager.GetGroups()
}

// GetResourceGroup gets the resource group by its name.
func (c *resourceController[ClientT, InformerT]) GetResourceGroup(
	name string,
) (types.ResourceGroup, error) {
	return c.resourceTypeManager.GetGroup(name)
}

// GetResourceTypes gets the resource types available to the resource controller.
func (c *resourceController[ClientT, InformerT]) GetResourceTypes() map[string]types.ResourceMeta {
	return c.resourceTypeManager.GetResourceTypes()
}

// GetResourceType gets the resource type information by its string representation.
func (c *resourceController[ClientT, InformerT]) GetResourceType(
	resource string,
) (*types.ResourceMeta, error) {
	return c.resourceTypeManager.GetResourceType(resource)
}

// HasResourceType checks to see if the resource type exists.
func (c *resourceController[ClientT, InformerT]) HasResourceType(resource string) bool {
	return c.resourceTypeManager.HasResourceType(resource)
}

// ================================= Resource Type Methods ================================= //

func (c *resourceController[ClientT, InformerT]) GetLayout(
	layoutID string,
) ([]types.LayoutItem, error) {
	return c.layoutManager.GetLayout(layoutID)
}

func (c *resourceController[ClientT, InformerT]) GetDefaultLayout() ([]types.LayoutItem, error) {
	return c.layoutManager.GetDefaultLayout()
}

func (c *resourceController[ClientT, InformerT]) SetLayout(
	id string,
	layout []types.LayoutItem,
) error {
	return c.layoutManager.SetLayout(id, layout)
}
