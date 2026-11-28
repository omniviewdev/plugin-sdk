package resourcev2

import (
	"context"
	"io"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/omniviewdev/plugin-sdk/pkg/resource/v2/internal/relatedwire"
)

// RelatedReadRequest contains only host-admitted native state. Sources are the
// separately authorized collection targets, never a provider-selected query.
// Their authorities may differ even within the same connection and provider.
type RelatedReadRequest struct {
	Operation string            `json:"operation"`
	Root      ResourceReference `json:"root"`
	Route     AccessRoute       `json:"route"`
	Sources   []ReadTarget      `json:"sources,omitzero"`
}

// RelatedReadPage is private broker input, not a disclosed result. Sources are
// distinct supported families examined under this caller; never object counts.
// Complete certifies exhaustive acquisition AND the caller's read rights over
// each examined nonroot family scope, independently of candidate presence.
// A missing or denied object must never determine coverage or reveal an edge.
// Partial knowledge is not absence. Only the broker can establish truncation.
type RelatedReadPage struct {
	Sources  []string               `json:"sources"`
	Coverage string                 `json:"coverage"`
	Rows     []RelatedReadCandidate `json:"rows"`
}

// RelatedReadCandidate carries only scalar display fields and private canonical
// citations with native incarnations. Evidence may include the exact root.
type RelatedReadCandidate struct {
	Row      map[string]any      `json:"row"`
	Evidence []ResourceReference `json:"evidence"`
	Neighbor *ResourceReference  `json:"neighbor,omitempty"`
}

// RelatedReadRegistration installs one fixed operation for one root type.
// Read must authorize the root UID and related reads with the leased native
// client, check the root again at delivery, and honor cancellation. It must
// reserve before I/O: 8 page/lookups, 64 objects/page, 2 MiB total decoded
// input. It must not acquire a withheld family or collect an unbounded result.
// Inputs are borrowed. A returned page must remain stable during transport.
type RelatedReadRegistration[T any] struct {
	Operation string
	RootType  string
	Sources   []string
	Read      func(context.Context, *T, RelatedReadRequest) (RelatedReadPage, error)
}

// RelatedReadOwner is the optional native owner service. Source discovery is
// static and cannot acquire any records, call a handler or start a connection.
type RelatedReadOwner interface {
	RelatedSourcesV1(context.Context, RelatedReadRequest) ([]string, error)
	ReadRelatedV1(context.Context, RelatedReadRequest) (RelatedReadPage, error)
	relatedReadController()
}

func (*Controller[T]) relatedReadController() {}

// RelatedReadOwnerV1 returns nil unless fixed operations were installed. The
// ordinary resource service and protocol version do not depend on this trait.
func (c *Controller[T]) RelatedReadOwnerV1() RelatedReadOwner {
	if len(c.cfg.RelatedReads) == 0 {
		return nil
	}
	return c
}

const (
	maxRelatedFamilies   = 8
	maxRelatedCandidates = 129 // one additional *private* witness
	maxRelatedFields     = 64
	maxRelatedCitations  = 4
	maxRelatedFrame      = 512 << 10
)

func relatedSegment(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

func relatedOwnerID(op string) string {
	if len(op) > 128+1+128+1+9 {
		return ""
	}
	owner, rest, _ := strings.Cut(op, "/")
	name, major, _ := strings.Cut(rest, "@")
	if !relatedSegment(owner) || !relatedSegment(name) || len(major) == 0 || len(major) > 9 || major[0] == '0' {
		return ""
	}
	for i := range len(major) {
		if major[i] < '0' || major[i] > '9' {
			return ""
		}
	}
	return owner
}

func relatedFamilies(ids []string) bool {
	if len(ids) == 0 || len(ids) > maxRelatedFamilies {
		return false
	}
	remaining := relatedwire.MetadataBytes
	for i, id := range ids {
		if id == "" || len(id) > remaining || !utf8.ValidString(id) || slices.Contains(ids[:i], id) {
			return false
		}
		remaining -= len(id)
	}
	return true
}

func prepareRelatedReads[T any](reads []RelatedReadRegistration[T], managed bool) ([]RelatedReadRegistration[T], error) {
	if len(reads) == 0 {
		return nil, nil
	}
	if !managed || len(reads) > 1024 {
		return nil, invalid("Related reads require a managed connection and bounded declarations.")
	}
	reads = slices.Clone(reads)
	seen := make(map[[2]string]bool, len(reads))
	for i, r := range reads {
		key := [2]string{r.Operation, r.RootType}
		if relatedOwnerID(r.Operation) == "" || r.RootType == "" || len(r.RootType) > 16<<10 || !utf8.ValidString(r.RootType) ||
			r.Read == nil || !relatedFamilies(r.Sources) || seen[key] {
			return nil, invalid("Invalid or duplicate related read declaration.")
		}
		// Count the exact wire encoding, including JSON escaping and the protobuf envelope.
		if err := relatedwire.WriteJSON(io.Discard, relatedwire.Discovery{Sources: r.Sources}, relatedwire.MetadataBytes); err != nil {
			return nil, invalid("Related source declaration exceeds the metadata frame limit.")
		}
		seen[key] = true
		reads[i].Sources = slices.Clone(r.Sources)
	}
	return reads, nil
}

// A read has no raw principal field. The trusted transport binds the session to
// the installed owner and connection; metadata alone grants no resource right.
func (c *Controller[T]) relatedRegistration(ctx context.Context, in RelatedReadRequest) (*RelatedReadRegistration[T], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, ok := SessionFromContext(ctx)
	owner := relatedOwnerID(in.Operation)
	if !ok || owner == "" || s.PluginID != owner || s.Connection == nil || s.Connection.ID != in.Route.Connection ||
		s.RequestID == "" || s.RequesterID == "" || len(s.RequestID) > 256 || len(s.RequesterID) > 256 ||
		!utf8.ValidString(s.RequestID) || !utf8.ValidString(s.RequesterID) {
		return nil, NewError("PERMISSION_DENIED", "Related read unavailable.")
	}
	remaining := 16 << 10
	if len(in.Sources) > maxRelatedFamilies || in.Route.Connection == "" ||
		!chargeRelatedStrings(&remaining, in.Operation, in.Route.Connection) || !chargeRelatedReference(in.Root, &remaining) {
		return nil, invalid("Invalid related read request.")
	}
	for _, t := range in.Sources {
		if !chargeRelatedStrings(&remaining, t.Route.Connection, t.Provider, t.Authority, t.Type, t.Representation) || len(t.Scope) > remaining {
			return nil, invalid("Invalid related source target.")
		}
		for _, sc := range t.Scope {
			if !chargeRelatedStrings(&remaining, sc.Dimension) || sc.Values != nil && (len(*sc.Values) != 1 || !chargeRelatedStrings(&remaining, (*sc.Values)[0])) {
				return nil, invalid("Invalid related source scope.")
			}
		}
	}
	revision, _ := CatalogRevisionForRouteFromContext(ctx, in.Root.Address.Provider, in.Route)
	if err := ValidateCatalogRevision(revision); err != nil {
		return nil, err
	}
	for i := range c.cfg.RelatedReads {
		r := &c.cfg.RelatedReads[i]
		if r.Operation == in.Operation && r.RootType == in.Root.Address.Type {
			return r, nil
		}
	}
	return nil, NewError("UNSUPPORTED", "Related read unavailable.")
}

// RelatedSourcesV1 reports only immutable declarations, even for an empty
// database. It cannot call provider code or reveal native object existence.
func (c *Controller[T]) RelatedSourcesV1(ctx context.Context, in RelatedReadRequest) ([]string, error) {
	r, err := c.relatedRegistration(ctx, in)
	if err != nil {
		return nil, err
	}
	if in.Sources != nil {
		return nil, invalid("Unexpected source targets for related source discovery.")
	}
	return slices.Clone(r.Sources), nil
}

// One physical read per authenticated window. Cancellation need not stop a
// native callback immediately; a second request must not spend its budget
// while that first callback is still running. The host admits 64 live windows.
func (c *Controller[T]) acquireRelatedReader(requester string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.relatedReaders[requester]; busy || c.closed || len(c.relatedReaders) >= 64 {
		return false
	}
	if c.relatedReaders == nil {
		c.relatedReaders = make(map[string]struct{})
	}
	c.relatedReaders[requester] = struct{}{}
	return true
}

func (c *Controller[T]) releaseRelatedReader(requester string) {
	c.mu.Lock()
	delete(c.relatedReaders, requester)
	c.mu.Unlock()
}

// ReadRelatedV1 dispatches only a fixed registered callback. Check the accepted
// catalog on the held generation before work and again before returning data.
func (c *Controller[T]) ReadRelatedV1(ctx context.Context, in RelatedReadRequest) (page RelatedReadPage, err error) {
	var registration *Registration[T]
	defer c.guardContext(ctx, "related_read", &registration, &err)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := c.relatedRegistration(ctx, in)
	if err != nil {
		return RelatedReadPage{}, err
	}
	if len(in.Sources) == 0 || len(in.Sources) > maxRelatedFamilies {
		return RelatedReadPage{}, NewError("PERMISSION_DENIED", "Related source targets unavailable.")
	}
	session, _ := SessionFromContext(ctx)
	if !c.acquireRelatedReader(session.RequesterID) {
		return RelatedReadPage{}, NewError("UNAVAILABLE", "Related read unavailable.")
	}
	defer c.releaseRelatedReader(session.RequesterID)
	ctx, client, done, err := c.lease(ctx, in.Route)
	if err != nil {
		return RelatedReadPage{}, err
	}
	defer done()
	catalog, err := c.Catalog(ctx, CatalogRequest{Route: in.Route})
	if err != nil {
		return RelatedReadPage{}, err
	}
	if err := CheckCatalogRevisionForRoute(ctx, catalog, in.Route); err != nil {
		return RelatedReadPage{}, err
	}
	rootType, _, err := admitTarget(catalog, operationTarget{route: in.Route, provider: in.Root.Address.Provider, typ: in.Root.Address.Type,
		address: &in.Root.Address, operation: OperationGet})
	if err != nil {
		return RelatedReadPage{}, err
	}
	for i, target := range in.Sources {
		if !slices.Contains(r.Sources, target.Type) || !relatedTarget(in, target) || slices.ContainsFunc(in.Sources[:i], func(t ReadTarget) bool { return t.Type == target.Type }) {
			return RelatedReadPage{}, NewError("PERMISSION_DENIED", "Related source target unavailable.")
		}
		descriptor := rootType
		if target.Type == rootType.Type {
			// This family denotes the already-authorized point Get. Never
			// require or grant enumeration of other objects of the root type.
			if !declaresRepresentation(&rootType, target.Representation) || ValidateScope(target.Scope, rootType.QueryDimensions) != nil {
				return RelatedReadPage{}, invalid("Invalid related root target.")
			}
		} else {
			descriptor, _, err = admitTarget(catalog, operationTarget{route: in.Route, provider: target.Provider, typ: target.Type,
				scope: target.Scope, representation: target.Representation, operation: OperationEnumerate})
			if err != nil {
				return RelatedReadPage{}, err
			}
		}
		// A selection-only dimension may be any only after the host admitted
		// this whole collection. Never widen or drop an identity dimension.
		if len(descriptor.IdentityDimensions) != len(in.Root.Address.Scope) {
			return RelatedReadPage{}, NewError("PERMISSION_DENIED", "Related identity scope unavailable.")
		}
		for _, dim := range descriptor.IdentityDimensions {
			sc := scopeConstraint(target.Scope, dim.Name)
			if sc == nil || sc.Match != ScopeOneOf {
				return RelatedReadPage{}, NewError("PERMISSION_DENIED", "Related identity scope unavailable.")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return RelatedReadPage{}, err
	}
	// The handler receives a separate root and target set. Its return cannot
	// rewrite the admission against which its own citations will be checked.
	borrowed := in
	uid := *in.Root.Incarnation
	borrowed.Root.Incarnation = &uid
	borrowed.Root.Address.Scope = slices.Clone(in.Root.Address.Scope)
	borrowed.Sources = make([]ReadTarget, len(in.Sources))
	for i, target := range in.Sources {
		borrowed.Sources[i] = cloneReadTarget(target)
	}
	page, err = r.Read(ctx, client, borrowed)
	if canceled := ctx.Err(); canceled != nil {
		return RelatedReadPage{}, canceled
	}
	leased, _ := SessionFromContext(ctx)
	if leased.Connection == nil || leased.Connection.ID != in.Route.Connection {
		return RelatedReadPage{}, NewError("RESET_REQUIRED", "Related connection changed.")
	}
	// An obsolete native error is no more admissible than obsolete rows.
	if fenceErr := c.fenceCatalog(ctx, in.Route); fenceErr != nil {
		return RelatedReadPage{}, fenceErr
	}
	if err != nil {
		return RelatedReadPage{}, err
	}
	return c.finishRelatedRead(ctx, in, page, len(r.Sources))
}

func (c *Controller[T]) finishRelatedRead(
	ctx context.Context, in RelatedReadRequest, page RelatedReadPage, declaredSources int,
) (RelatedReadPage, error) {
	if err := validateRelatedPage(in, page); err != nil {
		return RelatedReadPage{}, err
	}
	if len(in.Sources) < declaredSources {
		page.Coverage = "partial"
	}
	if err := ctx.Err(); err != nil {
		return RelatedReadPage{}, err
	}
	// Lease cancellation propagates asynchronously. Linearize delivery against
	// retirement under the same lock as StopConnection, after result validation.
	slot, _ := ctx.Value(catalogSlotKey{}).(*clientSlot[T])
	c.mu.Lock()
	current := slot != nil && !c.closed && c.clients[in.Route.Connection] == slot && !slot.stopping && slot.ctx.Err() == nil
	c.mu.Unlock()
	if !current {
		return RelatedReadPage{}, NewError("RESET_REQUIRED", "Related connection changed.")
	}
	return page, nil
}

func relatedTarget(in RelatedReadRequest, t ReadTarget) bool {
	a := in.Root.Address
	if validateTarget(t) != nil || t.Route != in.Route || t.Provider != a.Provider || t.Type == a.Type && t.Authority != a.Authority {
		return false
	}
	for _, entry := range a.Scope {
		sc := scopeConstraint(t.Scope, entry.Dimension)
		if sc == nil || sc.Match != ScopeOneOf || sc.Values == nil || len(*sc.Values) != 1 || (*sc.Values)[0] != entry.Value {
			return false
		}
	}
	for _, sc := range t.Scope {
		if sc.Match == ScopeAny {
			continue
		}
		if sc.Match != ScopeOneOf || sc.Values == nil || len(*sc.Values) != 1 {
			return false
		}
		if !slices.Contains(a.Scope, ScopeEntry{Dimension: sc.Dimension, Value: (*sc.Values)[0]}) && (t.Type != a.Type || (*sc.Values)[0] != a.ID) {
			return false
		}
	}
	return true
}

func relatedScalar(v any, remaining *int) bool {
	switch n := v.(type) {
	case string:
		if len(n) > *remaining || len(n) > 4096*3 || !utf8.ValidString(n) {
			return false
		}
		*remaining -= len(n)
		units := 0
		for _, r := range n {
			units += utf16.RuneLen(r)
		}
		return units <= 4096
	case float64:
		return !math.IsNaN(n) && !math.IsInf(n, 0)
	case float32:
		return !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0)
	case int:
		return exactRelatedInt(int64(n))
	case int64:
		return exactRelatedInt(n)
	case uint:
		return exactRelatedUint(uint64(n))
	case uint64:
		return exactRelatedUint(n)
	case int8, int16, int32, uint8, uint16, uint32:
		return true
	default:
		return false
	}
}

// Do not silently round an owner's integral fact on its way to a JS number.
func exactRelatedInt(n int64) bool   { f := float64(n); return f < 1<<63 && int64(f) == n }
func exactRelatedUint(n uint64) bool { f := float64(n); return f < 1<<64 && uint64(f) == n }

func chargeRelatedStrings(remaining *int, values ...string) bool {
	for _, s := range values {
		if len(s) >= *remaining || !utf8.ValidString(s) {
			return false
		}
		*remaining -= len(s) + 1
	}
	return true
}

// Charge at least the raw identity bytes before any ordinary resource validator
// can traverse or copy an oversized native result. Transport bounds the exact
// serialized frame (including escaping and protobuf framing) separately.
func chargeRelatedReference(ref ResourceReference, remaining *int) bool {
	if ref.Incarnation == nil || len(ref.Address.Scope) > *remaining/2 {
		return false
	}
	a := ref.Address
	if !chargeRelatedStrings(remaining, *ref.Incarnation, a.Provider, a.Authority, a.Type, a.ID) {
		return false
	}
	for _, s := range a.Scope {
		if !chargeRelatedStrings(remaining, s.Dimension, s.Value) {
			return false
		}
	}
	return validateReference(ref) == nil
}

func relatedCitation(in RelatedReadRequest, ref ResourceReference, families []string, remaining *int) bool {
	if !chargeRelatedReference(ref, remaining) || ref.Address.Provider != in.Root.Address.Provider || len(ref.Address.Scope) != len(in.Root.Address.Scope) {
		return false
	}
	if ref.Address.Type == in.Root.Address.Type {
		return sameAddress(ref.Address, in.Root.Address) && *ref.Incarnation == *in.Root.Incarnation
	}
	for _, t := range in.Sources {
		if ref.Address.Type != t.Type || ref.Address.Authority != t.Authority || families != nil && !slices.Contains(families, t.Type) {
			continue
		}
		for _, sc := range t.Scope {
			if sc.Match != ScopeAny && !slices.Contains(ref.Address.Scope, ScopeEntry{Dimension: sc.Dimension, Value: (*sc.Values)[0]}) {
				return false
			}
		}
		return true
	}
	return false
}

func validateRelatedPage(in RelatedReadRequest, p RelatedReadPage) error {
	if len(p.Sources) == 0 {
		return NewError("PERMISSION_DENIED", "No authorized source family available.")
	}
	if !relatedFamilies(p.Sources) || p.Coverage != "partial" && p.Coverage != "complete" ||
		p.Coverage == "complete" && len(p.Sources) != len(in.Sources) || p.Rows == nil || len(p.Rows) > maxRelatedCandidates {
		return invalid("Invalid related read page.")
	}
	for _, id := range p.Sources {
		if !slices.ContainsFunc(in.Sources, func(t ReadTarget) bool { return t.Type == id }) {
			return invalid("Unadmitted related source family.")
		}
	}
	remaining := maxRelatedFrame
	for _, candidate := range p.Rows {
		if len(candidate.Row) == 0 || len(candidate.Row) > maxRelatedFields || len(candidate.Evidence) == 0 || len(candidate.Evidence) > maxRelatedCitations {
			return invalid("Invalid related read candidate.")
		}
		for k, v := range candidate.Row {
			if !relatedSegment(k) || len(k) >= remaining {
				return invalid("Invalid related row field.")
			}
			remaining -= len(k) + 1
			if !relatedScalar(v, &remaining) {
				return invalid("Invalid related scalar.")
			}
		}
		examined := false
		for _, ref := range candidate.Evidence {
			if !relatedCitation(in, ref, p.Sources, &remaining) {
				return invalid("Invalid related evidence.")
			}
			examined = examined || slices.Contains(p.Sources, ref.Address.Type)
		}
		if !examined || candidate.Neighbor != nil && !relatedCitation(in, *candidate.Neighbor, nil, &remaining) {
			return invalid("Unbound related candidate.")
		}
	}
	return nil
}
