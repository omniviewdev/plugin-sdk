package resourcev2_test

import (
	"context"
	"fmt"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// A catalog-only provider needs no client, CRUD methods, or update source.
type catalogOnly struct{}

func (catalogOnly) Catalog(ctx context.Context, request resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	if err := ctx.Err(); err != nil {
		return resourcev2.Catalog{}, err
	}
	if request.Route.Connection == "" {
		return resourcev2.Catalog{}, resourcev2.NewError("INVALID_REQUEST", "Select a connection.")
	}
	return resourcev2.Catalog{
		Provider: "example",
		Revision: "metadata-1",
		Coverage: resourcev2.Coverage{Kind: "complete"},
	}, nil
}

var _ resourcev2.Provider = catalogOnly{}

func ExampleProvider() {
	var provider resourcev2.Provider = catalogOnly{}
	catalog, err := provider.Catalog(
		context.Background(),
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "profile"}},
	)
	fmt.Println(catalog.Provider, err)
	// Output: example <nil>
}

func exampleRepresentation() resourcev2.Representation {
	return resourcev2.Representation{
		ID:       "document",
		Revision: "1",
		Schema:   resourcev2.SchemaBinding{State: "absent"},
	}
}

func exampleCatalog(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	representation := exampleRepresentation()
	return resourcev2.Catalog{
		Provider: "example", Revision: "metadata-1", Coverage: resourcev2.Coverage{Kind: "complete"},
		Types: []resourcev2.ResourceType{{
			Type: "document", DisplayName: "Document", Representations: []resourcev2.Representation{representation},
			DefaultRepresentation: representation.ID,
			Capabilities:          []resourcev2.Capability{{Operation: resourcev2.OperationGet}},
		}},
	}, nil
}

func ExampleGetter() {
	provider, err := resourcev2.NewController(resourcev2.Config[struct{}]{
		Catalog: exampleCatalog,
		Registrations: []resourcev2.Registration[struct{}]{{
			Type: "document",
			Get: func(ctx context.Context, _ *struct{}, request resourcev2.GetRequest) (resourcev2.Record, error) {
				return resourcev2.Record{
					Reference:      request.Reference,
					Body:           resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{}},
					Representation: exampleRepresentation(),
					Coverage:       resourcev2.Coverage{Kind: "complete"},
				}, ctx.Err()
			},
		}},
	})
	if err != nil {
		panic(err)
	}
	defer provider.Close()
	route := resourcev2.AccessRoute{Connection: "profile"}
	catalog, err := provider.Catalog(context.Background(), resourcev2.CatalogRequest{Route: route})
	if err != nil {
		panic(err)
	}
	// A real host issues this authority after its own admission, not the provider.
	request := resourcev2.GetRequest{
		Route: route,
		Reference: resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{
			Provider: "example", Authority: "host-issued", Type: "document", ID: "readme",
		}},
		Representation: exampleRepresentation().ID,
	}
	ctx := resourcev2.WithCatalogRevisionForRoute(context.Background(), catalog.Provider, route, catalog.Revision)
	record, err := provider.Get(ctx, request)
	fmt.Println(record.Reference.Address.ID, err)
	// Output: readme <nil>
}

// exampleClient stands for the provider's native SDK client.
type exampleClient struct{ closed bool }

func ExampleLifecycle() {
	provider, err := resourcev2.NewController(resourcev2.Config[exampleClient]{
		Catalog: exampleCatalog,
		Lifecycle: &resourcev2.Lifecycle[exampleClient]{
			LoadConnections: func(context.Context) ([]types.Connection, error) {
				return []types.Connection{{ID: "profile", Name: "Example"}}, nil
			},
			CreateClient: func(ctx context.Context, _ types.Connection) (*exampleClient, error) {
				// Real constructors use ctx and profile for native acquisition.
				return &exampleClient{}, ctx.Err()
			},
			DestroyClient: func(_ context.Context, client *exampleClient) error {
				client.closed = true
				return nil
			},
		},
	})
	if err != nil {
		panic(err)
	}
	defer provider.Close()
	ctx := context.Background()
	if _, loadErr := provider.LoadConnections(ctx); loadErr != nil {
		panic(loadErr)
	}
	if _, startErr := provider.StartConnection(ctx, "profile"); startErr != nil {
		panic(startErr)
	}
	err = provider.WithConnection(
		ctx,
		resourcev2.AccessRoute{Connection: "profile"},
		func(_ context.Context, profile types.Connection) error {
			fmt.Println(profile.ID)
			return nil
		},
	)
	if err != nil {
		panic(err)
	}
	if _, err := provider.StopConnection(ctx, "profile"); err != nil {
		panic(err)
	}
	// Output: profile
}

// generatedProvider is immutable after construction. A real mutable provider
// must synchronize publication and fence operations against generation retirement.
type generatedProvider struct{ catalog resourcev2.Catalog }

func (p *generatedProvider) Catalog(
	ctx context.Context,
	request resourcev2.CatalogRequest,
) (resourcev2.Catalog, error) {
	if request.Route.Connection != "profile" {
		return resourcev2.Catalog{}, resourcev2.NewError("NOT_FOUND", "Unknown connection.")
	}
	// This catalog has no mutable nested containers to share with callers.
	return p.catalog, ctx.Err()
}

var _ resourcev2.Provider = (*generatedProvider)(nil)

func ExampleNewCatalogBindingEpoch() {
	// Issue once per actual source/configuration generation, not per Catalog call.
	epoch, err := resourcev2.NewCatalogBindingEpoch()
	if err != nil {
		panic(err)
	}
	revision, err := resourcev2.BindCatalogRevision(epoch, "native-metadata-1")
	if err != nil {
		panic(err)
	}
	provider := &generatedProvider{
		catalog: resourcev2.Catalog{
			Provider: "custom",
			Revision: revision,
			Coverage: resourcev2.Coverage{Kind: "complete"},
		},
	}
	catalog, err := provider.Catalog(
		context.Background(),
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "profile"}},
	)
	if err != nil {
		panic(err)
	}
	observedEpoch, err := resourcev2.CatalogBindingEpoch(catalog.Revision)
	fmt.Println(observedEpoch == epoch, err)
	// Output: true <nil>
}
