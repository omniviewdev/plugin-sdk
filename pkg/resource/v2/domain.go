// Package resourcev2 defines the provider-neutral resource boundary. Generated
// protobuf messages are transport details, not provider-facing domain values.
package resourcev2

import (
	"context"
	"encoding/json"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	pkgsettings "github.com/omniviewdev/plugin-sdk/settings"
)

const WireVersion uint32 = 2

type ScopeEntry struct {
	Dimension string `json:"dimension"`
	Value     string `json:"value"`
}
type ResourceAddress struct {
	Provider  string       `json:"provider"`
	Authority string       `json:"authority"`
	Type      string       `json:"type"`
	Scope     []ScopeEntry `json:"scope"`
	ID        string       `json:"id"`
}
type AccessRoute struct {
	Connection string `json:"connection"`
}
type ResourceReference struct {
	Address     ResourceAddress `json:"address"`
	Incarnation *string         `json:"incarnation,omitempty"`
}
type ScopeMatch string

const (
	ScopeAny   ScopeMatch = "any"
	ScopeOneOf ScopeMatch = "oneOf"
)

type ScopeConstraint struct {
	Dimension string     `json:"dimension"`
	Match     ScopeMatch `json:"match"`
	Values    *[]string  `json:"values,omitempty"`
}
type QueryScope []ScopeConstraint

// Value carries scalar intent, including exact signed and unsigned integers.
// Only the payload selected by Kind may be populated. Int and Uint use canonical
// decimal strings; Timestamp uses UTC RFC3339 with nanosecond precision.
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

// SchemaBinding reports absence separately from failed acquisition. Source
// revisions and schema revisions are opaque equality tokens, never identity.
type SchemaBinding struct {
	State             string                   `json:"state"`
	Revision          *string                  `json:"revision,omitempty"`
	Dialect           string                   `json:"dialect,omitempty"`
	Root              string                   `json:"root,omitempty"`
	DocumentRef       *SchemaDocumentReference `json:"documentRef,omitempty"`
	Document          json.RawMessage          `json:"document,omitempty"`
	Validation        string                   `json:"validation"`
	Unvalidated       []string                 `json:"unvalidated"`
	UnavailableReason *string                  `json:"unavailableReason,omitempty"`
}

// SchemaDocumentReference shares one bounded raw document across catalog roots.
type SchemaDocumentReference struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}
type SchemaDocument struct {
	Reference SchemaDocumentReference `json:"reference"`
	Dialect   string                  `json:"dialect"`
	Document  json.RawMessage         `json:"document"`
}
type Representation struct {
	ID       string        `json:"id"`
	Revision string        `json:"revision"`
	Schema   SchemaBinding `json:"schema"`
}
type Dimension struct {
	Name       string `json:"name"`
	AllowEmpty bool   `json:"allowEmpty"`
}
type Operation string

const (
	OperationEnumerate   Operation = "enumerate"
	OperationGet         Operation = "get"
	OperationNativeQuery Operation = "nativeQuery"
	OperationUpdate      Operation = "update"
	OperationMutation    Operation = "mutation"
	OperationAction      Operation = "action"
)

type Capability struct {
	Operation Operation  `json:"operation"`
	Scope     QueryScope `json:"scope"`
	Guarantee string     `json:"guarantee,omitempty"`
}
type ResourceType struct {
	Type                  string               `json:"type"`
	DisplayName           string               `json:"displayName"`
	IdentityDimensions    []Dimension          `json:"identityDimensions"`
	QueryDimensions       []Dimension          `json:"queryDimensions"`
	Representations       []Representation     `json:"representations"`
	DefaultRepresentation string               `json:"defaultRepresentation,omitempty"`
	Capabilities          []Capability         `json:"capabilities"`
	NativeAuthorityHint   *NativeAuthorityHint `json:"nativeAuthorityHint,omitempty"`
}
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

type Catalog struct {
	Provider string         `json:"provider"`
	Types    []ResourceType `json:"types"`
	Revision string         `json:"revision"`
	Coverage Coverage       `json:"coverage"`
}
type ReadTarget struct {
	Route          AccessRoute `json:"route"`
	Provider       string      `json:"provider"`
	Authority      string      `json:"authority"`
	Type           string      `json:"type"`
	Scope          QueryScope  `json:"scope"`
	Representation string      `json:"representation"`
}
type GetRequest struct {
	Route          AccessRoute       `json:"route"`
	Reference      ResourceReference `json:"reference"`
	Representation string            `json:"representation"`
}
type EnumerateRequest struct {
	Target   ReadTarget `json:"target"`
	PageSize uint32     `json:"pageSize"`
	Cursor   *string    `json:"cursor,omitempty"`
}
type NativeQueryRequest struct {
	Target   ReadTarget `json:"target"`
	Language string     `json:"language"`
	Query    Value      `json:"query"`
	PageSize uint32     `json:"pageSize"`
	Cursor   *string    `json:"cursor,omitempty"`
}
type Coverage struct {
	Kind   string   `json:"kind"`
	Paths  []string `json:"paths"`
	Reason string   `json:"reason,omitempty"`
}
type Count struct {
	Kind  string  `json:"kind"`
	Value *string `json:"value,omitempty"`
}
type Record struct {
	Reference      ResourceReference `json:"reference"`
	Body           Value             `json:"body"`
	Representation Representation    `json:"representation"`
	Coverage       Coverage          `json:"coverage"`
	SourceRevision *string           `json:"sourceRevision,omitempty"`
}

// SourcePage is a bounded acquisition page, never a final host query page.
type SourcePage struct {
	Records        []Record `json:"records"`
	NextCursor     *string  `json:"nextCursor,omitempty"`
	Count          Count    `json:"count"`
	Coverage       Coverage `json:"coverage"`
	Consistency    string   `json:"consistency"`
	SourceRevision *string  `json:"sourceRevision,omitempty"`
}
type MutationRequest struct {
	Route            AccessRoute       `json:"route"`
	Reference        ResourceReference `json:"reference"`
	Operation        string            `json:"operation"`
	Input            Value             `json:"input"`
	ExpectedRevision *string           `json:"expectedRevision,omitempty"`
}
type ActionRequest struct {
	Route     AccessRoute       `json:"route"`
	Reference ResourceReference `json:"reference"`
	Action    string            `json:"action"`
	Input     Value             `json:"input"`
}

// Provider requires only connection-qualified catalog discovery. Finite
// operations and connection lifecycle are independent optional interfaces.
type Provider interface {
	Catalog(context.Context, CatalogRequest) (Catalog, error)
}
type Getter interface {
	Get(context.Context, GetRequest) (Record, error)
}
type Enumerator interface {
	Enumerate(context.Context, EnumerateRequest) (SourcePage, error)
}
type NativeQuerier interface {
	NativeQuery(context.Context, NativeQueryRequest) (SourcePage, error)
}
type Mutator interface {
	Mutate(context.Context, MutationRequest) (Record, error)
}
type Actioner interface {
	Action(context.Context, ActionRequest) (Value, error)
}

// ConnectionLifecycle is independent of finite operations and best-effort
// updates. Strong source continuity remains owned by the P05 SourceSession.
type ConnectionLifecycle interface {
	LoadConnections(context.Context) ([]types.Connection, error)
	GetConnection(context.Context, string) (types.Connection, error)
	StartConnection(context.Context, string) (types.ConnectionStatus, error)
	StopConnection(context.Context, string) (types.Connection, error)
	CheckConnection(context.Context, string) (types.ConnectionStatus, error)
}
type Closer interface{ Close() error }

// Session holds diagnostic and route context. RequesterID is never a grant.
type Session struct {
	Connection   *types.Connection
	PluginConfig pkgsettings.Provider
	RequestID    string
	RequesterID  string
	PluginID     string
	Values       map[string]any
}
type sessionKey struct{}

func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}
func SessionFromContext(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(Session)
	return s, ok
}

// TimestampValue constructs an exact timestamp without floating-point seconds.
func TimestampValue(t time.Time) Value {
	return Value{Kind: "timestamp", Timestamp: t.UTC().Format(time.RFC3339Nano)}
}
