package resourceconformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
)

// This entire adapter is test-only. It is a finite native source, not the
// configurable forwarding TestProvider (whose successful CRUD defaults would
// conceal read-only limitations). No product code imports or registers it.
type nativeConnections struct {
	corpus Corpus
	state  string
}

func (p *nativeConnections) LoadConnections(context.Context) ([]types.Connection, error) {
	out := make([]types.Connection, 0, len(p.corpus.Scenarios))
	for _, s := range p.corpus.Scenarios {
		out = append(out, types.Connection{ID: s.Name, Name: s.Name})
	}
	return out, nil
}

func (p *nativeConnections) CreateClient(ctx context.Context) (*Scenario, error) {
	session := resource.SessionFromContext(ctx)
	if session == nil || session.Connection == nil {
		return nil, nativeError("INVALID_INPUT", "missing source route")
	}
	for _, s := range p.corpus.Scenarios {
		if s.Name == session.Connection.ID {
			return &s, nil
		}
	}
	return nil, nativeError("NOT_FOUND", "unknown source route")
}

func (p *nativeConnections) DestroyClient(_ context.Context, s *Scenario) error {
	return os.WriteFile(filepath.Join(p.state, s.Name+".closed"), []byte("closed"), 0600)
}

func (p *nativeConnections) CheckConnection(_ context.Context, c *types.Connection, _ *Scenario) (types.ConnectionStatus, error) {
	return types.ConnectionStatus{Connection: c, Status: types.ConnectionStatusConnected}, nil
}

func (*nativeConnections) GetNamespaces(context.Context, *Scenario) ([]string, error) {
	// Scope is not smuggled through namespace discovery.
	return nil, nil
}

type nativeSource struct {
	corpus Corpus
	typeID string
	state  string
}

func (p *nativeSource) admit(ctx context.Context, s *Scenario, meta resource.ResourceMeta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Type != p.typeID {
		return nativeError("INVALID_INPUT", "resource type outside selected source")
	}
	// Exact native pattern keys intentionally cannot be parsed as GVK. Do not
	// populate missing metadata or treat the empty metadata as a native catalog.
	if meta != (resource.ResourceMeta{}) {
		return nativeError("INVALID_INPUT", "unexpected legacy metadata")
	}
	return nil
}

func (p *nativeSource) List(ctx context.Context, s *Scenario, meta resource.ResourceMeta, in resource.ListInput) (*resource.ListResult, error) {
	if err := p.admit(ctx, s, meta); err != nil {
		return nil, err
	}
	if len(in.Namespaces) != 0 || len(in.Order) != 0 || in.Pagination.Page != 0 {
		return nil, nativeError("INVALID_INPUT", "namespace/order/offset is not native source selection")
	}
	if in.Pagination.Cursor == "verification:wait-for-cancel" {
		if err := os.WriteFile(filepath.Join(p.state, s.Name+".waiting"), []byte("waiting"), 0600); err != nil {
			return nil, err
		}
		<-ctx.Done()
		if err := os.WriteFile(filepath.Join(p.state, s.Name+".cancelled"), []byte("cancelled"), 0600); err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}
	for _, page := range s.Pages {
		if page.Cursor != in.Pagination.Cursor {
			continue
		}
		if in.Pagination.PageSize > 0 && len(page.Records) > in.Pagination.PageSize {
			return nil, nativeError("INVALID_INPUT", "requested page limit below fixed native page")
		}
		out := &resource.ListResult{Success: true, TotalCount: -1, NextCursor: page.Next}
		for _, name := range page.Records {
			out.Result = append(out.Result, p.corpus.Record(name).Body)
		}
		return out, nil
	}
	return nil, nativeError("INVALID_INPUT", "unknown native cursor")
}

func (p *nativeSource) Get(ctx context.Context, s *Scenario, meta resource.ResourceMeta, in resource.GetInput) (*resource.GetResult, error) {
	if err := p.admit(ctx, s, meta); err != nil {
		return nil, err
	}
	if in.Namespace != "" {
		return nil, nativeError("INVALID_INPUT", "namespace cannot represent native identity scope")
	}
	var found *Record
	for _, page := range s.Pages {
		for _, name := range page.Records {
			r := p.corpus.Record(name)
			if r.ID == in.ID {
				if found != nil {
					return nil, nativeError("INVALID_INPUT", "ambiguous native ID; current Get has no exact scope tuple")
				}
				found = &r
			}
		}
	}
	if found == nil {
		return nil, nativeError("NOT_FOUND", "native resource not observed in selected source")
	}
	return &resource.GetResult{Success: true, Result: found.Body}, nil
}

func (*nativeSource) Find(context.Context, *Scenario, resource.ResourceMeta, resource.FindInput) (*resource.FindResult, error) {
	return nil, nativeError("UNSUPPORTED", "native server filtering is not supported")
}

func (*nativeSource) Create(context.Context, *Scenario, resource.ResourceMeta, resource.CreateInput) (*resource.CreateResult, error) {
	return nil, nativeError("UNSUPPORTED", "verification source is read-only")
}

func (*nativeSource) Update(context.Context, *Scenario, resource.ResourceMeta, resource.UpdateInput) (*resource.UpdateResult, error) {
	return nil, nativeError("UNSUPPORTED", "verification source is read-only")
}

func (*nativeSource) Delete(context.Context, *Scenario, resource.ResourceMeta, resource.DeleteInput) (*resource.DeleteResult, error) {
	return nil, nativeError("UNSUPPORTED", "verification source is read-only")
}

// Bundled and absent are selected native source states, not schema errors.
func (p *nativeSource) GetResourceSchema(ctx context.Context, s *Scenario, meta resource.ResourceMeta) (json.RawMessage, error) {
	if err := p.admit(ctx, s, meta); err != nil {
		return nil, err
	}
	return s.Schema, nil
}

func nativeError(code, message string) error {
	return &resource.ResourceOperationError{Code: code, Title: "Native verification source", Message: message}
}
