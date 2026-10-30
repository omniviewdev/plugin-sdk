package resourcev2

import (
 "context"
 "testing"
)

type snapshotActionCatalog struct { descriptor ActionDescriptor }
func (p *snapshotActionCatalog) GetActions(context.Context, AccessRoute, string) ([]ActionDescriptor, error) {
 return []ActionDescriptor{p.descriptor}, nil
}

func snapshotActionDescriptor() ActionDescriptor {
 return ActionDescriptor{ID:"run", Scope:ActionScopeType, ParamsSchema:&Schema{
  Properties:map[string]SchemaProperty{"object":{Type:SchemaObject, Properties:map[string]SchemaProperty{"name":{Type:SchemaString, Enum:[]string{"first"}}}, Required:[]string{"name"}}, "count":{Type:SchemaInteger, Minimum:PtrFloat64(1)}},
  Required:[]string{"object"},
 }, OutputSchema:&Schema{Properties:map[string]SchemaProperty{"status":{Type:SchemaString, Enum:[]string{"ready"}}}}}
}

func TestResolvedActionDescriptorOwnsSchemaSnapshot(t *testing.T) {
 provider := &snapshotActionCatalog{descriptor:snapshotActionDescriptor()}
 request := typeActionRequest()
 request.Input = Value{Kind:"map", Map:map[string]Value{"object":{Kind:"map", Map:map[string]Value{"name":{Kind:"string", String:"first"}}}}}
 observed, err := ResolveActionDescriptor(context.Background(), provider, request, false)
 if err != nil { t.Fatal(err) }
 request.ExpectedDescriptor = observed
 nested := provider.descriptor.ParamsSchema.Properties["object"]
 nested.Properties["name"].Enum[0] = "changed"
 nested.Required[0] = "other"
 provider.descriptor.ParamsSchema.Required[0] = "other"
 *provider.descriptor.ParamsSchema.Properties["count"].Minimum = 2
 provider.descriptor.OutputSchema.Properties["status"].Enum[0] = "changed"
 if observed.ParamsSchema.Properties["object"].Properties["name"].Enum[0] != "first" || observed.ParamsSchema.Properties["object"].Required[0] != "name" || observed.ParamsSchema.Required[0] != "object" || *observed.ParamsSchema.Properties["count"].Minimum != 1 || observed.OutputSchema.Properties["status"].Enum[0] != "ready" { t.Fatal("resolved descriptor shared mutable schema metadata") }
 if _, err := ResolveActionDescriptor(context.Background(), provider, request, false); ErrorCode(err) != "RESET_REQUIRED" { t.Fatalf("changed provider schema inherited retained observation: %v", err) }
}

func TestControllerActionDiscoveryOwnsRegistrationSchemas(t *testing.T) {
 descriptor := snapshotActionDescriptor()
 controller := scopedActionController(t, descriptor, descriptor, new(int))
 actions, err := controller.GetActions(context.Background(), controllerTarget().Route, "opaque/type")
 if err != nil || len(actions) != 1 { t.Fatalf("discovery: %+v %v", actions, err) }
 descriptor.ParamsSchema.Required[0] = "registration mutation"
 actions[0].ParamsSchema.Properties["object"].Properties["name"].Enum[0] = "discovery mutation"
 actions[0].OutputSchema.Properties["status"].Enum[0] = "output mutation"
 again, err := controller.GetActions(context.Background(), controllerTarget().Route, "opaque/type")
 if err != nil { t.Fatal(err) }
 if again[0].ParamsSchema.Required[0] != "object" || again[0].ParamsSchema.Properties["object"].Properties["name"].Enum[0] != "first" || again[0].OutputSchema.Properties["status"].Enum[0] != "ready" { t.Fatal("controller retained externally mutable schema metadata") }
}
