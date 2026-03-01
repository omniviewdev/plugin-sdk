package resourcetest

import (
	"context"
	"sync/atomic"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// StubConnectionProvider is a configurable test double for ConnectionProvider[ClientT].
// Every method checks for a non-nil function field first, then falls back to a sensible default.
type StubConnectionProvider[ClientT any] struct {
	CreateClientFunc    func(ctx context.Context) (*ClientT, error)
	DestroyClientFunc   func(ctx context.Context, client *ClientT) error
	LoadConnectionsFunc func(ctx context.Context) ([]types.Connection, error)
	CheckConnectionFunc func(ctx context.Context, conn *types.Connection, client *ClientT) (types.ConnectionStatus, error)
	GetNamespacesFunc   func(ctx context.Context, client *ClientT) ([]string, error)

	// Call counters for assertions (atomic for concurrent safety).
	CreateClientCalls  atomic.Int32
	DestroyClientCalls atomic.Int32
}

func (s *StubConnectionProvider[ClientT]) CreateClient(ctx context.Context) (*ClientT, error) {
	s.CreateClientCalls.Add(1)
	if s.CreateClientFunc != nil {
		return s.CreateClientFunc(ctx)
	}
	var zero ClientT
	return &zero, nil
}

func (s *StubConnectionProvider[ClientT]) DestroyClient(ctx context.Context, client *ClientT) error {
	s.DestroyClientCalls.Add(1)
	if s.DestroyClientFunc != nil {
		return s.DestroyClientFunc(ctx, client)
	}
	return nil
}

func (s *StubConnectionProvider[ClientT]) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	if s.LoadConnectionsFunc != nil {
		return s.LoadConnectionsFunc(ctx)
	}
	return []types.Connection{{ID: "conn-1", Name: "Test"}}, nil
}

func (s *StubConnectionProvider[ClientT]) CheckConnection(ctx context.Context, conn *types.Connection, client *ClientT) (types.ConnectionStatus, error) {
	if s.CheckConnectionFunc != nil {
		return s.CheckConnectionFunc(ctx, conn, client)
	}
	return types.ConnectionStatus{
		Connection: conn,
		Status:     types.ConnectionStatusConnected,
	}, nil
}

func (s *StubConnectionProvider[ClientT]) GetNamespaces(ctx context.Context, client *ClientT) ([]string, error) {
	if s.GetNamespacesFunc != nil {
		return s.GetNamespacesFunc(ctx, client)
	}
	return []string{"default"}, nil
}

// WatchingConnectionProvider embeds StubConnectionProvider and adds ConnectionWatcher support.
type WatchingConnectionProvider[ClientT any] struct {
	StubConnectionProvider[ClientT]
	WatchConnectionsFunc func(ctx context.Context) (<-chan []types.Connection, error)
}

func (w *WatchingConnectionProvider[ClientT]) WatchConnections(ctx context.Context) (<-chan []types.Connection, error) {
	if w.WatchConnectionsFunc != nil {
		return w.WatchConnectionsFunc(ctx)
	}
	ch := make(chan []types.Connection)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// RefreshableConnectionProvider embeds StubConnectionProvider and adds ClientRefresher support.
type RefreshableConnectionProvider[ClientT any] struct {
	StubConnectionProvider[ClientT]
	RefreshClientFunc func(ctx context.Context, client *ClientT) error
	RefreshCalls      atomic.Int32
}

func (r *RefreshableConnectionProvider[ClientT]) RefreshClient(ctx context.Context, client *ClientT) error {
	r.RefreshCalls.Add(1)
	if r.RefreshClientFunc != nil {
		return r.RefreshClientFunc(ctx, client)
	}
	return nil
}
