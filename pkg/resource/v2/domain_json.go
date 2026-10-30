package resourcev2

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// decodeDomainObject checks the original bytes before encoding/json can replace
// Unicode or erase duplicate members. Aliased destination structs avoid calling
// their own UnmarshalJSON method while retaining strict nested admission.
func decodeDomainObject(data []byte, out any, required ...string) (map[string]any, error) {
	raw, err := parseJSONWithLimit(data, MaxMessageBytes)
	if err != nil {
		return nil, err
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, invalid("domain DTO must be object")
	}
	if err := validateDomainFields(object, reflect.TypeOf(out).Elem()); err != nil {
		return nil, err
	}
	for _, name := range required {
		if _, present := object[name]; !present {
			return nil, invalid("missing required field: " + name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		if ErrorCode(err) != errorCodeInternal {
			return nil, err
		}
		return nil, invalid("invalid domain DTO JSON")
	}
	return object, nil
}

func validateDomainFields(object map[string]any, fields reflect.Type) error {
	for name, value := range object {
		fieldType, found := domainFieldType(fields, name)
		if !found {
			return invalid("unknown domain DTO field: " + name)
		}
		if value == nil && fieldType != reflect.TypeFor[json.RawMessage]() {
			return invalid("null domain DTO field: " + name)
		}
	}
	return nil
}

func domainFieldType(fields reflect.Type, name string) (reflect.Type, bool) {
	for i := range fields.NumField() {
		field := fields.Field(i)
		fieldName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if fieldName == "" {
			fieldName = field.Name
		}
		if name == fieldName && fieldName != "-" {
			return field.Type, true
		}
	}
	return nil, false
}

func rejectNullTokens(object map[string]any, names ...string) error {
	for _, name := range names {
		if value, present := object[name]; present && value == nil {
			return invalid("optional token must be omitted, not null: " + name)
		}
	}
	return nil
}

// UnmarshalJSON decodes ScopeEntry, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *ScopeEntry) UnmarshalJSON(data []byte) error {
	type plain ScopeEntry
	var out plain
	if _, err := decodeDomainObject(data, &out, "dimension", "value"); err != nil {
		return err
	}
	value := ScopeEntry(out)
	if err := validateEntry(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes ResourceAddress, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *ResourceAddress) UnmarshalJSON(data []byte) error {
	type plain ResourceAddress
	var out plain
	if _, err := decodeDomainObject(data, &out, "provider", "authority", "type", "scope", "id"); err != nil {
		return err
	}
	value := ResourceAddress(out)
	if value.Scope == nil {
		return invalid("address scope must be array")
	}
	if err := ValidateAddress(value); err != nil {
		return err
	}
	// This storage belongs to the decoder; never sort caller-owned slices.
	sort.Slice(value.Scope, func(i, j int) bool { return value.Scope[i].Dimension < value.Scope[j].Dimension })
	*v = value
	return nil
}

// UnmarshalJSON decodes AccessRoute, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *AccessRoute) UnmarshalJSON(data []byte) error {
	type plain AccessRoute
	var out plain
	if _, err := decodeDomainObject(data, &out, "connection"); err != nil {
		return err
	}
	value := AccessRoute(out)
	if err := validateRoute(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes ResourceReference, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *ResourceReference) UnmarshalJSON(data []byte) error {
	type plain ResourceReference
	var out plain
	object, err := decodeDomainObject(data, &out, "address")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "incarnation"); err != nil {
		return err
	}
	value := ResourceReference(out)
	if err := validateReference(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes ScopeConstraint, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *ScopeConstraint) UnmarshalJSON(data []byte) error {
	raw, err := parseJSONWithLimit(data, MaxMessageBytes)
	if err != nil {
		return err
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return invalid("scope constraint must be object")
	}
	dimension, ok := object["dimension"].(string)
	if !ok {
		return invalid("scope dimension must be string")
	}
	match, ok := object["match"].(string)
	if !ok {
		return invalid("scope match must be string")
	}
	value := ScopeConstraint{Dimension: dimension, Match: ScopeMatch(match)}
	switch value.Match {
	case ScopeAny:
		const anyScopeFields = 2 // dimension and match
		if len(object) != anyScopeFields {
			return invalid("any scope must omit values and unknown fields")
		}
	case ScopeOneOf:
		const oneOfScopeFields = 3 // dimension, match, and values
		if len(object) != oneOfScopeFields {
			return invalid("oneOf scope requires values only")
		}
		rawValues, ok := object["values"].([]any)
		if !ok {
			return invalid("oneOf scope values must be array")
		}
		values := make([]string, len(rawValues))
		for i, raw := range rawValues {
			s, ok := raw.(string)
			if !ok {
				return invalid("scope value must be string")
			}
			values[i] = s
		}
		value.Values = &values
	default:
		return invalid("unknown scope match")
	}
	if err := validateConstraint(value); err != nil {
		return err
	}
	if value.Values != nil {
		*value.Values = canonicalScopeValues(*value.Values)
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes QueryScope, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *QueryScope) UnmarshalJSON(data []byte) error {
	if _, err := parseJSONWithLimit(data, MaxMessageBytes); err != nil {
		return err
	}
	type plain QueryScope
	var out plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		if ErrorCode(err) != errorCodeInternal {
			return err
		}
		return invalid("query scope must be array")
	}
	if out == nil {
		return invalid("query scope must be nonnull array")
	}
	value := QueryScope(out)
	if err := ValidateScope(value); err != nil {
		return err
	}
	*v = normalizeScopeOwned(value)
	return nil
}

// UnmarshalJSON decodes ReadTarget, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *ReadTarget) UnmarshalJSON(data []byte) error {
	type plain ReadTarget
	var out plain
	if _, err := decodeDomainObject(
		data,
		&out,
		"route",
		"provider",
		"authority",
		"type",
		"scope",
		"representation",
	); err != nil {
		return err
	}
	value := ReadTarget(out)
	if value.Scope == nil {
		return invalid("target scope must be array")
	}
	if err := validateTarget(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes CatalogRequest, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *CatalogRequest) UnmarshalJSON(data []byte) error {
	type plain CatalogRequest
	var out plain
	if _, err := decodeDomainObject(data, &out, "route"); err != nil {
		return err
	}
	value := CatalogRequest(out)
	if err := validateRoute(value.Route); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes GetRequest, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *GetRequest) UnmarshalJSON(data []byte) error {
	type plain GetRequest
	var out plain
	if _, err := decodeDomainObject(data, &out, "route", "reference", "representation"); err != nil {
		return err
	}
	value := GetRequest(out)
	if err := ValidateGetRequest(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes EnumerateRequest, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *EnumerateRequest) UnmarshalJSON(data []byte) error {
	type plain EnumerateRequest
	var out plain
	object, err := decodeDomainObject(data, &out, "target")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "cursor"); err != nil {
		return err
	}
	value := EnumerateRequest(out)
	if err := ValidateEnumerateRequest(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes NativeQueryRequest, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *NativeQueryRequest) UnmarshalJSON(data []byte) error {
	type plain NativeQueryRequest
	var out plain
	object, err := decodeDomainObject(data, &out, "target", "language", "query")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "cursor"); err != nil {
		return err
	}
	value := NativeQueryRequest(out)
	if err := ValidateNativeQueryRequest(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes MutationRequest, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *MutationRequest) UnmarshalJSON(data []byte) error {
	type plain MutationRequest
	var out plain
	object, err := decodeDomainObject(data, &out, "route", "reference", "operation", "input")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "expectedRevision"); err != nil {
		return err
	}
	value := MutationRequest(out)
	if err := ValidateMutationRequest(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// MarshalJSON omits the unused instance reference for type-scoped actions.
func (v ActionRequest) MarshalJSON() ([]byte, error) {
	if err := ValidateActionRequest(v); err != nil {
		return nil, err
	}
	var reference *ResourceReference
	if v.TypeTarget == nil {
		reference = &v.Reference
	}
	return json.Marshal(struct {
		Route              AccessRoute        `json:"route"`
		Reference          *ResourceReference `json:"reference,omitempty"`
		TypeTarget         *TypeActionTarget  `json:"typeTarget,omitempty"`
		Action             string             `json:"action"`
		Input              Value              `json:"input"`
		ExpectedDescriptor *ActionDescriptor  `json:"expectedDescriptor,omitempty"`
	}{v.Route, reference, v.TypeTarget, v.Action, v.Input, v.ExpectedDescriptor})
}

// UnmarshalJSON decodes ActionRequest, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *ActionRequest) UnmarshalJSON(data []byte) error {
	type plain ActionRequest
	var out plain
	object, err := decodeDomainObject(data, &out, "route", "action", "input")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "expectedDescriptor"); err != nil {
		return err
	}
	value := ActionRequest(out)
	if err := ValidateActionRequest(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON rejects unknown or duplicate type-target members.
func (v *TypeActionTarget) UnmarshalJSON(data []byte) error {
	type plain TypeActionTarget
	var out plain
	if _, err := decodeDomainObject(data, &out, "provider", "authority", "type", "scope"); err != nil {
		return err
	}
	*v = TypeActionTarget(out)
	return nil
}

// UnmarshalJSON decodes SchemaDocumentReference, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *SchemaDocumentReference) UnmarshalJSON(data []byte) error {
	type plain SchemaDocumentReference
	var out plain
	if _, err := decodeDomainObject(data, &out, "id", "revision"); err != nil {
		return err
	}
	value := SchemaDocumentReference(out)
	if err := validateSchemaDocumentReference(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes SchemaBinding, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *SchemaBinding) UnmarshalJSON(data []byte) error {
	type plain SchemaBinding
	var out plain
	object, err := decodeDomainObject(data, &out, "state", "validation", "unvalidated")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "revision", "documentRef", "unavailableReason"); err != nil {
		return err
	}
	value := SchemaBinding(out)
	if err := validateSchema(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes Representation, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *Representation) UnmarshalJSON(data []byte) error {
	type plain Representation
	var out plain
	if _, err := decodeDomainObject(data, &out, "id", "revision", "schema"); err != nil {
		return err
	}
	value := Representation(out)
	if err := validateRepresentation(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes SchemaDocument, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *SchemaDocument) UnmarshalJSON(data []byte) error {
	type plain SchemaDocument
	var out plain
	if _, err := decodeDomainObject(data, &out, "reference", "dialect", "document"); err != nil {
		return err
	}
	value := SchemaDocument(out)
	if err := ValidateSchemaDocument(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes NativeAuthorityHint, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *NativeAuthorityHint) UnmarshalJSON(data []byte) error {
	type plain NativeAuthorityHint
	var out plain
	if _, err := decodeDomainObject(data, &out, "scheme", "id", "continuity"); err != nil {
		return err
	}
	value := NativeAuthorityHint(out)
	if err := validateNativeAuthorityHint(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes Coverage, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *Coverage) UnmarshalJSON(data []byte) error {
	type plain Coverage
	var out plain
	if _, err := decodeDomainObject(data, &out, "kind", "paths"); err != nil {
		return err
	}
	value := Coverage(out)
	if err := validateCoverage(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes Count, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *Count) UnmarshalJSON(data []byte) error {
	type plain Count
	var out plain
	object, err := decodeDomainObject(data, &out, "kind")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "value"); err != nil {
		return err
	}
	value := Count(out)
	if err := validateCount(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes Record, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *Record) UnmarshalJSON(data []byte) error {
	type plain Record
	var out plain
	object, err := decodeDomainObject(data, &out, "reference", "body", "representation", "coverage")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "sourceRevision"); err != nil {
		return err
	}
	value := Record(out)
	if err := ValidateRecord(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes SourcePage, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *SourcePage) UnmarshalJSON(data []byte) error {
	type plain SourcePage
	var out plain
	object, err := decodeDomainObject(data, &out, "records", "count", "coverage", "consistency")
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "nextCursor", "sourceRevision"); err != nil {
		return err
	}
	value := SourcePage(out)
	if err := ValidateSourcePage(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes Dimension, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *Dimension) UnmarshalJSON(data []byte) error {
	type plain Dimension
	var out plain
	if _, err := decodeDomainObject(data, &out, "name"); err != nil {
		return err
	}
	value := Dimension(out)
	if err := validateDimension(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes Capability, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *Capability) UnmarshalJSON(data []byte) error {
	type plain Capability
	var out plain
	if _, err := decodeDomainObject(data, &out, "operation", "scope"); err != nil {
		return err
	}
	value := Capability(out)
	if err := validateCapability(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes ResourceType, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *ResourceType) UnmarshalJSON(data []byte) error {
	type plain ResourceType
	var out plain
	object, err := decodeDomainObject(
		data,
		&out,
		"type",
		"identityDimensions",
		"queryDimensions",
		"representations",
		"capabilities",
	)
	if err != nil {
		return err
	}
	if err := rejectNullTokens(object, "nativeAuthorityHint", "nativeQueries", "modelRepresentation"); err != nil {
		return err
	}
	value := ResourceType(out)
	if err := validateType(value); err != nil {
		return err
	}
	*v = value
	return nil
}

// UnmarshalJSON decodes Catalog, rejecting duplicate members, unknown fields, and invalid domain values.
func (v *Catalog) UnmarshalJSON(data []byte) error {
	type plain Catalog
	var out plain
	if _, err := decodeDomainObject(data, &out, "provider", "types", "revision", "coverage"); err != nil {
		return err
	}
	value := Catalog(out)
	if err := ValidateCatalog(value); err != nil {
		return err
	}
	*v = value
	return nil
}

func marshalDomainJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateJSON(data); err != nil {
		return nil, err
	}
	return data, nil
}

// MarshalJSON validates ResourceAddress and emits canonical JSON without mutating caller-owned containers.
func (v ResourceAddress) MarshalJSON() ([]byte, error) {
	if err := ValidateAddress(v); err != nil {
		return nil, err
	}
	type plain ResourceAddress
	out := plain(v)
	out.Scope = append([]ScopeEntry{}, v.Scope...)
	sort.Slice(out.Scope, func(i, j int) bool { return out.Scope[i].Dimension < out.Scope[j].Dimension })
	return marshalDomainJSON(out)
}

// MarshalJSON validates QueryScope and emits canonical JSON without mutating caller-owned containers.
func (v QueryScope) MarshalJSON() ([]byte, error) {
	if err := ValidateScope(v); err != nil {
		return nil, err
	}
	type plain QueryScope
	out := append(plain{}, v...)
	sort.Slice(out, func(i, j int) bool { return out[i].Dimension < out[j].Dimension })
	return marshalDomainJSON(out)
}

// MarshalJSON validates ScopeConstraint and emits canonical JSON without mutating caller-owned containers.
func (v ScopeConstraint) MarshalJSON() ([]byte, error) {
	if err := validateConstraint(v); err != nil {
		return nil, err
	}
	type plain ScopeConstraint
	out := plain(v)
	if v.Values != nil {
		values := canonicalScopeValues(copyStrings(*v.Values))
		out.Values = &values
	}
	return marshalDomainJSON(out)
}

// MarshalJSON validates SchemaBinding and emits canonical JSON without mutating caller-owned containers.
func (v SchemaBinding) MarshalJSON() ([]byte, error) {
	if err := validateSchema(v); err != nil {
		return nil, err
	}
	type plain SchemaBinding
	out := plain(v)
	if out.Unvalidated == nil {
		out.Unvalidated = []string{}
	}
	return marshalDomainJSON(out)
}

// MarshalJSON validates Coverage and emits canonical JSON without mutating caller-owned containers.
func (v Coverage) MarshalJSON() ([]byte, error) {
	if err := validateCoverage(v); err != nil {
		return nil, err
	}
	type plain Coverage
	out := plain(v)
	if out.Paths == nil {
		out.Paths = []string{}
	}
	return marshalDomainJSON(out)
}

// MarshalJSON validates ResourceType and emits canonical JSON without mutating caller-owned containers.
func (v ResourceType) MarshalJSON() ([]byte, error) {
	if err := validateType(v); err != nil {
		return nil, err
	}
	type plain ResourceType
	out := plain(v)
	if out.IdentityDimensions == nil {
		out.IdentityDimensions = []Dimension{}
	}
	if out.QueryDimensions == nil {
		out.QueryDimensions = []Dimension{}
	}
	if out.Representations == nil {
		out.Representations = []Representation{}
	}
	if out.Capabilities == nil {
		out.Capabilities = []Capability{}
	}
	return marshalDomainJSON(out)
}

// MarshalJSON validates Catalog and emits canonical JSON without mutating caller-owned containers.
func (v Catalog) MarshalJSON() ([]byte, error) {
	if err := ValidateCatalog(v); err != nil {
		return nil, err
	}
	type plain Catalog
	out := plain(v)
	if out.Types == nil {
		out.Types = []ResourceType{}
	}
	return marshalDomainJSON(out)
}

// MarshalJSON validates SourcePage and emits canonical JSON without mutating caller-owned containers.
func (v SourcePage) MarshalJSON() ([]byte, error) {
	if err := ValidateSourcePage(v); err != nil {
		return nil, err
	}
	type plain SourcePage
	out := plain(v)
	if out.Records == nil {
		out.Records = []Record{}
	}
	return marshalDomainJSON(out)
}
