package resourcev2

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

// These finite ceilings apply independently of the host's predicate policy.
const (
	maxDocumentBytes = 16 << 20
	maxDomainDepth   = 64
	maxDomainNodes   = 262144
	maxDomainFields  = 1048576
	maxPageRecords   = 256
	// ResourceWirePolicy names the finite implementation-candidate wire policy,
	// independent of the host's smaller predicate/projection policy.
	ResourceWirePolicy = "omniview.resource-wire-policy.v1"
	MaxMessageBytes    = 32 << 20
	MaxDocumentBytes   = maxDocumentBytes
	MaxValueDepth      = maxDomainDepth
	// MaxProtoDepth bounds protobuf message nesting, including Value wrappers.
	MaxProtoDepth      = MaxValueDepth*3 + 16
	MaxValueNodes      = maxDomainNodes
	MaxWireFields      = maxDomainFields
	MaxPageRecords     = maxPageRecords
	MaxPageBodyBytes   = MaxMessageBytes
)

// Value tags are shared by domain validation and lossless JSON/protobuf codecs.
const (
	valueKindNull      = "null"
	valueKindBool      = "bool"
	valueKindString    = "string"
	valueKindInt64     = "int64"
	valueKindUint64    = "uint64"
	valueKindDouble    = "double"
	valueKindTimestamp = "timestamp"
	valueKindList      = "list"
	valueKindMap       = "map"

	schemaStateAvailable   = "available"
	schemaStateAbsent      = "absent"
	schemaStateUnavailable = "unavailable"
	coverageUnknown        = "unknown"
	countUnknown           = "unknown"

	reflectionDepthAllowance = 4
	reflectionNodeAllowance  = 16
	maxDecimalIntegerLength  = 20
	timestampZoneLength      = 6
	protobufTagBytes         = 1
	protobufBoolBytes        = 2
	protobufDoubleBytes      = 9
)

func invalid(detail string) error { return NewError("INVALID_REQUEST", detail) }
func limited(detail string) error { return NewError("RESOURCE_LIMIT", detail) }
func text(s string, required bool) error {
	if !utf8.ValidString(s) || (required && s == "") {
		return invalid("invalid or empty UTF-8 token")
	}
	return nil
}

func token(s *string) error {
	if s != nil {
		return text(*s, true)
	}
	return nil
}

// admission uses a private reflection-bookkeeping allowance for the many
// inactive fields of handwritten Value structs. It does not raise raw wire or
// semantic value limits; those are enforced independently at their boundaries.
func admission(v any) error {
	return metadataAdmission(v, maxDomainDepth*reflectionDepthAllowance, maxDomainNodes*reflectionNodeAllowance, false)
}

// ValidateMetadata bounds JSON-visible metadata before callers marshal shared
// connection or presentation DTOs. Hidden credentials and json:"-" fields do
// not belong to the output and are never traversed.
func ValidateMetadata(v any) error { return metadataAdmission(v, maxDomainDepth, maxDomainNodes, true) }

func metadataAdmission(v any, depthLimit, nodeLimit int, jsonOutput bool) error {
	budget := metadataBudget{depthLimit: depthLimit, nodeLimit: nodeLimit, jsonOutput: jsonOutput}
	return budget.visit(reflect.ValueOf(v), 0)
}

type metadataBudget struct {
	nodes, fields, bytes, values int
	depthLimit, nodeLimit        int
	jsonOutput                   bool
}

func (b *metadataBudget) visit(v reflect.Value, depth int) error {
	if depth > b.depthLimit {
		return limited("domain nesting limit")
	}
	b.nodes++
	if b.nodes > b.nodeLimit {
		return limited("domain node limit")
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return b.visit(v.Elem(), depth+1)
		}
	case reflect.String:
		s := v.String()
		if b.jsonOutput {
			b.bytes += quotedJSONSize(s)
		} else {
			b.bytes += len(s)
		}
		if b.bytes > MaxMessageBytes {
			return limited("domain byte limit")
		}
		return text(s, false)
	case reflect.Float32, reflect.Float64:
		if n := v.Float(); math.IsInf(n, 0) || math.IsNaN(n) {
			return invalid("nonfinite metadata number")
		}
	case reflect.Struct:
		return b.structFields(v, depth)
	case reflect.Slice, reflect.Array:
		return b.sequence(v, depth)
	case reflect.Map:
		return b.mapEntries(v, depth)
	case reflect.Invalid, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Complex64, reflect.Complex128, reflect.Chan, reflect.Func, reflect.UnsafePointer:
	}
	return nil
}

func (b *metadataBudget) structFields(v reflect.Value, depth int) error {
	if v.Type() == reflect.TypeFor[time.Time]() {
		t, ok := reflect.TypeAssert[time.Time](v)
		if !ok {
			return invalid("invalid native timestamp")
		}
		return validateNativeTime(t)
	}
	isValue := v.Type() == reflect.TypeFor[Value]()
	if isValue {
		b.values++
		if b.values > maxDomainNodes {
			return limited("aggregate value node limit")
		}
	}
	for i := range v.NumField() {
		field := v.Type().Field(i)
		if field.PkgPath != "" || (field.Tag.Get("json") == "-" && !isValue) {
			continue
		}
		if b.jsonOutput && strings.Contains(field.Tag.Get("json"), ",omitempty") && emptyJSONField(v.Field(i)) {
			continue
		}
		if err := b.visit(v.Field(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (b *metadataBudget) sequence(v reflect.Value, depth int) error {
	b.fields += v.Len()
	if b.fields > maxDomainFields {
		return limited("domain field limit")
	}
	if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
		return b.byteSlice(v)
	}
	for i := range v.Len() {
		if err := b.visit(v.Index(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (b *metadataBudget) byteSlice(v reflect.Value) error {
	switch {
	case b.jsonOutput && v.Type() == reflect.TypeFor[json.RawMessage]():
		if v.IsNil() {
			b.bytes += len("null")
		} else {
			if err := ValidateJSON(v.Bytes()); err != nil {
				return err
			}
			b.bytes += v.Len()
		}
	case b.jsonOutput:
		b.bytes += jsonQuoteBytes + base64.StdEncoding.EncodedLen(v.Len())
	default:
		b.bytes += v.Len()
	}
	if b.bytes > MaxMessageBytes {
		return limited("domain byte limit")
	}
	return nil
}

func (b *metadataBudget) mapEntries(v reflect.Value, depth int) error {
	b.fields += v.Len()
	if b.fields > maxDomainFields {
		return limited("domain field limit")
	}
	it := v.MapRange()
	for it.Next() {
		if err := b.visit(it.Key(), depth+1); err != nil {
			return err
		}
		if err := b.visit(it.Value(), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func dimensions(ds []Dimension) (map[string]bool, error) {
	if err := admission(ds); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ds))
	for _, d := range ds {
		if err := text(d.Name, true); err != nil {
			return nil, err
		}
		if _, ok := out[d.Name]; ok {
			return nil, invalid("duplicate dimension")
		}
		out[d.Name] = d.AllowEmpty
	}
	return out, nil
}

// ValidateAddress checks canonical identity shape and duplicate dimensions.
// An optional declaration requires exactly its dimensions and empty-value rules.
func ValidateAddress(a ResourceAddress, declared ...[]Dimension) error {
	if len(declared) > 1 {
		return invalid("multiple identity dimension declarations")
	}
	if err := admission(a); err != nil {
		return err
	}
	for _, s := range []string{a.Provider, a.Authority, a.Type, a.ID} {
		if err := text(s, true); err != nil {
			return err
		}
	}
	var allowed map[string]bool
	if len(declared) > 0 {
		var err error
		allowed, err = dimensions(declared[0])
		if err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(a.Scope))
	for _, s := range a.Scope {
		if err := text(s.Dimension, true); err != nil {
			return err
		}
		if seen[s.Dimension] {
			return invalid("duplicate identity dimension")
		}
		seen[s.Dimension] = true
		if err := validateIdentityDimension(s.Dimension, s.Value, allowed); err != nil {
			return err
		}
	}
	if allowed != nil && len(seen) != len(allowed) {
		return invalid("missing identity dimension")
	}
	return nil
}

func validateIdentityDimension(name, value string, allowed map[string]bool) error {
	if allowed == nil {
		return nil
	}
	empty, ok := allowed[name]
	if !ok {
		return invalid("unknown identity dimension")
	}
	if value == "" && !empty {
		return invalid("empty identity dimension")
	}
	return nil
}

// ValidateScope checks explicit any/oneOf selections without deduplicating values.
// An optional declaration requires every declared query dimension; empty oneOf is valid.
func ValidateScope(s QueryScope, declared ...[]Dimension) error {
	if len(declared) > 1 {
		return invalid("multiple query dimension declarations")
	}
	if err := admission(s); err != nil {
		return err
	}
	var allowed map[string]bool
	if len(declared) > 0 {
		var err error
		allowed, err = dimensions(declared[0])
		if err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(s))
	for _, c := range s {
		if err := text(c.Dimension, true); err != nil {
			return err
		}
		if seen[c.Dimension] {
			return invalid("duplicate query dimension")
		}
		seen[c.Dimension] = true
		empty := true
		if allowed != nil {
			var ok bool
			empty, ok = allowed[c.Dimension]
			if !ok {
				return invalid("unknown query dimension")
			}
		}
		if err := validateScopeSelection(c, empty); err != nil {
			return err
		}
	}
	if allowed != nil && len(seen) != len(allowed) {
		return invalid("missing query dimension")
	}
	return nil
}

func validateScopeSelection(c ScopeConstraint, allowEmpty bool) error {
	switch c.Match {
	case ScopeAny:
		if c.Values != nil {
			return invalid("any scope has values")
		}
	case ScopeOneOf:
		if c.Values == nil {
			return invalid("oneOf scope requires values")
		}
		for _, v := range *c.Values {
			if v == "" && !allowEmpty {
				return invalid("empty query dimension")
			}
		}
	default:
		return invalid("unknown scope match")
	}
	return nil
}

func canonicalInt(s string, unsigned bool) error {
	if len(s) > maxDecimalIntegerLength {
		return invalid("integer out of range")
	}
	if s == "" || s == "-0" || s[0] == '+' || (len(s) > 1 && s[0] == '0') || (len(s) > 2 && s[:2] == "-0") {
		return invalid("noncanonical integer")
	}
	start := 0
	if s[0] == '-' {
		if unsigned {
			return invalid("negative unsigned integer")
		}
		start = 1
	}
	if start == len(s) {
		return invalid("invalid integer")
	}
	for i := start; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return invalid("invalid integer")
		}
	}
	if unsigned {
		if _, err := strconv.ParseUint(s, 10, 64); err != nil {
			return invalid("uint64 out of range")
		}
	} else {
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return invalid("int64 out of range")
		}
	}
	return nil
}

func timestamp(s string) (time.Time, error) {
	if len(s) < 20 || len(s) > 40 {
		return time.Time{}, invalid("invalid timestamp")
	}
	// time.Parse accepts excess fractional precision and comma separators. Reject
	// both before parsing, and require an explicit RFC3339 timezone.
	if strings.Contains(s, ",") {
		return time.Time{}, invalid("invalid timestamp fraction")
	}
	if s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' {
		return time.Time{}, invalid("invalid timestamp shape")
	}
	for _, i := range []int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if s[i] < '0' || s[i] > '9' {
			return time.Time{}, invalid("invalid timestamp digit")
		}
	}
	if err := validateTimestampPrecision(s); err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 1 || t.Year() > 9999 || t.UTC().Year() < 1 || t.UTC().Year() > 9999 {
		return time.Time{}, invalid("timestamp out of range")
	}
	// The standard parser permits timezone hours 24 and minutes 60.
	if err := validateTimestampZone(s); err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func validateTimestampPrecision(s string) error {
	const maxFractionDigits = 9
	if i := strings.IndexByte(s, '.'); i >= 0 {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j-i-1 < 1 || j-i-1 > maxFractionDigits {
			return invalid("timestamp precision")
		}
	}
	return nil
}

func validateTimestampZone(s string) error {
	if s[len(s)-1] == 'Z' {
		return nil
	}
	if len(s) < timestampZoneLength {
		return invalid("invalid timestamp zone")
	}
	z := s[len(s)-timestampZoneLength:]
	if (z[0] != '+' && z[0] != '-') || z[3] != ':' || z[1:3] > "23" || z[4:6] > "59" {
		return invalid("invalid timestamp zone")
	}
	return nil
}

// ValidateValue checks the active tagged payload, exact scalar syntax, and tree budgets.
// Inactive negative zero is a payload; list/map nil containers remain valid empty values.
func ValidateValue(v Value) error {
	budget := valueValidationBudget{}
	if err := budget.walk(&v, 0); err != nil {
		return err
	}
	if valueWireSize(v) > maxDocumentBytes {
		return limited("encoded value byte limit")
	}
	return nil
}

type valueValidationBudget struct{ nodes, bytes int }

func (b *valueValidationBudget) walk(v *Value, depth int) error {
	b.nodes++
	if depth > maxDomainDepth || b.nodes > maxDomainNodes {
		return limited("value complexity limit")
	}
	b.bytes += len(v.String)
	if b.bytes > maxDocumentBytes {
		return limited("value byte limit")
	}
	if !utf8.ValidString(v.String) {
		return invalid("invalid UTF-8 value")
	}
	if inactiveValuePayload(v) {
		return invalid("inactive value payload")
	}
	switch v.Kind {
	case valueKindNull, valueKindBool, valueKindString:
		return nil
	case valueKindInt64:
		return canonicalInt(v.Int, false)
	case valueKindUint64:
		return canonicalInt(v.Uint, true)
	case valueKindDouble:
		if math.IsNaN(v.Double) || math.IsInf(v.Double, 0) {
			return invalid("nonfinite double")
		}
	case valueKindTimestamp:
		_, err := timestamp(v.Timestamp)
		return err
	case valueKindList:
		return b.list(v.List, depth)
	case valueKindMap:
		return b.mapEntries(v.Map, depth)
	default:
		return invalid("unknown value kind")
	}
	return nil
}

func inactiveValuePayload(v *Value) bool {
	return (v.Kind != valueKindBool && v.Bool) ||
		(v.Kind != valueKindString && v.String != "") ||
		(v.Kind != valueKindInt64 && v.Int != "") ||
		(v.Kind != valueKindUint64 && v.Uint != "") ||
		(v.Kind != valueKindDouble && (v.Double != 0 || math.Signbit(v.Double))) ||
		(v.Kind != valueKindTimestamp && v.Timestamp != "") ||
		(v.Kind != valueKindList && v.List != nil) ||
		(v.Kind != valueKindMap && v.Map != nil)
}

func (b *valueValidationBudget) list(values []Value, depth int) error {
	if len(values) > maxDomainNodes-b.nodes {
		return limited("value node limit")
	}
	for i := range values {
		if err := b.walk(&values[i], depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (b *valueValidationBudget) mapEntries(values map[string]Value, depth int) error {
	if len(values) > maxDomainNodes-b.nodes {
		return limited("value node limit")
	}
	for k := range values {
		b.bytes += len(k)
		if b.bytes > maxDocumentBytes {
			return limited("value byte limit")
		}
		if err := text(k, false); err != nil {
			return err
		}
		v := values[k]
		if err := b.walk(&v, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateReference(r ResourceReference) error {
	if err := ValidateAddress(r.Address); err != nil {
		return err
	}
	return token(r.Incarnation)
}
func validateRoute(r AccessRoute) error { return text(r.Connection, true) }

// ValidateSchemaUnavailableReason admits only bounded, safe machine codes.
// It never transports provider messages, URLs, credentials or diagnostics.
func ValidateSchemaUnavailableReason(reason string) error {
	if len(reason) == 0 || len(reason) > 64 {
		return invalid("invalid schema unavailable reason")
	}
	for _, value := range reason {
		if value != '_' && (value < 'A' || value > 'Z') {
			return invalid("invalid schema unavailable reason")
		}
	}
	if !IsErrorCode(reason) {
		return invalid("unknown schema unavailable reason")
	}
	return nil
}

func validateSchema(s SchemaBinding) error {
	if err := admission(s); err != nil {
		return err
	}
	if err := token(s.Revision); err != nil {
		return err
	}
	if err := validateSchemaRoot(s.Root); err != nil {
		return err
	}
	if s.UnavailableReason != nil {
		if s.State != schemaStateUnavailable {
			return invalid("schema unavailable reason requires unavailable state")
		}
		if err := ValidateSchemaUnavailableReason(*s.UnavailableReason); err != nil {
			return err
		}
	}
	if s.DocumentRef != nil {
		if err := validateSchemaDocumentReference(*s.DocumentRef); err != nil {
			return err
		}
	}
	return validateSchemaState(s)
}

func validateSchemaState(s SchemaBinding) error {
	switch s.State {
	case schemaStateAvailable:
		if s.Revision == nil || s.Dialect == "" || (len(s.Document) > 0) == (s.DocumentRef != nil) {
			return invalid("available schema requires exactly one document binding")
		}
		if len(s.Document) > 0 {
			if _, err := parseJSON(s.Document); err != nil {
				return err
			}
		}
	case schemaStateAbsent:
		if s.Revision != nil || s.Dialect != "" || s.Root != "" || s.DocumentRef != nil || len(s.Document) != 0 {
			return invalid("absent schema has document binding")
		}
	case schemaStateUnavailable:
		if len(s.Document) != 0 {
			return invalid("unavailable schema has inline document")
		}
	default:
		return invalid("unknown schema state")
	}
	return nil
}

func validateRepresentation(r Representation) error {
	if err := text(r.ID, true); err != nil {
		return err
	}
	if err := text(r.Revision, true); err != nil {
		return err
	}
	return validateSchema(r.Schema)
}

func validateCoverage(c Coverage) error {
	if err := admission(c); err != nil {
		return err
	}
	switch c.Kind {
	case "complete":
		if len(c.Paths) != 0 || c.Reason != "" {
			return invalid("complete coverage has limitations")
		}
	case "partial", coverageUnknown:
		if c.Reason == "" {
			return invalid("incomplete coverage lacks reason")
		}
		for _, path := range c.Paths {
			if err := validateCoveragePath(path); err != nil {
				return err
			}
		}
	default:
		return invalid("unknown coverage kind")
	}
	return nil
}

// validateCoveragePath admits one RFC6901 JSON pointer naming a limited subtree.
func validateCoveragePath(path string) error {
	if !utf8.ValidString(path) || !strings.HasPrefix(path, "/") {
		return invalid("coverage path must be an RFC6901 JSON pointer starting with \"/\"")
	}
	for i := 0; i < len(path); i++ {
		if path[i] != '~' {
			continue
		}
		if i+1 >= len(path) || (path[i+1] != '0' && path[i+1] != '1') {
			return invalid("coverage path has an invalid RFC6901 escape")
		}
	}
	return nil
}

func validateCount(c Count) error {
	switch c.Kind {
	case countUnknown:
		if c.Value != nil {
			return invalid("unknown count has value")
		}
	case "exact", "estimate":
		if c.Value == nil {
			return invalid("count lacks value")
		}
		return canonicalInt(*c.Value, true)
	default:
		return invalid("unknown count kind")
	}
	return nil
}

// ValidateCatalog checks type, representation, schema, and scoped capability declarations.
// It validates structure, not caller authorization or live source availability.
func ValidateCatalog(c Catalog) error {
	if err := admission(c); err != nil {
		return err
	}
	if err := text(c.Provider, true); err != nil {
		return err
	}
	if err := text(c.Revision, true); err != nil {
		return err
	}
	if err := validateCoverage(c.Coverage); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range c.Types {
		t := &c.Types[i]
		if t.NativeAuthorityHint != nil {
			if err := validateNativeAuthorityHint(*t.NativeAuthorityHint); err != nil {
				return err
			}
		}
		if err := text(t.Type, true); err != nil {
			return err
		}
		if seen[t.Type] {
			return invalid("duplicate resource type")
		}
		seen[t.Type] = true
		if err := validateResourceType(t); err != nil {
			return err
		}
	}
	return nil
}

func validateResourceType(t *ResourceType) error {
	if _, err := dimensions(t.IdentityDimensions); err != nil {
		return err
	}
	if _, err := dimensions(t.QueryDimensions); err != nil {
		return err
	}
	representations := make(map[string]bool, len(t.Representations))
	for i := range t.Representations {
		representation := &t.Representations[i]
		if err := validateRepresentation(*representation); err != nil {
			return err
		}
		if representations[representation.ID] {
			return invalid("duplicate representation")
		}
		representations[representation.ID] = true
	}
	if t.DefaultRepresentation != "" && !representations[t.DefaultRepresentation] {
		return invalid("default representation is not declared")
	}
	if t.ModelRepresentation != "" && !representations[t.ModelRepresentation] {
		return invalid("model representation is not declared")
	}
	if err := validateNativeQueryDescriptors(t.NativeQueries); err != nil {
		return err
	}
	for _, capability := range t.Capabilities {
		if err := validateCapability(capability); err != nil {
			return err
		}
		if err := ValidateScope(capability.Scope, t.QueryDimensions); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeQueryDescriptors(queries []NativeQueryDescriptor) error {
	languages := make(map[string]bool, len(queries))
	for _, query := range queries {
		if err := text(query.Language, true); err != nil {
			return err
		}
		if err := text(query.Description, false); err != nil {
			return err
		}
		if languages[query.Language] {
			return invalid("duplicate native query language")
		}
		languages[query.Language] = true
		if err := ValidateSchema(query.InputSchema); err != nil {
			return err
		}
	}
	return nil
}

func validateTarget(t ReadTarget) error {
	if err := admission(t); err != nil {
		return err
	}
	if err := validateRoute(t.Route); err != nil {
		return err
	}
	for _, s := range []string{t.Provider, t.Authority, t.Type, t.Representation} {
		if err := text(s, true); err != nil {
			return err
		}
	}
	return ValidateScope(t.Scope)
}

// ValidateGetRequest requires an explicit route, canonical reference, and representation.
// Catalog support and accepted revision checks belong to operation admission.
func ValidateGetRequest(v GetRequest) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if err := validateReference(v.Reference); err != nil {
		return err
	}
	return text(v.Representation, true)
}

// ValidateEnumerateRequest checks target shape, optional cursor, and the page-size ceiling.
func ValidateEnumerateRequest(v EnumerateRequest) error {
	if err := validateTarget(v.Target); err != nil {
		return err
	}
	if v.PageSize > maxPageRecords {
		return limited("page size limit")
	}
	return token(v.Cursor)
}

// ValidateNativeQueryRequest checks enumeration bounds, language, and a lossless query value.
func ValidateNativeQueryRequest(v NativeQueryRequest) error {
	if err := ValidateEnumerateRequest(
		EnumerateRequest{Target: v.Target, PageSize: v.PageSize, Cursor: v.Cursor},
	); err != nil {
		return err
	}
	if err := text(v.Language, true); err != nil {
		return err
	}
	return ValidateValue(v.Query)
}

// ValidateMutationRequest checks reference, operation token, revision presence, and input shape.
// It does not infer supported mutation verbs from the request.
func ValidateMutationRequest(v MutationRequest) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if err := validateReference(v.Reference); err != nil {
		return err
	}
	if err := text(v.Operation, true); err != nil {
		return err
	}
	if err := token(v.ExpectedRevision); err != nil {
		return err
	}
	return ValidateValue(v.Input)
}

// ValidateActionRequest checks exactly one target, route, action token, and tagged input.
func ValidateActionRequest(v ActionRequest) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if v.TypeTarget != nil {
		a := v.Reference.Address
		if a.Provider != "" || a.Authority != "" || a.Type != "" || a.ID != "" || len(a.Scope) != 0 ||
			v.Reference.Incarnation != nil {
			return invalid("action requires exactly one target")
		}
		t := v.TypeTarget
		for _, value := range []string{t.Provider, t.Authority, t.Type} {
			if err := text(value, true); err != nil {
				return err
			}
		}
		if err := ValidateScope(t.Scope); err != nil {
			return err
		}
	} else if err := validateReference(v.Reference); err != nil {
		return err
	}
	if err := text(v.Action, true); err != nil {
		return err
	}
	if v.ExpectedDescriptor != nil {
		if err := ValidateActionDescriptors([]ActionDescriptor{*v.ExpectedDescriptor}); err != nil {
			return err
		}
	}
	return ValidateValue(v.Input)
}

// ValidateRecord requires a map-root body plus valid identity, representation, and coverage.
func ValidateRecord(r Record) error {
	if err := admission(r); err != nil {
		return err
	}
	if err := validateReference(r.Reference); err != nil {
		return err
	}
	if r.Body.Kind != valueKindMap {
		return invalid("resource body root must be map")
	}
	if err := ValidateValue(r.Body); err != nil {
		return err
	}
	if err := validateRepresentation(r.Representation); err != nil {
		return err
	}
	if err := validateCoverage(r.Coverage); err != nil {
		return err
	}
	return token(r.SourceRevision)
}

// ValidateSourcePage bounds records and aggregate protobuf frame size.
// Coverage, consistency, count, and continuation remain explicit source claims.
func ValidateSourcePage(p SourcePage) error {
	if len(p.Records) > maxPageRecords {
		return limited("page record limit")
	}
	if err := admission(p); err != nil {
		return err
	}
	bodyBytes, pageBytes := 0, 0
	for i := range p.Records {
		r := &p.Records[i]
		if err := ValidateRecord(*r); err != nil {
			return err
		}
		bodyBytes += valueWireSize(r.Body)
		pageBytes += messageFieldSize(recordWireSize(*r))
		if bodyBytes > MaxPageBodyBytes || pageBytes > MaxMessageBytes {
			return limited("page byte limit")
		}
	}
	if err := token(p.NextCursor); err != nil {
		return err
	}
	if err := token(p.SourceRevision); err != nil {
		return err
	}
	if err := validateCount(p.Count); err != nil {
		return err
	}
	if p.Count.Kind == "exact" {
		n, _ := strconv.ParseUint(*p.Count.Value, 10, 64)
		if n < uint64(len(p.Records)) {
			return invalid("exact count below page length")
		}
	}
	if err := validateCoverage(p.Coverage); err != nil {
		return err
	}
	pageBytes += optionalStringFieldSize(
		p.NextCursor,
	) + messageFieldSize(
		stringFieldSize(p.Count.Kind)+optionalStringFieldSize(p.Count.Value),
	) + messageFieldSize(
		coverageWireSize(p.Coverage),
	) + stringFieldSize(
		p.Consistency,
	) + optionalStringFieldSize(
		p.SourceRevision,
	)
	// Include the negotiated Response version and page wrapper, not just bodies.
	if 2+messageFieldSize(pageBytes) > MaxMessageBytes {
		return limited("page frame byte limit")
	}
	switch p.Consistency {
	case "bestEffort", "pointInTime":
		return nil
	default:
		return invalid("unknown source consistency")
	}
}

func varintSize(n uint64) int {
	size := 1
	for n >= 128 {
		n >>= 7
		size++
	}
	return size
}

func messageFieldSize(n int) int {
	if n < 0 {
		panic("negative protobuf message size")
	}
	return protobufTagBytes + varintSize(uint64(n)) + n
}

func valueWireSize(v Value) int {
	switch v.Kind {
	case valueKindNull, valueKindBool:
		return protobufBoolBytes
	case valueKindString:
		return messageFieldSize(len(v.String))
	case valueKindInt64:
		n, _ := strconv.ParseInt(v.Int, 10, 64)
		return protobufTagBytes + protowire.SizeVarint(protowire.EncodeZigZag(n))
	case valueKindUint64:
		n, _ := strconv.ParseUint(v.Uint, 10, 64)
		return 1 + varintSize(n)
	case valueKindDouble:
		return protobufDoubleBytes
	case valueKindTimestamp:
		t, _ := timestamp(v.Timestamp)
		size := 0
		if n := t.Unix(); n != 0 {
			size += protobufTagBytes + protowire.SizeVarint(protowire.EncodeZigZag(n))
		}
		if n := t.Nanosecond(); n > 0 {
			size += protobufTagBytes + varintSize(uint64(n))
		}
		return messageFieldSize(size)
	case valueKindList:
		size := 0
		for i := range v.List {
			size += messageFieldSize(valueWireSize(v.List[i]))
		}
		return messageFieldSize(size)
	case valueKindMap:
		size := 0
		for k := range v.Map {
			entry := messageFieldSize(valueWireSize(v.Map[k]))
			if k != "" {
				entry += messageFieldSize(len(k))
			}
			size += messageFieldSize(entry)
		}
		return messageFieldSize(size)
	}
	panic("validated value has no protobuf mapping")
}

// ValueWireSize reports the exact generated protobuf Value size without
// allocating transport messages. Acquisition adapters can sum sizes before
// retaining another record, then apply ValidateSourcePage to the complete page.
func ValueWireSize(v Value) (int, error) {
	if err := ValidateValue(v); err != nil {
		return 0, err
	}
	size := valueWireSize(v)
	if size > maxDocumentBytes {
		return 0, limited("encoded value byte limit")
	}
	return size, nil
}

func validateNativeAuthorityHint(v NativeAuthorityHint) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := text(v.Scheme, true); err != nil {
		return err
	}
	if err := text(v.ID, true); err != nil {
		return err
	}
	switch v.Continuity {
	case ContinuityConnectionLocal, ContinuityStableNative:
		return nil
	default:
		return invalid("unknown native authority continuity")
	}
}

func validateNativeTime(t time.Time) error {
	if t.Year() < 1 || t.Year() > 9999 || t.UTC().Year() < 1 || t.UTC().Year() > 9999 {
		return invalid("timestamp out of range")
	}
	return nil
}

func validateSchemaRoot(root string) error {
	if root == "" {
		return nil
	}
	if !strings.HasPrefix(root, "#/") {
		return invalid("schema root must be local JSON pointer fragment")
	}
	for _, r := range root[1:] {
		if r == '#' || r < 0x21 || r == 0x7f {
			return invalid("invalid schema root URI fragment")
		}
	}
	pointer, err := url.PathUnescape(root[1:])
	if err != nil || !utf8.ValidString(pointer) {
		return invalid("invalid schema root encoding")
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' {
			if i+1 == len(pointer) || (pointer[i+1] != '0' && pointer[i+1] != '1') {
				return invalid("invalid schema root pointer escape")
			}
			i++
		}
	}
	return nil
}

func validateSchemaDocumentReference(v SchemaDocumentReference) error {
	if err := text(v.ID, true); err != nil {
		return err
	}
	return text(v.Revision, true)
}

// ValidateSchemaDocument checks document identity, dialect, and strict bounded JSON.
// It performs no external document resolution or schema normalization.
func ValidateSchemaDocument(v SchemaDocument) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateSchemaDocumentReference(v.Reference); err != nil {
		return err
	}
	if err := text(v.Dialect, true); err != nil {
		return err
	}
	_, err := parseJSON(v.Document)
	return err
}

func stringFieldSize(s string) int {
	if s == "" {
		return 0
	}
	return messageFieldSize(len(s))
}

func optionalStringFieldSize(s *string) int {
	if s == nil {
		return 0
	}
	return messageFieldSize(len(*s))
}

func addressWireSize(a ResourceAddress) int {
	size := stringFieldSize(a.Provider) + stringFieldSize(a.Authority) + stringFieldSize(a.Type) + stringFieldSize(a.ID)
	for _, entry := range a.Scope {
		size += messageFieldSize(stringFieldSize(entry.Dimension) + stringFieldSize(entry.Value))
	}
	return size
}

func referenceWireSize(r ResourceReference) int {
	return messageFieldSize(addressWireSize(r.Address)) + optionalStringFieldSize(r.Incarnation)
}

func schemaWireSize(s SchemaBinding) int {
	size := stringFieldSize(
		s.State,
	) + optionalStringFieldSize(
		s.Revision,
	) + stringFieldSize(
		s.Dialect,
	) + stringFieldSize(
		s.Root,
	) + optionalStringFieldSize(
		s.UnavailableReason,
	)
	if len(s.Document) > 0 {
		size += messageFieldSize(len(s.Document))
	}
	if s.DocumentRef != nil {
		size += messageFieldSize(stringFieldSize(s.DocumentRef.ID) + stringFieldSize(s.DocumentRef.Revision))
	}
	return size
}

func coverageWireSize(c Coverage) int {
	size := stringFieldSize(c.Kind) + stringFieldSize(c.Reason)
	for _, path := range c.Paths {
		size += messageFieldSize(len(path))
	}
	return size
}

func recordWireSize(r Record) int {
	representation := stringFieldSize(
		r.Representation.ID,
	) + stringFieldSize(
		r.Representation.Revision,
	) + messageFieldSize(
		schemaWireSize(r.Representation.Schema),
	)
	return messageFieldSize(
		referenceWireSize(r.Reference),
	) + messageFieldSize(
		valueWireSize(r.Body),
	) + messageFieldSize(
		representation,
	) + messageFieldSize(
		coverageWireSize(r.Coverage),
	) + optionalStringFieldSize(
		r.SourceRevision,
	)
}

// RecordWireSize reports exact protobuf Record bytes without building a second
// record tree. The enclosing page/frame still includes its own metadata.
func RecordWireSize(r Record) (int, error) {
	if err := ValidateRecord(r); err != nil {
		return 0, err
	}
	size := recordWireSize(r)
	if size > MaxMessageBytes {
		return 0, limited("record wire byte limit")
	}
	return size, nil
}

func emptyJSONField(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool,
		reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.Uintptr,
		reflect.Float32,
		reflect.Float64,
		reflect.Interface,
		reflect.Pointer:
		return v.IsZero()
	case reflect.Invalid, reflect.Complex64, reflect.Complex128,
		reflect.Chan, reflect.Func, reflect.Struct, reflect.UnsafePointer:
	}
	return false
}
