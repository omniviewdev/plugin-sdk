package hostconnections

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	rp "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	pb "github.com/omniviewdev/plugin-sdk/proto/v1/hostconnections"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type testService struct {
	schemaCalls     int
	resolveCalls    int
	resolvedRequest resourcev2.SchemaRequest
	documentRequest resourcev2.SchemaDocumentRequest
}

func (*testService) Catalog(context.Context, []byte, string, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	return resourcev2.Catalog{}, resourcev2.NewError("GRANT_DENIED", "private diagnostic")
}
func (*testService) ResolveTarget(context.Context, []byte, string, ResolveTargetRequest) (ResolvedTarget, error) {
	return ResolvedTarget{}, invalid()
}
func (s *testService) ResolveSchema(_ context.Context, token []byte, owner string, in resourcev2.SchemaRequest) (resourcev2.SchemaBinding, error) {
	s.resolveCalls++
	if string(token) != "token" || owner != "owner" || in.Route.Connection != "route" {
		return resourcev2.SchemaBinding{}, invalid()
	}
	s.resolvedRequest = in
	revision := "schema-1"
	return resourcev2.SchemaBinding{
		State: "available", Revision: &revision, Dialect: "json-schema",
		DocumentRef: &resourcev2.SchemaDocumentReference{ID: "schema", Revision: "1"},
	}, nil
}
func (s *testService) GetSchemaDocument(_ context.Context, token []byte, owner string, in resourcev2.SchemaDocumentRequest) (resourcev2.SchemaDocument, error) {
	s.schemaCalls++
	if string(token) != "token" || owner != "owner" || in.Request.Route.Connection != "route" {
		return resourcev2.SchemaDocument{}, invalid()
	}
	s.documentRequest = in
	return resourcev2.SchemaDocument{Reference: in.Reference, Dialect: "json-schema", Document: []byte(`{"type":"object"}`)}, nil
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
	request := schemaFixtureRequest(route)
	resolved, err := client.ResolveSchema(ctx, "owner", request)
	require.NoError(t, err)
	require.Equal(t, request, service.resolvedRequest)
	require.Equal(t, 1, service.resolveCalls)
	require.NotNil(t, resolved.DocumentRef)
	in := resourcev2.SchemaDocumentRequest{Request: request, Reference: *resolved.DocumentRef}
	doc, err := client.GetSchemaDocument(ctx, "owner", in)
	require.NoError(t, err)
	require.Equal(t, in.Reference, doc.Reference)
	require.Equal(t, in, service.documentRequest)
	require.Equal(t, 1, service.schemaCalls)
	_, err = client.ResolveSchema(ctx, "other", request)
	require.Error(t, err)
	_, err = client.GetSchemaDocument(ctx, "other", in)
	require.Error(t, err)
	in.Request.Route.Connection = "other"
	_, err = client.ResolveSchema(ctx, "owner", in.Request)
	require.Error(t, err)
	_, err = client.GetSchemaDocument(ctx, "owner", in)
	require.Error(t, err)
	require.Equal(t, 1, service.resolveCalls)
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
	representation := resourcev2.Representation{ID: "document", Revision: revision}
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

func schemaFixtureRequest(route resourcev2.AccessRoute) resourcev2.SchemaRequest {
	return resourcev2.SchemaRequest{
		Route: route, Type: "document",
		Target: resourcev2.SchemaTarget{
			Representation: &resourcev2.Representation{ID: "document", Revision: "format-1"},
		},
	}
}

func schemaFixtureClient(t *testing.T, register func(*grpc.Server)) (Client, *grpc.ClientConn, context.Context) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	register(server)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///schema", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	binding := &Binding{}
	require.NoError(t, binding.Bind(conn))
	ctx, err := binding.Context(context.Background(), "owner", resourcev2.AccessRoute{Connection: "route"}, []byte("token"))
	require.NoError(t, err)
	client, err := FromContext(ctx)
	require.NoError(t, err)
	return client, conn, ctx
}

func TestSchemaRejectsInvalidTargetsBeforeDispatch(t *testing.T) {
	service := &testService{}
	client, _, ctx := schemaFixtureClient(t, func(server *grpc.Server) { RegisterServer(server, service) })
	for _, test := range []struct {
		name   string
		change func(*resourcev2.SchemaRequest)
	}{
		{"missing target", func(in *resourcev2.SchemaRequest) { in.Target = resourcev2.SchemaTarget{} }},
		{"both targets", func(in *resourcev2.SchemaRequest) {
			in.Target.Auxiliary = &resourcev2.AuxiliarySchemaTarget{Key: "key"}
		}},
		{"missing format revision", func(in *resourcev2.SchemaRequest) { in.Target.Representation.Revision = "" }},
		{"auxiliary type mismatch", func(in *resourcev2.SchemaRequest) {
			in.Target = resourcev2.SchemaTarget{Auxiliary: &resourcev2.AuxiliarySchemaTarget{
				Key: "key", Reference: resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{
					Provider: "example", Authority: "authority", Type: "other", ID: "fixture",
				}},
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := schemaFixtureRequest(resourcev2.AccessRoute{Connection: "route"})
			test.change(&in)
			_, err := client.ResolveSchema(ctx, "owner", in)
			require.Equal(t, "INVALID_REQUEST", resourcev2.ErrorCode(err))
			_, err = client.GetSchemaDocument(ctx, "owner", resourcev2.SchemaDocumentRequest{
				Request: in, Reference: resourcev2.SchemaDocumentReference{ID: "schema", Revision: "1"},
			})
			require.Equal(t, "INVALID_REQUEST", resourcev2.ErrorCode(err))
		})
	}
	_, err := client.GetSchemaDocument(ctx, "owner", resourcev2.SchemaDocumentRequest{
		Request: schemaFixtureRequest(resourcev2.AccessRoute{Connection: "route"}),
	})
	require.Equal(t, "INVALID_REQUEST", resourcev2.ErrorCode(err))
	require.Zero(t, service.resolveCalls)
	require.Zero(t, service.schemaCalls)
}

type schemaTargetService struct {
	testService
	allowed resourcev2.SchemaRequest
}

func (s *schemaTargetService) ResolveSchema(ctx context.Context, token []byte, owner string, in resourcev2.SchemaRequest) (resourcev2.SchemaBinding, error) {
	if !reflect.DeepEqual(in, s.allowed) {
		return resourcev2.SchemaBinding{}, resourcev2.NewError("GRANT_DENIED", "Schema target is not authorized.")
	}
	return s.testService.ResolveSchema(ctx, token, owner, in)
}

func (s *schemaTargetService) GetSchemaDocument(ctx context.Context, token []byte, owner string, in resourcev2.SchemaDocumentRequest) (resourcev2.SchemaDocument, error) {
	if !reflect.DeepEqual(in.Request, s.allowed) {
		return resourcev2.SchemaDocument{}, resourcev2.NewError("GRANT_DENIED", "Schema target is not authorized.")
	}
	return s.testService.GetSchemaDocument(ctx, token, owner, in)
}

func TestSchemaDocumentReferenceDoesNotGrantAnotherTarget(t *testing.T) {
	sourceRevision, incarnation := "source-1", "uid"
	allowed := resourcev2.SchemaRequest{
		Route: resourcev2.AccessRoute{Connection: "route"}, Type: "document",
		Target: resourcev2.SchemaTarget{Auxiliary: &resourcev2.AuxiliarySchemaTarget{
			Key: "values", SourceRevision: &sourceRevision,
			Reference: resourcev2.ResourceReference{
				Address:     resourcev2.ResourceAddress{Provider: "example", Authority: "authority", Type: "document", ID: "fixture", Scope: []resourcev2.ScopeEntry{}},
				Incarnation: &incarnation,
			},
		}},
	}
	service := &schemaTargetService{allowed: allowed}
	client, _, ctx := schemaFixtureClient(t, func(server *grpc.Server) { RegisterServer(server, service) })
	binding, err := client.ResolveSchema(ctx, "owner", allowed)
	require.NoError(t, err)
	require.Equal(t, allowed, service.resolvedRequest)
	require.NotNil(t, binding.DocumentRef)
	document := resourcev2.SchemaDocumentRequest{Request: allowed, Reference: *binding.DocumentRef}
	_, err = client.GetSchemaDocument(ctx, "owner", document)
	require.NoError(t, err)
	require.Equal(t, document, service.documentRequest)
	for _, test := range []struct {
		name   string
		change func(*resourcev2.SchemaRequest)
	}{
		{"key", func(in *resourcev2.SchemaRequest) { in.Target.Auxiliary.Key = "other" }},
		{"resource", func(in *resourcev2.SchemaRequest) { in.Target.Auxiliary.Reference.Address.ID = "other" }},
		{"incarnation", func(in *resourcev2.SchemaRequest) {
			other := "other"
			in.Target.Auxiliary.Reference.Incarnation = &other
		}},
		{"source revision", func(in *resourcev2.SchemaRequest) { other := "other"; in.Target.Auxiliary.SourceRevision = &other }},
		{"representation", func(in *resourcev2.SchemaRequest) {
			in.Target = schemaFixtureRequest(in.Route).Target
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := allowed
			auxiliary := *allowed.Target.Auxiliary
			in.Target.Auxiliary = &auxiliary
			test.change(&in)
			_, err := client.ResolveSchema(ctx, "owner", in)
			require.Equal(t, "GRANT_DENIED", resourcev2.ErrorCode(err))
			_, err = client.GetSchemaDocument(ctx, "owner", resourcev2.SchemaDocumentRequest{Request: in, Reference: document.Reference})
			require.Equal(t, "GRANT_DENIED", resourcev2.ErrorCode(err))
		})
	}
	require.Equal(t, 1, service.resolveCalls)
	require.Equal(t, 1, service.schemaCalls)
}

func TestSchemaServerRejectsMissingTargetsAndFiniteIDs(t *testing.T) {
	service := &testService{}
	_, conn, ctx := schemaFixtureClient(t, func(server *grpc.Server) { RegisterServer(server, service) })
	raw := pb.NewHostConnectionsClient(conn)
	request, err := resourcev2.ToProtoSchemaRequest(schemaFixtureRequest(resourcev2.AccessRoute{Connection: "route"}))
	require.NoError(t, err)
	reference := &resourcepb.SchemaDocumentReference{Id: "schema", Revision: "1"}
	for _, operation := range []string{"resolve", "document"} {
		for _, test := range []string{"missing target", "finite ID"} {
			t.Run(operation+"/"+test, func(t *testing.T) {
				in := proto.Clone(request).(*resourcepb.SchemaRequest)
				req := &pb.CallRequest{OwnerPlugin: "owner", DelegationToken: []byte("token")}
				if test == "missing target" {
					in.Target = nil
				} else {
					req.TargetId = "finite-target"
				}
				if operation == "resolve" {
					req.Operation = &pb.CallRequest_ResolveSchema{ResolveSchema: in}
				} else {
					req.Operation = &pb.CallRequest_SchemaDocumentRequest{SchemaDocumentRequest: &resourcepb.SchemaDocumentRequest{Request: in, Reference: reference}}
				}
				_, err := raw.Call(ctx, req)
				require.Equal(t, "INVALID_REQUEST", resourcev2.ErrorCode(rp.ResourceDomainError(err)))
			})
		}
	}
	require.Zero(t, service.resolveCalls)
	require.Zero(t, service.schemaCalls)
}

type schemaResponseServer struct {
	pb.UnimplementedHostConnectionsServer
	response *pb.CallResponse
}

func (s *schemaResponseServer) Call(context.Context, *pb.CallRequest) (*pb.CallResponse, error) {
	return s.response, nil
}

type mismatchedDocumentService struct{ testService }

func (*mismatchedDocumentService) GetSchemaDocument(context.Context, []byte, string, resourcev2.SchemaDocumentRequest) (resourcev2.SchemaDocument, error) {
	return resourcev2.SchemaDocument{
		Reference: resourcev2.SchemaDocumentReference{ID: "other", Revision: "1"},
		Dialect:   "json-schema", Document: []byte(`{"type":"object"}`),
	}, nil
}

func TestSchemaDocumentServerRejectsChangedReference(t *testing.T) {
	client, _, ctx := schemaFixtureClient(t, func(server *grpc.Server) { RegisterServer(server, &mismatchedDocumentService{}) })
	_, err := client.GetSchemaDocument(ctx, "owner", resourcev2.SchemaDocumentRequest{
		Request:   schemaFixtureRequest(resourcev2.AccessRoute{Connection: "route"}),
		Reference: resourcev2.SchemaDocumentReference{ID: "schema", Revision: "1"},
	})
	require.Equal(t, "INVALID_REQUEST", resourcev2.ErrorCode(err))
}

func TestSchemaClientRejectsChangedReferencesAndWrongResults(t *testing.T) {
	for _, test := range []struct {
		name     string
		resolve  bool
		response *pb.CallResponse
	}{
		{"document ID", false, &pb.CallResponse{Result: &pb.CallResponse_SchemaDocument{SchemaDocument: &resourcepb.SchemaDocument{
			Reference: &resourcepb.SchemaDocumentReference{Id: "other", Revision: "1"}, Dialect: "json-schema", Document: []byte(`{}`),
		}}}},
		{"document revision", false, &pb.CallResponse{Result: &pb.CallResponse_SchemaDocument{SchemaDocument: &resourcepb.SchemaDocument{
			Reference: &resourcepb.SchemaDocumentReference{Id: "schema", Revision: "other"}, Dialect: "json-schema", Document: []byte(`{}`),
		}}}},
		{"binding instead of document", false, &pb.CallResponse{Result: &pb.CallResponse_SchemaBinding{SchemaBinding: &resourcepb.SchemaBinding{State: "absent"}}}},
		{"document instead of binding", true, &pb.CallResponse{Result: &pb.CallResponse_SchemaDocument{SchemaDocument: &resourcepb.SchemaDocument{
			Reference: &resourcepb.SchemaDocumentReference{Id: "schema", Revision: "1"}, Dialect: "json-schema", Document: []byte(`{}`),
		}}}},
		{"invalid binding", true, &pb.CallResponse{Result: &pb.CallResponse_SchemaBinding{SchemaBinding: &resourcepb.SchemaBinding{State: "available"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _, ctx := schemaFixtureClient(t, func(server *grpc.Server) {
				pb.RegisterHostConnectionsServer(server, &schemaResponseServer{response: test.response})
			})
			request := schemaFixtureRequest(resourcev2.AccessRoute{Connection: "route"})
			var err error
			if test.resolve {
				_, err = client.ResolveSchema(ctx, "owner", request)
			} else {
				_, err = client.GetSchemaDocument(ctx, "owner", resourcev2.SchemaDocumentRequest{
					Request: request, Reference: resourcev2.SchemaDocumentReference{ID: "schema", Revision: "1"},
				})
			}
			require.Equal(t, "INVALID_REQUEST", resourcev2.ErrorCode(err))
		})
	}
}

func TestSchemaWireRejectsRetiredAndAmbiguousOperations(t *testing.T) {
	request, err := resourcev2.ToProtoSchemaRequest(schemaFixtureRequest(resourcev2.AccessRoute{Connection: "route"}))
	require.NoError(t, err)
	encodedRequest, err := proto.Marshal(request)
	require.NoError(t, err)
	document, err := proto.Marshal(&resourcepb.SchemaDocumentRequest{
		Request: request, Reference: &resourcepb.SchemaDocumentReference{Id: "schema", Revision: "1"},
	})
	require.NoError(t, err)
	envelope := &pb.CallRequest{OwnerPlugin: "owner", DelegationToken: []byte("token")}
	base, err := proto.Marshal(envelope)
	require.NoError(t, err)
	resolve := protowire.AppendBytes(protowire.AppendTag(nil, 7, protowire.BytesType), encodedRequest)
	getDocument := protowire.AppendBytes(protowire.AppendTag(nil, 8, protowire.BytesType), document)
	for _, test := range []struct {
		name string
		wire []byte
		code string
	}{
		{"retired bare document", protowire.AppendBytes(protowire.AppendTag(nil, 6, protowire.BytesType), nil), "UNSUPPORTED"},
		{"duplicate resolver", append(append([]byte(nil), resolve...), resolve...), "INVALID_REQUEST"},
		{"mixed operations", append(append([]byte(nil), resolve...), getDocument...), "INVALID_REQUEST"},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := append(append([]byte(nil), base...), test.wire...)
			require.Equal(t, test.code, resourcev2.ErrorCode(rp.ValidateWire(wire, envelope.ProtoReflect().Descriptor())))
		})
	}
	request.Target.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), nil))
	envelope.Operation = &pb.CallRequest_ResolveSchema{ResolveSchema: request}
	wire, err := proto.Marshal(envelope)
	require.NoError(t, err)
	require.Equal(t, "INVALID_REQUEST", resourcev2.ErrorCode(rp.ValidateWire(wire, envelope.ProtoReflect().Descriptor())))
}

func TestSchemaDocumentReferenceDoesNotGrantAnotherRepresentation(t *testing.T) {
	allowed := schemaFixtureRequest(resourcev2.AccessRoute{Connection: "route"})
	service := &schemaTargetService{allowed: allowed}
	client, _, ctx := schemaFixtureClient(t, func(server *grpc.Server) { RegisterServer(server, service) })
	binding, err := client.ResolveSchema(ctx, "owner", allowed)
	require.NoError(t, err)
	require.NotNil(t, binding.DocumentRef)
	for _, representation := range []resourcev2.Representation{
		{ID: "other", Revision: allowed.Target.Representation.Revision},
		{ID: allowed.Target.Representation.ID, Revision: "other"},
	} {
		t.Run(representation.ID+"/"+representation.Revision, func(t *testing.T) {
			in := allowed
			in.Target.Representation = &representation
			_, err := client.ResolveSchema(ctx, "owner", in)
			require.Equal(t, "GRANT_DENIED", resourcev2.ErrorCode(err))
			_, err = client.GetSchemaDocument(ctx, "owner", resourcev2.SchemaDocumentRequest{Request: in, Reference: *binding.DocumentRef})
			require.Equal(t, "GRANT_DENIED", resourcev2.ErrorCode(err))
		})
	}
	require.Equal(t, 1, service.resolveCalls)
	require.Zero(t, service.schemaCalls)
}

type canceledSchemaService struct {
	testService
	started  chan struct{}
	canceled chan struct{}
}

func (s *canceledSchemaService) ResolveSchema(ctx context.Context, _ []byte, _ string, _ resourcev2.SchemaRequest) (resourcev2.SchemaBinding, error) {
	close(s.started)
	<-ctx.Done()
	close(s.canceled)
	return resourcev2.SchemaBinding{}, ctx.Err()
}

func (s *canceledSchemaService) GetSchemaDocument(ctx context.Context, _ []byte, _ string, _ resourcev2.SchemaDocumentRequest) (resourcev2.SchemaDocument, error) {
	close(s.started)
	<-ctx.Done()
	close(s.canceled)
	return resourcev2.SchemaDocument{}, ctx.Err()
}

func TestSchemaCallbacksPropagateCancellation(t *testing.T) {
	for _, operation := range []string{"resolve", "document"} {
		t.Run(operation, func(t *testing.T) {
			service := &canceledSchemaService{started: make(chan struct{}), canceled: make(chan struct{})}
			client, _, ctx := schemaFixtureClient(t, func(server *grpc.Server) { RegisterServer(server, service) })
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				request := schemaFixtureRequest(resourcev2.AccessRoute{Connection: "route"})
				var err error
				if operation == "resolve" {
					_, err = client.ResolveSchema(ctx, "owner", request)
				} else {
					_, err = client.GetSchemaDocument(ctx, "owner", resourcev2.SchemaDocumentRequest{
						Request: request, Reference: resourcev2.SchemaDocumentReference{ID: "schema", Revision: "1"},
					})
				}
				result <- err
			}()
			select {
			case <-service.started:
			case <-time.After(5 * time.Second):
				t.Fatal("schema callback did not reach host service")
			}
			cancel()
			select {
			case err := <-result:
				require.Error(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("schema callback ignored invocation cancellation")
			}
			select {
			case <-service.canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("schema callback cancellation did not reach host service")
			}
		})
	}
}
