package resourcev2

import (
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func copyToken(p *string) *string {
	if p == nil {
		return nil
	}
	s := *p
	return &s
}
func copyStrings(s []string) []string { out := make([]string, len(s)); copy(out, s); return out }

// admitProto protects direct callers of the handwritten adapters. It cannot
// recover duplicate singular fields lost by generated decoding: transports must
// run ValidateWire before decoding, not rely on this semantic guard alone.
func admitProto(p proto.Message) error {
	if p == nil || !p.ProtoReflect().IsValid() {
		return invalid("missing protobuf message")
	}
	nodes, fields, bytes := 0, 0, 0
	var visit func(protoreflect.Message, int) error
	visit = func(m protoreflect.Message, depth int) error {
		if !m.IsValid() {
			return invalid("missing protobuf message")
		}
		nodes++
		if depth > maxDomainDepth*3+16 || nodes > maxDomainNodes*4 {
			return limited("protobuf complexity limit")
		}
		if len(m.GetUnknown()) != 0 {
			return NewError("UNSUPPORTED", "unknown resource protobuf field")
		}
		var err error
		m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			check := func(v protoreflect.Value) error {
				switch fd.Kind() {
				case protoreflect.MessageKind:
					return visit(v.Message(), depth+1)
				case protoreflect.StringKind:
					s := v.String()
					bytes += len(s)
					if !utf8.ValidString(s) {
						return invalid("invalid protobuf UTF-8")
					}
				case protoreflect.BytesKind:
					bytes += len(v.Bytes())
				}
				if bytes > 32<<20 {
					return limited("protobuf byte limit")
				}
				return nil
			}
			if fd.IsList() {
				list := v.List()
				fields += list.Len()
				if fields > maxDomainFields {
					err = limited("protobuf field limit")
					return false
				}
				for i := range list.Len() {
					if err = check(list.Get(i)); err != nil {
						return false
					}
				}
			} else {
				fields++
				if fields > maxDomainFields {
					err = limited("protobuf field limit")
					return false
				}
				err = check(v)
			}
			return err == nil
		})
		return err
	}
	if err := visit(p.ProtoReflect(), 0); err != nil {
		return err
	}
	if proto.Size(p) > MaxMessageBytes {
		return limited("protobuf message byte limit")
	}
	return nil
}

// canonicalScopeValues owns its slice; duplicate dimensions have already failed.
func canonicalScopeValues(values []string) []string {
	sort.Strings(values)
	n := 0
	for _, value := range values {
		if n == 0 || values[n-1] != value {
			values[n] = value
			n++
		}
	}
	return values[:n]
}
func normalizeScopeOwned(scope QueryScope) QueryScope {
	for i := range scope {
		if scope[i].Values != nil {
			*scope[i].Values = canonicalScopeValues(*scope[i].Values)
		}
	}
	sort.Slice(scope, func(i, j int) bool { return scope[i].Dimension < scope[j].Dimension })
	return scope
}
func toValue(v Value) *resourcepb.Value {
	p := &resourcepb.Value{}
	switch v.Kind {
	case "null":
		p.Selection = &resourcepb.Value_NullValue{NullValue: &resourcepb.NullValue{}}
	case "bool":
		p.Selection = &resourcepb.Value_BoolValue{BoolValue: v.Bool}
	case "string":
		p.Selection = &resourcepb.Value_StringValue{StringValue: v.String}
	case "int64":
		n, _ := strconv.ParseInt(v.Int, 10, 64)
		p.Selection = &resourcepb.Value_Int64Value{Int64Value: n}
	case "uint64":
		n, _ := strconv.ParseUint(v.Uint, 10, 64)
		p.Selection = &resourcepb.Value_Uint64Value{Uint64Value: n}
	case "double":
		p.Selection = &resourcepb.Value_DoubleValue{DoubleValue: v.Double}
	case "timestamp":
		t, _ := timestamp(v.Timestamp)
		p.Selection = &resourcepb.Value_TimestampValue{TimestampValue: &resourcepb.TimestampValue{Seconds: t.Unix(), Nanos: uint32(t.Nanosecond())}}
	case "list":
		list := &resourcepb.ListValue{Values: make([]*resourcepb.Value, len(v.List))}
		for i, x := range v.List {
			list.Values[i] = toValue(x)
		}
		p.Selection = &resourcepb.Value_ListValue{ListValue: list}
	case "map":
		keys := make([]string, 0, len(v.Map))
		for k := range v.Map {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m := &resourcepb.MapValue{Entries: make([]*resourcepb.ValueEntry, 0, len(keys))}
		for _, k := range keys {
			m.Entries = append(m.Entries, &resourcepb.ValueEntry{Key: k, Value: toValue(v.Map[k])})
		}
		p.Selection = &resourcepb.Value_MapValue{MapValue: m}
	}
	return p
}
func toScopeEntry(v ScopeEntry) *resourcepb.ScopeEntry {
	return &resourcepb.ScopeEntry{Dimension: v.Dimension, Value: v.Value}
}
func toResourceAddress(v ResourceAddress) *resourcepb.ResourceAddress {
	p := &resourcepb.ResourceAddress{Provider: v.Provider, Authority: v.Authority, Type: v.Type, Id: v.ID, Scope: make([]*resourcepb.ScopeEntry, len(v.Scope))}
	for i, x := range v.Scope {
		p.Scope[i] = toScopeEntry(x)
	}
	sort.Slice(p.Scope, func(i, j int) bool { return p.Scope[i].Dimension < p.Scope[j].Dimension })
	return p
}
func toAccessRoute(v AccessRoute) *resourcepb.AccessRoute {
	return &resourcepb.AccessRoute{Connection: v.Connection}
}
func toResourceReference(v ResourceReference) *resourcepb.ResourceReference {
	return &resourcepb.ResourceReference{Address: toResourceAddress(v.Address), Incarnation: copyToken(v.Incarnation)}
}
func toScopeConstraint(v ScopeConstraint) *resourcepb.ScopeConstraint {
	p := &resourcepb.ScopeConstraint{Dimension: v.Dimension}
	if v.Match == ScopeAny {
		p.Selection = &resourcepb.ScopeConstraint_Any{Any: &resourcepb.AnyScope{}}
	} else {
		p.Selection = &resourcepb.ScopeConstraint_OneOf{OneOf: &resourcepb.OneOfScope{Values: canonicalScopeValues(copyStrings(*v.Values))}}
	}
	return p
}
func toQueryScope(v QueryScope) *resourcepb.QueryScope {
	p := &resourcepb.QueryScope{Constraints: make([]*resourcepb.ScopeConstraint, len(v))}
	for i, x := range v {
		p.Constraints[i] = toScopeConstraint(x)
	}
	sort.Slice(p.Constraints, func(i, j int) bool { return p.Constraints[i].Dimension < p.Constraints[j].Dimension })
	return p
}
func toSchemaBinding(v SchemaBinding) *resourcepb.SchemaBinding {
	p := &resourcepb.SchemaBinding{State: v.State, Revision: copyToken(v.Revision), Dialect: v.Dialect, Root: v.Root, Document: append([]byte(nil), v.Document...), Validation: v.Validation, Unvalidated: copyStrings(v.Unvalidated), UnavailableReason: copyToken(v.UnavailableReason)}
	if v.DocumentRef != nil {
		p.DocumentRef = toSchemaDocumentReference(*v.DocumentRef)
	}
	return p
}
func toRepresentation(v Representation) *resourcepb.Representation {
	return &resourcepb.Representation{Id: v.ID, Revision: v.Revision, Schema: toSchemaBinding(v.Schema)}
}
func toDimension(v Dimension) *resourcepb.Dimension {
	return &resourcepb.Dimension{Name: v.Name, AllowEmpty: v.AllowEmpty}
}
func toCapability(v Capability) *resourcepb.Capability {
	return &resourcepb.Capability{Operation: string(v.Operation), Scope: toQueryScope(v.Scope), Guarantee: v.Guarantee}
}
func toResourceType(v ResourceType) *resourcepb.ResourceType {
	p := &resourcepb.ResourceType{Type: v.Type, DisplayName: v.DisplayName, DefaultRepresentation: v.DefaultRepresentation, IdentityDimensions: make([]*resourcepb.Dimension, len(v.IdentityDimensions)), QueryDimensions: make([]*resourcepb.Dimension, len(v.QueryDimensions)), Representations: make([]*resourcepb.Representation, len(v.Representations)), Capabilities: make([]*resourcepb.Capability, len(v.Capabilities))}
	if v.NativeAuthorityHint != nil {
		p.NativeAuthorityHint = toNativeAuthorityHint(*v.NativeAuthorityHint)
	}
	for i, x := range v.IdentityDimensions {
		p.IdentityDimensions[i] = toDimension(x)
	}
	for i, x := range v.QueryDimensions {
		p.QueryDimensions[i] = toDimension(x)
	}
	for i, x := range v.Representations {
		p.Representations[i] = toRepresentation(x)
	}
	for i, x := range v.Capabilities {
		p.Capabilities[i] = toCapability(x)
	}
	return p
}
func toNativeAuthorityHint(v NativeAuthorityHint) *resourcepb.NativeAuthorityHint {
	return &resourcepb.NativeAuthorityHint{Scheme: v.Scheme, Id: v.ID, Continuity: v.Continuity}
}
func toCatalog(v Catalog) *resourcepb.Catalog {
	p := &resourcepb.Catalog{Provider: v.Provider, Revision: v.Revision, Types: make([]*resourcepb.ResourceType, len(v.Types)), Coverage: toCoverage(v.Coverage)}
	for i, x := range v.Types {
		p.Types[i] = toResourceType(x)
	}
	return p
}
func toReadTarget(v ReadTarget) *resourcepb.ReadTarget {
	return &resourcepb.ReadTarget{Route: toAccessRoute(v.Route), Provider: v.Provider, Authority: v.Authority, Type: v.Type, Scope: toQueryScope(v.Scope), Representation: v.Representation}
}
func toGetRequest(v GetRequest) *resourcepb.GetInput {
	return &resourcepb.GetInput{Route: toAccessRoute(v.Route), Reference: toResourceReference(v.Reference), Representation: v.Representation}
}
func toEnumerateRequest(v EnumerateRequest) *resourcepb.EnumerateInput {
	return &resourcepb.EnumerateInput{Target: toReadTarget(v.Target), PageSize: v.PageSize, Cursor: copyToken(v.Cursor)}
}
func toNativeQueryRequest(v NativeQueryRequest) *resourcepb.NativeQueryInput {
	return &resourcepb.NativeQueryInput{Target: toReadTarget(v.Target), Language: v.Language, Query: toValue(v.Query), PageSize: v.PageSize, Cursor: copyToken(v.Cursor)}
}
func toCoverage(v Coverage) *resourcepb.Coverage {
	return &resourcepb.Coverage{Kind: v.Kind, Paths: copyStrings(v.Paths), Reason: v.Reason}
}
func toCount(v Count) *resourcepb.Count {
	return &resourcepb.Count{Kind: v.Kind, Value: copyToken(v.Value)}
}
func toRecord(v Record) *resourcepb.Record {
	return &resourcepb.Record{Reference: toResourceReference(v.Reference), Body: toValue(v.Body), Representation: toRepresentation(v.Representation), Coverage: toCoverage(v.Coverage), SourceRevision: copyToken(v.SourceRevision)}
}
func toSourcePage(v SourcePage) *resourcepb.SourcePage {
	p := &resourcepb.SourcePage{Records: make([]*resourcepb.Record, len(v.Records)), NextCursor: copyToken(v.NextCursor), Count: toCount(v.Count), Coverage: toCoverage(v.Coverage), Consistency: v.Consistency, SourceRevision: copyToken(v.SourceRevision)}
	for i, x := range v.Records {
		p.Records[i] = toRecord(x)
	}
	return p
}
func toMutationRequest(v MutationRequest) *resourcepb.MutationInput {
	return &resourcepb.MutationInput{Route: toAccessRoute(v.Route), Reference: toResourceReference(v.Reference), Operation: v.Operation, Input: toValue(v.Input), ExpectedRevision: copyToken(v.ExpectedRevision)}
}
func toActionRequest(v ActionRequest) *resourcepb.ActionInput {
	return &resourcepb.ActionInput{Route: toAccessRoute(v.Route), Reference: toResourceReference(v.Reference), Action: v.Action, Input: toValue(v.Input)}
}

// adapterDecoder records the first failure. Admission bounds the complete graph
// before these explicit, allocating conversions run.
type adapterDecoder struct {
	err   error
	nodes int
}

func (d *adapterDecoder) fail(detail string) {
	if d.err == nil {
		d.err = invalid(detail)
	}
}
func (d *adapterDecoder) value(p *resourcepb.Value) Value { return d.valueAt(p, 0) }
func (d *adapterDecoder) valueAt(p *resourcepb.Value, depth int) Value {
	if d.err != nil {
		return Value{}
	}
	d.nodes++
	if depth > maxDomainDepth || d.nodes > maxDomainNodes {
		d.err = limited("value complexity limit")
		return Value{}
	}
	if p == nil {
		d.fail("missing value")
		return Value{}
	}
	var v Value
	switch x := p.Selection.(type) {
	case *resourcepb.Value_NullValue:
		if x == nil || x.NullValue == nil {
			d.fail("missing null marker")
			break
		}
		v.Kind = "null"
	case *resourcepb.Value_BoolValue:
		if x == nil {
			d.fail("missing bool")
			break
		}
		v = Value{Kind: "bool", Bool: x.BoolValue}
	case *resourcepb.Value_StringValue:
		if x == nil {
			d.fail("missing string")
			break
		}
		v = Value{Kind: "string", String: x.StringValue}
	case *resourcepb.Value_Int64Value:
		if x == nil {
			d.fail("missing int64")
			break
		}
		v = Value{Kind: "int64", Int: strconv.FormatInt(x.Int64Value, 10)}
	case *resourcepb.Value_Uint64Value:
		if x == nil {
			d.fail("missing uint64")
			break
		}
		v = Value{Kind: "uint64", Uint: strconv.FormatUint(x.Uint64Value, 10)}
	case *resourcepb.Value_DoubleValue:
		if x == nil {
			d.fail("missing double")
			break
		}
		v = Value{Kind: "double", Double: x.DoubleValue}
	case *resourcepb.Value_TimestampValue:
		if x == nil || x.TimestampValue == nil {
			d.fail("missing timestamp")
			break
		}
		t := x.TimestampValue
		if t.Seconds < -62135596800 || t.Seconds > 253402300799 || t.Nanos > 999999999 {
			d.fail("timestamp out of range")
			break
		}
		v = TimestampValue(time.Unix(t.Seconds, int64(t.Nanos)))
	case *resourcepb.Value_ListValue:
		if x == nil || x.ListValue == nil {
			d.fail("missing list")
			break
		}
		if len(x.ListValue.Values) > maxDomainNodes-d.nodes {
			d.err = limited("value node limit")
			break
		}
		v = Value{Kind: "list", List: make([]Value, len(x.ListValue.Values))}
		for i, p := range x.ListValue.Values {
			v.List[i] = d.valueAt(p, depth+1)
			if d.err != nil {
				break
			}
		}
	case *resourcepb.Value_MapValue:
		if x == nil || x.MapValue == nil {
			d.fail("missing map")
			break
		}
		entries := x.MapValue.Entries
		if len(entries) > maxDomainNodes-d.nodes {
			d.err = limited("value node limit")
			break
		}
		keys := make(map[string]struct{}, len(entries))
		for _, e := range entries {
			if e == nil {
				d.fail("missing map entry")
				break
			}
			if _, ok := keys[e.Key]; ok {
				d.fail("duplicate map key")
				break
			}
			keys[e.Key] = struct{}{}
		}
		if d.err != nil {
			break
		}
		v = Value{Kind: "map", Map: make(map[string]Value, len(entries))}
		for _, e := range entries {
			v.Map[e.Key] = d.valueAt(e.Value, depth+1)
			if d.err != nil {
				break
			}
		}
	default:
		d.fail("unset value selection")
	}
	return v
}
func (d *adapterDecoder) scopeEntry(p *resourcepb.ScopeEntry) ScopeEntry {
	if p == nil {
		d.fail("missing scope entry")
		return ScopeEntry{}
	}
	return ScopeEntry{Dimension: p.Dimension, Value: p.Value}
}
func (d *adapterDecoder) address(p *resourcepb.ResourceAddress) ResourceAddress {
	if p == nil {
		d.fail("missing address")
		return ResourceAddress{}
	}
	v := ResourceAddress{Provider: p.Provider, Authority: p.Authority, Type: p.Type, ID: p.Id, Scope: make([]ScopeEntry, len(p.Scope))}
	for i, p := range p.Scope {
		v.Scope[i] = d.scopeEntry(p)
	}
	if err := ValidateAddress(v); err != nil {
		if d.err == nil {
			d.err = err
		}
		return v
	}
	sort.Slice(v.Scope, func(i, j int) bool { return v.Scope[i].Dimension < v.Scope[j].Dimension })
	return v
}
func (d *adapterDecoder) route(p *resourcepb.AccessRoute) AccessRoute {
	if p == nil {
		d.fail("missing route")
		return AccessRoute{}
	}
	return AccessRoute{Connection: p.Connection}
}
func (d *adapterDecoder) reference(p *resourcepb.ResourceReference) ResourceReference {
	if p == nil {
		d.fail("missing reference")
		return ResourceReference{}
	}
	return ResourceReference{Address: d.address(p.Address), Incarnation: copyToken(p.Incarnation)}
}
func (d *adapterDecoder) constraint(p *resourcepb.ScopeConstraint) ScopeConstraint {
	if p == nil {
		d.fail("missing scope constraint")
		return ScopeConstraint{}
	}
	v := ScopeConstraint{Dimension: p.Dimension}
	switch x := p.Selection.(type) {
	case *resourcepb.ScopeConstraint_Any:
		if x == nil || x.Any == nil {
			d.fail("missing any marker")
			break
		}
		v.Match = ScopeAny
	case *resourcepb.ScopeConstraint_OneOf:
		if x == nil || x.OneOf == nil {
			d.fail("missing oneOf marker")
			break
		}
		v.Match = ScopeOneOf
		values := copyStrings(x.OneOf.Values)
		v.Values = &values
	default:
		d.fail("unset scope selection")
	}
	return v
}
func (d *adapterDecoder) scope(p *resourcepb.QueryScope) QueryScope {
	if p == nil {
		return QueryScope{}
	}
	v := make(QueryScope, len(p.Constraints))
	for i, p := range p.Constraints {
		v[i] = d.constraint(p)
	}
	if err := ValidateScope(v); err != nil {
		if d.err == nil {
			d.err = err
		}
		return v
	}
	return normalizeScopeOwned(v)
}
func (d *adapterDecoder) schema(p *resourcepb.SchemaBinding) SchemaBinding {
	if p == nil {
		d.fail("missing schema binding")
		return SchemaBinding{}
	}
	v := SchemaBinding{State: p.State, Revision: copyToken(p.Revision), Dialect: p.Dialect, Root: p.Root, Document: append([]byte(nil), p.Document...), Validation: p.Validation, Unvalidated: copyStrings(p.Unvalidated), UnavailableReason: copyToken(p.UnavailableReason)}
	if p.DocumentRef != nil {
		reference := d.schemaDocumentReference(p.DocumentRef)
		v.DocumentRef = &reference
	}
	return v
}
func (d *adapterDecoder) representation(p *resourcepb.Representation) Representation {
	if p == nil {
		d.fail("missing representation")
		return Representation{}
	}
	return Representation{ID: p.Id, Revision: p.Revision, Schema: d.schema(p.Schema)}
}
func (d *adapterDecoder) dimension(p *resourcepb.Dimension) Dimension {
	if p == nil {
		d.fail("missing dimension")
		return Dimension{}
	}
	return Dimension{Name: p.Name, AllowEmpty: p.AllowEmpty}
}
func (d *adapterDecoder) capability(p *resourcepb.Capability) Capability {
	if p == nil {
		d.fail("missing capability")
		return Capability{}
	}
	return Capability{Operation: Operation(p.Operation), Scope: d.scope(p.Scope), Guarantee: p.Guarantee}
}
func (d *adapterDecoder) resourceType(p *resourcepb.ResourceType) ResourceType {
	if p == nil {
		d.fail("missing resource type")
		return ResourceType{}
	}
	v := ResourceType{Type: p.Type, DisplayName: p.DisplayName, DefaultRepresentation: p.DefaultRepresentation, IdentityDimensions: make([]Dimension, len(p.IdentityDimensions)), QueryDimensions: make([]Dimension, len(p.QueryDimensions)), Representations: make([]Representation, len(p.Representations)), Capabilities: make([]Capability, len(p.Capabilities))}
	if p.NativeAuthorityHint != nil {
		hint := d.nativeAuthorityHint(p.NativeAuthorityHint)
		v.NativeAuthorityHint = &hint
	}
	for i, p := range p.IdentityDimensions {
		v.IdentityDimensions[i] = d.dimension(p)
	}
	for i, p := range p.QueryDimensions {
		v.QueryDimensions[i] = d.dimension(p)
	}
	for i, p := range p.Representations {
		v.Representations[i] = d.representation(p)
	}
	for i, p := range p.Capabilities {
		v.Capabilities[i] = d.capability(p)
	}
	return v
}
func (d *adapterDecoder) nativeAuthorityHint(p *resourcepb.NativeAuthorityHint) NativeAuthorityHint {
	if p == nil {
		d.fail("missing native authority hint")
		return NativeAuthorityHint{}
	}
	return NativeAuthorityHint{Scheme: p.Scheme, ID: p.Id, Continuity: p.Continuity}
}
func (d *adapterDecoder) catalog(p *resourcepb.Catalog) Catalog {
	if p == nil {
		d.fail("missing catalog")
		return Catalog{}
	}
	v := Catalog{Provider: p.Provider, Revision: p.Revision, Types: make([]ResourceType, len(p.Types)), Coverage: d.coverage(p.Coverage)}
	for i, p := range p.Types {
		v.Types[i] = d.resourceType(p)
	}
	return v
}
func (d *adapterDecoder) target(p *resourcepb.ReadTarget) ReadTarget {
	if p == nil {
		d.fail("missing target")
		return ReadTarget{}
	}
	return ReadTarget{Route: d.route(p.Route), Provider: p.Provider, Authority: p.Authority, Type: p.Type, Scope: d.scope(p.Scope), Representation: p.Representation}
}
func (d *adapterDecoder) get(p *resourcepb.GetInput) GetRequest {
	if p == nil {
		d.fail("missing get request")
		return GetRequest{}
	}
	return GetRequest{Route: d.route(p.Route), Reference: d.reference(p.Reference), Representation: p.Representation}
}
func (d *adapterDecoder) enumerate(p *resourcepb.EnumerateInput) EnumerateRequest {
	if p == nil {
		d.fail("missing enumerate request")
		return EnumerateRequest{}
	}
	return EnumerateRequest{Target: d.target(p.Target), PageSize: p.PageSize, Cursor: copyToken(p.Cursor)}
}
func (d *adapterDecoder) nativeQuery(p *resourcepb.NativeQueryInput) NativeQueryRequest {
	if p == nil {
		d.fail("missing native query request")
		return NativeQueryRequest{}
	}
	return NativeQueryRequest{Target: d.target(p.Target), Language: p.Language, Query: d.value(p.Query), PageSize: p.PageSize, Cursor: copyToken(p.Cursor)}
}
func (d *adapterDecoder) coverage(p *resourcepb.Coverage) Coverage {
	if p == nil {
		d.fail("missing coverage")
		return Coverage{}
	}
	return Coverage{Kind: p.Kind, Paths: copyStrings(p.Paths), Reason: p.Reason}
}
func (d *adapterDecoder) count(p *resourcepb.Count) Count {
	if p == nil {
		d.fail("missing count")
		return Count{}
	}
	return Count{Kind: p.Kind, Value: copyToken(p.Value)}
}
func (d *adapterDecoder) record(p *resourcepb.Record) Record {
	if p == nil {
		d.fail("missing record")
		return Record{}
	}
	return Record{Reference: d.reference(p.Reference), Body: d.value(p.Body), Representation: d.representation(p.Representation), Coverage: d.coverage(p.Coverage), SourceRevision: copyToken(p.SourceRevision)}
}
func (d *adapterDecoder) page(p *resourcepb.SourcePage) SourcePage {
	if p == nil {
		d.fail("missing page")
		return SourcePage{}
	}
	if len(p.Records) > maxPageRecords {
		d.err = limited("page record limit")
		return SourcePage{}
	}
	v := SourcePage{Records: make([]Record, len(p.Records)), NextCursor: copyToken(p.NextCursor), Count: d.count(p.Count), Coverage: d.coverage(p.Coverage), Consistency: p.Consistency, SourceRevision: copyToken(p.SourceRevision)}
	for i, p := range p.Records {
		v.Records[i] = d.record(p)
	}
	return v
}
func (d *adapterDecoder) mutation(p *resourcepb.MutationInput) MutationRequest {
	if p == nil {
		d.fail("missing mutation request")
		return MutationRequest{}
	}
	return MutationRequest{Route: d.route(p.Route), Reference: d.reference(p.Reference), Operation: p.Operation, Input: d.value(p.Input), ExpectedRevision: copyToken(p.ExpectedRevision)}
}
func (d *adapterDecoder) action(p *resourcepb.ActionInput) ActionRequest {
	if p == nil {
		d.fail("missing action request")
		return ActionRequest{}
	}
	return ActionRequest{Route: d.route(p.Route), Reference: d.reference(p.Reference), Action: p.Action, Input: d.value(p.Input)}
}

func toProto[D any, P proto.Message](v D, validate func(D) error, encode func(D) P) (P, error) {
	var zero P
	if err := admission(v); err != nil {
		return zero, err
	}
	if err := validate(v); err != nil {
		return zero, err
	}
	p := encode(v)
	if proto.Size(p) > MaxMessageBytes {
		return zero, limited("encoded message byte limit")
	}
	return p, nil
}
func fromProto[D any, P proto.Message](p P, decode func(*adapterDecoder, P) D, validate func(D) error) (D, error) {
	var zero D
	if err := admitProto(p); err != nil {
		return zero, err
	}
	d := &adapterDecoder{}
	v := decode(d, p)
	if d.err != nil {
		return zero, d.err
	}
	if err := validate(v); err != nil {
		return zero, err
	}
	return v, nil
}
func validateEntry(v ScopeEntry) error {
	if err := text(v.Dimension, true); err != nil {
		return err
	}
	return text(v.Value, false)
}
func validateDimension(v Dimension) error         { return text(v.Name, true) }
func validateConstraint(v ScopeConstraint) error  { return ValidateScope(QueryScope{v}) }
func validateAddressOnly(v ResourceAddress) error { return ValidateAddress(v) }
func validateScopeOnly(v QueryScope) error        { return ValidateScope(v) }
func validateCapability(v Capability) error {
	switch v.Operation {
	case OperationGet, OperationEnumerate, OperationNativeQuery, OperationMutation, OperationAction:
		if v.Guarantee != "" {
			return invalid("finite capability has update guarantee")
		}
	case OperationUpdate:
		if err := validateGuarantee(v.Guarantee); err != nil {
			return err
		}
	default:
		return invalid("unknown capability operation")
	}
	return ValidateScope(v.Scope)
}
func validateType(v ResourceType) error {
	return ValidateCatalog(Catalog{Provider: "validation", Revision: "validation", Coverage: Coverage{Kind: "complete"}, Types: []ResourceType{v}})
}

// ToProto helpers validate before allocating transport messages. FromProto
// helpers reject unknown fields and missing selections, copy mutable storage,
// validate semantics, and normalize scope only after duplicate checks.
func ToProtoValue(v Value) (*resourcepb.Value, error) { return toProto(v, ValidateValue, toValue) }
func FromProtoValue(p *resourcepb.Value) (Value, error) {
	return fromProto(p, (*adapterDecoder).value, ValidateValue)
}
func ToProtoScopeEntry(v ScopeEntry) (*resourcepb.ScopeEntry, error) {
	return toProto(v, validateEntry, toScopeEntry)
}
func FromProtoScopeEntry(p *resourcepb.ScopeEntry) (ScopeEntry, error) {
	return fromProto(p, (*adapterDecoder).scopeEntry, validateEntry)
}
func ToProtoResourceAddress(v ResourceAddress) (*resourcepb.ResourceAddress, error) {
	return toProto(v, validateAddressOnly, toResourceAddress)
}
func FromProtoResourceAddress(p *resourcepb.ResourceAddress) (ResourceAddress, error) {
	return fromProto(p, (*adapterDecoder).address, validateAddressOnly)
}
func ToProtoAccessRoute(v AccessRoute) (*resourcepb.AccessRoute, error) {
	return toProto(v, validateRoute, toAccessRoute)
}
func FromProtoAccessRoute(p *resourcepb.AccessRoute) (AccessRoute, error) {
	return fromProto(p, (*adapterDecoder).route, validateRoute)
}
func ToProtoResourceReference(v ResourceReference) (*resourcepb.ResourceReference, error) {
	return toProto(v, validateReference, toResourceReference)
}
func FromProtoResourceReference(p *resourcepb.ResourceReference) (ResourceReference, error) {
	return fromProto(p, (*adapterDecoder).reference, validateReference)
}
func ToProtoScopeConstraint(v ScopeConstraint) (*resourcepb.ScopeConstraint, error) {
	return toProto(v, validateConstraint, toScopeConstraint)
}
func FromProtoScopeConstraint(p *resourcepb.ScopeConstraint) (ScopeConstraint, error) {
	v, err := fromProto(p, (*adapterDecoder).constraint, validateConstraint)
	if err != nil {
		return ScopeConstraint{}, err
	}
	if v.Values != nil {
		*v.Values = canonicalScopeValues(*v.Values)
	}
	return v, nil
}
func ToProtoQueryScope(v QueryScope) (*resourcepb.QueryScope, error) {
	return toProto(v, validateScopeOnly, toQueryScope)
}
func FromProtoQueryScope(p *resourcepb.QueryScope) (QueryScope, error) {
	return fromProto(p, (*adapterDecoder).scope, validateScopeOnly)
}
func ToProtoSchemaBinding(v SchemaBinding) (*resourcepb.SchemaBinding, error) {
	return toProto(v, validateSchema, toSchemaBinding)
}
func FromProtoSchemaBinding(p *resourcepb.SchemaBinding) (SchemaBinding, error) {
	return fromProto(p, (*adapterDecoder).schema, validateSchema)
}
func ToProtoRepresentation(v Representation) (*resourcepb.Representation, error) {
	return toProto(v, validateRepresentation, toRepresentation)
}
func FromProtoRepresentation(p *resourcepb.Representation) (Representation, error) {
	return fromProto(p, (*adapterDecoder).representation, validateRepresentation)
}
func ToProtoDimension(v Dimension) (*resourcepb.Dimension, error) {
	return toProto(v, validateDimension, toDimension)
}
func FromProtoDimension(p *resourcepb.Dimension) (Dimension, error) {
	return fromProto(p, (*adapterDecoder).dimension, validateDimension)
}
func ToProtoCapability(v Capability) (*resourcepb.Capability, error) {
	return toProto(v, validateCapability, toCapability)
}
func FromProtoCapability(p *resourcepb.Capability) (Capability, error) {
	return fromProto(p, (*adapterDecoder).capability, validateCapability)
}
func ToProtoResourceType(v ResourceType) (*resourcepb.ResourceType, error) {
	return toProto(v, validateType, toResourceType)
}
func FromProtoResourceType(p *resourcepb.ResourceType) (ResourceType, error) {
	return fromProto(p, (*adapterDecoder).resourceType, validateType)
}
func ToProtoCatalog(v Catalog) (*resourcepb.Catalog, error) {
	return toProto(v, ValidateCatalog, toCatalog)
}
func FromProtoCatalog(p *resourcepb.Catalog) (Catalog, error) {
	return fromProto(p, (*adapterDecoder).catalog, ValidateCatalog)
}
func ToProtoReadTarget(v ReadTarget) (*resourcepb.ReadTarget, error) {
	return toProto(v, validateTarget, toReadTarget)
}
func FromProtoReadTarget(p *resourcepb.ReadTarget) (ReadTarget, error) {
	return fromProto(p, (*adapterDecoder).target, validateTarget)
}
func ToProtoGetRequest(v GetRequest) (*resourcepb.GetInput, error) {
	return toProto(v, ValidateGetRequest, toGetRequest)
}
func FromProtoGetRequest(p *resourcepb.GetInput) (GetRequest, error) {
	return fromProto(p, (*adapterDecoder).get, ValidateGetRequest)
}
func ToProtoEnumerateRequest(v EnumerateRequest) (*resourcepb.EnumerateInput, error) {
	return toProto(v, ValidateEnumerateRequest, toEnumerateRequest)
}
func FromProtoEnumerateRequest(p *resourcepb.EnumerateInput) (EnumerateRequest, error) {
	return fromProto(p, (*adapterDecoder).enumerate, ValidateEnumerateRequest)
}
func ToProtoNativeQueryRequest(v NativeQueryRequest) (*resourcepb.NativeQueryInput, error) {
	return toProto(v, ValidateNativeQueryRequest, toNativeQueryRequest)
}
func FromProtoNativeQueryRequest(p *resourcepb.NativeQueryInput) (NativeQueryRequest, error) {
	return fromProto(p, (*adapterDecoder).nativeQuery, ValidateNativeQueryRequest)
}
func ToProtoCoverage(v Coverage) (*resourcepb.Coverage, error) {
	return toProto(v, validateCoverage, toCoverage)
}
func FromProtoCoverage(p *resourcepb.Coverage) (Coverage, error) {
	return fromProto(p, (*adapterDecoder).coverage, validateCoverage)
}
func ToProtoCount(v Count) (*resourcepb.Count, error) { return toProto(v, validateCount, toCount) }
func FromProtoCount(p *resourcepb.Count) (Count, error) {
	return fromProto(p, (*adapterDecoder).count, validateCount)
}
func ToProtoRecord(v Record) (*resourcepb.Record, error) { return toProto(v, ValidateRecord, toRecord) }
func FromProtoRecord(p *resourcepb.Record) (Record, error) {
	return fromProto(p, (*adapterDecoder).record, ValidateRecord)
}
func ToProtoSourcePage(v SourcePage) (*resourcepb.SourcePage, error) {
	return toProto(v, ValidateSourcePage, toSourcePage)
}
func FromProtoSourcePage(p *resourcepb.SourcePage) (SourcePage, error) {
	return fromProto(p, (*adapterDecoder).page, ValidateSourcePage)
}
func ToProtoMutationRequest(v MutationRequest) (*resourcepb.MutationInput, error) {
	return toProto(v, ValidateMutationRequest, toMutationRequest)
}
func FromProtoMutationRequest(p *resourcepb.MutationInput) (MutationRequest, error) {
	return fromProto(p, (*adapterDecoder).mutation, ValidateMutationRequest)
}
func ToProtoActionRequest(v ActionRequest) (*resourcepb.ActionInput, error) {
	return toProto(v, ValidateActionRequest, toActionRequest)
}
func FromProtoActionRequest(p *resourcepb.ActionInput) (ActionRequest, error) {
	return fromProto(p, (*adapterDecoder).action, ValidateActionRequest)
}
func ToProtoCatalogRequest(v CatalogRequest) (*resourcepb.AccessRoute, error) {
	return ToProtoAccessRoute(v.Route)
}
func FromProtoCatalogRequest(p *resourcepb.AccessRoute) (CatalogRequest, error) {
	v, err := FromProtoAccessRoute(p)
	return CatalogRequest{Route: v}, err
}
func ToProtoNativeAuthorityHint(v NativeAuthorityHint) (*resourcepb.NativeAuthorityHint, error) {
	return toProto(v, validateNativeAuthorityHint, toNativeAuthorityHint)
}
func FromProtoNativeAuthorityHint(p *resourcepb.NativeAuthorityHint) (NativeAuthorityHint, error) {
	return fromProto(p, (*adapterDecoder).nativeAuthorityHint, validateNativeAuthorityHint)
}

func toSchemaDocumentReference(v SchemaDocumentReference) *resourcepb.SchemaDocumentReference {
	return &resourcepb.SchemaDocumentReference{Id: v.ID, Revision: v.Revision}
}
func (d *adapterDecoder) schemaDocumentReference(p *resourcepb.SchemaDocumentReference) SchemaDocumentReference {
	if p == nil {
		d.fail("missing schema document reference")
		return SchemaDocumentReference{}
	}
	return SchemaDocumentReference{ID: p.Id, Revision: p.Revision}
}
func toSchemaDocument(v SchemaDocument) *resourcepb.SchemaDocument {
	return &resourcepb.SchemaDocument{Reference: toSchemaDocumentReference(v.Reference), Dialect: v.Dialect, Document: append([]byte(nil), v.Document...)}
}
func (d *adapterDecoder) schemaDocument(p *resourcepb.SchemaDocument) SchemaDocument {
	if p == nil {
		d.fail("missing schema document")
		return SchemaDocument{}
	}
	return SchemaDocument{Reference: d.schemaDocumentReference(p.Reference), Dialect: p.Dialect, Document: append([]byte(nil), p.Document...)}
}
func ToProtoSchemaDocumentReference(v SchemaDocumentReference) (*resourcepb.SchemaDocumentReference, error) {
	return toProto(v, validateSchemaDocumentReference, toSchemaDocumentReference)
}
func FromProtoSchemaDocumentReference(p *resourcepb.SchemaDocumentReference) (SchemaDocumentReference, error) {
	return fromProto(p, (*adapterDecoder).schemaDocumentReference, validateSchemaDocumentReference)
}
func ToProtoSchemaDocument(v SchemaDocument) (*resourcepb.SchemaDocument, error) {
	return toProto(v, ValidateSchemaDocument, toSchemaDocument)
}
func FromProtoSchemaDocument(p *resourcepb.SchemaDocument) (SchemaDocument, error) {
	return fromProto(p, (*adapterDecoder).schemaDocument, ValidateSchemaDocument)
}
