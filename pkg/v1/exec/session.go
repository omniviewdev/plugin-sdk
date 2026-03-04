package exec

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

// Session is a snapshot value type — safe for concurrent reads, returned by
// the public API. It carries no mutable internal state.
type Session struct {
	CreatedAt time.Time         `json:"created_at"`
	Labels    map[string]string `json:"labels"`
	Params    map[string]string `json:"params"`
	ID        string            `json:"id"`
	Command   []string          `json:"command"`
	Attached  bool              `json:"attached"`
}

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
	done     chan struct{}   // closed when wg reaches zero
	wg       sync.WaitGroup // tracks handleStream + handleSignals

	ttyResize bool // if true, manager calls terminal.Resize(); else sends to resize chan
}

// snapshot returns a deep copy of the session for safe external reads.
func (ss *sessionState) snapshot() Session {
	ss.mu.RLock()
	cp := ss.session
	// Deep-copy maps
	if ss.session.Labels != nil {
		cp.Labels = make(map[string]string, len(ss.session.Labels))
		for k, v := range ss.session.Labels {
			cp.Labels[k] = v
		}
	}
	if ss.session.Params != nil {
		cp.Params = make(map[string]string, len(ss.session.Params))
		for k, v := range ss.session.Params {
			cp.Params[k] = v
		}
	}
	if ss.session.Command != nil {
		cp.Command = make([]string, len(ss.session.Command))
		copy(cp.Command, ss.session.Command)
	}
	ss.mu.RUnlock()
	return cp
}

func (ss *sessionState) setAttached(v bool) {
	ss.mu.Lock()
	ss.session.Attached = v
	ss.mu.Unlock()
}

func (ss *sessionState) isAttached() bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.session.Attached
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

// SessionOptions contains options for creating a new terminal session.
type SessionOptions struct {
	Params         map[string]string      `json:"params"`
	Labels         map[string]string      `json:"labels"`
	ID             string                 `json:"id"`
	ResourcePlugin string                 `json:"resource_plugin"`
	ResourceKey    string                 `json:"resource_key"`
	ResourceData   map[string]interface{} `json:"resource_data"`
	Command        []string               `json:"command"`
	TTY            bool                   `json:"tty"`
}

func NewSessionOptionsFromProto(opts *execpb.SessionOptions) *SessionOptions {
	var resourceData map[string]interface{}
	if rd := opts.GetResourceData(); rd != nil {
		resourceData = rd.AsMap()
	}
	return &SessionOptions{
		ID:             opts.GetId(),
		Command:        opts.GetCommand(),
		TTY:            opts.GetTty(),
		Params:         opts.GetParams(),
		Labels:         opts.GetLabels(),
		ResourcePlugin: opts.GetResourcePlugin(),
		ResourceKey:    opts.GetResourceKey(),
		ResourceData:   resourceData,
	}
}

func (o *SessionOptions) ToProto() (*execpb.SessionOptions, error) {
	if o == nil {
		return nil, fmt.Errorf("nil SessionOptions receiver")
	}
	data, err := structpb.NewStruct(o.ResourceData)
	if err != nil {
		return nil, fmt.Errorf("failed to convert resource data: %w", err)
	}

	return &execpb.SessionOptions{
		Id:             o.ID,
		Command:        o.Command,
		Tty:            o.TTY,
		Params:         o.Params,
		Labels:         o.Labels,
		ResourcePlugin: o.ResourcePlugin,
		ResourceKey:    o.ResourceKey,
		ResourceData:   data,
	}, nil
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
