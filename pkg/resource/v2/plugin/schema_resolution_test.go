package plugin

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func schemaFixtureRequest() resourcev2.SchemaRequest {
	return resourcev2.SchemaRequest{
		Route: testGet().Route,
		Type:  testGet().Reference.Address.Type,
		Target: resourcev2.SchemaTarget{Representation: &resourcev2.Representation{
			ID: "document", Revision: "rep-1",
		}},
	}
}

type targetedSchemaProvider struct {
	qualifiedProvider
	resolve     func(context.Context, resourcev2.SchemaRequest) (resourcev2.SchemaBinding, error)
	schemaCalls atomic.Int32
}

func (p *targetedSchemaProvider) ResolveSchema(
	ctx context.Context,
	request resourcev2.SchemaRequest,
) (resourcev2.SchemaBinding, error) {
	p.schemaCalls.Add(1)
	if p.resolve != nil {
		return p.resolve(ctx, request)
	}
	return resourcev2.SchemaBinding{State: "absent"}, nil
}

type negotiatedSchemaProvider struct {
	targetedSchemaProvider
	traits []resourcev2.Feature
}

func (p *negotiatedSchemaProvider) Features(context.Context) (resourcev2.FeatureSet, error) {
	return resourcev2.FeatureSet{Traits: p.traits}, nil
}

type schemaFeatureOnlyProvider struct {
	qualifiedProvider
	traits []resourcev2.Feature
}

func (p *schemaFeatureOnlyProvider) Features(context.Context) (resourcev2.FeatureSet, error) {
	return resourcev2.FeatureSet{Traits: p.traits}, nil
}

type schemaDocumentOnlyProvider struct{ qualifiedProvider }

func (*schemaDocumentOnlyProvider) GetSchemaDocument(
	_ context.Context,
	_ resourcev2.AccessRoute,
	reference resourcev2.SchemaDocumentReference,
) (resourcev2.SchemaDocument, error) {
	return resourcev2.SchemaDocument{Reference: reference, Dialect: "json-schema", Document: []byte(`{}`)}, nil
}

func TestActualRPCSchemaResolverRequiresInstalledOptionalFeature(t *testing.T) {
	detected := &targetedSchemaProvider{}
	disabled := &negotiatedSchemaProvider{}
	enabled := &negotiatedSchemaProvider{traits: []resourcev2.Feature{resourcev2.FeatureSchemaResolver}}
	uninstalled := &schemaFeatureOnlyProvider{traits: []resourcev2.Feature{resourcev2.FeatureSchemaResolver}}
	for _, test := range []struct {
		name      string
		provider  resourcev2.Provider
		available bool
		calls     *atomic.Int32
	}{
		{name: "no resolver", provider: &qualifiedProvider{}},
		{name: "document lookup only", provider: &schemaDocumentOnlyProvider{}},
		{name: "uninstalled declaration", provider: uninstalled},
		{name: "explicitly disabled", provider: disabled, calls: &disabled.schemaCalls},
		{name: "detected", provider: detected, available: true, calls: &detected.schemaCalls},
		{name: "explicitly enabled", provider: enabled, available: true, calls: &enabled.schemaCalls},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := grpc.NewServer()
			RegisterServer(server, test.provider)
			client := NewClient(pipe(t, server))
			ctx := acceptedCatalogContext(context.Background(), t, client, testGet().Route)
			features, err := client.Features(ctx)
			if err != nil || features.Has(resourcev2.FeatureSchemaResolver) != test.available {
				t.Fatalf("optional resolver feature mismatch: %+v %v", features, err)
			}
			binding, err := client.ResolveSchema(ctx, schemaFixtureRequest())
			if test.available {
				if err != nil || binding.State != "absent" {
					t.Fatalf("installed resolver failed: %+v %v", binding, err)
				}
			} else if resourcev2.ErrorCode(err) != "UNSUPPORTED" || binding.State != "" {
				t.Fatalf("unsupported resolver fabricated enrichment: %+v %v", binding, err)
			}
			request, err := resourcev2.ToProtoSchemaRequest(schemaFixtureRequest())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ancillary(ctx, &resourcepb.AncillaryRequest{
				Operation: ancillaryResolveSchema, SchemaRequest: request,
			})
			if !test.available && resourcev2.ErrorCode(err) != "UNSUPPORTED" {
				t.Fatalf("direct RPC bypassed optional feature: %v", err)
			}
			if test.available && err != nil {
				t.Fatal(err)
			}
			if test.calls != nil {
				want := int32(0)
				if test.available {
					want = 2
				}
				if got := test.calls.Load(); got != want {
					t.Fatalf("resolver dispatched %d times, want %d", got, want)
				}
			}
		})
	}
	if len(uninstalled.traits) != 1 || uninstalled.traits[0] != resourcev2.FeatureSchemaResolver {
		t.Fatal("feature intersection changed provider-owned traits")
	}
	if err := resourcev2.ValidateFeatures(resourcev2.FeatureSet{
		Traits: []resourcev2.Feature{"editorSchemas"},
	}); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("removed broad schema-list feature admitted: %v", err)
	}
}

func TestActualRPCSchemaResolverPreservesConsumerTarget(t *testing.T) {
	seen := make(chan resourcev2.SchemaRequest, 3)
	provider := &targetedSchemaProvider{resolve: func(
		_ context.Context, request resourcev2.SchemaRequest,
	) (resourcev2.SchemaBinding, error) {
		seen <- request
		return resourcev2.SchemaBinding{State: "absent"}, nil
	}}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	ctx := acceptedCatalogContext(context.Background(), t, client, testGet().Route)
	representation := schemaFixtureRequest()
	auxiliary := schemaFixtureRequest()
	auxiliary.Target = resourcev2.SchemaTarget{Auxiliary: &resourcev2.AuxiliarySchemaTarget{
		Key: "values", Reference: testGet().Reference,
	}}
	revision := "resource-observation-1"
	observed := schemaFixtureRequest()
	observed.Target = resourcev2.SchemaTarget{Auxiliary: &resourcev2.AuxiliarySchemaTarget{
		Key: "values", Reference: testGet().Reference, SourceRevision: &revision,
	}}
	for _, request := range []resourcev2.SchemaRequest{representation, auxiliary, observed} {
		if binding, err := client.ResolveSchema(ctx, request); err != nil || binding.State != "absent" {
			t.Fatalf("targeted schema failed: %+v %v", binding, err)
		}
		if received := <-seen; !reflect.DeepEqual(received, request) {
			t.Fatalf("selected target changed: got %+v, want %+v", received, request)
		}
	}
	invalid := auxiliary
	invalid.Target.Auxiliary.Reference.Address.Type = "other/type"
	if _, err := client.ResolveSchema(ctx, invalid); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("mismatched auxiliary type admitted: %v", err)
	}
	if got := provider.schemaCalls.Load(); got != 3 {
		t.Fatalf("invalid target reached provider: %d calls", got)
	}
}

func TestActualRPCSchemaCancellationDoesNotCancelAnotherDemand(t *testing.T) {
	entered := make(chan context.Context, 2)
	joined := make(chan struct{}, 2)
	release := make(chan struct{})
	provider := &targetedSchemaProvider{resolve: func(
		ctx context.Context, _ resourcev2.SchemaRequest,
	) (resourcev2.SchemaBinding, error) {
		entered <- ctx
		defer func() { joined <- struct{}{} }()
		select {
		case <-ctx.Done():
			// A late successful provider result must not escape canceled admission.
			return resourcev2.SchemaBinding{State: "absent"}, nil
		case <-release:
			return resourcev2.SchemaBinding{State: "absent"}, nil
		}
	}}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	base, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	base = acceptedCatalogContext(base, t, client, testGet().Route)
	if _, err := client.Features(base); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(base)
	defer cancel()
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := client.ResolveSchema(canceled, schemaFixtureRequest()); first <- err }()
	go func() { _, err := client.ResolveSchema(base, schemaFixtureRequest()); second <- err }()
	for range 2 {
		select {
		case <-entered:
		case <-base.Done():
			t.Fatal("schema demands did not reach provider")
		}
	}
	cancel()
	select {
	case err := <-first:
		if testErrorClass(err) != "CANCELLED" {
			t.Fatalf("canceled schema returned late success: %v", err)
		}
	case <-base.Done():
		t.Fatal("canceled schema demand did not stop")
	}
	select {
	case <-joined:
	case <-base.Done():
		t.Fatal("canceled schema work did not join")
	}
	select {
	case err := <-second:
		t.Fatalf("one canceled demand ended another: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("remaining demand failed: %v", err)
		}
	case <-base.Done():
		t.Fatal("remaining schema demand did not finish")
	}
	select {
	case <-joined:
	case <-base.Done():
		t.Fatal("remaining schema work did not join")
	}
}

func TestActualRPCServerRejectsInvalidResolvedSchemaBinding(t *testing.T) {
	provider := &targetedSchemaProvider{resolve: func(
		context.Context, resourcev2.SchemaRequest,
	) (resourcev2.SchemaBinding, error) {
		return resourcev2.SchemaBinding{State: "available"}, nil
	}}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	ctx := acceptedCatalogContext(context.Background(), t, client, testGet().Route)
	if binding, err := client.ResolveSchema(ctx, schemaFixtureRequest()); resourcev2.ErrorCode(err) != "INVALID_REQUEST" || binding.State != "" {
		t.Fatalf("invalid resolved binding escaped server: %+v %v", binding, err)
	}
}

type schemaBindingWire struct {
	resourcepb.UnimplementedResourceAncillaryServer
	response *resourcepb.AncillaryResponse
}

func (s schemaBindingWire) Call(context.Context, *resourcepb.AncillaryRequest) (*resourcepb.AncillaryResponse, error) {
	return s.response, nil
}

func TestActualRPCClientRejectsInvalidSchemaBindingResults(t *testing.T) {
	for name, result := range map[string]*resourcepb.AncillaryResponse{
		"missing binding": {Version: resourcev2.WireVersion},
		"wrong result":    {Version: resourcev2.WireVersion, Result: &resourcepb.AncillaryResponse_Json{Json: []byte(`{}`)}},
		"invalid binding": {Version: resourcev2.WireVersion, Result: &resourcepb.AncillaryResponse_SchemaBinding{
			SchemaBinding: &resourcepb.SchemaBinding{State: "available"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			server := grpc.NewServer()
			resourcepb.RegisterResourceAncillaryServer(server, schemaBindingWire{response: result})
			client := NewClient(pipe(t, server))
			// This adversarial fixture supplies raw ancillary results, not feature RPCs.
			client.features.Store(&resourcev2.FeatureSet{Traits: []resourcev2.Feature{resourcev2.FeatureSchemaResolver}})
			ctx := resourcev2.WithCatalogRevision(context.Background(), "cat-1")
			if binding, err := client.ResolveSchema(ctx, schemaFixtureRequest()); resourcev2.ErrorCode(err) != "INVALID_REQUEST" || binding.State != "" {
				t.Fatalf("invalid resolved binding escaped client: %+v %v", binding, err)
			}
		})
	}
}

func TestTargetedSchemaRawWireRequiredFields(t *testing.T) {
	valid, err := resourcev2.ToProtoSchemaRequest(schemaFixtureRequest())
	if err != nil {
		t.Fatal(err)
	}
	auxiliary := &resourcepb.AuxiliarySchemaTarget{
		Key: "values", Reference: &resourcepb.ResourceReference{Address: &resourcepb.ResourceAddress{
			Provider: "test", Authority: "authority", Type: "opaque/type", Id: "opaque/id",
		}},
	}
	for name, message := range map[string]proto.Message{
		"request route":    &resourcepb.SchemaRequest{Type: valid.Type, Target: valid.Target},
		"request type":     &resourcepb.SchemaRequest{Route: valid.Route, Target: valid.Target},
		"request target":   &resourcepb.SchemaRequest{Route: valid.Route, Type: valid.Type},
		"target selection": &resourcepb.SchemaTarget{},
		"representation revision": &resourcepb.SchemaTarget{Selection: &resourcepb.SchemaTarget_Representation{
			Representation: &resourcepb.Representation{Id: "document"},
		}},
		"auxiliary key":       &resourcepb.AuxiliarySchemaTarget{Reference: auxiliary.Reference},
		"auxiliary reference": &resourcepb.AuxiliarySchemaTarget{Key: auxiliary.Key},
		"document target": &resourcepb.SchemaDocumentRequest{
			Reference: &resourcepb.SchemaDocumentReference{Id: "document", Revision: "revision"},
		},
		"document reference": &resourcepb.SchemaDocumentRequest{Request: valid},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := proto.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateWire(data, message.ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("missing schema field passed raw admission: %v", err)
			}
		})
	}
	representation, err := proto.Marshal(valid.Target.GetRepresentation())
	if err != nil {
		t.Fatal(err)
	}
	auxiliaryBytes, err := proto.Marshal(auxiliary)
	if err != nil {
		t.Fatal(err)
	}
	both := protowire.AppendTag(nil, 1, protowire.BytesType)
	both = protowire.AppendBytes(both, representation)
	both = protowire.AppendTag(both, 2, protowire.BytesType)
	both = protowire.AppendBytes(both, auxiliaryBytes)
	if err := ValidateWire(both, (&resourcepb.SchemaTarget{}).ProtoReflect().Descriptor()); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("multiple schema targets passed raw admission: %v", err)
	}
	removed := protowire.AppendTag(nil, 1, protowire.VarintType)
	removed = protowire.AppendVarint(removed, uint64(resourcev2.WireVersion))
	if err := ValidateWire(removed, (&resourcepb.AncillaryResponse{}).ProtoReflect().Descriptor()); err != nil {
		t.Fatalf("valid empty ancillary response rejected: %v", err)
	}
	removed = protowire.AppendTag(removed, 8, protowire.BytesType)
	removed = protowire.AppendBytes(removed, nil)
	if err := ValidateWire(removed, (&resourcepb.AncillaryResponse{}).ProtoReflect().Descriptor()); err == nil {
		t.Fatalf("removed editor schema list result passed raw admission: %v", err)
	}
}
