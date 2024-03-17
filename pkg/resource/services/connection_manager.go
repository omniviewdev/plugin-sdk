package services

import (
	"fmt"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/factories"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// TODO - rename to AuthorizationsManager

// ConnectionManager is an interface that resource managers must implement
// in order to manage working against resources within a authd resources. T is the type of the client
// that the resource manager will manage, and O is the options type that the resource manager will use.
//
// Authorizations in the context of this plugin are not to be confused with Kubernetes connections,
// which are a way to divide cluster resources between multiple users. Instead, connections
// in the context of the plugin are a way to have orchestrate multiple clients within the resource
// backend to separate contexts and resources.
//
// For example, a user may have multiple AWS accounts and roles they would like to incorporate
// into the IDE. However, each account (and role) has it's own authorizations, and must used different
// credentials to access. As such, the user would like to separate these backends into different
// connections, so that they can easily switch between them. For this example, a resource auth
// would consist of the account and role, and the resource auth manager would be responsible for
// setting up, switching between, and managing these authd clients.
//
// The resource auth manager is designed to be used in conjunction with the resource manager, and
// acts as a provider that resourcers can use to get the appropriate client for the given auth.
// When creating a new resource manager, the type and options type should be provided to the auth manager
// so that it can be provided the necessary client factory to create and manage clients for the resource manager.
type ConnectionManager[ClientT any] interface {
	// InjectConnection injects the connection by id into the plugin context
	InjectConnection(ctx *types.PluginContext, id string) error

	// LoadConnections loads the possible connections for the resource manager
	LoadConnections(ctx *types.PluginContext) ([]types.Connection, error)

	// ListConnections lists the connections for the resource manager
	ListConnections(ctx *types.PluginContext) ([]types.Connection, error)

	// GetConnection returns the connection for the resource manager
	GetConnection(ctx *types.PluginContext, id string) (types.Connection, error)

	// UpdateConnection updates the connection for the resource manager
	UpdateConnection(
		ctx *types.PluginContext,
		connection types.Connection,
	) (types.Connection, error)

	// DeleteConnection deletes the connection for the resource manager
	DeleteConnection(ctx *types.PluginContext, id string) error

	// Create creates a new connection for the resource manager
	// This method should perform any necessary setup so that client retrieval
	// can be done for the auth after it is created
	Create(ctx *types.PluginContext, auth types.Connection) error

	// RemoveAuthorization removes a auth from the resource manager
	Remove(ctx *types.PluginContext, id string) error

	// ListAuthorizations lists the connections for the resource manager
	List(ctx *types.PluginContext) ([]types.Connection, error)

	// GetConnectionClient returns the necessary client for the given auth
	// This method should be used by resourcers to get the client for the given
	// auth.
	GetConnectionClient(ctx *types.PluginContext, id string) (*ClientT, error)

	// RefreshConnectionClient performs any actions necessary to refresh a client for the given auth.
	// This may include refreshing credentials, or re-initializing the client if it has been
	// invalidated.
	RefreshConnectionClient(ctx *types.PluginContext, id string) error

	// GetCurrentConnectionClient returns the current client for the given auth
	GetCurrentConnectionClient(ctx *types.PluginContext) (*ClientT, error)

	// RefreshCurrentConnectionClient performs any actions necessary to refresh the current client for the given auth.
	RefreshCurrentConnectionClient(ctx *types.PluginContext) error
}

// NewConnectionManager creates a new service to manage the various auth contexts for a
// plugin with an authenticated backend.
func NewConnectionManager[ClientT any](
	factory factories.ResourceClientFactory[ClientT],
	loader func(*types.PluginContext) ([]types.Connection, error),
) ConnectionManager[ClientT] {
	return &connectionManager[ClientT]{
		factory:     factory,
		loader:      loader,
		connections: make(map[string]types.Connection),
		clients:     make(map[string]*ClientT),
	}
}

type connectionManager[ClientT any] struct {
	factory     factories.ResourceClientFactory[ClientT]
	loader      func(*types.PluginContext) ([]types.Connection, error)
	connections map[string]types.Connection
	clients     map[string]*ClientT
	sync.RWMutex
}

func (r *connectionManager[ClientT]) InjectConnection(
	ctx *types.PluginContext,
	id string,
) error {
	r.RLock()
	defer r.RUnlock()

	auth, ok := r.connections[id]
	if !ok {
		return fmt.Errorf("auth %s does not exist", id)
	}
	ctx.Connection = &auth
	return nil
}

func (r *connectionManager[ClientT]) LoadConnections(
	ctx *types.PluginContext,
) ([]types.Connection, error) {
	if r.loader == nil {
		// If hasn't been specified, just return nothing
		return nil, nil
	}

	connections, err := r.loader(ctx)
	if err != nil {
		return nil, err
	}

	// perform an upsert on our existing connections
	if len(r.connections) == 0 {
		// if we have no existing connections, just set the new ones
		for _, conn := range connections {
			r.connections[conn.ID] = conn
		}
		return connections, nil
	}

	// if we have existing connections, we need to merge the new ones in
	for _, conn := range connections {
		existing, ok := r.connections[conn.ID]
		if !ok {
			r.connections[conn.ID] = conn
			continue
		}

		// we have an existing connection, merge in the data and labels
		for k, v := range conn.Data {
			if _, ok = existing.Data[k]; !ok {
				existing.Data[k] = v
			}
		}
		for k, v := range conn.Labels {
			if _, ok = existing.Labels[k]; !ok {
				existing.Labels[k] = v
			}
		}
	}

	return connections, nil
}

func (r *connectionManager[ClientT]) ListConnections(
	ctx *types.PluginContext,
) ([]types.Connection, error) {
	r.RLock()
	defer r.RUnlock()
	connections := make([]types.Connection, 0, len(r.connections))
	for _, auth := range r.connections {
		connections = append(connections, auth)
	}
	return connections, nil
}

func (r *connectionManager[ClientT]) GetConnection(
	ctx *types.PluginContext,
	id string,
) (types.Connection, error) {
	r.RLock()
	defer r.RUnlock()
	auth, ok := r.connections[id]
	if !ok {
		return types.Connection{}, fmt.Errorf("auth %s does not exist", id)
	}
	return auth, nil
}

func (r *connectionManager[ClientT]) UpdateConnection(
	ctx *types.PluginContext,
	conn types.Connection,
) (types.Connection, error) {
	r.Lock()
	defer r.Unlock()
	current, ok := r.connections[conn.ID]
	if !ok {
		return types.Connection{}, fmt.Errorf("connection %s does not exist", conn.ID)
	}
	if conn.Name != "" {
		current.Name = conn.Name
	}
	if conn.Description != "" {
		current.Description = conn.Description
	}
	if conn.Avatar != "" {
		current.Avatar = conn.Avatar
	}
	if len(conn.Labels) > 0 {
		for k, v := range conn.Labels {
			current.Labels[k] = v
		}
	}
	if len(conn.Data) > 0 {
		for k, v := range conn.Data {
			current.Data[k] = v
		}
	}

	r.connections[conn.ID] = current
	return current, nil
}

func (r *connectionManager[ClientT]) DeleteConnection(
	ctx *types.PluginContext,
	id string,
) error {
	r.Lock()
	defer r.Unlock()
	client, ok := r.clients[id]
	if ok && client != nil {
		delete(r.clients, id)
		if err := r.factory.StopClient(ctx, client); err != nil {
			return err
		}
	}
	delete(r.connections, id)
	return nil
}

func (r *connectionManager[ClientT]) Create(
	ctx *types.PluginContext,
	auth types.Connection,
) error {
	r.Lock()
	defer r.Unlock()

	_, hasClient := r.clients[auth.ID]
	_, hasAuthorization := r.connections[auth.ID]

	if hasClient || hasAuthorization {
		return fmt.Errorf("auth %s already exists", auth.ID)
	}

	client, err := r.factory.CreateClient(ctx)
	if err != nil {
		return err
	}

	r.clients[auth.ID] = client
	r.connections[auth.ID] = auth

	return nil
}

func (r *connectionManager[ClientT]) Remove(
	ctx *types.PluginContext,
	id string,
) error {
	r.Lock()
	defer r.Unlock()

	client, ok := r.clients[id]

	if ok && client != nil {
		delete(r.clients, id)
		if err := r.factory.StopClient(ctx, client); err != nil {
			return err
		}
	}

	delete(r.connections, id)

	return nil
}

func (r *connectionManager[ClientT]) List(
	_ *types.PluginContext,
) ([]types.Connection, error) {
	r.RLock()
	defer r.RUnlock()

	connections := make([]types.Connection, 0, len(r.connections))

	for _, auth := range r.connections {
		connections = append(connections, auth)
	}
	return connections, nil
}

func (r *connectionManager[ClientT]) GetConnectionClient(
	_ *types.PluginContext,
	id string,
) (*ClientT, error) {
	r.RLock()
	defer r.RUnlock()
	client, ok := r.clients[id]

	if !ok {
		return nil, fmt.Errorf("client for auth context %s does not exist", id)
	}

	return client, nil
}

func (r *connectionManager[ClientT]) RefreshConnectionClient(
	ctx *types.PluginContext,
	id string,
) error {
	r.RLock()
	defer r.RUnlock()

	client, clientOk := r.clients[id]
	_, authOk := r.connections[id]

	if !clientOk {
		return fmt.Errorf("client for auth %s does not exist", id)
	}
	if !authOk {
		return fmt.Errorf("auth %s does not exist", id)
	}

	return r.factory.RefreshClient(ctx, client)
}

func (r *connectionManager[ClientT]) GetCurrentConnectionClient(
	ctx *types.PluginContext,
) (*ClientT, error) {
	r.RLock()
	defer r.RUnlock()

	if ctx.Connection == nil {
		return nil, fmt.Errorf("auth context is nil")
	}

	client, ok := r.clients[ctx.Connection.ID]

	if !ok {
		return nil, fmt.Errorf("client for auth context %s does not exist", ctx.Connection.ID)
	}

	return client, nil
}

func (r *connectionManager[ClientT]) RefreshCurrentConnectionClient(
	ctx *types.PluginContext,
) error {
	r.RLock()
	defer r.RUnlock()
	if ctx.Connection == nil {
		return fmt.Errorf("auth context is nil")
	}
	client, clientOk := r.clients[ctx.Connection.ID]
	_, authOk := r.connections[ctx.Connection.ID]

	if !clientOk {
		return fmt.Errorf("client for auth %s does not exist", ctx.Connection.ID)
	}
	if !authOk {
		return fmt.Errorf("auth %s does not exist", ctx.Connection.ID)
	}

	return r.factory.RefreshClient(ctx, client)
}
