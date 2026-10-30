package resourcev2

import (
 "encoding/json"
 "math"
 "reflect"
 "testing"
)

func TestRestrictedSchemaDeclarations(t *testing.T) {
 for _, input := range []string{
  `{"type":"object","properties":{"x":{"type":"string","minimum":0}}}`,
  `{"type":"object","properties":{"x":{"type":"integer","enum":["1"]}}}`,
  `{"type":"object","properties":{"x":{"type":"array","items":{"type":"string"}}}}`,
  `{"type":"object","properties":{"x":{"type":"string","default":null}}}`,
  `{"type":"object","properties":{"x":{"type":"string","default":"x"}}}`,
  `{"type":"object","properties":{"x":{"type":"string","properties":{}}}}`,
  `{"type":"object","required":["x","x"],"properties":{"x":{"type":"string"}}}`,
  `{"type":"object","properties":{"x":{"type":"string","enum":[]}}}`,
  `{"type":"object","properties":{"x":{"type":"string","type":"integer"}}}`,
  `{"type":"object","properties":{"x":{"type":"string"},"x":{"type":"integer"}}}`,
 } {
  t.Run(input, func(t *testing.T) {
   original := Schema{Required: []string{"unchanged"}}
   if err := json.Unmarshal([]byte(input), &original); err == nil { t.Fatal("unsupported schema admitted") }
   if len(original.Required) != 1 || original.Required[0] != "unchanged" { t.Fatal("failed decode changed schema") }
  })
 }
 for _, bound := range []float64{math.NaN(), math.Inf(1)} {
  if err := ValidateSchema(Schema{Properties: map[string]SchemaProperty{"n": {Type: SchemaNumber, Minimum: &bound}}}); err == nil { t.Fatal("nonfinite bound admitted") }
 }
}

func TestRestrictedSchemaExactNumericKinds(t *testing.T) {
 bound := float64(9223372036854775808.0)
 s := Schema{Properties: map[string]SchemaProperty{"n": {Type: SchemaInteger, Minimum: &bound}}, Required: []string{"n"}}
 for _, tc := range []struct{value Value; valid bool}{
  {Value{Kind:"int64", Int:"9223372036854775807"}, false},
  {Value{Kind:"uint64", Uint:"9223372036854775808"}, true},
  {Value{Kind:"uint64", Uint:"18446744073709551615"}, true},
  {Value{Kind:"double", Double:bound}, true},
  {Value{Kind:"string", String:"9223372036854775808"}, false},
 } {
  input := Value{Kind:"map", Map:map[string]Value{"n":tc.value}}
  err := ValidateSchemaValue(s, input)
  if (err == nil) != tc.valid { t.Fatalf("%+v: %v", tc.value, err) }
  if !reflect.DeepEqual(input.Map["n"], tc.value) { t.Fatal("numeric value changed") }
 }
}

func TestRestrictedSchemaNestedRequiredAndEnum(t *testing.T) {
 s := Schema{Properties: map[string]SchemaProperty{"o": {Type:SchemaObject, Properties:map[string]SchemaProperty{"s":{Type:SchemaString, Enum:[]string{"yes"}}}, Required:[]string{"s"}}}, Required:[]string{"o"}}
 for _, tc := range []struct{value Value; valid bool}{
  {Value{Kind:"map", Map:map[string]Value{"o":{Kind:"map", Map:map[string]Value{"s":{Kind:"string", String:"yes"}, "extra":{Kind:"bool", Bool:true}}}}}, true},
  {Value{Kind:"map", Map:map[string]Value{"o":{Kind:"map"}}}, false},
  {Value{Kind:"map", Map:map[string]Value{"o":{Kind:"map", Map:map[string]Value{"s":{Kind:"string", String:"no"}}}}}, false},
  {Value{Kind:"map"}, false},
 } { if err := ValidateSchemaValue(s, tc.value); (err == nil) != tc.valid { t.Fatalf("valid=%v: %v", tc.valid, err) } }
}

func TestActionDescriptorSchemaScopeAndLane(t *testing.T) {
 request := typeActionRequest()
 request.Input = Value{Kind:"map"}
 descriptor := ActionDescriptor{ID:request.Action, Scope:ActionScopeType, ParamsSchema: (&Schema{}).Ptr(), OutputSchema: (&Schema{}).Ptr()}
 if err := ValidateActionDescriptorRequest(request, descriptor, false); err != nil { t.Fatal(err) }
 if err := ValidateActionDescriptorRequest(request, descriptor, true); err == nil { t.Fatal("wrong lane admitted") }
 descriptor.Scope = ActionScopeInstance
 if err := ValidateActionDescriptorRequest(request, descriptor, false); err == nil { t.Fatal("wrong scope admitted") }
 if err := ValidateActionOutput(descriptor, Value{Kind:"string", String:"not object"}); err == nil { t.Fatal("invalid output admitted") }
}
