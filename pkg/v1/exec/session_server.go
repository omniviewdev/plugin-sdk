package exec

import (
	"context"
	"errors"
	"slices"
	"strings"

	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The calls that manage sessions without a stream (FEATURE_SESSION_MANAGEMENT): ListSessions, ReadOutput and EndSession. They are
// unary and bounded by their callers' contexts, and they are admitted like Options: the request is strict (a field this plugin does
// not know is refused UNSUPPORTED), and the plugin's session context of the call is read (the catalog pin, the diagnostic ids),
// never required. They address a session by the id the host chose, and reach the live ones in the registry and the ended ones in
// the tombstones.

const (
	// defaultReadOutputBytes is what a ReadOutput with max_bytes zero returns at most.
	defaultReadOutputBytes = 64 << 10
	// maxReadOutputBytes is the most a ReadOutput answers, whatever it asks and whatever the ring holds: the answer is one message of
	// the transport, which is bounded.
	maxReadOutputBytes = 1 << 20
	// maxReadOutputRuns is the most chunks a ReadOutput answers. A session without a terminal that alternates its two streams in
	// tiny writes has a chunk for each alternation, and the answer is bounded by this and not by its bytes alone; the host reads on
	// from next_offset.
	maxReadOutputRuns = 1024
)

// ListSessions lists the live sessions and the ones that ended recently (the tombstones), by id.
func (s *serverV2) ListSessions(ctx context.Context, req *execv2.ListSessionsRequest) (*execv2.ListSessionsResponse, error) {
	if refusal := rejectUnknownFields(req); refusal != nil {
		return nil, s.refuse(ctx, "ListSessions", refusal)
	}
	if _, err := resourceplugin.IncomingSessionContext(ctx); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	return &execv2.ListSessionsResponse{Sessions: s.engine.listSessions()}, nil
}

// ReadOutput is a cursor read of a session's replay ring, for a live session and for a tombstone.
func (s *serverV2) ReadOutput(ctx context.Context, req *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error) {
	if refusal := rejectUnknownFields(req); refusal != nil {
		return nil, s.refuse(ctx, "ReadOutput", refusal)
	}
	if refusal := validateSessionID(req.GetSessionId()); refusal != nil {
		return nil, s.refuse(ctx, "ReadOutput", refusal)
	}
	if _, err := resourceplugin.IncomingSessionContext(ctx); err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	resp, refusal := s.engine.readOutput(req.GetSessionId(), req.GetFromOffset(), int(req.GetMaxBytes()))
	if refusal != nil {
		return nil, s.refuse(ctx, "ReadOutput", refusal)
	}
	return resp, nil
}

// EndSession destroys a session that has no stream, and answers once it has ended.
func (s *serverV2) EndSession(ctx context.Context, req *execv2.EndSessionRequest) (*execv2.EndSessionResponse, error) {
	if refusal := rejectUnknownFields(req); refusal != nil {
		return nil, s.refuse(ctx, "EndSession", refusal)
	}
	if refusal := validateSessionID(req.GetSessionId()); refusal != nil {
		return nil, s.refuse(ctx, "EndSession", refusal)
	}
	ctx, err := resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	// An unset mode is graceful, and so is a mode this build does not know: nothing destructive is the reading of a value that was
	// not understood.
	kill := req.GetMode() == execv2.CloseMode_CLOSE_MODE_KILL
	if refusal := s.engine.endSession(ctx, req.GetSessionId(), kill); refusal != nil {
		return nil, s.refuse(ctx, "EndSession", refusal)
	}
	return &execv2.EndSessionResponse{}, nil
}

// session returns the live session with the id: a command of the kind session that was handed to its function. A run is not a
// session, and an id that only a tombstone holds is not live.
func (e *engine) session(id string) (*command, bool) {
	entry, ok := e.reg.Get(id)
	if !ok {
		return nil, false
	}
	c := entry.Value()
	if c.kind != kindSession || !c.handedOver() {
		return nil, false
	}
	return c, true
}

// unknownSession is the refusal for an id that names no session: it never existed, or its tombstone is gone.
func unknownSession() *Error {
	return NewError(ErrorClassNotFound, ErrorReasonSessionUnknown, errors.New("no session has this id"))
}

// listSessions is every session the engine knows, live or ended, sorted by id.
//
// The live sessions and the tombstones are read under two locks (the registry's and the store's), so a session can end between the
// two reads. The order of the reads is what makes that harmless: the engine makes a session's tombstone before it releases its id
// (onEnd), so a session that is gone from the registry is already in the store, and the live sessions are read first. A session that
// ends between the reads is then in both and listed once, from its tombstone, with its end; read the other way round it would be in
// neither, and not listed at all.
func (e *engine) listSessions() []*execv2.SessionInfo {
	entries := e.reg.Entries()
	if hook := e.betweenStoreReads; hook != nil {
		hook()
	}
	tombs := e.tombs.list()
	ended := make(map[string]struct{}, len(tombs))
	infos := make([]*execv2.SessionInfo, 0, len(entries)+len(tombs))
	for _, t := range tombs {
		ended[t.id] = struct{}{}
		infos = append(infos, t.info())
	}
	for _, entry := range entries {
		c := entry.Value()
		if _, done := ended[c.id]; done || c.kind != kindSession || !c.handedOver() {
			continue
		}
		infos = append(infos, c.sessionInfo())
	}
	slices.SortFunc(infos, func(a, b *execv2.SessionInfo) int {
		return strings.Compare(a.GetFacts().GetSessionId(), b.GetFacts().GetSessionId())
	})
	return infos
}

// readOutput reads the ring of a session from an offset: the chunks from the later of the offset and the front of the ring, at
// most max bytes (zero is the default, and the ring's size and the answer's bound are the most). A session that ended is read
// from its tombstone, which says so; one whose tombstone lost its output answers no chunks.
//
// The tombstones and the live sessions are two stores under two locks, so a session can end between the lookups. The engine makes a
// session's tombstone before it releases its id (onEnd), so one that is gone from the registry is in the tombstones: a lookup that
// finds the session in neither of the two has to look in the tombstones once more, or a host that asked for the output of a session
// that is just ending would be told that it does not exist.
func (e *engine) readOutput(id string, from uint64, max int) (*execv2.ReadOutputResponse, *Error) {
	var r *ring
	var ended bool
	var last uint64
	if t, ok := e.tombs.get(id); ok {
		r, ended, last = t.ring, true, t.ended.GetLastOffset()
	} else {
		if hook := e.betweenStoreReads; hook != nil {
			hook()
		}
		if c, ok := e.session(id); ok {
			r = c.ring
		} else if t, ok := e.tombs.get(id); ok {
			r, ended, last = t.ring, true, t.ended.GetLastOffset()
		} else {
			return nil, unknownSession()
		}
	}
	if r == nil {
		// The tombstone kept the end and not the bytes.
		if from > last {
			return nil, invalidRequest("from_offset %d is beyond the end of the output, %d", from, last)
		}
		return &execv2.ReadOutputResponse{ReplayStart: last, NextOffset: last, Ended: true}, nil
	}
	if _, end := r.window(); from > end {
		return nil, invalidRequest("from_offset %d is beyond the end of the output, %d", from, end)
	}
	if max <= 0 {
		max = defaultReadOutputBytes
	}
	runs, start, next := r.readAt(from, min(max, r.capacity(), maxReadOutputBytes), maxReadOutputRuns)
	resp := &execv2.ReadOutputResponse{ReplayStart: start, NextOffset: next, Ended: ended, Chunks: make([]*execv2.Output, len(runs))}
	for i, run := range runs {
		resp.Chunks[i] = &execv2.Output{Stream: run.Stream, Offset: run.Offset, Data: run.Data}
	}
	return resp, nil
}

// endSession destroys a session with no stream and waits until it is gone, which is when its place is free and its tombstone is
// readable: the caller's context ends the wait, and the session then still ends. A session that has ended already is not an error,
// which is what the caller asked for; one that has a stream attached is CONFLICT, because the stream is the session's owner and
// the host closes it there; one that never existed is unknown. A stream whose context has ended is not attached: its host cancelled
// it or lost its connection, and a host that cancels the stream of a session whose function never reads its input and ends it with
// a kill (the way out of one) must not be refused because the server's handler of that stream has not yet noticed.
func (e *engine) endSession(ctx context.Context, id string, kill bool) *Error {
	c, live := e.session(id)
	if !live {
		if _, ok := e.tombs.get(id); ok {
			return nil
		}
		return unknownSession()
	}
	c.releaseIfStreamOver()
	if !c.ring.endIfDetached(kill) {
		return &Error{Class: ErrorClassConflict, Err: errors.New("the session has a stream attached: close it there")}
	}
	select {
	case <-c.Gone():
		return nil
	case <-ctx.Done():
		return contextRefusal(ctx.Err())
	}
}
