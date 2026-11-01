package resourcev2

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func controllerRepresentation() Representation {
	return Representation{
		ID:       "document",
		Revision: "representation-1",
		Schema:   SchemaBinding{State: "absent"},
	}
}

func controllerAddress() ResourceAddress {
	return ResourceAddress{
		Provider:  "test",
		Authority: "authority",
		Type:      "opaque/type",
		Scope:     []ScopeEntry{},
		ID:        "opaque/id",
	}
}

func controllerRecord() Record {
	return Record{
		Reference:      ResourceReference{Address: controllerAddress()},
		Body:           Value{Kind: "map", Map: map[string]Value{}},
		Representation: controllerRepresentation(),
		Coverage:       Coverage{Kind: "complete", Paths: []string{}},
	}
}

func controllerTarget() ReadTarget {
	return ReadTarget{
		Route:          AccessRoute{Connection: "explicit"},
		Provider:       "test",
		Authority:      "authority",
		Type:           "opaque/type",
		Scope:          QueryScope{},
		Representation: "document",
	}
}

func controllerConfig() Config[string] {
	return Config[string]{Catalog: func(_ context.Context, request CatalogRequest) (Catalog, error) {
		if request.Route.Connection == "" {
			return Catalog{}, errors.New("missing route")
		}
		return Catalog{
			Provider: "test",
			Revision: "catalog-1",
			Coverage: Coverage{Kind: "complete", Paths: []string{}},
			Types: []ResourceType{
				{
					Type:               "opaque/type",
					IdentityDimensions: []Dimension{},
					QueryDimensions:    []Dimension{},
					Representations:    []Representation{controllerRepresentation()},
					Capabilities: []Capability{
						{
							Operation: OperationGet,
							Scope:     QueryScope{},
						},
						{Operation: OperationEnumerate, Scope: QueryScope{}},
						{Operation: OperationNativeQuery, Scope: QueryScope{}},
						{Operation: OperationCreate, Scope: QueryScope{}},
						{Operation: OperationMutationUpdate, Scope: QueryScope{}},
						{Operation: OperationDelete, Scope: QueryScope{}},
						{Operation: OperationAction, Scope: QueryScope{}},
						{Operation: OperationUpdate, Scope: QueryScope{}, Guarantee: BestEffortGuarantee},
					},
				},
			},
		}, nil
	}}
}

func newTestController(t *testing.T, cfg Config[string]) *Controller[string] {
	t.Helper()
	controller, err := NewController(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := controller.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return controller
}

func acceptedControllerContext(ctx context.Context, t *testing.T, provider Provider) context.Context {
	t.Helper()
	catalog, err := provider.Catalog(ctx, CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	return WithCatalogRevision(ctx, catalog.Revision)
}

func assertControllerCode(t *testing.T, err error, code string) {
	t.Helper()
	if ErrorCode(err) != code {
		t.Fatalf("error code = %q, want %q: %v", ErrorCode(err), code, err)
	}
}

func TestControllerRequiresOnlyCatalog(t *testing.T) {
	if _, err := NewController(Config[string]{}); err == nil {
		t.Fatal("missing catalog accepted")
	}
	c := newTestController(t, controllerConfig())
	catalog, err := c.Catalog(context.Background(), CatalogRequest{Route: AccessRoute{Connection: "explicit"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Types[0].Capabilities) != 0 {
		t.Fatal("unregistered operations advertised")
	}
	_, err = c.LoadConnections(context.Background())
	assertControllerCode(t, err, "UNSUPPORTED")
}

func TestControllerGetOnlyProvider(t *testing.T) {
	cfg := controllerConfig()
	calls := 0
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Get: func(_ context.Context, client *string, _ GetRequest) (Record, error) {
			if client != nil {
				t.Fatal("client fabricated without lifecycle")
			}
			calls++
			return controllerRecord(), nil
		}},
	}
	c := newTestController(t, cfg)
	catalog, err := c.Catalog(context.Background(), CatalogRequest{Route: AccessRoute{Connection: "explicit"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Types[0].Capabilities) != 1 || catalog.Types[0].Capabilities[0].Operation != OperationGet {
		t.Fatal("get-only catalog widened")
	}
	_, err = c.Get(
		acceptedControllerContext(context.Background(), t, c),
		GetRequest{
			Route:          AccessRoute{Connection: "explicit"},
			Reference:      ResourceReference{Address: controllerAddress()},
			Representation: "document",
		},
	)
	if err != nil || calls != 1 {
		t.Fatalf("get failed: %v; calls %d", err, calls)
	}
	_, err = c.Enumerate(
		acceptedControllerContext(context.Background(), t, c),
		EnumerateRequest{Target: controllerTarget()},
	)
	assertControllerCode(t, err, "UNSUPPORTED")
}

func TestControllerCreateOnlyRejectsOtherMutationsBeforeDispatch(t *testing.T) {
	cfg := controllerConfig()
	base := cfg.Catalog
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, err := base(ctx, request)
		catalog.Types[0].Capabilities = []Capability{{Operation: OperationCreate, Scope: QueryScope{}}}
		return catalog, err
	}
	calls := 0
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Mutate: func(context.Context, *string, MutationRequest) (Record, error) {
			calls++
			return controllerRecord(), nil
		}},
	}
	c := newTestController(t, cfg)
	ctx := acceptedControllerContext(context.Background(), t, c)
	catalog, err := c.Catalog(ctx, CatalogRequest{Route: controllerTarget().Route})
	if err != nil || len(catalog.Types[0].Capabilities) != 1 ||
		catalog.Types[0].Capabilities[0].Operation != OperationCreate {
		t.Fatalf("create-only discovery widened: %+v, %v", catalog, err)
	}
	for _, verb := range []string{"update", "delete", "replace"} {
		request := MutationRequest{
			Route:     controllerTarget().Route,
			Reference: controllerRecord().Reference,
			Operation: verb,
			Input:     Value{Kind: "null"},
		}
		_, err = AdmitOperation(ctx, c, request)
		assertControllerCode(t, err, "UNSUPPORTED")
		_, err = c.Mutate(ctx, request)
		assertControllerCode(t, err, "UNSUPPORTED")
	}
	if calls != 0 {
		t.Fatal("unsupported mutation reached callback")
	}
	_, err = c.Mutate(
		ctx,
		MutationRequest{
			Route:     controllerTarget().Route,
			Reference: controllerRecord().Reference,
			Operation: "create",
			Input:     Value{Kind: "null"},
		},
	)
	if err != nil || calls != 1 {
		t.Fatalf("create failed: %v; calls %d", err, calls)
	}
}

func TestMutationAdmissionUsesEachVerbScope(t *testing.T) {
	catalog, err := controllerConfig().Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := &catalog.Types[0]
	descriptor.IdentityDimensions = []Dimension{{Name: "region"}}
	descriptor.QueryDimensions = []Dimension{{Name: "region"}}
	descriptor.Capabilities = nil
	for _, operation := range []Operation{OperationCreate, OperationMutationUpdate, OperationDelete} {
		values := []string{string(operation)}
		descriptor.Capabilities = append(
			descriptor.Capabilities,
			Capability{
				Operation: operation,
				Scope:     QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}},
			},
		)
	}
	for _, operation := range []Operation{OperationCreate, OperationMutationUpdate, OperationDelete} {
		for _, region := range []string{"create", "mutationUpdate", "delete"} {
			address := controllerAddress()
			address.Scope = []ScopeEntry{{Dimension: "region", Value: region}}
			_, _, err := admitTarget(
				catalog,
				operationTarget{provider: address.Provider, typ: address.Type, address: &address, operation: operation},
			)
			if region == string(operation) {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertControllerCode(t, err, "UNSUPPORTED")
			}
		}
	}
}

func TestControllerNativeQueryOnlySourceCursor(t *testing.T) {
	cfg := controllerConfig()
	cursor := "native-source-token"
	cfg.Registrations = []Registration[string]{
		{
			Type: "opaque/type",
			NativeQuery: func(_ context.Context, _ *string, request NativeQueryRequest) (SourcePage, error) {
				if request.Cursor == nil || *request.Cursor != cursor {
					t.Fatal("native cursor changed")
				}
				return SourcePage{
					Records:     []Record{controllerRecord()},
					NextCursor:  &cursor,
					Count:       Count{Kind: "unknown"},
					Coverage:    Coverage{Kind: "complete"},
					Consistency: "bestEffort",
				}, nil
			},
		},
	}
	c := newTestController(t, cfg)
	page, err := c.NativeQuery(
		acceptedControllerContext(context.Background(), t, c),
		NativeQueryRequest{
			Target:   controllerTarget(),
			Language: "native",
			Query:    Value{Kind: "map", Map: map[string]Value{}},
			Cursor:   &cursor,
			PageSize: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == nil || *page.NextCursor != cursor || page.Count.Kind != "unknown" {
		t.Fatal("source continuation or unknown count changed")
	}
	catalog, err := c.Catalog(context.Background(), CatalogRequest{Route: controllerTarget().Route})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Types[0].Capabilities) != 1 || catalog.Types[0].Capabilities[0].Operation != OperationNativeQuery {
		t.Fatal("native-only catalog widened")
	}
	_, err = c.Get(
		acceptedControllerContext(context.Background(), t, c),
		GetRequest{
			Route:          controllerTarget().Route,
			Reference:      ResourceReference{Address: controllerAddress()},
			Representation: "document",
		},
	)
	assertControllerCode(t, err, "UNSUPPORTED")
}

func TestControllerReadOnlyNeverDispatchesWrites(t *testing.T) {
	cfg := controllerConfig()
	cfg.Registrations = []Registration[string]{
		{
			Type:      "opaque/type",
			Get:       func(context.Context, *string, GetRequest) (Record, error) { return controllerRecord(), nil },
			Enumerate: func(context.Context, *string, EnumerateRequest) (SourcePage, error) { return SourcePage{}, nil },
		},
	}
	c := newTestController(t, cfg)
	_, err := c.Mutate(
		acceptedControllerContext(context.Background(), t, c),
		MutationRequest{
			Route:     controllerTarget().Route,
			Reference: ResourceReference{Address: controllerAddress()},
			Operation: "replace",
			Input:     Value{Kind: "null"},
		},
	)
	assertControllerCode(t, err, "UNSUPPORTED")
	_, err = c.Action(
		acceptedControllerContext(context.Background(), t, c),
		ActionRequest{
			Route:     controllerTarget().Route,
			Reference: ResourceReference{Address: controllerAddress()},
			Action:    "run",
			Input:     Value{Kind: "null"},
		},
	)
	assertControllerCode(t, err, "UNSUPPORTED")
	_, err = c.Get(
		acceptedControllerContext(context.Background(), t, c),
		GetRequest{Reference: ResourceReference{Address: controllerAddress()}, Representation: "document"},
	)
	assertControllerCode(t, err, "INVALID_REQUEST")
}

func TestControllerCatalogMustDeclareHandlerOperation(t *testing.T) {
	cfg := controllerConfig()
	base := cfg.Catalog
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, err := base(ctx, request)
		catalog.Types[0].Capabilities = []Capability{}
		return catalog, err
	}
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Get: func(context.Context, *string, GetRequest) (Record, error) {
			t.Fatal("undeclared handler dispatched")
			return Record{}, nil
		}},
	}
	c := newTestController(t, cfg)
	_, err := c.Get(
		acceptedControllerContext(context.Background(), t, c),
		GetRequest{
			Route:          controllerTarget().Route,
			Reference:      ResourceReference{Address: controllerAddress()},
			Representation: "document",
		},
	)
	assertControllerCode(t, err, "UNSUPPORTED")
}

func controllerLifecycle(destroyed *atomic.Int32) *Lifecycle[string] {
	return &Lifecycle[string]{
		LoadConnections: func(context.Context) ([]types.Connection, error) {
			return []types.Connection{{ID: "explicit", Name: "Explicit"}, {ID: "other", Name: "Other"}}, nil
		},
		CreateClient: func(ctx context.Context, connection types.Connection) (*string, error) {
			client := connection.ID
			return &client, ctx.Err()
		},
		DestroyClient: func(ctx context.Context, _ *string) error {
			if ctx.Err() != nil {
				return errors.New("cleanup context canceled")
			}
			destroyed.Add(1)
			return nil
		},
	}
}

func startTestConnection(t *testing.T, c *Controller[string]) {
	t.Helper()
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
}

func TestControllerStopCancelsAndDestroysExactlyOnce(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	entered := make(chan struct{})
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Get: func(ctx context.Context, client *string, _ GetRequest) (Record, error) {
			if client == nil || *client != "explicit" {
				return Record{}, errors.New("wrong client route")
			}
			close(entered)
			<-ctx.Done()
			return Record{}, ctx.Err()
		}},
	}
	c := newTestController(t, cfg)
	startTestConnection(t, c)
	getDone := make(chan error, 1)
	go func() {
		_, err := c.Get(
			acceptedControllerContext(context.Background(), t, c),
			GetRequest{
				Route:          controllerTarget().Route,
				Reference:      ResourceReference{Address: controllerAddress()},
				Representation: "document",
			},
		)
		getDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("get did not start")
	}
	if _, err := c.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	assertControllerCode(t, <-getDone, "CANCELLED")
	if _, err := c.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 1 {
		t.Fatalf("destroy calls = %d", destroyed.Load())
	}
}

func TestControllerCloseCancelsAllClientsAndRequests(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	c := newTestController(t, cfg)
	startTestConnection(t, c)
	if _, err := c.StartConnection(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- c.WithClient(context.Background(), controllerTarget().Route, func(ctx context.Context, _ *string) error { close(entered); <-ctx.Done(); return ctx.Err() })
	}()
	<-entered
	var callers sync.WaitGroup
	for range 4 {
		callers.Go(func() {
			if err := c.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	callers.Wait()
	assertControllerCode(t, <-finished, "CANCELLED")
	if destroyed.Load() != 2 {
		t.Fatalf("destroy calls = %d", destroyed.Load())
	}
}

func TestControllerFailedStartDestroysClient(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	cfg.Lifecycle.OnStart = func(context.Context, types.Connection, *string) error { return errors.New("start failed") }
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(context.Background(), "explicit"); err == nil {
		t.Fatal("failed start accepted")
	}
	if destroyed.Load() != 1 {
		t.Fatalf("destroy calls = %d", destroyed.Load())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 1 {
		t.Fatal("failed client destroyed twice")
	}
}

func TestControllerStartRequestDoesNotOwnClientLifetime(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	clientContexts := make(chan context.Context, 1)
	cfg.Lifecycle.CreateClient = func(ctx context.Context, _ types.Connection) (*string, error) {
		clientContexts <- ctx
		client := "client"
		return &client, nil
	}
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := c.StartConnection(ctx, "explicit"); err != nil {
		t.Fatal(err)
	}
	cancel()
	clientContext := <-clientContexts
	if clientContext.Err() != nil {
		t.Fatal("start request cancellation canceled active client")
	}
	if _, err := c.StartConnection(context.Background(), ""); err == nil {
		t.Fatal("first connection fallback used")
	}
	if _, err := c.StopConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	if clientContext.Err() == nil || destroyed.Load() != 1 {
		t.Fatal("stop did not cancel and destroy")
	}
}

type controllerClassifier func(error) error

func (f controllerClassifier) ClassifyError(err error) error { return f(err) }

func TestControllerClassifierPrecedenceAndPanic(t *testing.T) {
	cfg := controllerConfig()
	cfg.ErrorClassifier = controllerClassifier(func(error) error { return NewError("FORBIDDEN", "global") })
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Get: func(context.Context, *string, GetRequest) (Record, error) {
			return Record{}, errors.New("native error")
		}, ErrorClassifier: controllerClassifier(func(error) error { return NewError("NOT_FOUND", "registration") })},
	}
	c := newTestController(t, cfg)
	request := GetRequest{
		Route:          controllerTarget().Route,
		Reference:      ResourceReference{Address: controllerAddress()},
		Representation: "document",
	}
	_, err := c.Get(acceptedControllerContext(context.Background(), t, c), request)
	assertControllerCode(t, err, "NOT_FOUND")
	cfg.Registrations[0].ErrorClassifier = controllerClassifier(func(error) error { panic("private data") })
	panicController := newTestController(t, cfg)
	_, err = panicController.Get(acceptedControllerContext(context.Background(), t, panicController), request)
	assertControllerCode(t, err, "INTERNAL")
	cfg.Registrations[0].Get = func(context.Context, *string, GetRequest) (Record, error) { panic("private data") }
	handlerPanic := newTestController(t, cfg)
	_, err = handlerPanic.Get(acceptedControllerContext(context.Background(), t, handlerPanic), request)
	assertControllerCode(t, err, "INTERNAL")
}

func TestControllerRejectsChangedResponseIdentityAndRevision(t *testing.T) {
	for _, change := range []string{"identity", "representation", "coverage", "revision"} {
		t.Run(change, func(t *testing.T) {
			cfg := controllerConfig()
			cfg.Registrations = []Registration[string]{
				{Type: "opaque/type", Get: func(context.Context, *string, GetRequest) (Record, error) {
					record := controllerRecord()
					switch change {
					case "identity":
						record.Reference.Address.ID = "different"
					case "representation":
						record.Representation.Revision = "different"
					case "coverage":
						record.Coverage.Kind = "invented"
					case "revision":
						empty := ""
						record.SourceRevision = &empty
					}
					return record, nil
				}},
			}
			c := newTestController(t, cfg)
			_, err := c.Get(
				acceptedControllerContext(context.Background(), t, c),
				GetRequest{
					Route:          controllerTarget().Route,
					Reference:      ResourceReference{Address: controllerAddress()},
					Representation: "document",
				},
			)
			if err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestControllerDynamicExactTypeRegistration(t *testing.T) {
	cfg := controllerConfig()
	cfg.ResolveRegistration = func(_ context.Context, route AccessRoute, typ string) (Registration[string], error) {
		if route.Connection != "explicit" || typ != "opaque/type" {
			return Registration[string]{}, errors.New("wrong dynamic target")
		}
		return Registration[string]{
			Type:       typ,
			Connection: route.Connection,
			Get:        func(context.Context, *string, GetRequest) (Record, error) { return controllerRecord(), nil },
		}, nil
	}
	c := newTestController(t, cfg)
	_, err := c.Get(
		acceptedControllerContext(context.Background(), t, c),
		GetRequest{
			Route:          controllerTarget().Route,
			Reference:      ResourceReference{Address: controllerAddress()},
			Representation: "document",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestControllerScopeAndRouteAdmission(t *testing.T) {
	cfg := controllerConfig()
	base := cfg.Catalog
	allowed := []string{"east"}
	anyScope := QueryScope{{Dimension: "region", Match: ScopeAny}}
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, err := base(ctx, request)
		catalog.Types[0].IdentityDimensions = []Dimension{{Name: "region"}}
		catalog.Types[0].QueryDimensions = []Dimension{{Name: "region"}}
		catalog.Types[0].Capabilities = []Capability{{Operation: OperationGet, Scope: anyScope}}
		return catalog, err
	}
	calls := 0
	cfg.Registrations = []Registration[string]{
		{
			Type:       "opaque/type",
			Connection: "explicit",
			Scope:      QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &allowed}},
			Get: func(_ context.Context, _ *string, request GetRequest) (Record, error) {
				calls++
				record := controllerRecord()
				record.Reference = request.Reference
				return record, nil
			},
		},
	}
	c := newTestController(t, cfg)
	address := controllerAddress()
	address.Scope = []ScopeEntry{{Dimension: "region", Value: "east"}}
	request := GetRequest{
		Route:          controllerTarget().Route,
		Reference:      ResourceReference{Address: address},
		Representation: "document",
	}
	if _, err := c.Get(acceptedControllerContext(context.Background(), t, c), request); err != nil {
		t.Fatal(err)
	}
	request.Reference.Address.Scope = []ScopeEntry{{Dimension: "region", Value: "west"}}
	_, err := c.Get(acceptedControllerContext(context.Background(), t, c), request)
	assertControllerCode(t, err, "UNSUPPORTED")
	request.Reference.Address.Scope = nil
	_, err = c.Get(acceptedControllerContext(context.Background(), t, c), request)
	assertControllerCode(t, err, "INVALID_REQUEST")
	request.Reference.Address = address
	request.Route.Connection = "other"
	_, err = c.Get(acceptedControllerContext(context.Background(), t, c), request)
	assertControllerCode(t, err, "UNSUPPORTED")
	if calls != 1 {
		t.Fatalf("inadmissible request dispatched: %d", calls)
	}
}

func TestControllerSourceAndRecordRevisionsStayIndependent(t *testing.T) {
	cfg := controllerConfig()
	pageRevision, recordRevision := "snapshot-20", "object-12"
	cfg.Registrations = []Registration[string]{
		{Type: "opaque/type", Enumerate: func(context.Context, *string, EnumerateRequest) (SourcePage, error) {
			record := controllerRecord()
			record.SourceRevision = &recordRevision
			return SourcePage{
				Records:        []Record{record},
				Count:          Count{Kind: "unknown"},
				Coverage:       Coverage{Kind: "complete"},
				Consistency:    "pointInTime",
				SourceRevision: &pageRevision,
			}, nil
		}},
	}
	c := newTestController(t, cfg)
	page, err := c.Enumerate(
		acceptedControllerContext(context.Background(), t, c),
		EnumerateRequest{Target: controllerTarget(), PageSize: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if *page.SourceRevision != pageRevision || *page.Records[0].SourceRevision != recordRevision {
		t.Fatal("source revisions were conflated")
	}
}

func TestControllerCanceledStartDestroysReturnedClient(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	entered := make(chan struct{})
	cfg.Lifecycle.CreateClient = func(ctx context.Context, _ types.Connection) (*string, error) {
		close(entered)
		<-ctx.Done()
		client := "partial"
		return &client, ctx.Err()
	}
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := c.StartConnection(ctx, "explicit"); result <- err }()
	<-entered
	cancel()
	assertControllerCode(t, <-result, "CANCELLED")
	if destroyed.Load() != 1 {
		t.Fatalf("destroy calls = %d", destroyed.Load())
	}
}

func TestControllerTeardownPanicStillDestroysClient(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	cfg.Lifecycle.OnStop = func(context.Context, types.Connection, *string) error { panic("private provider detail") }
	c := newTestController(t, cfg)
	startTestConnection(t, c)
	_, err := c.StopConnection(context.Background(), "explicit")
	assertControllerCode(t, err, "INTERNAL")
	if destroyed.Load() != 1 {
		t.Fatalf("destroy calls = %d", destroyed.Load())
	}
}

func TestControllerRejectsInvalidSourcePageMetadata(t *testing.T) {
	for _, change := range []string{"count", "pageSize", "cursor", "revision", "coverage", "identity"} {
		t.Run(change, func(t *testing.T) {
			cfg := controllerConfig()
			cfg.Registrations = []Registration[string]{
				{Type: "opaque/type", Enumerate: func(context.Context, *string, EnumerateRequest) (SourcePage, error) {
					page := SourcePage{
						Records:     []Record{controllerRecord()},
						Count:       Count{Kind: "unknown"},
						Coverage:    Coverage{Kind: "complete"},
						Consistency: "bestEffort",
					}
					empty := ""
					switch change {
					case "count":
						zero := "0"
						page.Count = Count{Kind: "exact", Value: &zero}
					case "pageSize":
						other := controllerRecord()
						other.Reference.Address.ID = "second"
						page.Records = append(page.Records, other)
					case "cursor":
						page.NextCursor = &empty
					case "revision":
						page.SourceRevision = &empty
					case "coverage":
						page.Coverage.Kind = "invented"
					case "identity":
						page.Records[0].Reference.Address.Authority = "other"
					}
					return page, nil
				}},
			}
			c := newTestController(t, cfg)
			_, err := c.Enumerate(
				acceptedControllerContext(context.Background(), t, c),
				EnumerateRequest{Target: controllerTarget(), PageSize: 1},
			)
			if err == nil {
				t.Fatal("invalid source page accepted")
			}
		})
	}
}

type controllerStreamOnly struct {
	catalog func(context.Context, CatalogRequest) (Catalog, error)
}

func (p controllerStreamOnly) Catalog(ctx context.Context, request CatalogRequest) (Catalog, error) {
	return p.catalog(ctx, request)
}

func (controllerStreamOnly) GetActions(context.Context, AccessRoute, string) ([]ActionDescriptor, error) {
	return []ActionDescriptor{{ID: "stream", Scope: ActionScopeInstance, Streaming: true}}, nil
}

func (controllerStreamOnly) StreamAction(context.Context, ActionRequest, chan<- ActionEvent) error {
	return errors.New("admission must not dispatch the streaming action")
}

func TestStreamingActionAdmissionDoesNotRequireFiniteAction(t *testing.T) {
	provider := controllerStreamOnly{catalog: controllerConfig().Catalog}
	request := ActionRequest{
		Route:     controllerTarget().Route,
		Reference: ResourceReference{Address: controllerAddress()},
		Action:    "stream",
		Input:     Value{Kind: "null"},
	}
	if _, err := AdmitStreamingAction(
		acceptedControllerContext(context.Background(), t, provider),
		provider,
		request,
	); err != nil {
		t.Fatal(err)
	}
	_, err := AdmitOperation(acceptedControllerContext(context.Background(), t, provider), provider, request)
	assertControllerCode(t, err, "UNSUPPORTED")
	base := provider.catalog
	provider.catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalog, catalogErr := base(ctx, request)
		catalog.Types[0].Capabilities = []Capability{}
		return catalog, catalogErr
	}
	_, err = AdmitStreamingAction(acceptedControllerContext(context.Background(), t, provider), provider, request)
	assertControllerCode(t, err, "UNSUPPORTED")
}

func TestControllerFeaturesReflectInstalledHandlers(t *testing.T) {
	cfg := controllerConfig()
	c := newTestController(t, cfg)
	features, err := DetectFeatures(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(features.Traits) != 0 {
		t.Fatal("static optional methods fabricated ancillary support")
	}
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	cfg.Registrations = []Registration[string]{
		{
			Type: "opaque/type",
			Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	withHandlers := newTestController(t, cfg)
	features, err = DetectFeatures(context.Background(), withHandlers)
	if err != nil {
		t.Fatal(err)
	}
	if !features.Has(FeatureConnections) || !features.Has(FeatureBestEffortUpdates) ||
		features.Has(FeatureConnectionBrowser) {
		t.Fatal("installed traits reported incorrectly")
	}
	cfg.Registrations[0].SyncPolicy = SyncNever
	disabled := newTestController(t, cfg)
	features, err = disabled.Features(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if features.Has(FeatureBestEffortUpdates) {
		t.Fatal("disabled callback advertised live support")
	}
}

func TestControllerDynamicUpdateFeatureRequiresExplicitConfiguration(t *testing.T) {
	cfg := controllerConfig()
	cfg.DynamicUpdates = true
	if _, err := NewController(cfg); err == nil {
		t.Fatal("dynamic updates accepted without resolver")
	}
	cfg.DynamicUpdates = false
	cfg.ResolveRegistration = func(_ context.Context, _ AccessRoute, typ string) (Registration[string], error) {
		return Registration[string]{
			Type: typ,
			Watch: func(ctx context.Context, _ *string, _ ReadTarget, _ Representation, _ WatchEventSink) error {
				<-ctx.Done()
				return ctx.Err()
			},
		}, nil
	}
	c := newTestController(t, cfg)
	features, err := c.Features(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if features.Has(FeatureBestEffortUpdates) {
		t.Fatal("resolver alone fabricated live support")
	}
	cfg.DynamicUpdates = true
	declared := newTestController(t, cfg)
	features, err = declared.Features(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !features.Has(FeatureBestEffortUpdates) {
		t.Fatal("explicit dynamic update installation missing")
	}
}

func TestControllerConnectionUpsertCreatesExplicitManagedRoute(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	c := newTestController(t, cfg)
	connection := types.Connection{ID: "created", Name: "Created"}
	if _, err := c.UpdateConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(context.Background(), connection.ID); err != nil {
		t.Fatal(err)
	}
	connection.Name = "Changed"
	connection.Data = map[string]any{"endpoint": "https://replacement.invalid"}
	if _, err := c.UpdateConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 1 {
		t.Fatal("updating active configuration did not retire its client")
	}
	got, err := c.GetConnection(context.Background(), connection.ID)
	if err != nil || got.Name != connection.Name {
		t.Fatalf("upsert lost configuration: %v %v", got, err)
	}
	_, err = c.UpdateConnection(context.Background(), types.Connection{Name: "No route"})
	assertControllerCode(t, err, "INVALID_REQUEST")
}

func TestControllerOverlaySurvivesNativeRefreshAndResets(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	c := newTestController(t, cfg)
	startTestConnection(t, c)
	if _, err := c.StartConnection(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	overlay := ConnectionOverlay{
		Upserts: []types.Connection{
			{ID: "explicit", Name: "Override", Data: map[string]any{"endpoint": "https://override.invalid"}},
			{ID: "manual", Name: "Manual"},
		},
		DisabledIDs: []string{"other"},
	}
	if err := c.ApplyConnectionOverlay(context.Background(), overlay); err != nil {
		t.Fatal(err)
	}
	if destroyed.Load() != 2 {
		t.Fatal("overlay did not retire changed and disabled clients")
	}
	refreshed := []types.Connection{{ID: "explicit", Name: "Fresh native"}, {ID: "other", Name: "Fresh other"}}
	if err := c.ApplyConnections(context.Background(), refreshed); err != nil {
		t.Fatal(err)
	}
	connection, err := c.GetConnection(context.Background(), "explicit")
	if err != nil || connection.Name != "Override" {
		t.Fatalf("native refresh erased override: %v %v", connection, err)
	}
	_, err = c.GetConnection(context.Background(), "other")
	assertControllerCode(t, err, "NOT_FOUND")
	if _, err = c.GetConnection(context.Background(), "manual"); err != nil {
		t.Fatal("native refresh erased manual profile")
	}
	if err = c.ApplyConnections(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err = c.ApplyConnections(context.Background(), refreshed); err != nil {
		t.Fatal(err)
	}
	_, err = c.GetConnection(context.Background(), "other")
	assertControllerCode(t, err, "NOT_FOUND")
	if err = c.ApplyConnectionOverlay(context.Background(), ConnectionOverlay{}); err != nil {
		t.Fatal(err)
	}
	connection, err = c.GetConnection(context.Background(), "explicit")
	if err != nil || connection.Name != "Fresh native" {
		t.Fatal("empty overlay did not restore latest native facts")
	}
	if _, err = c.GetConnection(context.Background(), "other"); err != nil {
		t.Fatal("empty overlay did not restore disabled native route")
	}
	_, err = c.GetConnection(context.Background(), "manual")
	assertControllerCode(t, err, "NOT_FOUND")
}

func TestControllerNativeSnapshotExcludesDesiredOverlay(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	var loads atomic.Int32
	load := cfg.Lifecycle.LoadConnections
	cfg.Lifecycle.LoadConnections = func(ctx context.Context) ([]types.Connection, error) { loads.Add(1); return load(ctx) }
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyConnectionOverlay(
		context.Background(),
		ConnectionOverlay{
			Upserts:     []types.Connection{{ID: "explicit", Name: "Override"}, {ID: "manual", Name: "Manual"}},
			DisabledIDs: []string{"other"},
		},
	); err != nil {
		t.Fatal(err)
	}
	native, err := c.NativeConnections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 || len(native) != 2 || native[0].ID != "explicit" || native[0].Name != "Explicit" ||
		native[1].ID != "other" {
		t.Fatalf("native provenance mixed with overlay: %v", native)
	}
	native[0].Name = "Caller copy"
	again, err := c.NativeConnections(context.Background())
	if err != nil || again[0].Name != "Explicit" {
		t.Fatal("native snapshot slice aliases managed inventory")
	}
	effective, err := c.LoadConnections(context.Background())
	if err != nil || len(effective) != 2 || effective[0].ID != "explicit" || effective[0].Name != "Override" ||
		effective[1].ID != "manual" {
		t.Fatalf("load published raw native inventory: %v %v", effective, err)
	}
}

func TestControllerInvalidOverlayLeavesDesiredStateAndClientUnchanged(t *testing.T) {
	for _, overlay := range []ConnectionOverlay{
		{Upserts: []types.Connection{{ID: ""}}},
		{Upserts: []types.Connection{{ID: "explicit"}, {ID: "explicit"}}},
		{DisabledIDs: []string{"other", "other"}},
		{Upserts: []types.Connection{{ID: "explicit"}}, DisabledIDs: []string{"explicit"}},
		{DisabledIDs: []string{""}},
	} {
		cfg := controllerConfig()
		var destroyed atomic.Int32
		cfg.Lifecycle = controllerLifecycle(&destroyed)
		c := newTestController(t, cfg)
		startTestConnection(t, c)
		assertControllerCode(t, c.ApplyConnectionOverlay(context.Background(), overlay), "INVALID_REQUEST")
		connection, err := c.GetConnection(context.Background(), "explicit")
		if err != nil || connection.Name != "Explicit" || destroyed.Load() != 0 {
			t.Fatal("invalid overlay changed inventory or retired client")
		}
	}
}

func TestControllerOverlayFailureDoesNotPublishDesiredConfiguration(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	cfg.Lifecycle.DestroyClient = func(context.Context, *string) error { destroyed.Add(1); return errors.New("teardown failed") }
	c := newTestController(t, cfg)
	startTestConnection(t, c)
	err := c.ApplyConnectionOverlay(
		context.Background(),
		ConnectionOverlay{
			Upserts: []types.Connection{
				{
					ID:   "explicit",
					Name: "Not committed",
					Data: map[string]any{"endpoint": "https://uncommitted.invalid"},
				},
			},
		},
	)
	if err == nil {
		t.Fatal("failed client teardown acknowledged overlay")
	}
	connection, getErr := c.GetConnection(context.Background(), "explicit")
	if getErr != nil || connection.Name != "Explicit" || len(c.overrides) != 0 || destroyed.Load() != 1 {
		t.Fatal("failed apply published desired state")
	}
}

func TestControllerDirectConnectionEditsRetainOverlayOwnership(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateConnection(
		context.Background(),
		types.Connection{ID: "explicit", Name: "Desired"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateConnection(context.Background(), types.Connection{ID: "manual", Name: "Manual"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteConnection(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteConnection(context.Background(), "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	connection, err := c.GetConnection(context.Background(), "explicit")
	if err != nil || connection.Name != "Desired" {
		t.Fatal("native reload erased direct desired edit")
	}
	_, err = c.GetConnection(context.Background(), "other")
	assertControllerCode(t, err, "NOT_FOUND")
	_, err = c.GetConnection(context.Background(), "manual")
	assertControllerCode(t, err, "NOT_FOUND")
	if _, disabled := c.disabled["manual"]; disabled {
		t.Fatal("manual profile deletion fabricated native tombstone")
	}
	features, err := c.Features(context.Background())
	if err != nil || !features.Has(FeatureConnectionOverlay) {
		t.Fatal("installed overlay feature missing")
	}
}
