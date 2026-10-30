package plugin

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

type nativeOnlyActionProvider struct{ qualifiedProvider }

func (p *nativeOnlyActionProvider) Catalog(
	ctx context.Context,
	request resourcev2.CatalogRequest,
) (resourcev2.Catalog, error) {
	catalog, err := p.qualifiedProvider.Catalog(ctx, request)
	catalog.Types[0].Capabilities = []resourcev2.Capability{
		{Operation: resourcev2.OperationAction, Scope: resourcev2.QueryScope{}},
	}
	return catalog, err
}

func (p *nativeOnlyActionProvider) Action(context.Context, resourcev2.ActionRequest) (resourcev2.Value, error) {
	p.calls.Add(1)
	return resourcev2.Value{Kind: "null"}, nil
}

func TestNativeActionTransportWithoutDiscovery(t *testing.T) {
	provider := &nativeOnlyActionProvider{}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	request := actionInput()
	ctx := acceptedCatalogContext(context.Background(), t, client, request.Route)
	if _, err := client.Action(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.ExpectedDescriptor = &resourcev2.ActionDescriptor{ID: request.Action, Scope: resourcev2.ActionScopeInstance}
	if _, err := client.Action(ctx, request); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("expected missing descriptor reset: %v", err)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("native calls = %d", provider.calls.Load())
	}
}
