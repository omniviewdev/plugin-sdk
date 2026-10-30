package resourcev2

import (
 "encoding/json"
 "reflect"
 "testing"

 resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func typeActionRequest() ActionRequest {
 return ActionRequest{
  Route: AccessRoute{Connection: "connection"},
  TypeTarget: &TypeActionTarget{Provider: "provider", Authority: "authority", Type: "type", Scope: QueryScope{}},
  Action: "run", Input: Value{Kind: "null"},
 }
}

func TestActionTargetExclusive(t *testing.T) {
 request := typeActionRequest()
 if err := ValidateActionRequest(request); err != nil { t.Fatal(err) }
 request.Reference = ResourceReference{Address: ResourceAddress{Provider: "provider", Authority: "authority", Type: "type", ID: "object"}}
 if err := ValidateActionRequest(request); err == nil { t.Fatal("accepted both targets") }
 request.TypeTarget = nil
 if err := ValidateActionRequest(request); err != nil { t.Fatal(err) }
 request.Reference = ResourceReference{}
 if err := ValidateActionRequest(request); err == nil { t.Fatal("accepted missing target") }
}

func TestTypeActionTargetRoundTrip(t *testing.T) {
 request := typeActionRequest()
 encoded, err := json.Marshal(request)
 if err != nil { t.Fatal(err) }
 var decoded ActionRequest
 if err = json.Unmarshal(encoded, &decoded); err != nil { t.Fatal(err) }
 if !reflect.DeepEqual(request, decoded) { t.Fatalf("JSON target changed: %#v", decoded) }
 wire, err := ToProtoActionRequest(request)
 if err != nil { t.Fatal(err) }
 if wire.Reference != nil || wire.TypeTarget == nil { t.Fatal("type target encoded as instance") }
 decoded, err = FromProtoActionRequest(wire)
 if err != nil { t.Fatal(err) }
 if !reflect.DeepEqual(request, decoded) { t.Fatalf("protobuf target changed: %#v", decoded) }
 wire.Reference = &resourcepb.ResourceReference{}
 if _, err = FromProtoActionRequest(wire); err == nil { t.Fatal("accepted two wire targets") }
 wire.Reference, wire.TypeTarget = nil, nil
 if _, err = FromProtoActionRequest(wire); err == nil { t.Fatal("accepted no wire target") }
}

func TestTypeActionTargetStrictJSON(t *testing.T) {
 for _, input := range []string{
  `{"route":{"connection":"connection"},"typeTarget":{"provider":"provider","authority":"authority","type":"type","scope":[],"id":"fake"},"action":"run","input":{"kind":"null"}}`,
  `{"route":{"connection":"connection"},"typeTarget":{"provider":"provider","provider":"other","authority":"authority","type":"type","scope":[]},"action":"run","input":{"kind":"null"}}`,
 } {
  var request ActionRequest
  if err := json.Unmarshal([]byte(input), &request); err == nil { t.Fatal("accepted invalid type target") }
 }
}
