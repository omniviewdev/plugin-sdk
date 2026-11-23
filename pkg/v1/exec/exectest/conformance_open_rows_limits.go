package exectest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// ---------------------------------------------------------------------------------------------------------------
// case 12: limits are typed
// ---------------------------------------------------------------------------------------------------------------

// refuses holds an Open that must be refused to the contract of a refusal: it is a status and no frame, as the generated client reads it; the
// SDK's client says the same class and reason; and the function never ran. want names the refusal ("LIMIT/TOO_MANY_SESSIONS").
func (r *wireRig) refuses(what string, req *execv2.OpenRequest, want string) {
	r.t.Helper()
	before := r.started.Load()
	if frame, err := r.rawFirst(req); frame != nil || err == nil {
		r.t.Errorf("%s: the Open gave %s and %v: a refusal is a status and no frame", what, frameName(frame), err)
	}
	conn, err := r.client.Open(r.request(), req)
	switch {
	case err == nil:
		_ = conn.Close()
		r.t.Errorf("%s: the Open was admitted, want the refusal %s", what, want)
	case refusalName(err) != want:
		r.t.Errorf("%s: the Open was refused with %s (%v), want %s", what, refusalName(err), err, want)
	}
	if got := r.started.Load(); got != before {
		r.t.Errorf("%s: the function ran %d times for a request that was refused: a refusal means nothing ran", what, got-before)
	}
}

// caseWireLimits: what a target declares it does not serve, and what it limits, is refused with a typed status that no frame comes before,
// and the function never runs: a session over the target's limit is LIMIT / TOO_MANY_SESSIONS, and a place is free again once a session
// has ended; an input frame of exactly the target's maximum is taken and one byte over it ends the stream with LIMIT / INPUT_TOO_LARGE and
// the session with it; an environment or a working directory that the target does not honor is UNSUPPORTED (and one it honors reaches
// the function); a terminal that the target cannot give is UNSUPPORTED / TTY_UNSUPPORTED, and a size is refused above the target's
// largest dimension, taken at it, and refused for a session with no terminal; a request that breaks a bound of its own, an argv with a NUL, an
// id that is not one, is INVALID_REQUEST; and the id of a live session is CONFLICT / SESSION_EXISTS, while the session that holds it goes on.
func caseWireLimits(t checkT, target exec.Target, env *wireEnv) {
	t.Helper()
	if target.Info.Session.IsZero() {
		t.Skipf(noSessions)
	}
	rig := newWireRig(t, env, target)
	rig.script("idle", Block())
	rig.script("echo", CopyInput())
	rig.checkTooManySessions()
	rig.checkInputTooLarge()
	rig.checkUnsupportedFields()
	rig.checkTerminalLimits()
	rig.checkRequestBounds()
	rig.checkLiveID()
}

// closeAll ends the sessions the host holds and reads each to its end: every place is free when it has.
func (r *wireRig) closeAll(sessions []*wireSession) {
	r.t.Helper()
	for _, s := range sessions {
		_ = s.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	}
	for _, s := range sessions {
		s.finish()
	}
}

// checkTooManySessions fills every place of the target with a session that waits, checks that the one over is refused and that the
// sessions that hold the places go on, and ends one: its place is free by the time the host has its Ended, and a session is admitted again.
func (r *wireRig) checkTooManySessions() {
	r.t.Helper()
	limit := int(r.described().GetSession().GetMaxSessions())
	if limit < 1 || limit > 64 {
		r.t.Logf("the target serves %d sessions at once: too many to fill, so the limit is not checked", limit)
		return
	}
	held := make([]*wireSession, limit)
	for i := range held {
		held[i] = r.mustOpen(r.newID("held"), "idle")
	}
	r.await(fmt.Sprintf("the functions of %d sessions to run", limit), func() bool { return r.started.Load() == int64(limit) })
	r.refuses(fmt.Sprintf("session %d of a target that serves %d", limit+1, limit), r.openRequest(r.newID("over"), "idle"), "LIMIT/TOO_MANY_SESSIONS")
	// The sessions that hold the places are not disturbed by the one that was refused: ending one of them gives its place back.
	first := held[0]
	_ = first.closeSession(execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	first.finish()
	replacement := r.mustOpen(r.newID("replacement"), "idle")
	r.refuses(fmt.Sprintf("session %d of a target that serves %d, after a session ended and another took its place", limit+1, limit), r.openRequest(r.newID("over"), "idle"), "LIMIT/TOO_MANY_SESSIONS")
	r.closeAll(append(held[1:], replacement))
}

// checkInputTooLarge: an input frame of the target's maximum is taken whole and echoed, and a frame one byte over it is a breach of
// the protocol: the stream ends with LIMIT / INPUT_TOO_LARGE, no Ended comes, the function is ended and its place is free.
func (r *wireRig) checkInputTooLarge() {
	r.t.Helper()
	maxFrame := int(r.described().GetSession().GetMaxInputFrame())
	if maxFrame < 1 || maxFrame >= 3<<20 {
		r.t.Logf("the target takes input frames of %d bytes: too large to send, so the limit is not checked", maxFrame)
		return
	}
	s := r.mustOpen(r.newID("input"), "echo")
	audit := s.audit(patternByte)
	s.mustInput(patternBytes(0, maxFrame))
	if got := s.readBytes(maxFrame); !bytes.Equal(got, patternBytes(0, maxFrame)) {
		r.t.Errorf("an input frame of the target's maximum of %d bytes came back as %d other bytes: a frame at the limit is taken whole", maxFrame, len(got))
	}
	_ = s.send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: patternBytes(uint64(maxFrame), maxFrame+1)}})
	var err error
	for err == nil {
		var frame *execv2.ServerFrame
		if frame, err = s.recv(); err == nil && frame.GetEnded() != nil {
			r.t.Errorf("an input frame of %d bytes, one over the target's maximum, was answered with an Ended: a host that breaks the protocol is told so with a status", maxFrame+1)
			return
		}
	}
	switch {
	case errors.Is(err, io.EOF):
		r.t.Errorf("an input frame of %d bytes, one over the target's maximum, ended the stream with status OK: want LIMIT / INPUT_TOO_LARGE", maxFrame+1)
	case refusalName(err) != "LIMIT/INPUT_TOO_LARGE":
		r.t.Errorf("an input frame of %d bytes, one over the target's maximum, ended the stream with %v, want LIMIT / INPUT_TOO_LARGE", maxFrame+1, err)
	}
	for _, problem := range audit.problems {
		r.t.Errorf("%s", problem)
	}
	r.await("the function of the session whose host broke the protocol to return", func() bool { return r.started.Load() == r.returned.Load() })
	// Its place is free, and nothing of it is left to disturb the next session.
	again := r.mustOpen(r.newID("input"), "echo")
	again.roundTrip([]byte("again"))
	r.closeAll([]*wireSession{again})
}

// seesRequest registers a script that hands the request it was given to a channel and waits to be ended.
func (r *wireRig) seesRequest(name string) <-chan exec.Request {
	seen := make(chan exec.Request, 1)
	r.scriptFunc(name, func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- p.Request
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	})
	return seen
}

// checkUnsupportedFields: an environment, and a working directory, are refused UNSUPPORTED by a target that does not honor them and taken, whole, by
// one that does.
func (r *wireRig) checkUnsupportedFields() {
	r.t.Helper()
	caps := r.info.Capabilities
	r.script("never", Block())
	type field struct {
		name   string
		honors bool
		set    func(*execv2.Start)
		seen   func(exec.Request) bool
	}
	for _, f := range []field{
		{"an environment", caps.Env, func(st *execv2.Start) {
			st.Env = []*execv2.NameValue{{Name: "EXECTEST_A", Value: "b c"}, {Name: "EXECTEST_B", Value: ""}}
		},
			func(req exec.Request) bool {
				return len(req.Env) == 2 && req.Env["EXECTEST_A"] == "b c" && req.Env["EXECTEST_B"] == ""
			}},
		{"a working directory", caps.Cwd, func(st *execv2.Start) { st.Cwd = "/srv/exectest" }, func(req exec.Request) bool { return req.Cwd == "/srv/exectest" }},
	} {
		if !f.honors {
			r.refuses(f.name+" for a target that honors none", r.openRequest(r.newID("field"), "never", f.set), "UNSUPPORTED")
			continue
		}
		name := r.newID("sees")
		seen := r.seesRequest(name)
		s := r.mustOpen(r.newID("field"), name, f.set)
		select {
		case req := <-seen:
			if !f.seen(req) {
				r.t.Errorf("%s for a target that honors it: the function was given env %v, cwd %q", f.name, req.Env, req.Cwd)
			}
		case <-time.After(caseBound):
			r.t.Errorf("%s for a target that honors it: the function did not run within %v", f.name, caseBound)
		}
		r.closeAll([]*wireSession{s})
	}
}

// checkTerminalLimits: a terminal the target cannot give is UNSUPPORTED / TTY_UNSUPPORTED; for a target that can, the largest size it declares
// is taken and the next one is refused; and a size for a session with no terminal is refused.
func (r *wireRig) checkTerminalLimits() {
	r.t.Helper()
	r.script("never", Block())
	if !r.tty() {
		r.refuses("a terminal for a target that cannot give one", r.openRequest(r.newID("tty"), "never", func(st *execv2.Start) { st.Tty = true }), "UNSUPPORTED/TTY_UNSUPPORTED")
	} else {
		maxDimension := r.described().GetSession().GetMaxDimension()
		largest := &execv2.Size{Cols: maxDimension, Rows: maxDimension}
		s := r.mustOpen(r.newID("size"), "never", func(st *execv2.Start) { st.Size = largest })
		if got := s.conn.Opened().GetFacts().GetSize(); got.GetCols() != maxDimension || got.GetRows() != maxDimension {
			r.t.Errorf("a size of the target's largest dimension, %d, was opened as %v", maxDimension, got)
		}
		r.closeAll([]*wireSession{s})
		for _, tc := range []struct {
			name string
			size *execv2.Size
		}{
			{"a width over the target's largest dimension", &execv2.Size{Cols: maxDimension + 1, Rows: 24}},
			{"a height over the target's largest dimension", &execv2.Size{Cols: 80, Rows: maxDimension + 1}},
		} {
			r.refuses(tc.name, r.openRequest(r.newID("size"), "never", func(st *execv2.Start) { st.Size = tc.size }), "INVALID_REQUEST")
		}
	}
	r.refuses("a size for a session with no terminal", r.openRequest(r.newID("size"), "never", func(st *execv2.Start) {
		st.Tty, st.Size = false, &execv2.Size{Cols: 80, Rows: 24}
	}), "INVALID_REQUEST")
}

// checkRequestBounds: a request that breaks a bound of its own is INVALID_REQUEST, and one at the bound is taken: an argv with a NUL, an
// id that is longer than 128 bytes or is not printable ASCII, and an id of exactly 128 bytes.
func (r *wireRig) checkRequestBounds() {
	r.t.Helper()
	r.script("never", Block())
	for _, tc := range []struct {
		name string
		id   string
		mod  func(*execv2.Start)
	}{
		{"an argv element with a NUL", r.newID("bound"), func(st *execv2.Start) { st.Argv = []string{"exec\x00test"} }},
		{"an argv whose first element is empty", r.newID("bound"), func(st *execv2.Start) { st.Argv = []string{"", "x"} }},
		{"an id of 129 bytes", strings.Repeat("a", 129), nil},
		{"an id with a space", "session id", nil},
		{"an id with a control character", "session\nid", nil},
	} {
		var mods []func(*execv2.Start)
		if tc.mod != nil {
			mods = append(mods, tc.mod)
		}
		r.refuses(tc.name, r.openRequest(tc.id, "never", mods...), "INVALID_REQUEST")
	}
	// An id of exactly the longest length is an id.
	s := r.mustOpen(strings.Repeat("b", 128), "never")
	if got := s.conn.Opened().GetFacts().GetSessionId(); got != strings.Repeat("b", 128) {
		r.t.Errorf("a session id of 128 bytes was opened as %q", got)
	}
	r.closeAll([]*wireSession{s})
}

// checkLiveID: the id of a live session is taken: another start with it is CONFLICT / SESSION_EXISTS, and the session that holds it is not
// disturbed by the attempt.
func (r *wireRig) checkLiveID() {
	r.t.Helper()
	id := r.newID("live")
	live := r.mustOpen(id, "echo")
	live.roundTrip([]byte("before"))
	r.refuses("a second start with the id of a live session", r.openRequest(id, "echo"), "CONFLICT/SESSION_EXISTS")
	live.roundTrip([]byte("after"))
	r.closeAll([]*wireSession{live})
}
