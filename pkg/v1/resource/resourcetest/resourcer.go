package resourcetest

import (
	"context"
	"sync/atomic"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
)

// StubResourcer is a configurable test double for Resourcer[ClientT].
// Every method checks for a non-nil function field first, then falls back to returning nil, nil.
type StubResourcer[ClientT any] struct {
	GetFunc    func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.GetInput) (*resource.GetResult, error)
	ListFunc   func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.ListInput) (*resource.ListResult, error)
	FindFunc   func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error)
	CreateFunc func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.CreateInput) (*resource.CreateResult, error)
	UpdateFunc func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.UpdateInput) (*resource.UpdateResult, error)
	DeleteFunc func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.DeleteInput) (*resource.DeleteResult, error)

	GetCalls, ListCalls, FindCalls, CreateCalls, UpdateCalls, DeleteCalls atomic.Int32
}

func (s *StubResourcer[ClientT]) Get(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.GetInput) (*resource.GetResult, error) {
	s.GetCalls.Add(1)
	if s.GetFunc != nil {
		return s.GetFunc(ctx, client, meta, input)
	}
	return nil, nil
}

func (s *StubResourcer[ClientT]) List(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.ListInput) (*resource.ListResult, error) {
	s.ListCalls.Add(1)
	if s.ListFunc != nil {
		return s.ListFunc(ctx, client, meta, input)
	}
	return nil, nil
}

func (s *StubResourcer[ClientT]) Find(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.FindInput) (*resource.FindResult, error) {
	s.FindCalls.Add(1)
	if s.FindFunc != nil {
		return s.FindFunc(ctx, client, meta, input)
	}
	return nil, nil
}

func (s *StubResourcer[ClientT]) Create(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.CreateInput) (*resource.CreateResult, error) {
	s.CreateCalls.Add(1)
	if s.CreateFunc != nil {
		return s.CreateFunc(ctx, client, meta, input)
	}
	return nil, nil
}

func (s *StubResourcer[ClientT]) Update(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.UpdateInput) (*resource.UpdateResult, error) {
	s.UpdateCalls.Add(1)
	if s.UpdateFunc != nil {
		return s.UpdateFunc(ctx, client, meta, input)
	}
	return nil, nil
}

func (s *StubResourcer[ClientT]) Delete(ctx context.Context, client *ClientT, meta resource.ResourceMeta, input resource.DeleteInput) (*resource.DeleteResult, error) {
	s.DeleteCalls.Add(1)
	if s.DeleteFunc != nil {
		return s.DeleteFunc(ctx, client, meta, input)
	}
	return nil, nil
}

// FilterableResourcer embeds StubResourcer and adds FilterableProvider support.
type FilterableResourcer[ClientT any] struct {
	StubResourcer[ClientT]
	FilterFieldsFunc func(ctx context.Context, connectionID string) ([]resource.FilterField, error)
	FilterFieldsCalls atomic.Int32
}

func (f *FilterableResourcer[ClientT]) FilterFields(ctx context.Context, connectionID string) ([]resource.FilterField, error) {
	f.FilterFieldsCalls.Add(1)
	if f.FilterFieldsFunc != nil {
		return f.FilterFieldsFunc(ctx, connectionID)
	}
	return nil, nil
}

// SearchableResourcer embeds StubResourcer and adds TextSearchProvider support.
type SearchableResourcer[ClientT any] struct {
	StubResourcer[ClientT]
	SearchFunc  func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, query string, limit int) (*resource.FindResult, error)
	SearchCalls atomic.Int32
}

func (s *SearchableResourcer[ClientT]) Search(ctx context.Context, client *ClientT, meta resource.ResourceMeta, query string, limit int) (*resource.FindResult, error) {
	s.SearchCalls.Add(1)
	if s.SearchFunc != nil {
		return s.SearchFunc(ctx, client, meta, query, limit)
	}
	return nil, nil
}

// FilterableSearchableResourcer embeds StubResourcer and implements both
// FilterableProvider and TextSearchProvider.
type FilterableSearchableResourcer[ClientT any] struct {
	StubResourcer[ClientT]
	FilterFieldsFunc  func(ctx context.Context, connectionID string) ([]resource.FilterField, error)
	FilterFieldsCalls atomic.Int32
	SearchFunc        func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, query string, limit int) (*resource.FindResult, error)
	SearchCalls       atomic.Int32
}

func (fs *FilterableSearchableResourcer[ClientT]) FilterFields(ctx context.Context, connectionID string) ([]resource.FilterField, error) {
	fs.FilterFieldsCalls.Add(1)
	if fs.FilterFieldsFunc != nil {
		return fs.FilterFieldsFunc(ctx, connectionID)
	}
	return nil, nil
}

func (fs *FilterableSearchableResourcer[ClientT]) Search(ctx context.Context, client *ClientT, meta resource.ResourceMeta, query string, limit int) (*resource.FindResult, error) {
	fs.SearchCalls.Add(1)
	if fs.SearchFunc != nil {
		return fs.SearchFunc(ctx, client, meta, query, limit)
	}
	return nil, nil
}

// WatchableResourcer embeds StubResourcer and adds Watcher[ClientT] support.
// Optionally implements SyncPolicyDeclarer and DefinitionProvider.
type WatchableResourcer[ClientT any] struct {
	StubResourcer[ClientT]
	WatchFunc     func(ctx context.Context, client *ClientT, meta resource.ResourceMeta, sink resource.WatchEventSink) error
	PolicyVal     *resource.SyncPolicy
	DefinitionVal *resource.ResourceDefinition
}

func (w *WatchableResourcer[ClientT]) Watch(ctx context.Context, client *ClientT, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
	if w.WatchFunc != nil {
		return w.WatchFunc(ctx, client, meta, sink)
	}
	// Default: block until context cancelled.
	<-ctx.Done()
	return nil
}

func (w *WatchableResourcer[ClientT]) SyncPolicy() resource.SyncPolicy {
	if w.PolicyVal != nil {
		return *w.PolicyVal
	}
	return resource.SyncOnConnect
}

func (w *WatchableResourcer[ClientT]) Definition() resource.ResourceDefinition {
	if w.DefinitionVal != nil {
		return *w.DefinitionVal
	}
	return resource.ResourceDefinition{}
}
