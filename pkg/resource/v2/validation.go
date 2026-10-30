package resourcev2

import (
	"encoding/json"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
	MaxValueNodes      = maxDomainNodes
	MaxWireFields      = maxDomainFields
	MaxPageRecords     = maxPageRecords
	MaxPageBodyBytes   = MaxMessageBytes
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
func admission(v any) error { return metadataAdmission(v, maxDomainDepth*4, maxDomainNodes*16, false) }

// ValidateMetadata bounds JSON-visible metadata before callers marshal shared
// connection or presentation DTOs. Hidden credentials and json:"-" fields do
// not belong to the output and are never traversed.
func ValidateMetadata(v any) error { return metadataAdmission(v, maxDomainDepth, maxDomainNodes, true) }

func metadataAdmission(v any, depthLimit, nodeLimit int, jsonOutput bool) error {
	nodes, fields, bytes, values := 0, 0, 0, 0
	var visit func(reflect.Value, int) error
	visit = func(v reflect.Value, depth int) error {
		if depth > depthLimit {
			return limited("domain nesting limit")
		}
		nodes++
		if nodes > nodeLimit {
			return limited("domain node limit")
		}
		if v.Kind() == reflect.Struct && v.Type() == reflect.TypeFor[time.Time]() {
			t := v.Interface().(time.Time)
			return validateNativeTime(t)
		}
		if v.Kind() == reflect.Struct && v.Type() == reflect.TypeFor[Value]() {
			values++
			if values > maxDomainNodes {
				return limited("aggregate value node limit")
			}
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Pointer:
			if !v.IsNil() {
				return visit(v.Elem(), depth+1)
			}
		case reflect.String:
			s := v.String()
			if jsonOutput {
				bytes += quotedJSONSize(s)
			} else {
				bytes += len(s)
			}
			if bytes > MaxMessageBytes {
				return limited("domain byte limit")
			}
			return text(s, false)
		case reflect.Float32, reflect.Float64:
			if n := v.Float(); math.IsInf(n, 0) || math.IsNaN(n) {
				return invalid("nonfinite metadata number")
			}
		case reflect.Struct:
			for i := range v.NumField() {
				field := v.Type().Field(i)
				if field.PkgPath != "" || (field.Tag.Get("json") == "-" && v.Type() != reflect.TypeFor[Value]()) {
					continue
				}
				if jsonOutput && strings.Contains(field.Tag.Get("json"), ",omitempty") && emptyJSONField(v.Field(i)) {
					continue
				}
				if err := visit(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			fields += v.Len()
			if fields > maxDomainFields {
				return limited("domain field limit")
			}
			if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
				if jsonOutput && v.Type() == reflect.TypeFor[json.RawMessage]() {
					if v.IsNil() {
						bytes += 4
					} else {
						if err := ValidateJSON(v.Bytes()); err != nil {
							return err
						}
						bytes += v.Len()
					}
				} else if jsonOutput {
					bytes += 2 + ((v.Len()+2)/3)*4
				} else {
					bytes += v.Len()
				}
				if bytes > MaxMessageBytes {
					return limited("domain byte limit")
				}
				return nil
			}
			for i := range v.Len() {
				if err := visit(v.Index(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Map:
			fields += v.Len()
			if fields > maxDomainFields {
				return limited("domain field limit")
			}
			it := v.MapRange()
			for it.Next() {
				if err := visit(it.Key(), depth+1); err != nil {
					return err
				}
				if err := visit(it.Value(), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(reflect.ValueOf(v), 0)
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
		if allowed != nil {
			empty, ok := allowed[s.Dimension]
			if !ok {
				return invalid("unknown identity dimension")
			}
			if s.Value == "" && !empty {
				return invalid("empty identity dimension")
			}
		}
	}
	if allowed != nil && len(seen) != len(allowed) {
		return invalid("missing identity dimension")
	}
	return nil
}

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
				if v == "" && !empty {
					return invalid("empty query dimension")
				}
			}
		default:
			return invalid("unknown scope match")
		}
	}
	if allowed != nil && len(seen) != len(allowed) {
		return invalid("missing query dimension")
	}
	return nil
}

func canonicalInt(s string, unsigned bool) error {
	if len(s) > 20 {
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
	if i := strings.IndexByte(s, '.'); i >= 0 {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j-i-1 < 1 || j-i-1 > 9 {
			return time.Time{}, invalid("timestamp precision")
		}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 1 || t.Year() > 9999 || t.UTC().Year() < 1 || t.UTC().Year() > 9999 {
		return time.Time{}, invalid("timestamp out of range")
	}
	// The standard parser permits timezone hours 24 and minutes 60.
	if s[len(s)-1] != 'Z' {
		if len(s) < 6 {
			return time.Time{}, invalid("invalid timestamp zone")
		}
		z := s[len(s)-6:]
		if (z[0] != '+' && z[0] != '-') || z[3] != ':' || z[1:3] > "23" || z[4:6] > "59" {
			return time.Time{}, invalid("invalid timestamp zone")
		}
	}
	return t.UTC(), nil
}

func ValidateValue(v Value) error {
	nodes, bytes := 0, 0
	var walk func(Value, int) error
	walk = func(v Value, depth int) error {
		nodes++
		if depth > maxDomainDepth || nodes > maxDomainNodes {
			return limited("value complexity limit")
		}
		bytes += len(v.String)
		if bytes > maxDocumentBytes {
			return limited("value byte limit")
		}
		if !utf8.ValidString(v.String) {
			return invalid("invalid UTF-8 value")
		}
		if (v.Kind != "bool" && v.Bool) || (v.Kind != "string" && v.String != "") ||
			(v.Kind != "int64" && v.Int != "") ||
			(v.Kind != "uint64" && v.Uint != "") ||
			(v.Kind != "double" && (v.Double != 0 || math.Signbit(v.Double))) ||
			(v.Kind != "timestamp" && v.Timestamp != "") ||
			(v.Kind != "list" && v.List != nil) ||
			(v.Kind != "map" && v.Map != nil) {
			return invalid("inactive value payload")
		}
		switch v.Kind {
		case "null", "bool", "string":
		case "int64":
			return canonicalInt(v.Int, false)
		case "uint64":
			return canonicalInt(v.Uint, true)
		case "double":
			if math.IsNaN(v.Double) || math.IsInf(v.Double, 0) {
				return invalid("nonfinite double")
			}
		case "timestamp":
			_, err := timestamp(v.Timestamp)
			return err
		case "list":
			if len(v.List) > maxDomainNodes-nodes {
				return limited("value node limit")
			}
			for _, x := range v.List {
				if err := walk(x, depth+1); err != nil {
					return err
				}
			}
		case "map":
			if len(v.Map) > maxDomainNodes-nodes {
				return limited("value node limit")
			}
			for k, x := range v.Map {
				bytes += len(k)
				if bytes > maxDocumentBytes {
					return limited("value byte limit")
				}
				if err := text(k, false); err != nil {
					return err
				}
				if err := walk(x, depth+1); err != nil {
					return err
				}
			}
		default:
			return invalid("unknown value kind")
		}
		return nil
	}
	if err := walk(v, 0); err != nil {
		return err
	}
	if valueWireSize(v) > maxDocumentBytes {
		return limited("encoded value byte limit")
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
		if s.State != "unavailable" {
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
	switch s.State {
	case "available":
		if s.Revision == nil || s.Dialect == "" || (len(s.Document) > 0) == (s.DocumentRef != nil) {
			return invalid("available schema requires exactly one document binding")
		}
		if len(s.Document) > 0 {
			if _, err := parseJSON(s.Document); err != nil {
				return err
			}
		}
	case "absent":
		if s.Revision != nil || s.Dialect != "" || s.Root != "" || s.DocumentRef != nil || len(s.Document) != 0 {
			return invalid("absent schema has document binding")
		}
	case "unavailable":
		if len(s.Document) != 0 {
			return invalid("unavailable schema has inline document")
		}
	default:
		return invalid("unknown schema state")
	}
	switch s.Validation {
	case "full":
		if len(s.Unvalidated) != 0 || s.State != "available" {
			return invalid("invalid full schema validation")
		}
	case "reduced":
		if s.State != "available" || len(s.Unvalidated) == 0 {
			return invalid("reduced validation lacks constraints")
		}
	case "none":
		if len(s.Unvalidated) != 0 {
			return invalid("none validation has constraint claims")
		}
	default:
		return invalid("unknown schema validation")
	}
	for _, x := range s.Unvalidated {
		if err := text(x, true); err != nil {
			return err
		}
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
	case "partial", "unknown":
		if c.Reason == "" {
			return invalid("incomplete coverage lacks reason")
		}
	default:
		return invalid("unknown coverage kind")
	}
	return nil
}

func validateCount(c Count) error {
	switch c.Kind {
	case "unknown":
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
	for _, t := range c.Types {
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
		if _, err := dimensions(t.IdentityDimensions); err != nil {
			return err
		}
		if _, err := dimensions(t.QueryDimensions); err != nil {
			return err
		}
		representations := make(map[string]bool, len(t.Representations))
		for _, representation := range t.Representations {
			if err := validateRepresentation(representation); err != nil {
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
		for _, capability := range t.Capabilities {
			if err := validateCapability(capability); err != nil {
				return err
			}
			if err := ValidateScope(capability.Scope, t.QueryDimensions); err != nil {
				return err
			}
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

func ValidateEnumerateRequest(v EnumerateRequest) error {
	if err := validateTarget(v.Target); err != nil {
		return err
	}
	if v.PageSize > maxPageRecords {
		return limited("page size limit")
	}
	return token(v.Cursor)
}

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

func ValidateActionRequest(v ActionRequest) error {
	if err := admission(v); err != nil {
		return err
	}
	if err := validateRoute(v.Route); err != nil {
		return err
	}
	if err := validateReference(v.Reference); err != nil {
		return err
	}
	if err := text(v.Action, true); err != nil {
		return err
	}
	return ValidateValue(v.Input)
}

func ValidateRecord(r Record) error {
	if err := admission(r); err != nil {
		return err
	}
	if err := validateReference(r.Reference); err != nil {
		return err
	}
	if r.Body.Kind != "map" {
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

func ValidateSourcePage(p SourcePage) error {
	if len(p.Records) > maxPageRecords {
		return limited("page record limit")
	}
	if err := admission(p); err != nil {
		return err
	}
	bodyBytes, pageBytes := 0, 0
	for _, r := range p.Records {
		if err := ValidateRecord(r); err != nil {
			return err
		}
		bodyBytes += valueWireSize(r.Body)
		pageBytes += messageFieldSize(recordWireSize(r))
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
func messageFieldSize(n int) int { return 1 + varintSize(uint64(n)) + n }
func valueWireSize(v Value) int {
	switch v.Kind {
	case "null", "bool":
		return 2
	case "string":
		return messageFieldSize(len(v.String))
	case "int64":
		n, _ := strconv.ParseInt(v.Int, 10, 64)
		return 1 + varintSize(uint64(n<<1)^uint64(n>>63))
	case "uint64":
		n, _ := strconv.ParseUint(v.Uint, 10, 64)
		return 1 + varintSize(n)
	case "double":
		return 9
	case "timestamp":
		t, _ := timestamp(v.Timestamp)
		size := 0
		if n := t.Unix(); n != 0 {
			size += 1 + varintSize(uint64(n<<1)^uint64(n>>63))
		}
		if n := t.Nanosecond(); n != 0 {
			size += 1 + varintSize(uint64(n))
		}
		return messageFieldSize(size)
	case "list":
		size := 0
		for _, x := range v.List {
			size += messageFieldSize(valueWireSize(x))
		}
		return messageFieldSize(size)
	case "map":
		size := 0
		for k, x := range v.Map {
			entry := messageFieldSize(valueWireSize(x))
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
		s.Validation,
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
	for _, constraint := range s.Unvalidated {
		size += messageFieldSize(len(constraint))
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
	}
	return false
}
