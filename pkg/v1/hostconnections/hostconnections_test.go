package hostconnections

import (
	"context"
	"net"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	pb "github.com/omniviewdev/plugin-sdk/proto/v1/hostconnections"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type testService struct{ schemaCalls int }

func (*testService) Catalog(context.Context, []byte, string, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	return resourcev2.Catalog{}, resourcev2.NewError("GRANT_DENIED", "private diagnostic")
}
func (*testService) ResolveTarget(context.Context, []byte, string, ResolveTargetRequest) (ResolvedTarget, error) {
	return ResolvedTarget{}, invalid()
}
func (s *testService) GetSchemaDocument(_ context.Context, token []byte, owner string, route resourcev2.AccessRoute, ref resourcev2.SchemaDocumentReference) (resourcev2.SchemaDocument, error) {
	s.schemaCalls++
	if string(token) != "token" || owner != "owner" || route.Connection != "route" {
		return resourcev2.SchemaDocument{}, invalid()
	}
	return resourcev2.SchemaDocument{Reference: ref, Dialect: "json-schema", Document: []byte(`{"type":"object"}`)}, nil
}
func (*testService) Get(context.Context, []byte, string, string, resourcev2.GetRequest) (resourcev2.Record, error) {
	return resourcev2.Record{}, invalid()
}
func (*testService) Enumerate(context.Context, []byte, string, string, resourcev2.EnumerateRequest) (resourcev2.SourcePage, error) {
	return resourcev2.SourcePage{}, invalid()
}
func (*testService) Update(context.Context, []byte, string, string, resourcev2.MutationRequest) (resourcev2.Record, error) {
	return resourcev2.Record{}, invalid()
}
func TestCallbackSchemaAndAdmission(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	service := &testService{}
	RegisterServer(server, service)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///callback", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	require.NoError(t, err)
	defer conn.Close()
	binding := &Binding{}
	require.NoError(t, binding.Bind(conn))
	route := resourcev2.AccessRoute{Connection: "route"}
	ctx, err := binding.Context(context.Background(), "owner", route, []byte("token"))
	require.NoError(t, err)
	client, err := FromContext(ctx)
	require.NoError(t, err)
	ref := resourcev2.SchemaDocumentReference{ID: "schema", Revision: "1"}
	doc, err := client.GetSchemaDocument(ctx, "owner", route, ref)
	require.NoError(t, err)
	require.Equal(t, ref, doc.Reference)
	require.Equal(t, 1, service.schemaCalls)
	_, err = client.GetSchemaDocument(ctx, "other", route, ref)
	require.Error(t, err)
	_, err = client.GetSchemaDocument(ctx, "owner", resourcev2.AccessRoute{Connection: "other"}, ref)
	require.Error(t, err)
	require.Equal(t, 1, service.schemaCalls)
	_, err = client.Catalog(ctx, "owner", resourcev2.CatalogRequest{Route: route})
	require.Equal(t, "GRANT_DENIED", resourcev2.ErrorCode(err))
	require.NotContains(t, err.Error(), "private diagnostic")
	raw := pb.NewHostConnectionsClient(conn)
	_, err = raw.Call(ctx, &pb.CallRequest{OwnerPlugin: "owner", DelegationToken: []byte("token"), Operation: &pb.CallRequest_Resource{Resource: &resourcepb.Request{Version: resourcev2.WireVersion, Operation: &resourcepb.Request_Features{Features: &resourcepb.FeatureRequest{}}}}})
	require.Error(t, err)
	_, err = binding.Context(context.Background(), "owner", route, nil)
	require.ErrorIs(t, err, ErrHostConnectionsUnavailable)
	require.Error(t, binding.Bind(conn))
}

type recordService struct {
	testService
	record resourcev2.Record
}

func (s *recordService) Get(context.Context, []byte, string, string, resourcev2.GetRequest) (resourcev2.Record, error) {
	return s.record, nil
}

func (s *recordService) Update(context.Context, []byte, string, string, resourcev2.MutationRequest) (resourcev2.Record, error) {
	return s.record, nil
}

func TestFiniteRecordsRejectChangedResourceIdentity(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	service := &recordService{}
	RegisterServer(server, service)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///identity", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}))
	require.NoError(t, err)
	defer conn.Close()
	binding := &Binding{}
	require.NoError(t, binding.Bind(conn))
	route := resourcev2.AccessRoute{Connection: "route"}
	ctx, err := binding.Context(context.Background(), "owner", route, []byte("token"))
	require.NoError(t, err)
	client, err := FromContext(ctx)
	require.NoError(t, err)
	revision, incarnation := "1", "uid"
	representation := resourcev2.Representation{
		ID: "document", Revision: revision,
		Schema: resourcev2.SchemaBinding{
			State: "available", Revision: &revision,
			Dialect:     "https://json-schema.org/draft/2020-12/schema",
			DocumentRef: &resourcev2.SchemaDocumentReference{ID: "schema", Revision: revision},
			Validation:  "full",
		},
	}
	for _, test := range []struct {
		name      string
		change    func(*resourcev2.Record)
		wantError bool
	}{
		{"same identity", func(*resourcev2.Record) {}, false},
		{"provider", func(record *resourcev2.Record) { record.Reference.Address.Provider = "other" }, true},
		{"authority", func(record *resourcev2.Record) { record.Reference.Address.Authority = "other" }, true},
		{"type", func(record *resourcev2.Record) { record.Reference.Address.Type = "other" }, true},
		{"id", func(record *resourcev2.Record) { record.Reference.Address.ID = "other" }, true},
		{"scope", func(record *resourcev2.Record) { record.Reference.Address.Scope[0].Value = "other" }, true},
		{"scope order", func(record *resourcev2.Record) {
			record.Reference.Address.Scope[0], record.Reference.Address.Scope[1] = record.Reference.Address.Scope[1], record.Reference.Address.Scope[0]
		}, false},
		{"new incarnation", func(record *resourcev2.Record) { record.Reference.Incarnation = &incarnation }, false},
	} {
		for _, operation := range []string{"get", "update"} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				requested := resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{
					Provider: "example", Authority: "authority", Type: "document", ID: "fixture",
					Scope: []resourcev2.ScopeEntry{{Dimension: "namespace", Value: "scratch"}, {Dimension: "region", Value: "local"}},
				}}
				returned := requested
				returned.Address.Scope = append([]resourcev2.ScopeEntry(nil), requested.Address.Scope...)
				service.record = resourcev2.Record{
					Reference: returned, Representation: representation,
					Body:     resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{}},
					Coverage: resourcev2.Coverage{Kind: "complete"},
				}
				test.change(&service.record)
				if operation == "get" {
					_, err = client.Get(ctx, "owner", "target", resourcev2.GetRequest{Route: route, Reference: requested, Representation: representation.ID})
				} else {
					_, err = client.Update(ctx, "owner", "target", resourcev2.MutationRequest{Route: route, Reference: requested, Operation: "update", Input: service.record.Body, ExpectedRevision: &revision})
				}
				if test.wantError {
					require.Equal(t, "INVALID_REQUEST", resourcev2.ErrorCode(err))
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}
