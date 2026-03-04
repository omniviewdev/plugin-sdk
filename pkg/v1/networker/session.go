package networker

import (
	"maps"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	networkerpb "github.com/omniviewdev/plugin-sdk/proto/v1/networker"
)

// ---------------------------------------------------------------------------
// SessionState
// ---------------------------------------------------------------------------

type SessionState string

const (
	SessionStateActive  SessionState = "ACTIVE"
	SessionStatePaused  SessionState = "PAUSED"
	SessionStateStopped SessionState = "STOPPED"
	SessionStateFailed  SessionState = "FAILED"
)

func (s SessionState) String() string {
	return string(s)
}

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

// validTransitions defines the allowed state machine transitions.
var validTransitions = map[SessionState]map[SessionState]bool{
	SessionStateActive: {
		SessionStatePaused:  true,
		SessionStateStopped: true,
		SessionStateFailed:  true,
	},
	SessionStatePaused: {
		SessionStateActive:  true,
		SessionStateStopped: true,
		SessionStateFailed:  true,
	},
	// STOPPED and FAILED are terminal states.
}

// CanTransition returns true if transitioning from → to is valid.
func CanTransition(from, to SessionState) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	return targets[to]
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

type PortForwardProtocol string

const (
	PortForwardProtocolTCP PortForwardProtocol = "TCP"
	PortForwardProtocolUDP PortForwardProtocol = "UDP"
)

func (p PortForwardProtocol) String() string {
	return string(p)
}

func (p PortForwardProtocol) ToProto() networkerpb.PortForwardProtocol {
	switch p {
	case PortForwardProtocolTCP:
		return networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP
	case PortForwardProtocolUDP:
		return networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP
	default:
		return networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP
	}
}

type PortForwardConnectionType string

const (
	PortForwardConnectionTypeResource PortForwardConnectionType = "RESOURCE"
	PortForwardConnectionTypeStatic   PortForwardConnectionType = "STATIC"
)

func PortForwardProtocolFromProto(
	p networkerpb.PortForwardProtocol,
) PortForwardProtocol {
	switch p {
	case networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP:
		return PortForwardProtocolTCP
	case networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP:
		return PortForwardProtocolUDP
	default:
		return PortForwardProtocolTCP
	}
}

// ---------------------------------------------------------------------------
// sessionEntry — internal mutable state per session
// ---------------------------------------------------------------------------

type sessionEntry struct {
	mu      sync.RWMutex
	session PortForwardSession
	cancel  func()
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
	return nil
}

// snapshot returns a deep copy of the session for safe reads.
func (e *sessionEntry) snapshot() PortForwardSession {
	e.mu.RLock()
	cp := e.session
	cp.Labels = maps.Clone(e.session.Labels)
	e.mu.RUnlock()
	return cp
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

func (s *PortForwardSession) ToProto() *networkerpb.PortForwardSession {
	session := &networkerpb.PortForwardSession{
		CreatedAt:  timestamppb.New(s.CreatedAt),
		UpdatedAt:  timestamppb.New(s.UpdatedAt),
		Labels:     s.Labels,
		Id:         s.ID,
		State:      s.State.ToProto(),
		Encryption: s.Encryption.ToProto(),
		LocalPort:  int32(s.LocalPort),
		RemotePort: int32(s.RemotePort),
	}

	switch c := s.Connection.(type) {
	case PortForwardResourceConnection:
		session.Connection = c.ToSessionProto()
	case *PortForwardResourceConnection:
		session.Connection = c.ToSessionProto()
	case PortForwardStaticConnection:
		session.Connection = c.ToSessionProto()
	case *PortForwardStaticConnection:
		session.Connection = c.ToSessionProto()
	}

	switch s.Protocol {
	case PortForwardProtocolTCP:
		session.Protocol = networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP
	case PortForwardProtocolUDP:
		session.Protocol = networkerpb.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP
	}

	return session
}

// NewPortForwardSessionFromProto creates a PortForwardSession from a protobuf.
func NewPortForwardSessionFromProto(s *networkerpb.PortForwardSession) *PortForwardSession {
	if s == nil {
		return &PortForwardSession{}
	}

	var connection Connection
	var connectionType PortForwardConnectionType
	switch s.GetConnection().(type) {
	case *networkerpb.PortForwardSession_ResourceConnection:
		connection = PortForwardResourceConnectionFromProto(s.GetResourceConnection())
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
	}
}

// ---------------------------------------------------------------------------
// PortForwardResourceConnection
// ---------------------------------------------------------------------------

type PortForwardResourceConnection struct {
	ResourceData map[string]interface{} `json:"resource_data"`
	ConnectionID string                 `json:"connection_id"`
	PluginID     string                 `json:"plugin_id"`
	ResourceID   string                 `json:"resource_id"`
	ResourceKey  string                 `json:"resource_key"`
}

func (PortForwardResourceConnection) connectionType() PortForwardConnectionType {
	return PortForwardConnectionTypeResource
}

func (c *PortForwardResourceConnection) ToProto() *networkerpb.PortForwardResourceConnection {
	if c == nil {
		return nil
	}
	data, err := structpb.NewStruct(c.ResourceData)
	if err != nil {
		data, _ = structpb.NewStruct(map[string]interface{}{})
	}

	return &networkerpb.PortForwardResourceConnection{
		ConnectionId: c.ConnectionID,
		PluginId:     c.PluginID,
		ResourceId:   c.ResourceID,
		ResourceKey:  c.ResourceKey,
		ResourceData: data,
	}
}

func (c *PortForwardResourceConnection) ToSessionProto() *networkerpb.PortForwardSession_ResourceConnection {
	if c == nil {
		return nil
	}
	return &networkerpb.PortForwardSession_ResourceConnection{
		ResourceConnection: c.ToProto(),
	}
}

func (c *PortForwardResourceConnection) ToSessionOptionsProto() *networkerpb.PortForwardSessionOptions_ResourceConnection {
	if c == nil {
		return nil
	}
	return &networkerpb.PortForwardSessionOptions_ResourceConnection{
		ResourceConnection: c.ToProto(),
	}
}

func PortForwardResourceConnectionFromProto(
	o *networkerpb.PortForwardResourceConnection,
) PortForwardResourceConnection {
	if o == nil {
		return PortForwardResourceConnection{}
	}
	var resourceData map[string]interface{}
	if rd := o.GetResourceData(); rd != nil {
		resourceData = rd.AsMap()
	}
	return PortForwardResourceConnection{
		ConnectionID: o.GetConnectionId(),
		PluginID:     o.GetPluginId(),
		ResourceID:   o.GetResourceId(),
		ResourceKey:  o.GetResourceKey(),
		ResourceData: resourceData,
	}
}

func PortForwardResourceConnectionFromJson(o map[string]any) *PortForwardResourceConnection {
	conn := PortForwardResourceConnection{}

	if v, ok := o["connection_id"].(string); ok {
		conn.ConnectionID = v
	}
	if v, ok := o["plugin_id"].(string); ok {
		conn.PluginID = v
	}
	if v, ok := o["resource_id"].(string); ok {
		conn.ResourceID = v
	}
	if v, ok := o["resource_key"].(string); ok {
		conn.ResourceKey = v
	}
	if v, ok := o["resource_data"].(map[string]any); ok {
		conn.ResourceData = v
	}

	return &conn
}

// ---------------------------------------------------------------------------
// PortForwardStaticConnection
// ---------------------------------------------------------------------------

type PortForwardStaticConnection struct {
	Address string `json:"address"`
}

func (PortForwardStaticConnection) connectionType() PortForwardConnectionType {
	return PortForwardConnectionTypeStatic
}

func (c *PortForwardStaticConnection) ToProto() *networkerpb.PortForwardStaticConnection {
	if c == nil {
		return nil
	}
	return &networkerpb.PortForwardStaticConnection{
		Address: c.Address,
	}
}

func (c *PortForwardStaticConnection) ToSessionProto() *networkerpb.PortForwardSession_StaticConnection {
	if c == nil {
		return nil
	}
	return &networkerpb.PortForwardSession_StaticConnection{
		StaticConnection: c.ToProto(),
	}
}

func (c *PortForwardStaticConnection) ToSessionOptionsProto() *networkerpb.PortForwardSessionOptions_StaticConnection {
	if c == nil {
		return nil
	}
	return &networkerpb.PortForwardSessionOptions_StaticConnection{
		StaticConnection: c.ToProto(),
	}
}

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

type PortForwardSessionEncryption struct {
	Algorithm string `json:"algorithm"`
	Key       string `json:"key"`
	Enabled   bool   `json:"enabled"`
}

func (o *PortForwardSessionEncryption) ToProto() *networkerpb.PortForwardSessionEncryption {
	return &networkerpb.PortForwardSessionEncryption{
		Enabled:   o.Enabled,
		Algorithm: o.Algorithm,
		Key:       o.Key,
	}
}

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

type ResourcePortForwardHandlerOpts struct {
	Resource PortForwardResourceConnection `json:"resource"`
	Options  PortForwardSessionOptions     `json:"options"`
}

type StaticPortForwardHandlerOpts struct {
	Static  PortForwardStaticConnection `json:"static"`
	Options PortForwardSessionOptions   `json:"options"`
}

func (o *PortForwardSessionOptions) ToProto() *networkerpb.PortForwardSessionOptions {
	opts := &networkerpb.PortForwardSessionOptions{
		LocalPort:  o.LocalPort,
		RemotePort: o.RemotePort,
		Protocol:   o.Protocol.ToProto(),
		Labels:     o.Labels,
		Params:     o.Params,
		Encryption: o.Encryption.ToProto(),
	}

	switch c := o.Connection.(type) {
	case PortForwardResourceConnection:
		opts.Connection = c.ToSessionOptionsProto()
	case *PortForwardResourceConnection:
		opts.Connection = c.ToSessionOptionsProto()
	case PortForwardStaticConnection:
		opts.Connection = c.ToSessionOptionsProto()
	case *PortForwardStaticConnection:
		opts.Connection = c.ToSessionOptionsProto()
	}

	return opts
}

func NewPortForwardSessionOptionsFromProto(
	o *networkerpb.PortForwardSessionOptions,
) *PortForwardSessionOptions {
	if o == nil {
		return &PortForwardSessionOptions{}
	}

	var connection Connection
	var connectionType PortForwardConnectionType
	switch o.GetConnection().(type) {
	case *networkerpb.PortForwardSessionOptions_ResourceConnection:
		connection = PortForwardResourceConnectionFromProto(o.GetResourceConnection())
		connectionType = PortForwardConnectionTypeResource
	case *networkerpb.PortForwardSessionOptions_StaticConnection:
		connection = PortForwardStaticConnectionFromProto(o.GetStaticConnection())
		connectionType = PortForwardConnectionTypeStatic
	default:
		// Unknown connection type — leave both as zero values.
	}

	return &PortForwardSessionOptions{
		LocalPort:      o.GetLocalPort(),
		RemotePort:     o.GetRemotePort(),
		Protocol:       PortForwardProtocolFromProto(o.GetProtocol()),
		Labels:         o.GetLabels(),
		Params:         o.GetParams(),
		Encryption:     PortForwardSessionEncryptionFromProto(o.GetEncryption()),
		Connection:     connection,
		ConnectionType: connectionType,
	}
}

// ---------------------------------------------------------------------------
// FindPortForwardSessionRequest
// ---------------------------------------------------------------------------

type FindPortForwardSessionRequest struct {
	ResourceID   string `json:"resource_id"`
	ConnectionID string `json:"connection_id"`
}

func (p FindPortForwardSessionRequest) ToProto() *networkerpb.FindPortForwardSessionRequest {
	return &networkerpb.FindPortForwardSessionRequest{
		ResourceId:   p.ResourceID,
		ConnectionId: p.ConnectionID,
	}
}

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
