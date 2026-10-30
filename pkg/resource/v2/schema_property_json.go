package resourcev2

import (
 "bytes"
 "encoding/json"
 "math/big"
)

// UnmarshalJSON rejects duplicate fields and constraints outside this subset.
// A default keyword is unsupported even when its value is null.
func (p *SchemaProperty) UnmarshalJSON(data []byte) error {
 if err := ValidateJSON(data); err != nil { return err }
 var fields map[string]json.RawMessage
 if err := json.Unmarshal(data, &fields); err != nil { return err }
 if fields == nil { return invalid("action schema property must be object") }
 if _, ok := fields["default"]; ok { return invalid("action schema defaults are unsupported") }
 for _, name := range []string{"enum", "minimum", "maximum", "properties", "required"} {
  if raw, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) { return invalid("action schema constraint must not be null") }
 }
 type plain SchemaProperty
 var next plain
 decoder := json.NewDecoder(bytes.NewReader(data))
 decoder.DisallowUnknownFields()
 if err := decoder.Decode(&next); err != nil { return err }
 for _, constraint := range []struct {
  name string
  value *float64
 }{{"minimum", next.Minimum}, {"maximum", next.Maximum}} {
  if raw, present := fields[constraint.name]; present {
   bound, ok := new(big.Rat).SetString(string(bytes.TrimSpace(raw)))
   if !ok || constraint.value == nil {
    return invalid("action schema bound is not exactly representable as float64")
   }
   decoded := new(big.Rat).SetFloat64(*constraint.value)
   if decoded == nil || bound.Cmp(decoded) != 0 {
    return invalid("action schema bound is not exactly representable as float64")
   }
  }
 }
 if err := validateActionProperty(SchemaProperty(next), 0); err != nil { return err }
 *p = SchemaProperty(next)
 return nil
}

// MarshalJSON emits bounds as exact decimal values of their binary float64
// representation, so strict decoders never round a schema during transport.
func (p SchemaProperty) MarshalJSON() ([]byte, error) {
 if err := validateActionProperty(p, 0); err != nil { return nil, err }
 type plain SchemaProperty
 return json.Marshal(struct {
  plain
  Minimum json.RawMessage `json:"minimum,omitempty"`
  Maximum json.RawMessage `json:"maximum,omitempty"`
 }{plain(p), exactSchemaBound(p.Minimum), exactSchemaBound(p.Maximum)})
}

func exactSchemaBound(bound *float64) json.RawMessage {
 if bound == nil { return nil }
 rational := new(big.Rat).SetFloat64(*bound)
 // A reduced float64 denominator is a power of two. That exponent is also
 // the maximum decimal fractional precision needed for an exact expansion.
 precision := rational.Denom().BitLen() - 1
 decimal := []byte(rational.FloatString(precision))
 if precision > 0 {
  decimal = bytes.TrimRight(decimal, "0")
  decimal = bytes.TrimSuffix(decimal, []byte("."))
 }
 return json.RawMessage(decimal)
}
