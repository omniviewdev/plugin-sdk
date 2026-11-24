package exec

import (
	"bytes"
	"maps"
	"slices"
	"strings"
	"time"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The frames of an Open stream, built from what the engine holds. Each function here is pure: it reads a command, a result
// or a chunk and returns the wire message, so that what the host receives is decided in one place and can be checked
// without a transport.

// programName is the base name of the program argv names, or empty when it is unknown or not plain text.
func programName(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	name := argv[0]
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > maxFactBytes || !validText(name) {
		return ""
	}
	return name
}

// sizeToProto is a terminal size on the wire; the zero Size is absent.
func sizeToProto(s Size) *execv2.Size {
	if s.IsZero() {
		return nil
	}
	return &execv2.Size{Cols: uint32(s.Cols), Rows: uint32(s.Rows)}
}

// openedFacts are the facts of a new session as Opened carries them: what the SDK knows before the author runs. The
// target, the requested program and size, the capabilities the session was opened with and the limits as declared. The
// terminal, the pid, the transport and the TERM are the target's to say at Started, so they are absent here, and so is the
// program of a session whose choice runs none (an attach): the request's argv there is the target's default, which is no
// program that runs.
func (c *command) openedFacts() *execv2.SessionFacts {
	info := &c.target.info
	facts := &execv2.SessionFacts{
		SessionId: c.id,
		Target: &execv2.TargetFacts{
			Provider:    info.Provider,
			Type:        info.Type,
			Id:          c.request.Ref.Address.ID,
			DisplayName: info.DisplayName,
			Selection:   maps.Clone(c.request.Selection),
		},
		Capabilities:    capabilitiesToProto(c.Opened()),
		Limits:          sessionLimitsToProto(info.Session),
		CreatedAtUnixMs: c.created.UnixMilli(),
	}
	c.mu.Lock()
	noProgram := c.noProgram
	c.mu.Unlock()
	if !noProgram {
		facts.Program = &execv2.Program{Argv: slices.Clone(c.request.Argv), Name: programName(c.request.Argv)}
	}
	if c.request.TTY {
		facts.Size = sizeToProto(c.request.Size)
	}
	return facts
}

// startedFacts are the facts of the session at Started: Opened's, refined by what the function reported (or by what the
// SDK made when output came first). It reports false until there is a Started.
func (c *command) startedFacts(opened *execv2.SessionFacts) (*execv2.SessionFacts, bool) {
	reported, ok := c.Started()
	if !ok {
		return nil, false
	}
	facts := &execv2.SessionFacts{
		SessionId:       opened.GetSessionId(),
		Target:          opened.GetTarget(),
		Program:         opened.GetProgram(),
		Size:            opened.GetSize(),
		Capabilities:    opened.GetCapabilities(),
		Limits:          opened.GetLimits(),
		CreatedAtUnixMs: opened.GetCreatedAtUnixMs(),
		Pid:             reported.PID,
		Transport:       reported.Transport,
		Term:            reported.Term,
	}
	if p := reported.Program; len(p.Argv) > 0 || p.Name != "" || p.Path != "" {
		program := &execv2.Program{Argv: slices.Clone(p.Argv), Name: p.Name, Path: p.Path}
		if len(program.Argv) == 0 {
			program.Argv = slices.Clone(opened.GetProgram().GetArgv())
		}
		if program.Name == "" {
			program.Name = programName(program.Argv)
		}
		facts.Program = program
	}
	if reported.TTY != nil {
		facts.Tty = new(*reported.TTY)
	}
	if size := sizeToProto(reported.Size); size != nil {
		facts.Size = size
	}
	if reported.Capabilities != nil {
		facts.Capabilities = capabilitiesToProto(*reported.Capabilities)
	}
	return facts, true
}

// dropReason is why this command's ring overwrote output that its stream had not read: a target that is drained eagerly overflowed
// the host's reading, any other loses output only to a session that is ending. (What the ring loses while no stream is attached is
// DETACHED_OVERFLOW, which no stream is sent: the host learns of it from the start of the replay when it attaches.)
func (c *command) dropReason() execv2.DropReason {
	if c.gate != nil && c.gate.effective().Stall == StallLossy {
		return execv2.DropReason_DROP_REASON_EAGER_DRAIN_OVERFLOW
	}
	return execv2.DropReason_DROP_REASON_REPLAY_FULL
}

// openedFrame is the first frame of a stream: the facts and the replay that follows (for a new session, start = end = bytes = 0).
func openedFrame(opened *execv2.Opened) *execv2.ServerFrame {
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Opened{Opened: opened}}
}

// detachedFrame is the last frame of a stream whose session lives on: why it left and, when a lease runs, when the session ends if
// nobody attaches (zero when none does: a superseded stream's session is attached to another).
func detachedFrame(reason execv2.DetachReason, lease time.Time) *execv2.ServerFrame {
	detached := &execv2.Detached{Reason: reason}
	if !lease.IsZero() {
		detached.LeaseExpiresUnixMs = lease.UnixMilli()
	}
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Detached{Detached: detached}}
}

func noticeFrame(kind execv2.NoticeKind) *execv2.ServerFrame {
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Notice{Notice: &execv2.Notice{Kind: kind}}}
}

func startedFrame(facts *execv2.SessionFacts) *execv2.ServerFrame {
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Started{Started: &execv2.Started{Facts: facts}}}
}

func linkFrame(state LinkState) *execv2.ServerFrame {
	// silent_ms is how long nothing has moved; the function reports a state, not a duration, so it is not known here.
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Link{Link: &execv2.Link{State: state.toProto()}}}
}

func droppedFrame(d dropRun, reason execv2.DropReason) *execv2.ServerFrame {
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Dropped{Dropped: &execv2.Dropped{
		Stream: d.Stream, FromOffset: d.From, Bytes: d.Bytes, Reason: reason,
	}}}
}

// outputFrame is one Output frame. data is copied: the frame is handed to the transport, which may hold it after the
// caller reuses its buffer.
func outputFrame(stream execv2.Stream, offset uint64, data []byte) *execv2.ServerFrame {
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Output{Output: &execv2.Output{
		Stream: stream, Offset: offset, Data: bytes.Clone(data),
	}}}
}

// endedFrame is the end of the session and its accounts. A session without a terminal needs no mode reset (present and
// empty); with a terminal the exact reset is the scanner's (not served yet), so it is absent, which the host reads as "apply
// your own full reset".
func endedFrame(res commandResult, tty bool) *execv2.ServerFrame {
	ended := &execv2.Ended{
		End:           res.End,
		StartObserved: res.StartObserved,
		DurationMs:    uint64(res.Duration.Milliseconds()),
		BytesOut:      res.BytesOut,
		BytesIn:       res.BytesIn,
		DroppedBytes:  res.DroppedBytes,
		Output:        res.Output,
		LastOffset:    res.BytesOut,
	}
	if !tty {
		ended.ModeReset = []byte{}
	}
	return &execv2.ServerFrame{Frame: &execv2.ServerFrame_Ended{Ended: ended}}
}
