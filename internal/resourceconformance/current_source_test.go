package resourceconformance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"sync"

	resource "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Native numeric policy: integer tokens remain exact signed/unsigned integers;
// createdAt is a declared native timestamp. No floating-point JSON intermediary.
func nativeValue(raw json.RawMessage) resource.Value {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		panic(err)
	}
	var convert func(any) resource.Value
	convert = func(x any) resource.Value {
		switch x := x.(type) {
		case nil:
			return resource.Value{Kind: "null"}
		case bool:
			return resource.Value{Kind: "bool", Bool: x}
		case string:
			return resource.Value{Kind: "string", String: x}
		case json.Number:
			if _, err := strconv.ParseInt(string(x), 10, 64); err == nil {
				return resource.Value{Kind: "int64", Int: string(x)}
			}
			if _, err := strconv.ParseUint(string(x), 10, 64); err == nil {
				return resource.Value{Kind: "uint64", Uint: string(x)}
			}
			panic("fixture requires exact native integer")
		case []any:
			out := resource.Value{Kind: "list", List: make([]resource.Value, 0, len(x))}
			for _, item := range x {
				out.List = append(out.List, convert(item))
			}
			return out
		case map[string]any:
			out := resource.Value{Kind: "map", Map: make(map[string]resource.Value, len(x))}
			for k, item := range x {
				out.Map[k] = convert(item)
				if k == "createdAt" {
					out.Map[k] = resource.Value{Kind: "timestamp", Timestamp: item.(string)}
				}
			}
			return out
		default:
			panic("unsupported fixture value")
		}
	}
	return convert(v)
}

func scopeEntries(scope map[string]string) []resource.ScopeEntry {
	out := make([]resource.ScopeEntry, 0, len(scope))
	for k, v := range scope {
		out = append(out, resource.ScopeEntry{Dimension: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dimension < out[j].Dimension })
	return out
}
func queryScope(s Scenario) resource.QueryScope {
	out := make(resource.QueryScope, 0, len(s.Selection))
	for k, v := range s.Selection {
		values := append([]string{}, v...)
		sort.Strings(values)
		out = append(out, resource.ScopeConstraint{Dimension: k, Match: resource.ScopeOneOf, Values: &values})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dimension < out[j].Dimension })
	return out
}
func token(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func representation(s Scenario) resource.Representation {
	schema := resource.SchemaBinding{State: "absent", Validation: "none", Unvalidated: []string{}}
	if s.Schema != nil {
		schema.State = "available"
		schema.Revision = token("schema:opaque/1")
		schema.Dialect = "https://json-schema.org/draft/2020-12/schema"
		schema.Document = s.Schema
	}
	return resource.Representation{ID: "native", Revision: "representation:opaque/1", Schema: schema}
}
func nativeRecord(r Record, s Scenario) resource.Record {
	return resource.Record{Reference: resource.ResourceReference{Address: resource.ResourceAddress{Provider: r.Provider, Authority: r.Authority, Type: r.Type, Scope: scopeEntries(r.Scope), ID: r.ID}, Incarnation: token(r.Incarnation)}, Body: nativeValue(r.Body), Representation: representation(s), Coverage: resource.Coverage{Kind: "complete", Paths: []string{}}, SourceRevision: token(r.Revision)}
}
func nativeError(code, message string) error { return resource.NewError(code, message) }

type nativeProvider struct {
	corpus                Corpus
	state                 string
	profile               string
	mu                    sync.Mutex
	active                map[string]bool
	bindingEpochs         map[string]string
	nativeCatalogRevision string
}

func newNativeProvider(state, profile string) *nativeProvider {
	return &nativeProvider{corpus: NativeCorpus(), state: state, profile: profile, active: map[string]bool{}, bindingEpochs: map[string]string{}, nativeCatalogRevision: "catalog:opaque/1"}
}
func (p *nativeProvider) scenario(route resource.AccessRoute) (Scenario, error) {
	p.mu.Lock()
	active := p.active[route.Connection]
	p.mu.Unlock()
	if !active {
		return Scenario{}, nativeError("INVALID_REQUEST", "source is not started")
	}
	for _, s := range p.corpus.Scenarios {
		if s.Name == route.Connection {
			return s, nil
		}
	}
	return Scenario{}, nativeError("NOT_FOUND", "unknown source route")
}
func (p *nativeProvider) Catalog(ctx context.Context, in resource.CatalogRequest) (resource.Catalog, error) {
	s, err := p.scenario(in.Route)
	if err != nil {
		return resource.Catalog{}, err
	}
	if err = ctx.Err(); err != nil {
		return resource.Catalog{}, err
	}
	r := p.corpus.Record(s.Pages[0].Records[0])
	dims := []resource.Dimension{}
	for _, e := range scopeEntries(r.Scope) {
		dims = append(dims, resource.Dimension{Name: e.Dimension})
	}
	ops := []resource.Operation{resource.OperationGet, resource.OperationEnumerate}
	if p.profile == "get-only" {
		ops = []resource.Operation{resource.OperationGet}
	}
	if p.profile == "native-query-only" {
		ops = []resource.Operation{resource.OperationNativeQuery}
	}
	if p.profile == "catalog-only" {
		ops = []resource.Operation{}
	}
	if p.profile == "enumerate-only" {
		ops = []resource.Operation{resource.OperationEnumerate}
	}
	caps := make([]resource.Capability, 0, len(ops))
	for _, op := range ops {
		caps = append(caps, resource.Capability{Operation: op, Scope: queryScope(s)})
	}
	p.mu.Lock()
	bindingEpoch := p.bindingEpochs[in.Route.Connection]
	p.mu.Unlock()
	revision, err := resource.BindCatalogRevision(bindingEpoch, p.nativeCatalogRevision)
	if err != nil {
		return resource.Catalog{}, err
	}
	return resource.Catalog{Provider: r.Provider, Revision: revision, Coverage: resource.Coverage{Kind: "complete", Paths: []string{}}, Types: []resource.ResourceType{{Type: s.Type, DisplayName: s.Type, IdentityDimensions: dims, QueryDimensions: dims, Representations: []resource.Representation{representation(s)}, Capabilities: caps}}}, nil
}
func (p *nativeProvider) LoadConnections(context.Context) ([]types.Connection, error) {
	out := []types.Connection{}
	for _, s := range p.corpus.Scenarios {
		out = append(out, types.Connection{ID: s.Name, Name: s.Name})
	}
	return out, nil
}
func (p *nativeProvider) GetConnection(_ context.Context, id string) (types.Connection, error) {
	for _, s := range p.corpus.Scenarios {
		if s.Name == id {
			return types.Connection{ID: id, Name: id}, nil
		}
	}
	return types.Connection{}, nativeError("NOT_FOUND", "unknown route")
}
func (p *nativeProvider) StartConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	c, err := p.GetConnection(ctx, id)
	if err != nil {
		return types.ConnectionStatus{}, err
	}
	p.mu.Lock()
	if !p.active[id] {
		bindingEpoch, err := resource.NewCatalogBindingEpoch()
		if err != nil {
			p.mu.Unlock()
			return types.ConnectionStatus{}, err
		}
		p.bindingEpochs[id] = bindingEpoch
	}
	p.active[id] = true
	p.mu.Unlock()
	return types.ConnectionStatus{Connection: &c, Status: types.ConnectionStatusConnected}, nil
}
func (p *nativeProvider) CheckConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	_, err := p.scenario(resource.AccessRoute{Connection: id})
	if err != nil {
		return types.ConnectionStatus{}, err
	}
	c, _ := p.GetConnection(ctx, id)
	return types.ConnectionStatus{Connection: &c, Status: types.ConnectionStatusConnected}, nil
}
func (p *nativeProvider) StopConnection(ctx context.Context, id string) (types.Connection, error) {
	p.mu.Lock()
	delete(p.active, id)
	delete(p.bindingEpochs, id)
	p.mu.Unlock()
	if err := os.WriteFile(filepath.Join(p.state, id+".closed"), []byte("closed"), 0600); err != nil {
		return types.Connection{}, err
	}
	return p.GetConnection(ctx, id)
}
func (p *nativeProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.active)
	clear(p.bindingEpochs)
	return nil
}

type nativeReader struct{ *nativeProvider }

func (p *nativeReader) Get(ctx context.Context, in resource.GetRequest) (resource.Record, error) {
	s, err := p.scenario(in.Route)
	if err != nil {
		return resource.Record{}, err
	}
	if err = ctx.Err(); err != nil {
		return resource.Record{}, err
	}
	if in.Representation != "native" {
		return resource.Record{}, nativeError("INVALID_REQUEST", "unknown representation")
	}
	if in.Reference.Address.Authority == "" {
		return resource.Record{}, nativeError("INVALID_REQUEST", "missing host authority")
	}
	for _, page := range s.Pages {
		for _, name := range page.Records {
			r := nativeRecord(p.corpus.Record(name), s)
			r.Reference.Address.Authority = in.Reference.Address.Authority
			if reflect.DeepEqual(r.Reference.Address, in.Reference.Address) && (in.Reference.Incarnation == nil || reflect.DeepEqual(in.Reference.Incarnation, r.Reference.Incarnation)) {
				return r, nil
			}
		}
	}
	return resource.Record{}, nativeError("NOT_FOUND", "exact native address not observed")
}

type nativeEnumerable struct{ *nativeReader }

func (p *nativeEnumerable) Enumerate(ctx context.Context, in resource.EnumerateRequest) (resource.SourcePage, error) {
	return p.page(ctx, in)
}

type nativeEnumerationOnly struct{ *nativeProvider }

func (p *nativeEnumerationOnly) Enumerate(ctx context.Context, in resource.EnumerateRequest) (resource.SourcePage, error) {
	return p.page(ctx, in)
}
func (p *nativeProvider) page(ctx context.Context, in resource.EnumerateRequest) (resource.SourcePage, error) {
	s, err := p.scenario(in.Target.Route)
	if err != nil {
		return resource.SourcePage{}, err
	}
	r := p.corpus.Record(s.Pages[0].Records[0])
	if in.Target.Provider != r.Provider || in.Target.Authority == "" || in.Target.Type != s.Type || in.Target.Representation != "native" || !reflect.DeepEqual(in.Target.Scope, queryScope(s)) {
		return resource.SourcePage{}, nativeError("INVALID_REQUEST", "target outside selected native source")
	}
	cursor := ""
	if in.Cursor != nil {
		cursor = *in.Cursor
	}
	if cursor == "verification:wait-for-cancel" {
		if err := os.WriteFile(filepath.Join(p.state, s.Name+".waiting"), []byte("waiting"), 0600); err != nil {
			return resource.SourcePage{}, err
		}
		<-ctx.Done()
		if err := os.WriteFile(filepath.Join(p.state, s.Name+".cancelled"), []byte("cancelled"), 0600); err != nil {
			return resource.SourcePage{}, err
		}
		return resource.SourcePage{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return resource.SourcePage{}, err
	}
	for _, page := range s.Pages {
		if page.Cursor != cursor {
			continue
		}
		if in.PageSize > 0 && uint32(len(page.Records)) > in.PageSize {
			return resource.SourcePage{}, nativeError("INVALID_REQUEST", "page limit below native page")
		}
		coverage := resource.Coverage{Kind: "complete", Paths: []string{}}
		if s.Coverage.Enumeration == "partial" {
			coverage.Kind = "partial"
			coverage.Reason = "selected listing truncated; eventual visibility"
		}
		out := resource.SourcePage{Records: []resource.Record{}, NextCursor: token(page.Next), Count: resource.Count{Kind: "unknown"}, Coverage: coverage, Consistency: s.Coverage.Consistency}
		for _, name := range page.Records {
			record := nativeRecord(p.corpus.Record(name), s)
			record.Reference.Address.Authority = in.Target.Authority
			out.Records = append(out.Records, record)
		}
		return out, nil
	}
	return resource.SourcePage{}, nativeError("INVALID_REQUEST", "unknown native cursor")
}

// This profile has no Getter or Enumerator: native acquisition is independent.
type nativeQueryOnly struct{ *nativeProvider }

func (p *nativeQueryOnly) NativeQuery(ctx context.Context, in resource.NativeQueryRequest) (resource.SourcePage, error) {
	if in.Language != "fixture.compute.v1" || in.Query.Kind != "int64" {
		return resource.SourcePage{}, nativeError("UNSUPPORTED", "unsupported native fixture query")
	}
	n, err := strconv.ParseInt(in.Query.Int, 10, 64)
	if err != nil || n > 9223372036854775806 {
		return resource.SourcePage{}, nativeError("INVALID_REQUEST", "invalid compute operand")
	}
	page, err := p.page(ctx, resource.EnumerateRequest{Target: in.Target, PageSize: in.PageSize, Cursor: in.Cursor})
	if err != nil {
		return resource.SourcePage{}, err
	}
	// Computed output differs from input. This proves transport result encoding,
	// not CEL evaluation (which belongs to the host).
	page.Records[0].Body = resource.Value{Kind: "map", Map: map[string]resource.Value{"computed": {Kind: "int64", Int: strconv.FormatInt(n+1, 10)}, "beforeEpoch": {Kind: "timestamp", Timestamp: "1969-12-31T23:59:59.999999999Z"}, "nested": {Kind: "list", List: []resource.Value{{Kind: "uint64", Uint: "18446744073709551615"}, {Kind: "null"}, {Kind: "bool", Bool: true}}}}}
	return page, nil
}
