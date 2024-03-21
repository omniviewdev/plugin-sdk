package services

import (
	"fmt"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/factories"
	"github.com/omniviewdev/plugin-sdk/pkg/resource/types"
	pkgtypes "github.com/omniviewdev/plugin-sdk/pkg/types"
)

// InformerManager is an interface for managing informers for the various resource services.
// An informer watches for changes to resources and broadcasts those changes to the IDE event
// subsystem.
//
// The Informer system is heavily inspired and built around the concept pioneered by the
// Kubernetes API Server, and as such, the informer system is a generalized manager for
// informer implementation similar to that of the Kubernetes API Server and the corresponding
// client-go library.
//
// More information on the Kubernetes informer design can be found here:
// https://www.cncf.io/blog/2019/10/15/extend-kubernetes-via-a-shared-informer/
//
// An important note here is that due to the desired behavior of the resourcer clients being able
// to use the informer local cache for their operations, the informer manager will be provided the
// same client that the resourcer clients use for their operations. If multiple clients are used
// to set up informers, they should be injected as a dependency into the Client setup in the
// ResourceClientFactory.
type InformerManager[ClientT, InformerT any] struct {
	factory             factories.InformerFactory[ClientT, InformerT]
	registerHandler     types.RegisterResourceFunc[InformerT]
	runHandler          types.RunInformerFunc[InformerT]
	informers           map[string]informer[InformerT]
	addChan             chan types.InformerAddPayload
	updateChan          chan types.InformerUpdatePayload
	deleteChan          chan types.InformerDeletePayload
	startconnectionChan chan string
	stopconnectionChan  chan string
}

type informer[InformerT any] struct {
	// informer client
	informer InformerT
	// informer cancel function
	cancel chan struct{}
}

func NewInformerManager[ClientT, InformerT any](
	factory factories.InformerFactory[ClientT, InformerT],
	registerHandler types.RegisterResourceFunc[InformerT],
	runHandler types.RunInformerFunc[InformerT],
) *InformerManager[ClientT, InformerT] {
	return &InformerManager[ClientT, InformerT]{
		factory:             factory,
		registerHandler:     registerHandler,
		runHandler:          runHandler,
		informers:           make(map[string]informer[InformerT]),
		addChan:             make(chan types.InformerAddPayload),
		updateChan:          make(chan types.InformerUpdatePayload),
		deleteChan:          make(chan types.InformerDeletePayload),
		startconnectionChan: make(chan string),
		stopconnectionChan:  make(chan string),
	}
}

// Run starts the informer manager, and blocks until the stop channel is closed.
// Acts as a fan-in aggregator for the various informer channels.
func (i *InformerManager[CT, IT]) Run(
	stopCh <-chan struct{},
	controllerAddChan chan types.InformerAddPayload,
	controllerUpdateChan chan types.InformerUpdatePayload,
	controllerDeleteChan chan types.InformerDeletePayload,
) error {
	for {
		select {
		case <-stopCh:
			return nil
		case id := <-i.startconnectionChan:
			informer := i.informers[id]
			// TODO - handle this error case at some point
			go i.runHandler(
				informer.informer,
				informer.cancel,
				i.addChan,
				i.updateChan,
				i.deleteChan,
			)
		case id := <-i.stopconnectionChan:
			// stop the informer
			informer, ok := i.informers[id]
			if !ok {
				// ignore, probobly already stopped
				break
			}
			close(informer.cancel)
		case add := <-i.addChan:
			controllerAddChan <- add
		case update := <-i.updateChan:
			controllerUpdateChan <- update
		case del := <-i.deleteChan:
			controllerDeleteChan <- del
		}
	}
}

func (i *InformerManager[CT, IT]) CreateConnectionInformer(
	ctx *pkgtypes.PluginContext,
	connection *pkgtypes.Connection,
	client *CT,
) error {
	// make sure we don't already have an informer for this connection
	if _, ok := i.informers[connection.ID]; ok {
		return fmt.Errorf("informer already exists for connection %s", connection.ID)
	}

	// get an informer from the factory
	cache, err := i.factory.CreateInformer(ctx, connection, client)
	if err != nil {
		return fmt.Errorf("error creating informer for connection %s: %w", connection.ID, err)
	}

	// add to map
	i.informers[connection.ID] = informer[IT]{informer: cache, cancel: make(chan struct{})}
	return nil
}

// RegisterResource registers a resource with the informer manager for a given client. This is
// called when a new context has been started for the first time.
func (i *InformerManager[CT, IT]) RegisterResource(
	_ *pkgtypes.PluginContext,
	connection *pkgtypes.Connection,
	resource types.ResourceMeta,
) error {
	// get the informer
	informer, ok := i.informers[connection.ID]
	if !ok {
		return fmt.Errorf("informer not found for connection %s", connection.ID)
	}

	// register the resource
	return i.registerHandler(informer.informer, resource)
}

// Startconnection starts the informer for a given resource connection, and sends events to the given
// event channels.
func (i *InformerManager[CT, IT]) StartConnection(
	_ *pkgtypes.PluginContext,
	connectionID string,
) error {
	// make sure the informer exists before signalling a start
	_, ok := i.informers[connectionID]
	if !ok {
		return fmt.Errorf("informer not found for connection %s", connectionID)
	}

	i.startconnectionChan <- connectionID
	return nil
}

// Stopconnection stops the informer for a given resource connection.
func (i *InformerManager[CT, IT]) StopConnection(
	_ *pkgtypes.PluginContext,
	connectionID string,
) error {
	// make sure the informer exists before signalling a stop
	_, ok := i.informers[connectionID]
	if !ok {
		return fmt.Errorf("informer not found for connection %s", connectionID)
	}
	i.stopconnectionChan <- connectionID
	return nil
}
