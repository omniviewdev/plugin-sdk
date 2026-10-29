package resourcev2

import (
	"context"
	"encoding/json"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
)

const WireVersion uint32 = 2

// ScopeEntry is one literal identity dimension, not a query constraint.
// Empty Value is meaningful only when the declared dimension permits it.
type ScopeEntry struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value"`
}

// ResourceAddress identifies an object independently of the connection used to
// access it. Type and ID are opaque; callers must not parse native syntax from
// them. Authority is host-issued, not a provider's native authority hint.
type ResourceAddress struct {
	Provider  string `json:"provider"`
	Authority string `json:"authority"`
	Type      string `json:"type"`
	// Scope contains each declared identity dimension exactly once. Order does
	// not affect identity; no entries means the type has no identity dimensions.
	Scope []ScopeEntry `json:"scope"`
	ID    string       `json:"id"`
}

// AccessRoute selects a configured connection, not an identity or permission.
// Connection must be explicit; an empty route never selects a default client.
type AccessRoute struct {
	Connection string `json:"connection"`
}

// ResourceReference adds an optional native incarnation observation to identity.
// A nil Incarnation makes no claim about object recreation; it is not a source
// revision or an access route.
type ResourceReference struct {
	Address     ResourceAddress `json:"address"`
	Incarnation *string         `json:"incarnation,omitempty"`
}

// ScopeMatch chooses unrestricted matching or an explicit finite selection for
// one dimension. A zero value is invalid, not an implicit wildcard.
type ScopeMatch string

const (
	ScopeAny   ScopeMatch = "any"
	ScopeOneOf ScopeMatch = "oneOf"
)

// ScopeConstraint selects values within one declared query dimension.
type ScopeConstraint struct {
	Dimension string     `json:"dimension"`
	Match     ScopeMatch `json:"match"`
	// Values must be nil for any and nonnil for oneOf. A nonnil empty selection
	// matches nothing; it must never be widened to any.
	Values *[]string `json:"values,omitempty"`
}

// QueryScope intersects constraints across dimensions. Each declared query
// dimension must appear exactly once, including unrestricted dimensions.
// Nil or empty scope therefore applies only to a type with no query dimensions;
// it does not mean all dimensions. Constraint and selection order is immaterial.
// JSON requires a nonnull array; Go nil scopes serialize as an empty array.
type QueryScope []ScopeConstraint

// Value preserves scalar intent rather than reducing every number to float64.
// Kind is null, bool, string, int64, uint64, double, timestamp, list, or map; the
// zero Value is invalid. Only the selected payload may be populated. Int and Uint
// use canonical decimal strings within their respective 64-bit ranges. Double
// must be finite and preserves negative zero. Timestamp uses UTC RFC3339 with
// nanosecond precision. A selected nil List or Map is an empty container, not null.
// Nested containers remain mutable: copy them before retaining borrowed input
// or sharing values with code that may mutate them.
type Value struct {
	Kind      string           `json:"kind"`
	Bool      bool             `json:"-"`
	String    string           `json:"-"`
	Int       string           `json:"-"`
	Uint      string           `json:"-"`
	Double    float64          `json:"-"`
	Timestamp string           `json:"-"`
	List      []Value          `json:"-"`
	Map       map[string]Value `json:"-"`
}

// SchemaBinding distinguishes known absence from failed schema acquisition.
// Available schemas require a revision, dialect, and exactly one inline Document
// or DocumentRef. Absent schemas have no document binding. Unavailable schemas
// cannot carry inline documents and must not be reported as absent.
// Revisions are opaque equality observations, never object identity.
type SchemaBinding struct {
	State    string  `json:"state"`
	Revision *string `json:"revision,omitempty"`
	Dialect  string  `json:"dialect,omitempty"`
	// Root is a local RFC6901 URI-fragment pointer; empty selects the whole
	// document. Resolving it never implies external document acquisition.
	Root        string                   `json:"root,omitempty"`
	DocumentRef *SchemaDocumentReference `json:"documentRef,omitempty"`
	Document    json.RawMessage          `json:"document,omitempty"`
	// Validation is full, reduced, or none. Reduced requires explicit
	// Unvalidated constraints; full and none forbid such claims.
	Validation  string   `json:"validation"`
	Unvalidated []string `json:"unvalidated"`
	// UnavailableReason is an optional reviewed ErrorCode, not native diagnostic
	// text, and is valid only in the unavailable state.
	UnavailableReason *string `json:"unavailableReason,omitempty"`
}

// SchemaDocumentReference shares one bounded raw document across catalog roots.
type SchemaDocumentReference struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// SchemaDocument is a bounded raw schema shared by reference across bindings.
// Document is not normalized; its mutable bytes must remain stable during use.
type SchemaDocument struct {
	Reference SchemaDocumentReference `json:"reference"`
	Dialect   string                  `json:"dialect"`
	Document  json.RawMessage         `json:"document"`
}

// Representation declares a body format and its schema observation. Revision
// tracks that format's contract, independently of record or catalog revisions.
type Representation struct {
	ID       string        `json:"id"`
	Revision string        `json:"revision"`
	Schema   SchemaBinding `json:"schema"`
}

// Dimension declares a required identity or query coordinate. AllowEmpty permits
// the literal empty string; it does not permit omitting the coordinate.
type Dimension struct {
	Name       string `json:"name"`
	AllowEmpty bool   `json:"allowEmpty"`
}

// Operation names independently admitted capabilities, not required methods.
// Advertising one operation never implies support for another.
type Operation string

const (
	OperationEnumerate   Operation = "enumerate"
	OperationGet         Operation = "get"
	OperationNativeQuery Operation = "nativeQuery"
	// OperationUpdate describes advisory watch delivery, not mutation.
	OperationUpdate         Operation = "update"
	OperationCreate         Operation = "create"
	OperationMutationUpdate Operation = "mutationUpdate"
	OperationDelete         Operation = "delete"
	OperationAction         Operation = "action"
)

// Capability advertises one operation over an explicit query scope. Supporting
// create, update, or delete requires its own declaration even with one Mutator.
type Capability struct {
	Operation Operation  `json:"operation"`
	Scope     QueryScope `json:"scope"`
	// Guarantee labels advisory update semantics; finite operations leave it
	// empty. BestEffortGuarantee does not promise replay or source continuity.
	Guarantee string `json:"guarantee,omitempty"`
}

// ResourceType declares identity, query space, formats, and independent support.
// Query-only dimensions cannot be inferred from an object's identity scope.
type ResourceType struct {
	Type               string           `json:"type"`
	DisplayName        string           `json:"displayName"`
	IdentityDimensions []Dimension      `json:"identityDimensions"`
	QueryDimensions    []Dimension      `json:"queryDimensions"`
	Representations    []Representation `json:"representations"`
	// DefaultRepresentation is optional catalog selection metadata, not an
	// implicit provider default. When set it must name a declared representation.
	DefaultRepresentation string               `json:"defaultRepresentation,omitempty"`
	Capabilities          []Capability         `json:"capabilities"`
	NativeAuthorityHint   *NativeAuthorityHint `json:"nativeAuthorityHint,omitempty"`
}

// CatalogRequest discovers support through one explicit access route. Discovery
// does not start a connection or grant authority over the returned types.
type CatalogRequest struct {
	Route AccessRoute `json:"route"`
}

// NativeAuthorityHint is nonauthoritative provider metadata. Only the host may
// issue AuthorityID or decide sharing. Equal hints never establish permission.
type NativeAuthorityHint struct {
	Scheme     string `json:"scheme"`
	ID         string `json:"id"`
	Continuity string `json:"continuity"`
}

const (
	ContinuityConnectionLocal = "connectionLocal"
	ContinuityStableNative    = "stableNative"
)

// Catalog describes support observed through the requested route. Revision is
// an opaque observation used for admission, not an authorization credential.
// Failed or denied discovery is an error, not an empty complete catalog.
type Catalog struct {
	Provider string         `json:"provider"`
	Types    []ResourceType `json:"types"`
	Revision string         `json:"revision"`
	Coverage Coverage       `json:"coverage"`
}

// ReadTarget binds a scoped acquisition to an explicit route and host authority.
// Representation must already be resolved from catalog metadata; empty is invalid.
// Scope selects query dimensions, not merely the dimensions present in identity.
type ReadTarget struct {
	Route          AccessRoute `json:"route"`
	Provider       string      `json:"provider"`
	Authority      string      `json:"authority"`
	Type           string      `json:"type"`
	Scope          QueryScope  `json:"scope"`
	Representation string      `json:"representation"`
}

// GetRequest selects one reference through a route, with an explicit body format.
// Request containers are borrowed for the call; retaining or mutating them
// requires an independent copy.
type GetRequest struct {
	Route          AccessRoute       `json:"route"`
	Reference      ResourceReference `json:"reference"`
	Representation string            `json:"representation"`
}

// EnumerateRequest asks for one finite source page, not a subscription or a
// host-planned query result. A valid empty oneOf must return an empty page.
type EnumerateRequest struct {
	Target   ReadTarget `json:"target"`
	PageSize uint32     `json:"pageSize"`
	// Cursor is nil for an initial acquisition. A nonnil source token is opaque
	// and must not be reused after the target or source generation changes.
	Cursor *string `json:"cursor,omitempty"`
}

// NativeQueryRequest passes an explicitly named native query language and typed
// query value. Native query interpretation never broadens the admitted Target.
// Cursor has the same source-local lifetime as EnumerateRequest.Cursor.
type NativeQueryRequest struct {
	Target   ReadTarget `json:"target"`
	Language string     `json:"language"`
	Query    Value      `json:"query"`
	PageSize uint32     `json:"pageSize"`
	Cursor   *string    `json:"cursor,omitempty"`
}

// Coverage states known acquisition limitations, not operation success alone.
// Complete forbids Paths and Reason. Partial and unknown require a public-safe
// Reason; preserve any reported path limitations rather than claiming completeness.
type Coverage struct {
	Kind   string   `json:"kind"`
	Paths  []string `json:"paths"`
	Reason string   `json:"reason,omitempty"`
}

// Count distinguishes unknown cardinality from an observed zero. Unknown requires
// nil Value; exact and estimate require a canonical unsigned 64-bit decimal string.
// A page's record count does not by itself establish total source cardinality.
type Count struct {
	Kind  string  `json:"kind"`
	Value *string `json:"value,omitempty"`
}

// Record carries one map-root body with explicit identity, representation, and
// coverage provenance. Return independently owned mutable containers; do not
// reuse backing maps, slices, or schema bytes while a caller may consume them.
type Record struct {
	Reference      ResourceReference `json:"reference"`
	Body           Value             `json:"body"`
	Representation Representation    `json:"representation"`
	Coverage       Coverage          `json:"coverage"`
	// SourceRevision optionally observes native content state. Nil makes no
	// revision claim; a token is opaque and is neither identity nor incarnation.
	SourceRevision *string `json:"sourceRevision,omitempty"`
}

// SourcePage is a bounded acquisition page, never a final host query page.
// Empty Records alone does not establish complete coverage or a zero total.
// Providers must preserve explicit coverage and consistency even for empty pages.
type SourcePage struct {
	Records []Record `json:"records"`
	// NextCursor is nil when no continuation is offered. Nonempty tokens retain
	// native source semantics, not portable host-query or continuity semantics.
	NextCursor *string  `json:"nextCursor,omitempty"`
	Count      Count    `json:"count"`
	Coverage   Coverage `json:"coverage"`
	// Consistency is bestEffort or pointInTime. Neither establishes an atomic
	// transition from this finite page to advisory updates.
	Consistency    string  `json:"consistency"`
	SourceRevision *string `json:"sourceRevision,omitempty"`
}

// MutationRequest selects create, update, or delete. Catalog admission maps
// update to OperationMutationUpdate, never to the advisory OperationUpdate.
// Its mutable input is borrowed for the call, not transferred to the provider.
type MutationRequest struct {
	Route     AccessRoute       `json:"route"`
	Reference ResourceReference `json:"reference"`
	Operation string            `json:"operation"`
	Input     Value             `json:"input"`
	// ExpectedRevision optionally carries a native optimistic-write observation;
	// nil requests no such precondition. It is not a catalog admission pin.
	ExpectedRevision *string `json:"expectedRevision,omitempty"`
}

// ActionRequest invokes an independently declared named operation on a reference.
// Input preserves canonical Value scalar kinds and remains borrowed for the call.
type ActionRequest struct {
	Route     AccessRoute       `json:"route"`
	Reference ResourceReference `json:"reference"`
	Action    string            `json:"action"`
	Input     Value             `json:"input"`
}

// Provider requires only connection-qualified catalog discovery. Finite
// operations and connection lifecycle are independent optional interfaces.
// Calls may overlap, including across connections. Implementations must observe
// caller cancellation and synchronize shared native state. Request containers
// are borrowed read-only for each call; copy before retaining them. Result
// containers must remain stable for caller use, not be recycled across calls.
// On error, callers discard the accompanying result. Zero-value catalogs, pages,
// records, and Values are not substitutes for a successful empty result.
type Provider interface {
	Catalog(context.Context, CatalogRequest) (Catalog, error)
}

// Getter supplies point reads only; it does not imply enumeration or live updates.
// Get must return the requested identity and admitted representation or an error.
type Getter interface {
	Get(context.Context, GetRequest) (Record, error)
}

// Enumerator supplies finite, bounded source acquisition when the scoped catalog
// declares enumeration. Return discovery failures as errors, not complete emptiness.
type Enumerator interface {
	Enumerate(context.Context, EnumerateRequest) (SourcePage, error)
}

// NativeQuerier is independent of Enumerator. Native query support still requires
// a matching scoped catalog capability and explicit representation provenance.
type NativeQuerier interface {
	NativeQuery(context.Context, NativeQueryRequest) (SourcePage, error)
}

// Mutator is optional. Each supported verb still needs its own scope-qualified
// catalog capability; implementing this interface does not imply CRUD support.
type Mutator interface {
	Mutate(context.Context, MutationRequest) (Record, error)
}

// Actioner supplies finite named actions. An interface implementation alone does
// not advertise action capability or establish permission to invoke an action.
type Actioner interface {
	Action(context.Context, ActionRequest) (Value, error)
}

// ConnectionLifecycle is independent of finite operations and best-effort
// updates. Strong source continuity remains owned by the P05 SourceSession.
// Configured profiles and started native clients are distinct: discovery and
// GetConnection do not implicitly start clients. Implementations must honor caller
// cancellation and own the mutable configuration returned to callers independently.
// CheckConnection may probe native health; readiness inspection is separate.
type ConnectionLifecycle interface {
	LoadConnections(context.Context) ([]types.Connection, error)
	GetConnection(context.Context, string) (types.Connection, error)
	StartConnection(context.Context, string) (types.ConnectionStatus, error)
	StopConnection(context.Context, string) (types.Connection, error)
	CheckConnection(context.Context, string) (types.ConnectionStatus, error)
}

// ConnectionReadinessProvider reports only registered native lifecycle state.
// It never creates a client, probes network health or acquires a resource lease.
type ConnectionReadinessProvider interface {
	GetConnectionStatus(context.Context, string) (types.ConnectionStatus, error)
}

// Closer releases provider-owned lifetime state. Callers must not assume closing
// an individual request closes the provider or its accepted sessions.
type Closer interface{ Close() error }

// Session holds diagnostic and route context. RequesterID is never a grant.
// Connection may be nil when no profile is selected. Values and PluginConfig are
// borrowed references, not immutable snapshots or SDK-enforced synchronization.
type Session struct {
	Connection   *types.Connection
	PluginConfig pkgsettings.Provider
	RequestID    string
	RequesterID  string
	PluginID     string
	Values       map[string]any
}
type sessionKey struct{}

// WithSession stores diagnostic context without cloning mutable session members.
// Callers must keep shared members stable or synchronize their access.
func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

// SessionFromContext returns the stored session and whether one exists. Its
// pointer, map, and provider members retain the ownership passed to WithSession.
func SessionFromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(Session)
	return s, ok
}

// TimestampValue constructs an exact timestamp without floating-point seconds.
func TimestampValue(t time.Time) Value {
	return Value{Kind: "timestamp", Timestamp: t.UTC().Format(time.RFC3339Nano)}
}
