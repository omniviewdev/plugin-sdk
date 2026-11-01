package resourcev2

import (
	"encoding/json"
	"sort"
	"strconv"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func copyToken(p *string) *string {
	if p == nil {
		return nil
	}
	s := *p
	return &s
}
func copyStrings(s []string) []string { out := make([]string, len(s)); copy(out, s); return out }

func toValue(v Value) *resourcepb.Value {
	p := &resourcepb.Value{}
	switch v.Kind {
	case valueKindNull:
		p.Selection = &resourcepb.Value_NullValue{NullValue: &resourcepb.NullValue{}}
	case valueKindBool:
		p.Selection = &resourcepb.Value_BoolValue{BoolValue: v.Bool}
	case valueKindString:
		p.Selection = &resourcepb.Value_StringValue{StringValue: v.String}
	case valueKindInt64:
		n, _ := strconv.ParseInt(v.Int, 10, 64)
		p.Selection = &resourcepb.Value_Int64Value{Int64Value: n}
	case valueKindUint64:
		n, _ := strconv.ParseUint(v.Uint, 10, 64)
		p.Selection = &resourcepb.Value_Uint64Value{Uint64Value: n}
	case valueKindDouble:
		p.Selection = &resourcepb.Value_DoubleValue{DoubleValue: v.Double}
	case valueKindTimestamp:
		t, _ := timestamp(v.Timestamp)
		p.Selection = &resourcepb.Value_TimestampValue{TimestampValue: toTimestamp(t)}
	case valueKindList:
		list := &resourcepb.ListValue{Values: make([]*resourcepb.Value, len(v.List))}
		for i := range v.List {
			list.Values[i] = toValue(v.List[i])
		}
		p.Selection = &resourcepb.Value_ListValue{ListValue: list}
	case valueKindMap:
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
	p := &resourcepb.ResourceAddress{
		Provider:  v.Provider,
		Authority: v.Authority,
		Type:      v.Type,
		Id:        v.ID,
		Scope:     make([]*resourcepb.ScopeEntry, len(v.Scope)),
	}
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
		p.Selection = &resourcepb.ScopeConstraint_OneOf{
			OneOf: &resourcepb.OneOfScope{Values: canonicalScopeValues(copyStrings(*v.Values))},
		}
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
	p := &resourcepb.SchemaBinding{
		State:             v.State,
		Revision:          copyToken(v.Revision),
		Dialect:           v.Dialect,
		Root:              v.Root,
		Document:          append([]byte(nil), v.Document...),
		UnavailableReason: copyToken(v.UnavailableReason),
	}
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
	p := &resourcepb.ResourceType{
		Type:                  v.Type,
		DisplayName:           v.DisplayName,
		DefaultRepresentation: v.DefaultRepresentation,
		ModelRepresentation:   v.ModelRepresentation,
		IdentityDimensions:    make([]*resourcepb.Dimension, len(v.IdentityDimensions)),
		QueryDimensions:       make([]*resourcepb.Dimension, len(v.QueryDimensions)),
		Representations:       make([]*resourcepb.Representation, len(v.Representations)),
		Capabilities:          make([]*resourcepb.Capability, len(v.Capabilities)),
	}
	if len(v.NativeQueries) > 0 {
		p.NativeQueries = make([]*resourcepb.NativeQueryDescriptor, len(v.NativeQueries))
	}
	if v.NativeAuthorityHint != nil {
		p.NativeAuthorityHint = toNativeAuthorityHint(*v.NativeAuthorityHint)
	}
	for i, query := range v.NativeQueries {
		schema, _ := json.Marshal(query.InputSchema)
		p.NativeQueries[i] = &resourcepb.NativeQueryDescriptor{
			Language:        query.Language,
			Description:     query.Description,
			InputSchemaJson: schema,
		}
	}
	for i, x := range v.IdentityDimensions {
		p.IdentityDimensions[i] = toDimension(x)
	}
	for i, x := range v.QueryDimensions {
		p.QueryDimensions[i] = toDimension(x)
	}
	for i := range v.Representations {
		p.Representations[i] = toRepresentation(v.Representations[i])
	}
	for i := range v.Capabilities {
		p.Capabilities[i] = toCapability(v.Capabilities[i])
	}
	return p
}

func toNativeAuthorityHint(v NativeAuthorityHint) *resourcepb.NativeAuthorityHint {
	return &resourcepb.NativeAuthorityHint{Scheme: v.Scheme, Id: v.ID, Continuity: v.Continuity}
}

func toCatalog(v Catalog) *resourcepb.Catalog {
	p := &resourcepb.Catalog{
		Provider: v.Provider,
		Revision: v.Revision,
		Types:    make([]*resourcepb.ResourceType, len(v.Types)),
		Coverage: toCoverage(v.Coverage),
	}
	for i := range v.Types {
		p.Types[i] = toResourceType(v.Types[i])
	}
	return p
}

func toReadTarget(v ReadTarget) *resourcepb.ReadTarget {
	return &resourcepb.ReadTarget{
		Route:          toAccessRoute(v.Route),
		Provider:       v.Provider,
		Authority:      v.Authority,
		Type:           v.Type,
		Scope:          toQueryScope(v.Scope),
		Representation: v.Representation,
	}
}

func toGetRequest(v GetRequest) *resourcepb.GetInput {
	return &resourcepb.GetInput{
		Route:          toAccessRoute(v.Route),
		Reference:      toResourceReference(v.Reference),
		Representation: v.Representation,
	}
}

func toEnumerateRequest(v EnumerateRequest) *resourcepb.EnumerateInput {
	return &resourcepb.EnumerateInput{Target: toReadTarget(v.Target), PageSize: v.PageSize, Cursor: copyToken(v.Cursor)}
}

func toNativeQueryRequest(v NativeQueryRequest) *resourcepb.NativeQueryInput {
	return &resourcepb.NativeQueryInput{
		Target:   toReadTarget(v.Target),
		Language: v.Language,
		Query:    toValue(v.Query),
		PageSize: v.PageSize,
		Cursor:   copyToken(v.Cursor),
	}
}

func toCoverage(v Coverage) *resourcepb.Coverage {
	return &resourcepb.Coverage{Kind: v.Kind, Paths: copyStrings(v.Paths), Reason: v.Reason}
}

func toCount(v Count) *resourcepb.Count {
	return &resourcepb.Count{Kind: v.Kind, Value: copyToken(v.Value)}
}

func toRecord(v Record) *resourcepb.Record {
	return &resourcepb.Record{
		Reference:      toResourceReference(v.Reference),
		Body:           toValue(v.Body),
		Representation: toRepresentation(v.Representation),
		Coverage:       toCoverage(v.Coverage),
		SourceRevision: copyToken(v.SourceRevision),
	}
}

func toSourcePage(v SourcePage) *resourcepb.SourcePage {
	p := &resourcepb.SourcePage{
		Records:        make([]*resourcepb.Record, len(v.Records)),
		NextCursor:     copyToken(v.NextCursor),
		Count:          toCount(v.Count),
		Coverage:       toCoverage(v.Coverage),
		Consistency:    v.Consistency,
		SourceRevision: copyToken(v.SourceRevision),
	}
	for i := range v.Records {
		p.Records[i] = toRecord(v.Records[i])
	}
	return p
}

func toMutationRequest(v MutationRequest) *resourcepb.MutationInput {
	return &resourcepb.MutationInput{
		Route:            toAccessRoute(v.Route),
		Reference:        toResourceReference(v.Reference),
		Operation:        v.Operation,
		Input:            toValue(v.Input),
		ExpectedRevision: copyToken(v.ExpectedRevision),
	}
}

func toActionRequest(v ActionRequest) *resourcepb.ActionInput {
	p := &resourcepb.ActionInput{
		Route:  toAccessRoute(v.Route),
		Action: v.Action,
		Input:  toValue(v.Input),
	}
	if v.TypeTarget != nil {
		t := v.TypeTarget
		p.TypeTarget = &resourcepb.TypeActionTarget{
			Provider: t.Provider, Authority: t.Authority, Type: t.Type, Scope: toQueryScope(t.Scope),
		}
	} else {
		p.Reference = toResourceReference(v.Reference)
	}
	if v.ExpectedDescriptor != nil {
		p.ExpectedDescriptorJson, _ = json.Marshal(v.ExpectedDescriptor)
	}
	return p
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
	v := ResourceAddress{
		Provider:  p.Provider,
		Authority: p.Authority,
		Type:      p.Type,
		ID:        p.Id,
		Scope:     make([]ScopeEntry, len(p.Scope)),
	}
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
	v := SchemaBinding{
		State:             p.State,
		Revision:          copyToken(p.Revision),
		Dialect:           p.Dialect,
		Root:              p.Root,
		Document:          append([]byte(nil), p.Document...),
		UnavailableReason: copyToken(p.UnavailableReason),
	}
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
	v := ResourceType{
		Type:                  p.Type,
		DisplayName:           p.DisplayName,
		DefaultRepresentation: p.DefaultRepresentation,
		ModelRepresentation:   p.ModelRepresentation,
		IdentityDimensions:    make([]Dimension, len(p.IdentityDimensions)),
		QueryDimensions:       make([]Dimension, len(p.QueryDimensions)),
		Representations:       make([]Representation, len(p.Representations)),
		Capabilities:          make([]Capability, len(p.Capabilities)),
	}
	if len(p.NativeQueries) > 0 {
		v.NativeQueries = make([]NativeQueryDescriptor, len(p.NativeQueries))
	}
	if p.NativeAuthorityHint != nil {
		hint := d.nativeAuthorityHint(p.NativeAuthorityHint)
		v.NativeAuthorityHint = &hint
	}
	for i, query := range p.NativeQueries {
		if query == nil {
			d.fail("missing native query descriptor")
			continue
		}
		v.NativeQueries[i].Language = query.Language
		v.NativeQueries[i].Description = query.Description
		if err := json.Unmarshal(query.InputSchemaJson, &v.NativeQueries[i].InputSchema); err != nil {
			d.fail("invalid native query input schema")
		}
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
	v := Catalog{
		Provider: p.Provider,
		Revision: p.Revision,
		Types:    make([]ResourceType, len(p.Types)),
		Coverage: d.coverage(p.Coverage),
	}
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
	return ReadTarget{
		Route:          d.route(p.Route),
		Provider:       p.Provider,
		Authority:      p.Authority,
		Type:           p.Type,
		Scope:          d.scope(p.Scope),
		Representation: p.Representation,
	}
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
	return NativeQueryRequest{
		Target:   d.target(p.Target),
		Language: p.Language,
		Query:    d.value(p.Query),
		PageSize: p.PageSize,
		Cursor:   copyToken(p.Cursor),
	}
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
	return Record{
		Reference:      d.reference(p.Reference),
		Body:           d.value(p.Body),
		Representation: d.representation(p.Representation),
		Coverage:       d.coverage(p.Coverage),
		SourceRevision: copyToken(p.SourceRevision),
	}
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
	v := SourcePage{
		Records:        make([]Record, len(p.Records)),
		NextCursor:     copyToken(p.NextCursor),
		Count:          d.count(p.Count),
		Coverage:       d.coverage(p.Coverage),
		Consistency:    p.Consistency,
		SourceRevision: copyToken(p.SourceRevision),
	}
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
	return MutationRequest{
		Route:            d.route(p.Route),
		Reference:        d.reference(p.Reference),
		Operation:        p.Operation,
		Input:            d.value(p.Input),
		ExpectedRevision: copyToken(p.ExpectedRevision),
	}
}

func (d *adapterDecoder) action(p *resourcepb.ActionInput) ActionRequest {
	if p == nil {
		d.fail("missing action request")
		return ActionRequest{}
	}
	v := ActionRequest{
		Route:  d.route(p.Route),
		Action: p.Action,
		Input:  d.value(p.Input),
	}
	if p.Reference != nil {
		v.Reference = d.reference(p.Reference)
	}
	if p.TypeTarget != nil {
		t := p.TypeTarget
		v.TypeTarget = &TypeActionTarget{
			Provider: t.Provider, Authority: t.Authority, Type: t.Type, Scope: d.scope(t.Scope),
		}
	}
	if p.ExpectedDescriptorJson != nil {
		var descriptor ActionDescriptor
		if err := json.Unmarshal(p.ExpectedDescriptorJson, &descriptor); err != nil {
			d.fail("invalid expected action descriptor")
		} else {
			v.ExpectedDescriptor = &descriptor
		}
	}
	if (p.Reference == nil) == (p.TypeTarget == nil) {
		d.fail("action requires exactly one target")
	}
	return v
}

func validateEntry(v ScopeEntry) error {
	if err := text(v.Dimension, true); err != nil {
		return err
	}
	return text(v.Value, false)
}
func validateDimension(v Dimension) error        { return text(v.Name, true) }
func validateConstraint(v ScopeConstraint) error { return ValidateScope(QueryScope{v}) }
func validateCapability(v Capability) error {
	switch v.Operation {
	case OperationGet,
		OperationEnumerate,
		OperationNativeQuery,
		OperationCreate,
		OperationMutationUpdate,
		OperationDelete,
		OperationAction:
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

const adapterValidationToken = "validation"

func validateType(v ResourceType) error {
	return ValidateCatalog(
		Catalog{
			Provider: adapterValidationToken,
			Revision: adapterValidationToken,
			Coverage: Coverage{Kind: "complete"},
			Types:    []ResourceType{v},
		},
	)
}

// ToProtoValue encodes canonical scalar kinds and exact integer values.
// These synchronous transport codecs perform no I/O and have no
// cancellation or shared mutable state. Concurrent calls are safe only while
// callers keep each input's mutable storage unchanged. Successful outputs do not
// alias input containers. Discard output on any error: malformed shapes and
// semantic violations return INVALID_REQUEST; safety ceiling violations return
// RESOURCE_LIMIT; unknown fields return UNSUPPORTED. No codec admits catalog
// observations or authorizes operations.
//
// FromProto codecs reject nil messages, unknown fields, and missing selections.
// Scope normalization follows duplicate checks. These in-memory checks cannot
// replace raw-wire validation before protobuf decoding: decoding can erase
// duplicate singular fields and conflicting oneof selections.
//
// ToProtoValue encodes canonical scalar kinds and exact integer values.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoValue(v Value) (*resourcepb.Value, error) { return toProto(v, ValidateValue, toValue) }

// FromProtoValue decodes canonical scalar kinds and exact integer values.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoValue(p *resourcepb.Value) (Value, error) {
	return fromProto(p, (*adapterDecoder).value, ValidateValue)
}

// ToProtoAccessRoute encodes an explicit connection route without resolving or authorizing it.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoAccessRoute(v AccessRoute) (*resourcepb.AccessRoute, error) {
	return toProto(v, validateRoute, toAccessRoute)
}

// FromProtoAccessRoute decodes an explicit connection route without resolving or authorizing it.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoAccessRoute(p *resourcepb.AccessRoute) (AccessRoute, error) {
	return fromProto(p, (*adapterDecoder).route, validateRoute)
}

// ToProtoResourceReference encodes canonical identity and optional incarnation without selecting access.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoResourceReference(v ResourceReference) (*resourcepb.ResourceReference, error) {
	return toProto(v, validateReference, toResourceReference)
}

// FromProtoResourceReference decodes canonical identity and optional incarnation without selecting access.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoResourceReference(p *resourcepb.ResourceReference) (ResourceReference, error) {
	return fromProto(p, (*adapterDecoder).reference, validateReference)
}

// ToProtoCatalog encodes catalog declarations and coverage without granting capabilities.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoCatalog(v Catalog) (*resourcepb.Catalog, error) {
	return toProto(v, ValidateCatalog, toCatalog)
}

// FromProtoCatalog decodes catalog declarations and coverage without granting capabilities.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoCatalog(p *resourcepb.Catalog) (Catalog, error) {
	return fromProto(p, (*adapterDecoder).catalog, ValidateCatalog)
}

// ToProtoReadTarget encodes route-qualified scope without catalog admission.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoReadTarget(v ReadTarget) (*resourcepb.ReadTarget, error) {
	return toProto(v, validateTarget, toReadTarget)
}

// FromProtoReadTarget decodes route-qualified scope without catalog admission.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoReadTarget(p *resourcepb.ReadTarget) (ReadTarget, error) {
	return fromProto(p, (*adapterDecoder).target, validateTarget)
}

// ToProtoGetRequest encodes an explicit route, reference, and representation without dispatch.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoGetRequest(v GetRequest) (*resourcepb.GetInput, error) {
	return toProto(v, ValidateGetRequest, toGetRequest)
}

// FromProtoGetRequest decodes an explicit route, reference, and representation without dispatch.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoGetRequest(p *resourcepb.GetInput) (GetRequest, error) {
	return fromProto(p, (*adapterDecoder).get, ValidateGetRequest)
}

// ToProtoEnumerateRequest encodes target scope and opaque pagination without fetching records.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoEnumerateRequest(v EnumerateRequest) (*resourcepb.EnumerateInput, error) {
	return toProto(v, ValidateEnumerateRequest, toEnumerateRequest)
}

// FromProtoEnumerateRequest decodes target scope and opaque pagination without fetching records.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoEnumerateRequest(p *resourcepb.EnumerateInput) (EnumerateRequest, error) {
	return fromProto(p, (*adapterDecoder).enumerate, ValidateEnumerateRequest)
}

// ToProtoNativeQueryRequest encodes an opaque native query and target without executing it.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoNativeQueryRequest(v NativeQueryRequest) (*resourcepb.NativeQueryInput, error) {
	return toProto(v, ValidateNativeQueryRequest, toNativeQueryRequest)
}

// FromProtoNativeQueryRequest decodes an opaque native query and target without executing it.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoNativeQueryRequest(p *resourcepb.NativeQueryInput) (NativeQueryRequest, error) {
	return fromProto(p, (*adapterDecoder).nativeQuery, ValidateNativeQueryRequest)
}

// ToProtoRecord encodes body, representation, coverage, and source revision independently.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoRecord(v Record) (*resourcepb.Record, error) { return toProto(v, ValidateRecord, toRecord) }

// FromProtoRecord decodes body, representation, coverage, and source revision independently.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoRecord(p *resourcepb.Record) (Record, error) {
	return fromProto(p, (*adapterDecoder).record, ValidateRecord)
}

// ToProtoSourcePage encodes records, coverage, exact count intent, and opaque cursor independently.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoSourcePage(v SourcePage) (*resourcepb.SourcePage, error) {
	return toProto(v, ValidateSourcePage, toSourcePage)
}

// FromProtoSourcePage decodes records, coverage, exact count intent, and opaque cursor independently.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoSourcePage(p *resourcepb.SourcePage) (SourcePage, error) {
	return fromProto(p, (*adapterDecoder).page, ValidateSourcePage)
}

// ToProtoMutationRequest encodes mutation intent and expected revision without authorization or execution.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoMutationRequest(v MutationRequest) (*resourcepb.MutationInput, error) {
	return toProto(v, ValidateMutationRequest, toMutationRequest)
}

// FromProtoMutationRequest decodes mutation intent and expected revision without authorization or execution.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoMutationRequest(p *resourcepb.MutationInput) (MutationRequest, error) {
	return fromProto(p, (*adapterDecoder).mutation, ValidateMutationRequest)
}

// ToProtoActionRequest encodes action intent and canonical input without authorization or execution.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoActionRequest(v ActionRequest) (*resourcepb.ActionInput, error) {
	return toProto(v, ValidateActionRequest, toActionRequest)
}

// FromProtoActionRequest decodes action intent and canonical input without authorization or execution.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoActionRequest(p *resourcepb.ActionInput) (ActionRequest, error) {
	return fromProto(p, (*adapterDecoder).action, ValidateActionRequest)
}

// ToProtoCatalogRequest encodes the requested catalog route without connection discovery.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoCatalogRequest(v CatalogRequest) (*resourcepb.AccessRoute, error) {
	return ToProtoAccessRoute(v.Route)
}

// FromProtoCatalogRequest decodes the requested catalog route without connection discovery.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoCatalogRequest(p *resourcepb.AccessRoute) (CatalogRequest, error) {
	v, err := FromProtoAccessRoute(p)
	return CatalogRequest{Route: v}, err
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
	return &resourcepb.SchemaDocument{
		Reference: toSchemaDocumentReference(v.Reference),
		Dialect:   v.Dialect,
		Document:  append([]byte(nil), v.Document...),
	}
}

func (d *adapterDecoder) schemaDocument(p *resourcepb.SchemaDocument) SchemaDocument {
	if p == nil {
		d.fail("missing schema document")
		return SchemaDocument{}
	}
	return SchemaDocument{
		Reference: d.schemaDocumentReference(p.Reference),
		Dialect:   p.Dialect,
		Document:  append([]byte(nil), p.Document...),
	}
}

// ToProtoSchemaDocumentReference encodes an opaque schema document identity and revision without fetching it.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoSchemaDocumentReference(v SchemaDocumentReference) (*resourcepb.SchemaDocumentReference, error) {
	return toProto(v, validateSchemaDocumentReference, toSchemaDocumentReference)
}

// FromProtoSchemaDocumentReference decodes an opaque schema document identity and revision without fetching it.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoSchemaDocumentReference(p *resourcepb.SchemaDocumentReference) (SchemaDocumentReference, error) {
	return fromProto(p, (*adapterDecoder).schemaDocumentReference, validateSchemaDocumentReference)
}

// ToProtoSchemaDocument encodes schema bytes and reference without resolving external documents.
// It validates before encoding, returns nil on error, and owns output storage.
func ToProtoSchemaDocument(v SchemaDocument) (*resourcepb.SchemaDocument, error) {
	return toProto(v, ValidateSchemaDocument, toSchemaDocument)
}

// FromProtoSchemaDocument decodes schema bytes and reference without resolving external documents.
// It rejects nil or invalid messages; successful output owns mutable storage.
func FromProtoSchemaDocument(p *resourcepb.SchemaDocument) (SchemaDocument, error) {
	return fromProto(p, (*adapterDecoder).schemaDocument, ValidateSchemaDocument)
}
