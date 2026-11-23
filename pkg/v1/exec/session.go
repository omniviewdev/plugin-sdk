package exec

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
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

	// readerDone is closed when handleStream has returned: the terminal's output has been read to its end,
	// or the terminal was closed under it.
	readerDone chan struct{}
	// requested is set when the host (CloseSession), the manager (Close) or the end of the stream
	// the session was served by ended the session, and not the session itself: nobody waits for
	// what such a session has written.
	requested atomic.Bool
	// mark is the end mark of the session once its process is over (see Manager.drainTerminal): from the moment it
	// is set the reader looks for it in the terminal's output.
	mark atomic.Pointer[[]byte]
	// gone is set, under mu, when the session left the manager's map; an attach that found the
	// session in the map and takes mu afterwards sees it, and looks for what the session left.
	gone bool

	ttyResize bool // if true, manager calls terminal.Resize(); else sends to resize chan
	nextSeq   uint64

	// emitMu orders the session's output: data and signals are handed to the sink under it,
	// and ended is set under it once the Close signal is out. The Close signal is the last
	// thing a session sends: data still on its way when the session ends is delivered first,
	// and data that comes after the signal is dropped (it stays in the buffer).
	emitMu sync.Mutex
	ended  bool
}

// snapshot returns a deep copy of the session for safe external reads.
func (ss *sessionState) snapshot() Session {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.snapshotLocked()
}

// snapshotLocked is snapshot for a caller that holds mu.
func (ss *sessionState) snapshotLocked() Session {
	cp := ss.session
	cp.Labels = maps.Clone(ss.session.Labels)
	cp.Params = maps.Clone(ss.session.Params)
	cp.Command = slices.Clone(ss.session.Command)
	return cp
}

// stop ends the session at the request of whoever manages it, and not of its own: it cancels the
// session's context after noting that it was asked to.
func (ss *sessionState) stop() {
	ss.requested.Store(true)
	ss.cancel()
}

// endMark returns the end mark of the session, or nil while its process is not over.
func (ss *sessionState) endMark() []byte {
	if mark := ss.mark.Load(); mark != nil {
		return *mark
	}
	return nil
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

// retainedOutput is what a session leaves when it ends by itself while nobody is attached: the
// session as it was, and the output it wrote, for the first attach that comes for it. The manager keeps
// a bounded number of them, for a bounded time.
type retainedOutput struct {
	session Session
	output  []byte
	at      time.Time // when the session ended, by the manager's clock
	seq     uint64    // the order in which the manager kept them, to drop the oldest first
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
// ID must be unique for every session, a restart included (use a UUID). The manager refuses
// the ID of a live session with [ErrSessionExists], but it frees an ID when its session ends,
// which is before that session's Close signal is sent: a session started with the ID in
// between is admitted, and the old session's Close signal then arrives for an ID that is live
// again. Whoever keys state on the ID (a host that forgets a session on its Close signal)
// would forget the new one.
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
