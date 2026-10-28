package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"google.golang.org/grpc"
	"testing"
	"time"
)

type ancillaryFixtureProvider struct{ qualifiedProvider }

func (*ancillaryFixtureProvider) GetResourceGroups(context.Context, resourcev2.AccessRoute) ([]resourcev2.ResourceGroup, error) {
	return []resourcev2.ResourceGroup{{ID: "group", Name: "Group", Resources: []string{"opaque/type"}}}, nil
}
func (*ancillaryFixtureProvider) GetResourceDefinition(context.Context, resourcev2.AccessRoute, string) (resourcev2.ResourceDefinition, error) {
	return resourcev2.ResourceDefinition{ColumnDefs: []resourcev2.ColumnDefinition{{ID: "name", Header: "Name", Accessors: "metadata.name", ResourceLink: &resourcev2.ResourceLink{Type: "opaque/type", IDAccessor: "metadata.name", ScopeAccessors: []resourcev2.ScopeExtractor{{Dimension: "namespace", FieldPath: "metadata.namespace"}}}}}}, nil
}
func (*ancillaryFixtureProvider) GetEditorSchemas(context.Context, resourcev2.AccessRoute) ([]resourcev2.EditorSchema, error) {
	revision := "schema-1"
	return []resourcev2.EditorSchema{{Type: "opaque/type", FileMatch: "*.yaml", URI: "schema://document", Language: "yaml", Binding: resourcev2.SchemaBinding{State: "available", Revision: &revision, Dialect: "json-schema", DocumentRef: &resourcev2.SchemaDocumentReference{ID: "shared-document", Revision: "document-1"}, Root: "#/definitions/Thing", Validation: "none", Unvalidated: []string{}}}}, nil
}
func (*ancillaryFixtureProvider) GetSchemaDocument(_ context.Context, _ resourcev2.AccessRoute, ref resourcev2.SchemaDocumentReference) (resourcev2.SchemaDocument, error) {
	return resourcev2.SchemaDocument{Reference: ref, Dialect: "json-schema", Document: []byte(`{"definitions":{"Thing":{"type":"object"}}}`)}, nil
}
func testRelationship() resourcev2.RelationshipDescriptor {
	return resourcev2.RelationshipDescriptor{Type: resourcev2.RelUses, TargetType: "opaque/type", Label: "Uses", TargetScope: []resourcev2.ScopeEntry{{Dimension: "region", Value: "opaque-region"}}, ScopeExtractors: []resourcev2.ScopeExtractor{{Dimension: "namespace", FieldPath: "metadata.namespace"}}}
}
func (*ancillaryFixtureProvider) GetRelationships(context.Context, resourcev2.AccessRoute, string) ([]resourcev2.RelationshipDescriptor, error) {
	return []resourcev2.RelationshipDescriptor{testRelationship()}, nil
}
func (*ancillaryFixtureProvider) ResolveRelationships(context.Context, resourcev2.GetRequest) ([]resourcev2.ResolvedRelationship, error) {
	return []resourcev2.ResolvedRelationship{{Descriptor: testRelationship(), Targets: []resourcev2.RelationshipTarget{{Reference: testGet().Reference, Route: testGet().Route, DisplayName: "linked resource"}}}}, nil
}
func (*ancillaryFixtureProvider) GetHealth(context.Context, resourcev2.GetRequest, resourcev2.Value) (*resourcev2.ResourceHealth, error) {
	return nil, nil
}
func (*ancillaryFixtureProvider) GetResourceEvents(context.Context, resourcev2.GetRequest, uint32) ([]resourcev2.ResourceEvent, error) {
	return []resourcev2.ResourceEvent{{Type: resourcev2.SeverityNormal, Reason: "Observed", Message: "native event", Count: 1, FirstSeen: time.Unix(-1, 123).UTC(), LastSeen: time.Unix(0, 456).UTC()}}, nil
}
func (*ancillaryFixtureProvider) GetActions(context.Context, resourcev2.AccessRoute, string) ([]resourcev2.ActionDescriptor, error) {
	return []resourcev2.ActionDescriptor{{ID: "run", Label: "Run", Scope: resourcev2.ActionScopeInstance, Streaming: true}}, nil
}
func TestActualGRPCCanonicalAncillaryWorkflows(t *testing.T) {
	s := grpc.NewServer()
	RegisterServer(s, &ancillaryFixtureProvider{})
	c := NewClient(pipe(t, s))
	ctx := context.Background()
	route := testGet().Route
	groups, err := c.GetResourceGroups(ctx, route)
	if err != nil || len(groups) != 1 || groups[0].Resources[0] != "opaque/type" {
		t.Fatalf("groups: %v %v", groups, err)
	}
	definition, err := c.GetResourceDefinition(ctx, route, "opaque/type")
	if err != nil || len(definition.ColumnDefs) != 1 || definition.ColumnDefs[0].ResourceLink.ScopeAccessors[0].FieldPath != "metadata.namespace" {
		t.Fatalf("definition: %v %v", definition, err)
	}
	schemas, err := c.GetEditorSchemas(ctx, route)
	if err != nil || len(schemas) != 1 {
		t.Fatalf("schemas: %v %v", schemas, err)
	}
	binding := schemas[0].Binding
	if binding.DocumentRef == nil || binding.Root != "#/definitions/Thing" || len(binding.Document) != 0 {
		t.Fatal("editor schema lost shared reference or root")
	}
	document, err := c.GetSchemaDocument(ctx, route, *binding.DocumentRef)
	if err != nil || document.Reference != *binding.DocumentRef {
		t.Fatalf("schema document: %v %v", document, err)
	}
	descriptors, err := c.GetRelationships(ctx, route, "opaque/type")
	if err != nil || len(descriptors) != 1 || descriptors[0].ScopeExtractors[0].Dimension != "namespace" {
		t.Fatalf("relationships: %v %v", descriptors, err)
	}
	resolved, err := c.ResolveRelationships(ctx, testGet())
	if err != nil || len(resolved) != 1 || resolved[0].Targets[0].Reference.Address.ID != "opaque/id" {
		t.Fatalf("resolved: %v %v", resolved, err)
	}
	health, err := c.GetHealth(ctx, testGet(), resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{}})
	if err != nil || health != nil {
		t.Fatalf("unassessed health was fabricated: %v %v", health, err)
	}
	events, err := c.GetResourceEvents(ctx, testGet(), 1)
	if err != nil || len(events) != 1 || !events[0].FirstSeen.Equal(time.Unix(-1, 123)) {
		t.Fatalf("native timestamp event: %v %v", events, err)
	}
	actions, err := c.GetActions(ctx, route, "opaque/type")
	if err != nil || len(actions) != 1 || !actions[0].Streaming {
		t.Fatalf("actions: %v %v", actions, err)
	}
}
