package exec

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

// Session is a snapshot of public terminal-session metadata, not a live view.
// Manager snapshots own their maps and command slice. Callers may read a snapshot
// concurrently, but must synchronize mutations to those containers.
type Session struct {
	CreatedAt time.Time         `json:"created_at"`
	Labels    map[string]string `json:"labels"`
	Params    map[string]string `json:"params"`
	ID        string            `json:"id"`
	Command   []string          `json:"command"`
	Attached  bool              `json:"attached"`
}

// ToProto converts session metadata without changing session lifetime or state.
// A nil receiver returns nil. The result shares Command, Labels, and Params with
// the snapshot; callers must synchronize mutations while either value is in use.
func (s *Session) ToProto() *execpb.Session {
	if s == nil {
		return nil
	}
	return &execpb.Session{
		Id:        s.ID,
		Command:   s.Command,
		Labels:    s.Labels,
		Params:    s.Params,
		Attached:  s.Attached,
		CreatedAt: timestamppb.New(s.CreatedAt),
	}
}

// NewSessionFromProto converts public session metadata and returns nil for nil
// input. Command, Labels, and Params share the input message's containers; the
// result does not own or control the lifetime of a running terminal session.
func NewSessionFromProto(s *execpb.Session) *Session {
	if s == nil {
		return nil
	}
	return &Session{
		ID:        s.GetId(),
		Command:   s.GetCommand(),
		Labels:    s.GetLabels(),
		Params:    s.GetParams(),
		Attached:  s.GetAttached(),
		CreatedAt: s.GetCreatedAt().AsTime(),
	}
}

// ---------------------------------------------------------------------------
// sessionState — internal mutable state per session (not exported)
// ---------------------------------------------------------------------------

// sessionState holds the mutable state for a running session. Access is
// protected by mu. The manager goroutines (handleStream, handleSignals)
// interact with this via methods, never directly touching fields.
type sessionState struct {
	mu       sync.RWMutex
	session  Session // snapshot fields
	terminal Terminal
	buffer   *OutputBuffer

	ctx      context.Context
	cancel   context.CancelFunc
	stopChan chan error
	resize   chan SessionResizeInput
	done     chan struct{}  // closed when wg reaches zero
	wg       sync.WaitGroup // tracks handleStream + handleSignals

	ttyResize bool // if true, manager calls terminal.Resize(); else sends to resize chan
	nextSeq   uint64
}

// snapshot returns a deep copy of the session for safe external reads.
func (ss *sessionState) snapshot() Session {
	ss.mu.RLock()
	cp := ss.session
	cp.Labels = maps.Clone(ss.session.Labels)
	cp.Params = maps.Clone(ss.session.Params)
	cp.Command = slices.Clone(ss.session.Command)
	ss.mu.RUnlock()
	return cp
}

func (ss *sessionState) setAttached(v bool) {
	ss.mu.Lock()
	ss.session.Attached = v
	ss.mu.Unlock()
}

func (ss *sessionState) getBufferData() []byte {
	if ss.buffer == nil {
		return nil
	}
	return ss.buffer.GetAll()
}

// waitDone blocks until all goroutines for this session have finished.
func (ss *sessionState) waitDone() <-chan struct{} {
	return ss.done
}

// ---------------------------------------------------------------------------
// SessionOptions
// ---------------------------------------------------------------------------

// SessionOptions describes a terminal session and optional resource intent.
// ResourcePlugin and ResourceKey are opaque selectors, not authorization.
// ResourceData, when present, must be a canonical tagged map; scalar types remain
// exact through JSON and protobuf conversion. A Reference requires an explicit
// Route and must agree with any supplied selectors. Neither selectors nor a
// reference require a body or an unrelated resource Get operation.
//
// Validation and normalization do not acquire a connection or copy mutable
// containers. Callers must not mutate options concurrently with SDK use.
//
//nolint:recvcheck // Preserve value-method intent queries; JSON decoding mutates through a pointer receiver.
type SessionOptions struct {
	Params         map[string]string             `json:"params"`
	Labels         map[string]string             `json:"labels"`
	ID             string                        `json:"id"`
	ResourcePlugin string                        `json:"resource_plugin"`
	ResourceKey    string                        `json:"resource_key"`
	ResourceData   *resourcev2.Value             `json:"resource_data,omitempty"`
	Reference      *resourcev2.ResourceReference `json:"reference,omitempty"`
	Route          *resourcev2.AccessRoute       `json:"route,omitempty"`
	Command        []string                      `json:"command"`
	TTY            bool                          `json:"tty"`
}

// SessionResizeInput carries resize dimensions for a session.
type SessionResizeInput struct {
	Rows int32 `json:"rows"`
	Cols int32 `json:"cols"`
}

// ensureSessionID returns the ID or generates a new UUID.
func ensureSessionID(id string) string {
	if id == "" {
		return uuid.NewString()
	}
	return id
}
