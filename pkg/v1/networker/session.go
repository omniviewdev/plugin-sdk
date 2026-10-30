package networker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"

	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

const (
	resourceWireVersion  = 2
	resourceDataField    = 6
	sessionResourceField = 20
	optionsResourceField = 10
)

// ---------------------------------------------------------------------------
// SessionState
// ---------------------------------------------------------------------------

// SessionState describes tunnel availability; stopped and failed states are terminal.
type SessionState string

const (
	// SessionStateActive means the forwarder has reported readiness.
	SessionStateActive SessionState = "ACTIVE"
	// SessionStatePaused permits resumption or termination.
	SessionStatePaused SessionState = "PAUSED"
	// SessionStateStopped marks intentional termination.
	SessionStateStopped SessionState = "STOPPED"
	// SessionStateFailed marks unexpected termination.
	SessionStateFailed SessionState = "FAILED"
)

// String returns the stable state label.
func (s SessionState) String() string {
	return string(s)
}

// ToProto encodes the state; unknown states map to stopped.
func (s SessionState) ToProto() networkerpb.PortForwardSession_SessionState {
	switch s {
	case SessionStateActive:
		return networkerpb.PortForwardSession_ACTIVE
	case SessionStatePaused:
		return networkerpb.PortForwardSession_PAUSED
	case SessionStateStopped:
		return networkerpb.PortForwardSession_STOPPED
	case SessionStateFailed:
		return networkerpb.PortForwardSession_FAILED
	default:
		return networkerpb.PortForwardSession_STOPPED
	}
}

func sessionStateFromProto(p networkerpb.PortForwardSession_SessionState) SessionState {
	switch p {
	case networkerpb.PortForwardSession_ACTIVE:
		return SessionStateActive
	case networkerpb.PortForwardSession_PAUSED:
		return SessionStatePaused
	case networkerpb.PortForwardSession_STOPPED:
		return SessionStateStopped
	case networkerpb.PortForwardSession_FAILED:
		return SessionStateFailed
	default:
		return SessionStateStopped
	}
}

// CanTransition reports whether a nonterminal state permits the requested change.
func CanTransition(from, to SessionState) bool {
	switch from {
	case SessionStateActive:
		return to == SessionStatePaused || to == SessionStateStopped || to == SessionStateFailed
	case SessionStatePaused:
		return to == SessionStateActive || to == SessionStateStopped || to == SessionStateFailed
	case SessionStateStopped, SessionStateFailed:
		return false
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Connection interface — sealed via unexported method
// ---------------------------------------------------------------------------

// Connection is implemented by PortForwardResourceConnection and
// PortForwardStaticConnection. The unexported method seals the interface.
type Connection interface {
	connectionType() PortForwardConnectionType
}

var (
	_ Connection = PortForwardResourceConnection{}
	_ Connection = PortForwardStaticConnection{}
)

// ---------------------------------------------------------------------------
// Protocols and Connection Types
// ---------------------------------------------------------------------------

// PortForwardProtocol selects the native transport protocol.
type PortForwardProtocol string

const (
	// PortForwardProtocolTCP requests a TCP tunnel.
	PortForwardProtocolTCP PortForwardProtocol = "TCP"
	// PortForwardProtocolUDP requests a UDP tunnel.
	PortForwardProtocolUDP PortForwardProtocol = "UDP"
)

// String returns the protocol label.
func (p PortForwardProtocol) String() string {
	return string(p)
}

// Valid returns true if the protocol is a known value (TCP or UDP).
func (p PortForwardProtocol) Valid() bool {
	return p == PortForwardProtocolTCP || p == PortForwardProtocolUDP
}

// ToProto converts to the protobuf enum. Unknown values are encoded as an
// unknown enum value (-1), so callers should validate with Valid() upstream.
func (p PortForwardProtocol) ToProto() networkerpb.PortForwardProtocol {
	switch p {
	case PortForwardProtocolTCP:
		return networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP
	case PortForwardProtocolUDP:
		return networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP
	default:
		// Keep unknown values unknown instead of coercing to TCP.
		return networkerpb.PortForwardProtocol(-1)
	}
}

// PortForwardConnectionType distinguishes resource and static forwarding targets.
type PortForwardConnectionType string

const (
	// PortForwardConnectionTypeResource selects a registered resource forwarder.
	PortForwardConnectionTypeResource PortForwardConnectionType = "RESOURCE"
	// PortForwardConnectionTypeStatic selects a registered address forwarder.
	PortForwardConnectionTypeStatic PortForwardConnectionType = "STATIC"
)

// PortForwardProtocolFromProto converts from the protobuf enum. Unknown proto
// values map to an invalid internal protocol (empty string), which callers
// can detect via Valid().
func PortForwardProtocolFromProto(
	p networkerpb.PortForwardProtocol,
) PortForwardProtocol {
	switch p {
	case networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP:
		return PortForwardProtocolTCP
	case networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP:
		return PortForwardProtocolUDP
	default:
		return PortForwardProtocol("")
	}
}

// ---------------------------------------------------------------------------
// sessionEntry — internal mutable state per session
// ---------------------------------------------------------------------------

type sessionEntry struct {
	mu      sync.RWMutex
	session PortForwardSession
	ctx     context.Context
	cancel  context.CancelCauseFunc
}

// transition performs a validated state transition. Returns an error if
// the transition is not allowed by the state machine.
func (e *sessionEntry) transition(to SessionState) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	from := e.session.State
	if !CanTransition(from, to) {
		return NewInvalidStateTransitionError(e.session.ID, from, to)
	}
	e.session.State = to
	e.session.UpdatedAt = time.Now()
	return nil
}

// snapshot returns a deep copy of the session for safe reads.
func (e *sessionEntry) snapshot() PortForwardSession {
	e.mu.RLock()
	defer e.mu.RUnlock()
	cp := e.session
	cp.Labels = maps.Clone(e.session.Labels)
	cp.Connection = cloneConnection(e.session.Connection)
	return cp
}

// cloneConnection returns a deep copy of a Connection interface value.
// Value types are already copied by the interface assignment; pointer types
// and map fields need explicit cloning.
func cloneConnection(c Connection) Connection {
	switch v := c.(type) {
	case PortForwardResourceConnection:
		v = cloneResourceConnection(v)
		return v
	case *PortForwardResourceConnection:
		if v == nil {
			return nil
		}
		cp := *v
		cp = cloneResourceConnection(cp)
		return &cp
	case PortForwardStaticConnection:
		return v
	case *PortForwardStaticConnection:
		if v == nil {
			return nil
		}
		cp := *v
		return &cp
	default:
		return c
	}
}

// ---------------------------------------------------------------------------
// PortForwardSession
// ---------------------------------------------------------------------------

// PortForwardSession represents a session between a forwarding target and the host.
type PortForwardSession struct {
	CreatedAt      time.Time                    `json:"created_at"`
	UpdatedAt      time.Time                    `json:"updated_at"`
	Connection     Connection                   `json:"connection"`
	Labels         map[string]string            `json:"labels"`
	ID             string                       `json:"id"`
	Protocol       PortForwardProtocol          `json:"protocol"`
	State          SessionState                 `json:"state"`
	ConnectionType PortForwardConnectionType    `json:"connection_type"`
	Encryption     PortForwardSessionEncryption `json:"encryption"`
	LocalPort      int32                        `json:"local_port"`
	RemotePort     int32                        `json:"remote_port"`
}

// ToProto validates and copies the session into a bounded wire frame.
// A nil receiver represents an absent session and returns nil without error.
//
//nolint:nilnil // Nil session is an optional transport value, not a failed lookup.
func (s *PortForwardSession) ToProto() (*networkerpb.PortForwardSession, error) {
	if s == nil {
		return nil, nil
	}
	if err := validateResourceSession(*s); err != nil {
		return nil, err
	}
	metadata := *s
	metadata.Connection = nil
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return nil, err
	}
	session := &networkerpb.PortForwardSession{
		CreatedAt:  timestamppb.New(s.CreatedAt),
		UpdatedAt:  timestamppb.New(s.UpdatedAt),
		Labels:     maps.Clone(s.Labels),
		Id:         s.ID,
		State:      s.State.ToProto(),
		Protocol:   s.Protocol.ToProto(),
		Encryption: s.Encryption.ToProto(),
		LocalPort:  s.LocalPort,
		RemotePort: s.RemotePort,
	}
	if _, ok := resourceConnection(s.Connection); ok {
		if err := session.CreatedAt.CheckValid(); err != nil {
			return nil, resourcev2.NewError("INVALID_REQUEST", err.Error())
		}
		if err := session.UpdatedAt.CheckValid(); err != nil {
			return nil, resourcev2.NewError("INVALID_REQUEST", err.Error())
		}
	}
	if err := preflightResource(session, sessionResourceField, s.Connection); err != nil {
		return nil, err
	}

	switch c := s.Connection.(type) {
	case PortForwardResourceConnection:
		p, err := c.ToProto()
		if err != nil {
			return nil, err
		}
		session.Connection = &networkerpb.PortForwardSession_ResourceConnection{ResourceConnection: p}
	case *PortForwardResourceConnection:
		p, err := c.ToProto()
		if err != nil {
			return nil, err
		}
		session.Connection = &networkerpb.PortForwardSession_ResourceConnection{ResourceConnection: p}
	case PortForwardStaticConnection:
		session.Connection = c.ToSessionProto()
	case *PortForwardStaticConnection:
		session.Connection = c.ToSessionProto()
	}

	if err := validateFrame(session); err != nil {
		return nil, err
	}
	return session, nil
}

// NewPortForwardSessionFromProto validates and decodes a bounded session frame.
// Nil input stays absent; invalid resource states, timestamps, and versions fail.
//
//nolint:nilnil // Absent protobuf sessions remain absent; callers decide whether absence is valid.
func NewPortForwardSessionFromProto(s *networkerpb.PortForwardSession) (*PortForwardSession, error) {
	if s == nil {
		return nil, nil
	}
	if err := validateFrame(s); err != nil {
		return nil, err
	}
	metadata := PortForwardSession{
		ID:         s.Id,
		Labels:     s.Labels,
		LocalPort:  s.LocalPort,
		RemotePort: s.RemotePort,
		Encryption: PortForwardSessionEncryptionFromProto(s.Encryption),
	}
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return nil, err
	}
	if s.GetResourceConnection() != nil {
		if !PortForwardProtocolFromProto(s.Protocol).Valid() || s.State < networkerpb.PortForwardSession_ACTIVE ||
			s.State > networkerpb.PortForwardSession_FAILED {
			return nil, resourceInvalidResponse()
		}
		if s.CreatedAt == nil || s.UpdatedAt == nil {
			return nil, resourceInvalidResponse()
		}
		if err := s.CreatedAt.CheckValid(); err != nil {
			return nil, resourcev2.NewError("INVALID_REQUEST", err.Error())
		}
		if err := s.UpdatedAt.CheckValid(); err != nil {
			return nil, resourcev2.NewError("INVALID_REQUEST", err.Error())
		}
	}

	var connection Connection
	var connectionType PortForwardConnectionType
	switch s.GetConnection().(type) {
	case *networkerpb.PortForwardSession_ResourceConnection:
		c, err := PortForwardResourceConnectionFromProto(s.GetResourceConnection())
		if err != nil {
			return nil, err
		}
		connection = c
		connectionType = PortForwardConnectionTypeResource
	case *networkerpb.PortForwardSession_StaticConnection:
		connection = PortForwardStaticConnectionFromProto(s.GetStaticConnection())
		connectionType = PortForwardConnectionTypeStatic
	}

	var createdAt, updatedAt time.Time
	if s.GetCreatedAt() != nil {
		createdAt = s.GetCreatedAt().AsTime()
	}
	if s.GetUpdatedAt() != nil {
		updatedAt = s.GetUpdatedAt().AsTime()
	}

	return &PortForwardSession{
		ID:             s.GetId(),
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		Labels:         s.GetLabels(),
		Protocol:       PortForwardProtocolFromProto(s.GetProtocol()),
		State:          sessionStateFromProto(s.GetState()),
		Encryption:     PortForwardSessionEncryptionFromProto(s.GetEncryption()),
		ConnectionType: connectionType,
		Connection:     connection,
		LocalPort:      s.GetLocalPort(),
		RemotePort:     s.GetRemotePort(),
	}, nil
}

// ---------------------------------------------------------------------------
// PortForwardResourceConnection
// ---------------------------------------------------------------------------

// PortForwardResourceConnection carries exact tagged resource data and only the
// reference and route supplied by the caller. Opaque legacy selectors retain
// their original spelling; no identity or resource acquisition is inferred.
//
//nolint:recvcheck // Value receivers keep both values and pointers in sealed Connection; codecs require nil checks.
type PortForwardResourceConnection struct {
	ResourceData *resourcev2.Value             `json:"resource_data,omitempty"`
	Reference    *resourcev2.ResourceReference `json:"reference,omitempty"`
	Route        *resourcev2.AccessRoute       `json:"route,omitempty"`
	ConnectionID string                        `json:"connection_id"`
	PluginID     string                        `json:"plugin_id"`
	ResourceID   string                        `json:"resource_id"`
	ResourceKey  string                        `json:"resource_key"`
}

func (PortForwardResourceConnection) connectionType() PortForwardConnectionType {
	return PortForwardConnectionTypeResource
}

// ToProto validates exact tagged data and returns an independently owned wire value.
// Nil receivers and oversized frames return classified resource errors.
func (c *PortForwardResourceConnection) ToProto() (*networkerpb.PortForwardResourceConnection, error) {
	if c == nil {
		return nil, resourcev2.NewError("INVALID_REQUEST", "Resource connection must not be nil.")
	}
	if err := validateResourceConnection(*c); err != nil {
		return nil, err
	}
	out := &networkerpb.PortForwardResourceConnection{
		ConnectionId:        c.ConnectionID,
		PluginId:            c.PluginID,
		ResourceId:          c.ResourceID,
		ResourceKey:         c.ResourceKey,
		ResourceWireVersion: resourceWireVersion,
	}
	var err error
	if c.Reference != nil {
		out.Reference, err = resourcev2.ToProtoResourceReference(*c.Reference)
		if err != nil {
			return nil, err
		}
	}
	if c.Route != nil {
		out.Route, err = resourcev2.ToProtoAccessRoute(*c.Route)
		if err != nil {
			return nil, err
		}
	}
	frameBytes := proto.Size(out)
	if c.ResourceData != nil {
		size, sizeErr := resourcev2.ValueWireSize(*c.ResourceData)
		if sizeErr != nil {
			return nil, sizeErr
		}
		frameBytes += protowire.SizeTag(resourceDataField) + protowire.SizeBytes(size)
	}
	if frameBytes > resourcev2.MaxMessageBytes {
		return nil, resourcev2.NewError("RESOURCE_LIMIT", "Resource connection exceeds frame limit.")
	}
	if c.ResourceData != nil {
		out.ResourceData, err = resourcev2.ToProtoValue(*c.ResourceData)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PortForwardResourceConnectionFromProto validates version, unknown fields, and
// tagged values before decoding. The result owns its decoded resource data.
func PortForwardResourceConnectionFromProto(
	o *networkerpb.PortForwardResourceConnection,
) (PortForwardResourceConnection, error) {
	if o == nil {
		return PortForwardResourceConnection{}, resourcev2.NewError(
			"INVALID_REQUEST",
			"Resource connection must not be nil.",
		)
	}
	if err := validateFrame(o); err != nil {
		return PortForwardResourceConnection{}, err
	}
	if o.ResourceWireVersion != resourceWireVersion {
		return PortForwardResourceConnection{}, resourcev2.NewError(
			"UNSUPPORTED",
			"Resource wire version 2 is required.",
		)
	}
	if len(o.ProtoReflect().GetUnknown()) != 0 {
		return PortForwardResourceConnection{}, resourcev2.NewError(
			"UNSUPPORTED",
			"Unknown resource connection fields.",
		)
	}
	out := PortForwardResourceConnection{
		ConnectionID: o.ConnectionId,
		PluginID:     o.PluginId,
		ResourceID:   o.ResourceId,
		ResourceKey:  o.ResourceKey,
	}
	if o.Reference != nil {
		v, e := resourcev2.FromProtoResourceReference(o.Reference)
		if e != nil {
			return out, e
		}
		out.Reference = &v
	}
	if o.Route != nil {
		v, e := resourcev2.FromProtoAccessRoute(o.Route)
		if e != nil {
			return out, e
		}
		out.Route = &v
	}
	if err := validateResourceConnection(out); err != nil {
		return out, err
	}
	if o.ResourceData != nil {
		v, e := resourcev2.FromProtoValue(o.ResourceData)
		if e != nil {
			return out, e
		}
		out.ResourceData = &v
	}
	return out, validateResourceConnection(out)
}

// ---------------------------------------------------------------------------
// PortForwardStaticConnection
// ---------------------------------------------------------------------------

// PortForwardStaticConnection identifies a native address without resource identity.
//
//nolint:recvcheck // Value receivers preserve sealed Connection membership; pointer codecs preserve nil presence.
type PortForwardStaticConnection struct {
	Address string `json:"address"`
}

func (PortForwardStaticConnection) connectionType() PortForwardConnectionType {
	return PortForwardConnectionTypeStatic
}

// ToProto copies the address; a nil receiver stays absent.
func (c *PortForwardStaticConnection) ToProto() *networkerpb.PortForwardStaticConnection {
	if c == nil {
		return nil
	}
	return &networkerpb.PortForwardStaticConnection{
		Address: c.Address,
	}
}

// ToSessionProto wraps the address in the session connection oneof.
func (c *PortForwardStaticConnection) ToSessionProto() *networkerpb.PortForwardSession_StaticConnection {
	if c == nil {
		return nil
	}
	return &networkerpb.PortForwardSession_StaticConnection{
		StaticConnection: c.ToProto(),
	}
}

// ToSessionOptionsProto wraps the address in the options connection oneof.
func (c *PortForwardStaticConnection) ToSessionOptionsProto() *networkerpb.PortForwardSessionOptions_StaticConnection {
	if c == nil {
		return nil
	}
	return &networkerpb.PortForwardSessionOptions_StaticConnection{
		StaticConnection: c.ToProto(),
	}
}

// PortForwardStaticConnectionFromProto copies the address; nil produces the zero value.
func PortForwardStaticConnectionFromProto(
	o *networkerpb.PortForwardStaticConnection,
) PortForwardStaticConnection {
	if o == nil {
		return PortForwardStaticConnection{}
	}
	return PortForwardStaticConnection{
		Address: o.GetAddress(),
	}
}

// PortForwardStaticConnectionFromJson reads a string address; absent or nonstring
// addresses remain empty, matching the static target's zero value.
//
//nolint:revive,staticcheck // Retain the exported spelling for provider source compatibility.
func PortForwardStaticConnectionFromJson(o map[string]any) *PortForwardStaticConnection {
	conn := PortForwardStaticConnection{}

	if v, ok := o["address"].(string); ok {
		conn.Address = v
	}

	return &conn
}

// ---------------------------------------------------------------------------
// PortForwardSessionEncryption
// ---------------------------------------------------------------------------

// PortForwardSessionEncryption carries forwarder-specific encryption settings.
// The manager transports these settings; the native forwarder enforces support.
type PortForwardSessionEncryption struct {
	Algorithm string `json:"algorithm"`
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
}

// ToProto copies encryption settings; nil stays absent.
func (o *PortForwardSessionEncryption) ToProto() *networkerpb.PortForwardSessionEncryption {
	if o == nil {
		return nil
	}
	return &networkerpb.PortForwardSessionEncryption{
		Enabled:   o.Enabled,
		Algorithm: o.Algorithm,
		Key:       o.Key,
	}
}

// PortForwardSessionEncryptionFromProto copies settings; nil produces disabled encryption.
func PortForwardSessionEncryptionFromProto(
	o *networkerpb.PortForwardSessionEncryption,
) PortForwardSessionEncryption {
	if o == nil {
		return PortForwardSessionEncryption{}
	}
	return PortForwardSessionEncryption{
		Enabled:   o.GetEnabled(),
		Algorithm: o.GetAlgorithm(),
		Key:       o.GetKey(),
	}
}

// ---------------------------------------------------------------------------
// PortForwardSessionOptions
// ---------------------------------------------------------------------------

// PortForwardSessionOptions configures a new tunnel. LocalPort zero requests an
// available port. The manager copies labels and connection data on acceptance.
type PortForwardSessionOptions struct {
	Connection     Connection                   `json:"connection"`
	Labels         map[string]string            `json:"labels"`
	Params         map[string]string            `json:"params"`
	Protocol       PortForwardProtocol          `json:"protocol"`
	ConnectionType PortForwardConnectionType    `json:"connection_type"`
	Encryption     PortForwardSessionEncryption `json:"encryption"`
	LocalPort      int32                        `json:"local_port"`
	RemotePort     int32                        `json:"remote_port"`
}

// UnmarshalJSON decodes a strict options envelope and validates resource targets
// without inferring identity from legacy selectors.
func (o *PortForwardSessionOptions) UnmarshalJSON(data []byte) error {
	type Alias PortForwardSessionOptions
	aux := &struct {
		*Alias

		Connection json.RawMessage `json:"connection"`
	}{
		Alias: (*Alias)(o),
	}
	if err := strictNetworkJSON(data, aux); err != nil {
		return err
	}

	if len(aux.Connection) == 0 {
		return nil
	}

	// Reject JSON null payloads early.
	if string(aux.Connection) == "null" {
		return fmt.Errorf("connection payload is null")
	}

	switch o.ConnectionType {
	case PortForwardConnectionTypeResource:
		var connection PortForwardResourceConnection
		if err := strictNetworkJSON(aux.Connection, &connection); err != nil {
			return err
		}
		if err := validateResourceConnection(connection); err != nil {
			return err
		}
		o.Connection = &connection
	case PortForwardConnectionTypeStatic:
		var m map[string]any
		if err := json.Unmarshal(aux.Connection, &m); err != nil {
			return fmt.Errorf("unmarshal static connection: %w", err)
		}
		if m == nil {
			return fmt.Errorf("static connection payload decoded to nil")
		}
		o.Connection = PortForwardStaticConnectionFromJson(m)
	case "":
		return fmt.Errorf("connection_type is empty but connection payload is present")
	default:
		return fmt.Errorf("unknown connection_type %q", o.ConnectionType)
	}

	return nil
}

// ResourcePortForwardHandlerOpts pairs resource identity with forwarding options.
type ResourcePortForwardHandlerOpts struct {
	Resource PortForwardResourceConnection `json:"resource"`
	Options  PortForwardSessionOptions     `json:"options"`
}

// StaticPortForwardHandlerOpts pairs a native address with forwarding options.
type StaticPortForwardHandlerOpts struct {
	Static  PortForwardStaticConnection `json:"static"`
	Options PortForwardSessionOptions   `json:"options"`
}

// ToProto validates metadata and resource payloads before allocating a bounded frame.
// Nil options return INVALID_REQUEST.
func (o *PortForwardSessionOptions) ToProto() (*networkerpb.PortForwardSessionOptions, error) {
	if o == nil {
		return nil, resourcev2.NewError("INVALID_REQUEST", "Session options must not be nil.")
	}
	if err := validateOptionMetadata(*o); err != nil {
		return nil, err
	}
	opts := &networkerpb.PortForwardSessionOptions{
		LocalPort:  o.LocalPort,
		RemotePort: o.RemotePort,
		Protocol:   o.Protocol.ToProto(),
		Labels:     maps.Clone(o.Labels),
		Params:     maps.Clone(o.Params),
		Encryption: o.Encryption.ToProto(),
	}
	if err := preflightResource(opts, optionsResourceField, o.Connection); err != nil {
		return nil, err
	}

	switch c := o.Connection.(type) {
	case PortForwardResourceConnection:
		p, err := c.ToProto()
		if err != nil {
			return nil, err
		}
		opts.Connection = &networkerpb.PortForwardSessionOptions_ResourceConnection{ResourceConnection: p}
	case *PortForwardResourceConnection:
		p, err := c.ToProto()
		if err != nil {
			return nil, err
		}
		opts.Connection = &networkerpb.PortForwardSessionOptions_ResourceConnection{ResourceConnection: p}
	case PortForwardStaticConnection:
		opts.Connection = c.ToSessionOptionsProto()
	case *PortForwardStaticConnection:
		opts.Connection = c.ToSessionOptionsProto()
	}

	if err := validateFrame(opts); err != nil {
		return nil, err
	}
	return opts, nil
}

// NewPortForwardSessionOptionsFromProto validates the wire frame and resource
// version before decoding. Nil options return INVALID_REQUEST.
func NewPortForwardSessionOptionsFromProto(
	o *networkerpb.PortForwardSessionOptions,
) (*PortForwardSessionOptions, error) {
	if o == nil {
		return nil, resourcev2.NewError("INVALID_REQUEST", "Session options must not be nil.")
	}
	if err := validateFrame(o); err != nil {
		return nil, err
	}
	metadata := PortForwardSessionOptions{
		LocalPort:  o.LocalPort,
		RemotePort: o.RemotePort,
		Protocol:   PortForwardProtocolFromProto(o.Protocol),
		Labels:     o.Labels,
		Params:     o.Params,
		Encryption: PortForwardSessionEncryptionFromProto(o.Encryption),
	}
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return nil, err
	}

	var connection Connection
	var connectionType PortForwardConnectionType
	switch o.GetConnection().(type) {
	case *networkerpb.PortForwardSessionOptions_ResourceConnection:
		c, err := PortForwardResourceConnectionFromProto(o.GetResourceConnection())
		if err != nil {
			return nil, err
		}
		connection = c
		connectionType = PortForwardConnectionTypeResource
	case *networkerpb.PortForwardSessionOptions_StaticConnection:
		connection = PortForwardStaticConnectionFromProto(o.GetStaticConnection())
		connectionType = PortForwardConnectionTypeStatic
	default:
		// Unknown connection type — leave both as zero values.
	}

	out := &PortForwardSessionOptions{
		LocalPort:      o.GetLocalPort(),
		RemotePort:     o.GetRemotePort(),
		Protocol:       PortForwardProtocolFromProto(o.GetProtocol()),
		Labels:         o.GetLabels(),
		Params:         o.GetParams(),
		Encryption:     PortForwardSessionEncryptionFromProto(o.GetEncryption()),
		Connection:     connection,
		ConnectionType: connectionType,
	}
	if err := validateOptionMetadata(*out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// FindPortForwardSessionRequest
// ---------------------------------------------------------------------------

// FindPortForwardSessionRequest filters sessions by opaque resource and connection
// selectors. Empty selectors do not restrict that dimension.
type FindPortForwardSessionRequest struct {
	ResourceID   string `json:"resource_id"`
	ConnectionID string `json:"connection_id"`
}

// ToProto copies the filter selectors into a wire request.
func (p FindPortForwardSessionRequest) ToProto() *networkerpb.FindPortForwardSessionRequest {
	return &networkerpb.FindPortForwardSessionRequest{
		ResourceId:   p.ResourceID,
		ConnectionId: p.ConnectionID,
	}
}

// NewFindPortForwardSessionRequestFromProto copies selectors; nil matches all sessions.
func NewFindPortForwardSessionRequestFromProto(
	p *networkerpb.FindPortForwardSessionRequest,
) FindPortForwardSessionRequest {
	if p == nil {
		return FindPortForwardSessionRequest{}
	}
	return FindPortForwardSessionRequest{
		ResourceID:   p.GetResourceId(),
		ConnectionID: p.GetConnectionId(),
	}
}
