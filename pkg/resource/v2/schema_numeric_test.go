package resourcev2

import (
 "encoding/json"
 "math"
 "testing"
)

func TestRestrictedSchemaNumericBounds(t *testing.T) {
 zero, one := 0.0, 1.0
 for _, tc := range []struct{name string; typ SchemaType; value Value; valid bool}{
  {"negative zero",SchemaNumber,Value{Kind:"double",Double:math.Copysign(0,-1)},true},
  {"zero signed",SchemaInteger,Value{Kind:"int64",Int:"0"},true},
  {"zero unsigned",SchemaInteger,Value{Kind:"uint64",Uint:"0"},true},
  {"upper endpoint",SchemaNumber,Value{Kind:"double",Double:1},true},
  {"below",SchemaNumber,Value{Kind:"double",Double:-0.5},false},
  {"above",SchemaNumber,Value{Kind:"uint64",Uint:"2"},false},
  {"integer rejects fraction",SchemaInteger,Value{Kind:"double",Double:0.5},false},
  {"number accepts fraction",SchemaNumber,Value{Kind:"double",Double:0.5},true},
 } {
  t.Run(tc.name,func(t *testing.T){
   s := Schema{Properties:map[string]SchemaProperty{"n":{Type:tc.typ,Minimum:&zero,Maximum:&one}}}
   if err:=ValidateSchemaValue(s,Value{Kind:"map",Map:map[string]Value{"n":tc.value}}); (err==nil)!=tc.valid {t.Fatalf("valid=%v: %v",tc.valid,err)}
  })
 }
 // The subset has no item schema and must not invent array element constraints.
 s:=Schema{Properties:map[string]SchemaProperty{"items":{Type:SchemaArray}},Required:[]string{"items","opaque"}}
 v:=Value{Kind:"map",Map:map[string]Value{"items":{Kind:"list",List:[]Value{{Kind:"null"},{Kind:"uint64",Uint:"18446744073709551615"}}},"opaque":{Kind:"bool",Bool:true}}}
 if err:=ValidateSchemaValue(s,v);err!=nil {t.Fatal(err)}
 delete(v.Map,"opaque")
 if err:=ValidateSchemaValue(s,v);err==nil {t.Fatal("undeclared required member was not enforced")}
}

func TestRestrictedSchemaJSONBoundsRequireExactFloat64(t *testing.T) {
 for _, keyword := range []string{"minimum", "maximum"} {
  for _, tc := range []struct { literal string; valid bool }{
   {"9007199254740992", true},
   {"9007199254740993", false},
   {"-9007199254740993", false},
   {"0.5", true},
   {"0.1", false},
   {"0.1000000000000000055511151231257827021181583404541015625", true},
   {"-0.1000000000000000055511151231257827021181583404541015625", true},
   {"5e-1", true},
   {"1e-400", false},
   {"-0", true},
  } {
   t.Run(keyword+"/"+tc.literal, func(t *testing.T) {
    property := SchemaProperty{Type: SchemaString}
    input := []byte(`{"type":"number","` + keyword + `":` + tc.literal + `}`)
    err := json.Unmarshal(input, &property)
    if (err == nil) != tc.valid { t.Fatalf("valid=%v: %v", tc.valid, err) }
    if !tc.valid && property.Type != SchemaString { t.Fatal("failed decode changed property") }
   })
  }
 }
}

func TestRestrictedSchemaGoBoundsRoundTripExactly(t *testing.T) {
 for _, bound := range []float64{0.1, -0.1, math.SmallestNonzeroFloat64, math.MaxFloat64} {
  original := Schema{Properties: map[string]SchemaProperty{"n": {Type: SchemaNumber, Minimum: &bound, Maximum: &bound}}}
  encoded, err := json.Marshal(original)
  if err != nil { t.Fatal(err) }
  var decoded Schema
  if err := json.Unmarshal(encoded, &decoded); err != nil { t.Fatalf("bound=%g: %v", bound, err) }
  property := decoded.Properties["n"]
  if property.Minimum == nil || property.Maximum == nil || *property.Minimum != bound || *property.Maximum != bound {
   t.Fatalf("bound changed: original=%g decoded=%+v", bound, property)
  }
 }
}
